package session

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
)

// State represents the lifecycle state of a session.
type State string

const (
	StateOpen     State = "open"
	StateResolved State = "resolved"
)

// Session represents a conversation session between two agents.
type Session struct {
	ID         string
	FromHandle string
	ToHandle   string
	State      State
	TraceID    string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// New creates a new open session.
func New(from, to string) *Session {
	now := time.Now()
	return &Session{
		ID:         uuid.New().String(),
		FromHandle: from,
		ToHandle:   to,
		State:      StateOpen,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
}

// Clone returns a shallow copy of the session.
// All fields are value types so this is safe for concurrent use.
func (s *Session) Clone() *Session {
	cp := *s
	return &cp
}

// Resolve transitions the session to resolved state.
func (s *Session) Resolve() error {
	if s.State != StateOpen {
		return fmt.Errorf("session %s is %s, not open", s.ID, s.State)
	}
	s.State = StateResolved
	s.UpdatedAt = time.Now()
	return nil
}

// Store is an in-memory session store.
type Store struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

// NewStore creates a new session store.
func NewStore() *Store {
	return &Store{
		sessions: make(map[string]*Session),
	}
}

// StartEviction runs a background goroutine that removes resolved sessions
// older than ttl. The goroutine exits when ctx is cancelled.
func (s *Store) StartEviction(ctx context.Context, ttl, interval time.Duration, logger *slog.Logger) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				n := s.evict(ttl)
				if n > 0 && logger != nil {
					logger.Info("evicted resolved sessions", "count", n)
				}
			}
		}
	}()
}

// evict removes resolved sessions whose UpdatedAt is older than ttl. Returns count removed.
func (s *Store) evict(ttl time.Duration) int {
	cutoff := time.Now().Add(-ttl)
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for id, sess := range s.sessions {
		if sess.State == StateResolved && sess.UpdatedAt.Before(cutoff) {
			delete(s.sessions, id)
			count++
		}
	}
	return count
}

// Put stores a session.
func (s *Store) Put(sess *Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sess.ID] = sess
}

// Get retrieves a session by ID. Returns a clone safe for concurrent mutation.
func (s *Store) Get(id string) (*Session, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[id]
	if !ok {
		return nil, false
	}
	return sess.Clone(), true
}

// ListAll returns all sessions. Returns clones safe for concurrent mutation.
func (s *Store) ListAll() []*Session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		result = append(result, sess.Clone())
	}
	return result
}

// ListByHandle returns all sessions involving a handle (as from or to).
// Returns clones safe for concurrent mutation.
func (s *Store) ListByHandle(handle string) []*Session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []*Session
	for _, sess := range s.sessions {
		if sess.FromHandle == handle || sess.ToHandle == handle {
			result = append(result, sess.Clone())
		}
	}
	return result
}
