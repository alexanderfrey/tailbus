package main

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/signal"
	"sync"

	agentpb "github.com/alexanderfrey/tailbus/api/agentpb"
	messagepb "github.com/alexanderfrey/tailbus/api/messagepb"
)

// Inbound command types (stdin)

type inboundCmd struct {
	Type        string `json:"type"`
	Handle      string `json:"handle,omitempty"`
	Description string `json:"description,omitempty"`
	To          string `json:"to,omitempty"`
	Session     string `json:"session,omitempty"`
	Payload     string `json:"payload,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	TraceID     string `json:"trace_id,omitempty"`
}

// Outbound response types (stdout)

type registeredResp struct {
	Type   string `json:"type"`
	Handle string `json:"handle"`
}

type openedResp struct {
	Type      string `json:"type"`
	Session   string `json:"session"`
	MessageID string `json:"message_id"`
	TraceID   string `json:"trace_id"`
}

type sentResp struct {
	Type      string `json:"type"`
	MessageID string `json:"message_id"`
}

type resolvedResp struct {
	Type      string `json:"type"`
	MessageID string `json:"message_id"`
}

type describedResp struct {
	Type        string `json:"type"`
	Handle      string `json:"handle"`
	Description string `json:"description"`
	Found       bool   `json:"found"`
}

type messageResp struct {
	Type        string `json:"type"`
	Session     string `json:"session"`
	From        string `json:"from"`
	To          string `json:"to"`
	Payload     string `json:"payload"`
	ContentType string `json:"content_type"`
	MessageType string `json:"message_type"`
	TraceID     string `json:"trace_id"`
	MessageID   string `json:"message_id"`
	SentAt      int64  `json:"sent_at"`
}

type sessionItem struct {
	Session string `json:"session"`
	From    string `json:"from"`
	To      string `json:"to"`
	State   string `json:"state"`
}

type sessionsResp struct {
	Type     string        `json:"type"`
	Sessions []sessionItem `json:"sessions"`
}

type errorResp struct {
	Type        string `json:"type"`
	Error       string `json:"error"`
	RequestType string `json:"request_type"`
}

// jsonWriter provides mutex-protected JSON line output to stdout.
type jsonWriter struct {
	mu  sync.Mutex
	enc *json.Encoder
}

func newJSONWriter() *jsonWriter {
	return &jsonWriter{enc: json.NewEncoder(os.Stdout)}
}

func (w *jsonWriter) Write(v any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.enc.Encode(v) //nolint:errcheck
}

func envelopeTypeString(t messagepb.EnvelopeType) string {
	switch t {
	case messagepb.EnvelopeType_ENVELOPE_TYPE_SESSION_OPEN:
		return "session_open"
	case messagepb.EnvelopeType_ENVELOPE_TYPE_MESSAGE:
		return "message"
	case messagepb.EnvelopeType_ENVELOPE_TYPE_SESSION_RESOLVE:
		return "session_resolve"
	case messagepb.EnvelopeType_ENVELOPE_TYPE_ACK:
		return "ack"
	default:
		return "unknown"
	}
}

func runAgent(client agentpb.AgentAPIClient, logger *slog.Logger) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	w := newJSONWriter()
	var handle string

	// Scanner goroutine: reads stdin line-by-line into cmdCh.
	cmdCh := make(chan inboundCmd)
	go func() {
		defer close(cmdCh)
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}
			var cmd inboundCmd
			if err := json.Unmarshal(line, &cmd); err != nil {
				w.Write(errorResp{Type: "error", Error: "invalid JSON: " + err.Error(), RequestType: "unknown"})
				continue
			}
			select {
			case cmdCh <- cmd:
			case <-ctx.Done():
				return
			}
		}
	}()

	// Main dispatch loop.
	for {
		select {
		case cmd, ok := <-cmdCh:
			if !ok {
				// stdin closed
				logger.Info("stdin closed, exiting")
				return nil
			}

			switch cmd.Type {
			case "register":
				if handle != "" {
					w.Write(errorResp{Type: "error", Error: "already registered as " + handle, RequestType: "register"})
					continue
				}
				if cmd.Handle == "" {
					w.Write(errorResp{Type: "error", Error: "handle is required", RequestType: "register"})
					continue
				}
				resp, err := client.Register(ctx, &agentpb.RegisterRequest{Handle: cmd.Handle, Description: cmd.Description})
				if err != nil {
					w.Write(errorResp{Type: "error", Error: err.Error(), RequestType: "register"})
					continue
				}
				if !resp.Ok {
					w.Write(errorResp{Type: "error", Error: resp.Error, RequestType: "register"})
					continue
				}
				handle = cmd.Handle
				w.Write(registeredResp{Type: "registered", Handle: handle})

				// Launch subscribe goroutine.
				stream, err := client.Subscribe(ctx, &agentpb.SubscribeRequest{Handle: handle})
				if err != nil {
					logger.Error("subscribe failed", "error", err)
					w.Write(errorResp{Type: "error", Error: "subscribe failed: " + err.Error(), RequestType: "register"})
					continue
				}
				go func() {
					for {
						msg, err := stream.Recv()
						if err != nil {
							if ctx.Err() != nil {
								return
							}
							logger.Error("subscribe stream error", "error", err)
							return
						}
						env := msg.Envelope
						w.Write(messageResp{
							Type:        "message",
							Session:     env.SessionId,
							From:        env.FromHandle,
							To:          env.ToHandle,
							Payload:     string(env.Payload),
							ContentType: env.ContentType,
							MessageType: envelopeTypeString(env.Type),
							TraceID:     env.TraceId,
							MessageID:   env.MessageId,
							SentAt:      env.SentAtUnix,
						})
					}
				}()

			case "open":
				if handle == "" {
					w.Write(errorResp{Type: "error", Error: "must register first", RequestType: "open"})
					continue
				}
				if cmd.To == "" {
					w.Write(errorResp{Type: "error", Error: "to is required", RequestType: "open"})
					continue
				}
				ct := cmd.ContentType
				if ct == "" {
					ct = "text/plain"
				}
				resp, err := client.OpenSession(ctx, &agentpb.OpenSessionRequest{
					FromHandle:  handle,
					ToHandle:    cmd.To,
					Payload:     []byte(cmd.Payload),
					ContentType: ct,
					TraceId:     cmd.TraceID,
				})
				if err != nil {
					w.Write(errorResp{Type: "error", Error: err.Error(), RequestType: "open"})
					continue
				}
				w.Write(openedResp{Type: "opened", Session: resp.SessionId, MessageID: resp.MessageId, TraceID: resp.TraceId})

			case "send":
				if handle == "" {
					w.Write(errorResp{Type: "error", Error: "must register first", RequestType: "send"})
					continue
				}
				if cmd.Session == "" {
					w.Write(errorResp{Type: "error", Error: "session is required", RequestType: "send"})
					continue
				}
				ct := cmd.ContentType
				if ct == "" {
					ct = "text/plain"
				}
				resp, err := client.SendMessage(ctx, &agentpb.SendMessageRequest{
					SessionId:   cmd.Session,
					FromHandle:  handle,
					Payload:     []byte(cmd.Payload),
					ContentType: ct,
				})
				if err != nil {
					w.Write(errorResp{Type: "error", Error: err.Error(), RequestType: "send"})
					continue
				}
				w.Write(sentResp{Type: "sent", MessageID: resp.MessageId})

			case "resolve":
				if handle == "" {
					w.Write(errorResp{Type: "error", Error: "must register first", RequestType: "resolve"})
					continue
				}
				if cmd.Session == "" {
					w.Write(errorResp{Type: "error", Error: "session is required", RequestType: "resolve"})
					continue
				}
				ct := cmd.ContentType
				if ct == "" {
					ct = "text/plain"
				}
				resp, err := client.ResolveSession(ctx, &agentpb.ResolveSessionRequest{
					SessionId:   cmd.Session,
					FromHandle:  handle,
					Payload:     []byte(cmd.Payload),
					ContentType: ct,
				})
				if err != nil {
					w.Write(errorResp{Type: "error", Error: err.Error(), RequestType: "resolve"})
					continue
				}
				w.Write(resolvedResp{Type: "resolved", MessageID: resp.MessageId})

			case "sessions":
				if handle == "" {
					w.Write(errorResp{Type: "error", Error: "must register first", RequestType: "sessions"})
					continue
				}
				resp, err := client.ListSessions(ctx, &agentpb.ListSessionsRequest{Handle: handle})
				if err != nil {
					w.Write(errorResp{Type: "error", Error: err.Error(), RequestType: "sessions"})
					continue
				}
				items := make([]sessionItem, 0, len(resp.Sessions))
				for _, s := range resp.Sessions {
					items = append(items, sessionItem{
						Session: s.SessionId,
						From:    s.FromHandle,
						To:      s.ToHandle,
						State:   s.State,
					})
				}
				w.Write(sessionsResp{Type: "sessions", Sessions: items})

			case "describe":
				if cmd.Handle == "" {
					w.Write(errorResp{Type: "error", Error: "handle is required", RequestType: "describe"})
					continue
				}
				resp, err := client.DescribeHandle(ctx, &agentpb.DescribeHandleRequest{Handle: cmd.Handle})
				if err != nil {
					w.Write(errorResp{Type: "error", Error: err.Error(), RequestType: "describe"})
					continue
				}
				w.Write(describedResp{Type: "described", Handle: resp.Handle, Description: resp.Description, Found: resp.Found})

			default:
				w.Write(errorResp{Type: "error", Error: "unknown command type: " + cmd.Type, RequestType: cmd.Type})
			}

		case <-ctx.Done():
			logger.Info("shutting down")
			return nil
		}
	}
}
