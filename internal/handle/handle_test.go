package handle

import (
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		input   string
		name    string
		domain  string
		wantErr bool
	}{
		{"marketing", "marketing", "", false},
		{"sales", "sales", "", false},
		{"my-agent", "my-agent", "", false},
		{"agent_1", "agent_1", "", false},
		{"marketing@acme.com", "marketing", "acme.com", false},
		{"sales@big-corp.io", "sales", "big-corp.io", false},
		{"", "", "", true},
		{"UPPER", "upper", "", false},
		{"123invalid", "", "", true},
		{"-bad", "", "", true},
		{"ok@", "", "", true},
		{"ok@bad_domain", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			h, err := Parse(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("Parse(%q) expected error, got %v", tt.input, h)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse(%q) unexpected error: %v", tt.input, err)
			}
			if h.Name != tt.name {
				t.Errorf("Parse(%q).Name = %q, want %q", tt.input, h.Name, tt.name)
			}
			if h.Domain != tt.domain {
				t.Errorf("Parse(%q).Domain = %q, want %q", tt.input, h.Domain, tt.domain)
			}
		})
	}
}

func TestString(t *testing.T) {
	h := Handle{Name: "marketing"}
	if s := h.String(); s != "marketing" {
		t.Errorf("got %q, want %q", s, "marketing")
	}

	h = Handle{Name: "marketing", Domain: "acme.com"}
	if s := h.String(); s != "marketing@acme.com" {
		t.Errorf("got %q, want %q", s, "marketing@acme.com")
	}
}

func TestIsLocal(t *testing.T) {
	h := Handle{Name: "marketing"}
	if !h.IsLocal() {
		t.Error("expected local handle")
	}

	h = Handle{Name: "marketing", Domain: "acme.com"}
	if h.IsLocal() {
		t.Error("expected non-local handle")
	}
}
