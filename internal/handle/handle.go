package handle

import (
	"fmt"
	"regexp"
	"strings"
)

// Handle represents an agent handle, either local ("marketing") or federated ("marketing@acme.com").
type Handle struct {
	Name   string // e.g. "marketing"
	Domain string // e.g. "acme.com" (empty for local)
}

var nameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)
var domainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// Parse parses a handle string like "marketing" or "marketing@acme.com".
func Parse(s string) (Handle, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return Handle{}, fmt.Errorf("empty handle")
	}

	parts := strings.SplitN(s, "@", 2)
	name := parts[0]
	if !nameRe.MatchString(name) {
		return Handle{}, fmt.Errorf("invalid handle name %q: must be lowercase alphanumeric, dashes, underscores, 1-63 chars", name)
	}

	var domain string
	if len(parts) == 2 {
		domain = parts[1]
		if !domainRe.MatchString(domain) {
			return Handle{}, fmt.Errorf("invalid domain %q", domain)
		}
	}

	return Handle{Name: name, Domain: domain}, nil
}

// String returns the full handle string.
func (h Handle) String() string {
	if h.Domain == "" {
		return h.Name
	}
	return h.Name + "@" + h.Domain
}

// IsLocal returns true if the handle has no domain (local to this mesh).
func (h Handle) IsLocal() bool {
	return h.Domain == ""
}

// Validate checks if a handle is well-formed.
func Validate(s string) error {
	_, err := Parse(s)
	return err
}
