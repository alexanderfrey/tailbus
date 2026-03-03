package coord

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
)

// Admission controls which nodes are allowed to register with the coord server.
// In open mode (no tokens configured), all registrations are allowed.
// In closed mode (any tokens exist), a valid auth token is required.
type Admission struct {
	store  *Store
	logger *slog.Logger
}

// NewAdmission creates a new admission controller.
func NewAdmission(store *Store, logger *slog.Logger) *Admission {
	return &Admission{store: store, logger: logger}
}

// HashToken returns the SHA-256 hex digest of a raw token string.
func HashToken(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

// SeedToken hashes and inserts a token if it doesn't already exist.
// Used at startup to seed tokens from config/flags.
func (a *Admission) SeedToken(name, raw string, singleUse bool) error {
	hash := HashToken(raw)
	err := a.store.InsertAuthToken(name, hash, singleUse, nil)
	if err != nil {
		// Ignore duplicate insert (name or hash already exists)
		a.logger.Debug("seed token insert (may already exist)", "name", name, "error", err)
		return nil
	}
	a.logger.Info("auth token seeded", "name", name, "single_use", singleUse)
	return nil
}

// ValidateRegistration checks whether a registration should be allowed.
// Open mode: if no tokens exist in the DB, all registrations pass.
// Closed mode: the provided authToken must match a valid, unexpired, unconsumed token.
func (a *Admission) ValidateRegistration(authToken, nodeID string) error {
	hasTokens, err := a.store.HasAuthTokens()
	if err != nil {
		return fmt.Errorf("check auth tokens: %w", err)
	}

	// Open mode — no tokens configured, allow everyone
	if !hasTokens {
		return nil
	}

	// Closed mode — token required
	if authToken == "" {
		return fmt.Errorf("auth token required (coord has admission tokens configured)")
	}

	hash := HashToken(authToken)
	if err := a.store.ValidateAndConsumeToken(hash, nodeID); err != nil {
		return fmt.Errorf("auth token rejected: %w", err)
	}

	a.logger.Info("node admitted via auth token", "node_id", nodeID)
	return nil
}
