package session

import (
	"testing"
)

func TestNewSession(t *testing.T) {
	s := New("marketing", "sales")
	if s.ID == "" {
		t.Error("empty session ID")
	}
	if s.FromHandle != "marketing" || s.ToHandle != "sales" {
		t.Errorf("got from=%q to=%q", s.FromHandle, s.ToHandle)
	}
	if s.State != StateOpen {
		t.Errorf("state = %q, want %q", s.State, StateOpen)
	}
}

func TestResolve(t *testing.T) {
	s := New("a", "b")
	if err := s.Resolve(); err != nil {
		t.Fatal(err)
	}
	if s.State != StateResolved {
		t.Errorf("state = %q, want %q", s.State, StateResolved)
	}
	if err := s.Resolve(); err == nil {
		t.Error("expected error resolving already-resolved session")
	}
}

func TestStore(t *testing.T) {
	store := NewStore()

	s1 := New("marketing", "sales")
	s2 := New("sales", "support")
	store.Put(s1)
	store.Put(s2)

	got, ok := store.Get(s1.ID)
	if !ok || got.ID != s1.ID {
		t.Error("failed to get session")
	}

	_, ok = store.Get("nonexistent")
	if ok {
		t.Error("expected not found")
	}

	list := store.ListByHandle("sales")
	if len(list) != 2 {
		t.Errorf("ListByHandle(sales) = %d sessions, want 2", len(list))
	}

	list = store.ListByHandle("marketing")
	if len(list) != 1 {
		t.Errorf("ListByHandle(marketing) = %d sessions, want 1", len(list))
	}
}
