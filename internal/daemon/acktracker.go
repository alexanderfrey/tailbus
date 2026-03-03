package daemon

import (
	"context"
	"log/slog"
	"sync"
	"time"

	messagepb "github.com/alexanderfrey/tailbus/api/messagepb"
)

const (
	defaultACKTimeout  = 5 * time.Second
	defaultMaxRetries  = 3
)

type pendingMessage struct {
	env       *messagepb.Envelope
	peerAddr  string
	sentAt    time.Time
	retries   int
}

// AckTracker tracks pending messages awaiting ACK and retries on timeout.
type AckTracker struct {
	mu      sync.Mutex
	pending map[string]*pendingMessage // messageID -> pending
	sendFn  func(addr string, env *messagepb.Envelope) error
	logger  *slog.Logger
	timeout time.Duration
	maxRetries int
}

// NewAckTracker creates a new ACK tracker.
func NewAckTracker(sendFn func(addr string, env *messagepb.Envelope) error, logger *slog.Logger) *AckTracker {
	return &AckTracker{
		pending:    make(map[string]*pendingMessage),
		sendFn:     sendFn,
		logger:     logger,
		timeout:    defaultACKTimeout,
		maxRetries: defaultMaxRetries,
	}
}

// Track registers a sent envelope as pending ACK.
func (a *AckTracker) Track(env *messagepb.Envelope, peerAddr string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pending[env.MessageId] = &pendingMessage{
		env:      env,
		peerAddr: peerAddr,
		sentAt:   time.Now(),
	}
}

// Acknowledge removes a message from pending. Returns true if it was pending.
func (a *AckTracker) Acknowledge(messageID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.pending[messageID]
	if ok {
		delete(a.pending, messageID)
	}
	return ok
}

// PendingCount returns the number of messages awaiting ACK.
func (a *AckTracker) PendingCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.pending)
}

// StartRetryLoop runs a background loop that retries expired pending messages.
// It checks every interval and exits when ctx is cancelled.
func (a *AckTracker) StartRetryLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.sweep()
		}
	}
}

func (a *AckTracker) sweep() {
	a.mu.Lock()
	now := time.Now()
	var retryList []*pendingMessage
	var dropIDs []string

	for id, pm := range a.pending {
		if now.Sub(pm.sentAt) < a.timeout {
			continue
		}
		if pm.retries >= a.maxRetries {
			dropIDs = append(dropIDs, id)
			continue
		}
		retryList = append(retryList, pm)
	}

	for _, id := range dropIDs {
		pm := a.pending[id]
		delete(a.pending, id)
		a.logger.Warn("dropping unacked message after max retries",
			"message_id", id, "peer", pm.peerAddr, "retries", pm.retries)
	}
	a.mu.Unlock()

	// Retry outside the lock
	for _, pm := range retryList {
		if err := a.sendFn(pm.peerAddr, pm.env); err != nil {
			a.logger.Warn("retry send failed", "message_id", pm.env.MessageId, "error", err)
		}
		a.mu.Lock()
		// Update the pending entry if it still exists (might have been ACKed in the meantime)
		if existing, ok := a.pending[pm.env.MessageId]; ok {
			existing.retries++
			existing.sentAt = time.Now()
		}
		a.mu.Unlock()
	}
}
