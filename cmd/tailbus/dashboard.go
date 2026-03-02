package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	agentpb "github.com/alexanderfrey/tailbus/api/agentpb"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Styles
var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("15")).
			Background(lipgloss.Color("62")).
			Padding(0, 1)

	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("220"))

	statusBarStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("243"))

	sectionStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("62")).
			Padding(0, 1)

	keyStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("62")).
			Bold(true)

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("243"))

	connectedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("42"))

	openStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("220"))

	resolvedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("243"))

	actMsgStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("81"))

	actSessStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("220"))

	actRegStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("42"))
)

// Messages
type statusMsg *agentpb.GetNodeStatusResponse
type activityMsg *agentpb.ActivityEvent
type tickMsg time.Time
type errMsg struct{ error }

// Model
type dashboardModel struct {
	client   agentpb.AgentAPIClient
	status   *agentpb.GetNodeStatusResponse
	activity []activityEntry
	width    int
	height   int
	err      error
	quitting bool
}

type activityEntry struct {
	time  time.Time
	label string
	style lipgloss.Style
}

const maxActivity = 50

func newDashboardModel(client agentpb.AgentAPIClient) dashboardModel {
	return dashboardModel{
		client: client,
	}
}

func (m dashboardModel) Init() tea.Cmd {
	return tea.Batch(
		m.fetchStatus,
		m.watchActivity,
		tickCmd(),
	)
}

func tickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m dashboardModel) fetchStatus() tea.Msg {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := m.client.GetNodeStatus(ctx, &agentpb.GetNodeStatusRequest{})
	if err != nil {
		return errMsg{err}
	}
	return statusMsg(resp)
}

func (m dashboardModel) watchActivity() tea.Msg {
	stream, err := m.client.WatchActivity(context.Background(), &agentpb.WatchActivityRequest{})
	if err != nil {
		return errMsg{err}
	}
	event, err := stream.Recv()
	if err != nil {
		return errMsg{err}
	}
	return activityMsg(event)
}

// watchNext continues receiving from an existing stream.
// We need to reconnect each time because the stream is consumed in the Msg handler.
func (m dashboardModel) watchNext() tea.Msg {
	return m.watchActivity()
}

func (m dashboardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "r":
			return m, m.fetchStatus
		case "c":
			m.activity = nil
			return m, nil
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case statusMsg:
		m.status = (*agentpb.GetNodeStatusResponse)(msg)
		m.err = nil
		return m, nil

	case activityMsg:
		event := (*agentpb.ActivityEvent)(msg)
		entry := formatActivity(event)
		m.activity = append(m.activity, entry)
		if len(m.activity) > maxActivity {
			m.activity = m.activity[len(m.activity)-maxActivity:]
		}
		return m, m.watchNext

	case tickMsg:
		return m, tea.Batch(m.fetchStatus, tickCmd())

	case errMsg:
		m.err = msg.error
		return m, nil
	}

	return m, nil
}

func formatActivity(event *agentpb.ActivityEvent) activityEntry {
	ts := time.Now()
	if event.Timestamp != nil {
		ts = event.Timestamp.AsTime()
	}

	switch e := event.Event.(type) {
	case *agentpb.ActivityEvent_MessageRouted:
		dest := "LOCAL"
		if e.MessageRouted.Remote {
			dest = "REMOTE"
		}
		return activityEntry{
			time:  ts,
			label: fmt.Sprintf("MSG %s -> %s [%s]", e.MessageRouted.FromHandle, e.MessageRouted.ToHandle, dest),
			style: actMsgStyle,
		}
	case *agentpb.ActivityEvent_SessionOpened:
		return activityEntry{
			time:  ts,
			label: fmt.Sprintf("OPEN %s -> %s (%s)", e.SessionOpened.FromHandle, e.SessionOpened.ToHandle, e.SessionOpened.SessionId[:8]),
			style: actSessStyle,
		}
	case *agentpb.ActivityEvent_SessionResolved:
		return activityEntry{
			time:  ts,
			label: fmt.Sprintf("RESOLVE %s (%s)", e.SessionResolved.FromHandle, e.SessionResolved.SessionId[:8]),
			style: actSessStyle,
		}
	case *agentpb.ActivityEvent_HandleRegistered:
		return activityEntry{
			time:  ts,
			label: fmt.Sprintf("REG %q", e.HandleRegistered.Handle),
			style: actRegStyle,
		}
	default:
		return activityEntry{time: ts, label: "???", style: helpStyle}
	}
}

func (m dashboardModel) View() string {
	if m.quitting {
		return ""
	}

	if m.width == 0 {
		return "Loading..."
	}

	var b strings.Builder

	// Title bar
	title := titleStyle.Render(" tailbus dashboard ")
	statusInfo := ""
	if m.status != nil {
		uptime := time.Since(m.status.StartedAt.AsTime()).Truncate(time.Second)
		var totalMsgs, openSess, resolvedSess int64
		if m.status.Counters != nil {
			totalMsgs = m.status.Counters.MessagesRouted
		}
		for _, s := range m.status.Sessions {
			if s.State == "open" {
				openSess++
			} else {
				resolvedSess++
			}
		}
		statusInfo = statusBarStyle.Render(fmt.Sprintf(
			"  Node: %s  |  Uptime: %s  |  Msgs: %d  |  Sess: %d/%d",
			m.status.NodeId, uptime, totalMsgs, openSess, resolvedSess,
		))
	}
	b.WriteString(title + statusInfo + "\n")

	if m.err != nil {
		b.WriteString(fmt.Sprintf("\n  Error: %v\n", m.err))
	}

	// Calculate column widths
	totalWidth := m.width
	if totalWidth < 40 {
		totalWidth = 40
	}
	leftW := totalWidth/2 - 2
	rightW := totalWidth - leftW - 4

	// Top row: handles + peers
	handlesContent := m.renderHandles(leftW)
	peersContent := m.renderPeers(rightW)
	topRow := lipgloss.JoinHorizontal(lipgloss.Top,
		sectionStyle.Width(leftW).Render(handlesContent),
		sectionStyle.Width(rightW).Render(peersContent),
	)
	b.WriteString(topRow + "\n")

	// Bottom row: sessions + activity
	sessionsContent := m.renderSessions(leftW)
	activityContent := m.renderActivity(rightW)
	bottomRow := lipgloss.JoinHorizontal(lipgloss.Top,
		sectionStyle.Width(leftW).Render(sessionsContent),
		sectionStyle.Width(rightW).Render(activityContent),
	)
	b.WriteString(bottomRow + "\n")

	// Help bar
	help := fmt.Sprintf("  %s quit  %s refresh  %s clear activity",
		keyStyle.Render("q"),
		keyStyle.Render("r"),
		keyStyle.Render("c"),
	)
	b.WriteString(helpStyle.Render(help) + "\n")

	return b.String()
}

func (m dashboardModel) renderHandles(width int) string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("HANDLES") + "\n")

	if m.status == nil || len(m.status.Handles) == 0 {
		b.WriteString(helpStyle.Render("  (none)"))
		return b.String()
	}

	for _, h := range m.status.Handles {
		line := fmt.Sprintf("  %s (%d subs)", h.Name, h.SubscriberCount)
		if len(line) > width-2 {
			line = line[:width-2]
		}
		b.WriteString(line + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m dashboardModel) renderPeers(width int) string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("PEERS") + "\n")

	if m.status == nil || len(m.status.Peers) == 0 {
		b.WriteString(helpStyle.Render("  (none)"))
		return b.String()
	}

	for _, p := range m.status.Peers {
		status := resolvedStyle.Render("[disconnected]")
		if p.Connected {
			status = connectedStyle.Render("[connected]")
		}
		line := fmt.Sprintf("  %s %s %s", p.NodeId, p.AdvertiseAddr, status)
		if len(line) > width-2 {
			line = line[:width-2]
		}
		b.WriteString(line + "\n")
		if len(p.Handles) > 0 {
			handles := "    " + strings.Join(p.Handles, ", ")
			if len(handles) > width-2 {
				handles = handles[:width-2]
			}
			b.WriteString(helpStyle.Render(handles) + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m dashboardModel) renderSessions(width int) string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("SESSIONS") + "\n")

	if m.status == nil || len(m.status.Sessions) == 0 {
		b.WriteString(helpStyle.Render("  (none)"))
		return b.String()
	}

	for _, s := range m.status.Sessions {
		idShort := s.SessionId
		if len(idShort) > 8 {
			idShort = idShort[:8]
		}
		stateStr := openStyle.Render("[open]")
		if s.State == "resolved" {
			stateStr = resolvedStyle.Render("[resolved]")
		}
		line := fmt.Sprintf("  %s %s -> %s %s", idShort, s.FromHandle, s.ToHandle, stateStr)
		if len(line) > width-2 {
			line = line[:width-2]
		}
		b.WriteString(line + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m dashboardModel) renderActivity(width int) string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("ACTIVITY") + "\n")

	if len(m.activity) == 0 {
		b.WriteString(helpStyle.Render("  (waiting for events...)"))
		return b.String()
	}

	// Show most recent events (bottom = newest)
	maxLines := 10
	start := 0
	if len(m.activity) > maxLines {
		start = len(m.activity) - maxLines
	}

	for _, entry := range m.activity[start:] {
		ts := entry.time.Format("15:04:05")
		line := fmt.Sprintf("  %s %s", ts, entry.label)
		if len(line) > width-2 {
			line = line[:width-2]
		}
		b.WriteString(entry.style.Render(line) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func runDashboard(client agentpb.AgentAPIClient) error {
	p := tea.NewProgram(
		newDashboardModel(client),
		tea.WithAltScreen(),
	)
	_, err := p.Run()
	return err
}
