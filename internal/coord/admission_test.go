package coord

import (
	"log/slog"
	"os"
	"testing"
	"time"
)

func testAdmission(t *testing.T) (*Admission, *Store, func()) {
	t.Helper()
	store, cleanup := testStore(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	adm := NewAdmission(store, logger)
	return adm, store, cleanup
}

func TestAdmissionOpenMode(t *testing.T) {
	adm, _, cleanup := testAdmission(t)
	defer cleanup()

	// No tokens configured — should allow registration without a token
	if _, err := adm.ValidateRegistration("", "node-1"); err != nil {
		t.Fatalf("open mode should allow empty token: %v", err)
	}

	// Should also allow a random token in open mode (no validation needed)
	if _, err := adm.ValidateRegistration("random-token", "node-1"); err != nil {
		t.Fatalf("open mode should allow any token: %v", err)
	}
}

func TestAdmissionRejectsWithoutToken(t *testing.T) {
	adm, _, cleanup := testAdmission(t)
	defer cleanup()

	// Seed a token to enter closed mode
	if err := adm.SeedToken("test-token", "secret123", false); err != nil {
		t.Fatal(err)
	}

	// Empty token should be rejected
	_, err := adm.ValidateRegistration("", "node-1")
	if err == nil {
		t.Fatal("expected rejection with empty token in closed mode")
	}
}

func TestAdmissionAcceptsValidToken(t *testing.T) {
	adm, _, cleanup := testAdmission(t)
	defer cleanup()

	if err := adm.SeedToken("test-token", "secret123", false); err != nil {
		t.Fatal(err)
	}

	// Correct token should be accepted
	if _, err := adm.ValidateRegistration("secret123", "node-1"); err != nil {
		t.Fatalf("valid token should be accepted: %v", err)
	}
}

func TestAdmissionRejectsInvalidToken(t *testing.T) {
	adm, _, cleanup := testAdmission(t)
	defer cleanup()

	if err := adm.SeedToken("test-token", "secret123", false); err != nil {
		t.Fatal(err)
	}

	// Wrong token should be rejected
	_, err := adm.ValidateRegistration("wrong-token", "node-1")
	if err == nil {
		t.Fatal("expected rejection with invalid token")
	}
}

func TestAdmissionSingleUseConsumed(t *testing.T) {
	adm, _, cleanup := testAdmission(t)
	defer cleanup()

	// Seed a single-use token
	if err := adm.SeedToken("one-time", "use-once", true); err != nil {
		t.Fatal(err)
	}

	// First use should succeed
	if _, err := adm.ValidateRegistration("use-once", "node-1"); err != nil {
		t.Fatalf("first use of single-use token should succeed: %v", err)
	}

	// Second use should fail
	_, err := adm.ValidateRegistration("use-once", "node-2")
	if err == nil {
		t.Fatal("expected rejection on second use of single-use token")
	}
}

func TestAdmissionExpiredToken(t *testing.T) {
	adm, store, cleanup := testAdmission(t)
	defer cleanup()

	// Insert a token that expired in the past
	hash := HashToken("expired-tok")
	expiry := time.Now().Add(-1 * time.Hour)
	if err := store.InsertAuthToken("expired", hash, false, &expiry); err != nil {
		t.Fatal(err)
	}

	// Expired token should be rejected
	_, err := adm.ValidateRegistration("expired-tok", "node-1")
	if err == nil {
		t.Fatal("expected rejection for expired token")
	}
}

func TestAdmissionMultiUseToken(t *testing.T) {
	adm, _, cleanup := testAdmission(t)
	defer cleanup()

	// Seed a multi-use (non-single-use) token
	if err := adm.SeedToken("multi", "reusable", false); err != nil {
		t.Fatal(err)
	}

	// Should work multiple times
	for i := range 3 {
		if _, err := adm.ValidateRegistration("reusable", "node-"+string(rune('1'+i))); err != nil {
			t.Fatalf("multi-use token should work on use %d: %v", i+1, err)
		}
	}
}

func TestAdmissionSeedTokenIdempotent(t *testing.T) {
	adm, _, cleanup := testAdmission(t)
	defer cleanup()

	// Seeding the same token twice should not error
	if err := adm.SeedToken("tok", "value", false); err != nil {
		t.Fatal(err)
	}
	if err := adm.SeedToken("tok", "value", false); err != nil {
		t.Fatalf("duplicate seed should not error: %v", err)
	}
}

func TestHashTokenDeterministic(t *testing.T) {
	h1 := HashToken("test")
	h2 := HashToken("test")
	if h1 != h2 {
		t.Fatalf("HashToken should be deterministic: %q != %q", h1, h2)
	}

	h3 := HashToken("different")
	if h1 == h3 {
		t.Fatal("different inputs should produce different hashes")
	}
}

func TestAdmissionJWT(t *testing.T) {
	store, cleanup := testStore(t)
	defer cleanup()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	issuer, err := NewJWTIssuer(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}

	adm := NewAdmission(store, logger)
	adm.SetJWT(issuer)

	access, _, err := issuer.Issue("alice@example.com")
	if err != nil {
		t.Fatal(err)
	}

	result, err := adm.ValidateRegistration(access, "node-jwt-1")
	if err != nil {
		t.Fatalf("JWT admission should succeed: %v", err)
	}
	if result.Email != "alice@example.com" {
		t.Fatalf("expected email alice@example.com, got %q", result.Email)
	}

	// Verify user was created
	email, err := store.GetNodeUser("node-jwt-1")
	if err != nil {
		t.Fatal(err)
	}
	if email != "alice@example.com" {
		t.Fatalf("expected node bound to alice@example.com, got %q", email)
	}
}
