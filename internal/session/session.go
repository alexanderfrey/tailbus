package session

import (
	"fmt"
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

// Put stores a session.
func (s *Store) Put(sess *Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sess.ID] = sess
}

// Get retrieves a session by ID.
func (s *Store) Get(id string) (*Session, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[id]
	return sess, ok
}

// ListByHandle returns all sessions involving a handle (as from or to).
func (s *Store) ListByHandle(handle string) []*Session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []*Session
	for _, sess := range s.sessions {
		if sess.FromHandle == handle || sess.ToHandle == handle {
			result = append(result, sess)
		}
	}
	return result
}
