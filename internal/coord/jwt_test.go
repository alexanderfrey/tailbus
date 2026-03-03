package coord

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestJWTIssueAndValidate(t *testing.T) {
	issuer, err := NewJWTIssuer(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}

	access, refresh, err := issuer.Issue("alice@example.com")
	if err != nil {
		t.Fatal(err)
	}

	// Validate access token
	claims, err := issuer.Validate(access)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Email != "alice@example.com" {
		t.Fatalf("got email %q, want alice@example.com", claims.Email)
	}
	if claims.TokenType != "access" {
		t.Fatalf("got type %q, want access", claims.TokenType)
	}
	if claims.Issuer != "tailbus-coord" {
		t.Fatalf("got issuer %q, want tailbus-coord", claims.Issuer)
	}

	// Validate refresh token
	rclaims, err := issuer.Validate(refresh)
	if err != nil {
		t.Fatal(err)
	}
	if rclaims.TokenType != "refresh" {
		t.Fatalf("got type %q, want refresh", rclaims.TokenType)
	}
}

func TestJWTRefresh(t *testing.T) {
	issuer, err := NewJWTIssuer(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}

	_, refresh, err := issuer.Issue("bob@example.com")
	if err != nil {
		t.Fatal(err)
	}

	newAccess, newRefresh, err := issuer.Refresh(refresh)
	if err != nil {
		t.Fatal(err)
	}

	claims, err := issuer.Validate(newAccess)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Email != "bob@example.com" {
		t.Fatalf("got email %q, want bob@example.com", claims.Email)
	}

	// New refresh token should also be valid
	_, err = issuer.Validate(newRefresh)
	if err != nil {
		t.Fatal(err)
	}
}

func TestJWTRefreshRejectsAccessToken(t *testing.T) {
	issuer, err := NewJWTIssuer(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}

	access, _, err := issuer.Issue("alice@example.com")
	if err != nil {
		t.Fatal(err)
	}

	// Trying to refresh with an access token should fail
	_, _, err = issuer.Refresh(access)
	if err == nil {
		t.Fatal("expected error refreshing with access token")
	}
}

func TestJWTExpired(t *testing.T) {
	issuer := &JWTIssuer{key: []byte("test-secret-key-for-testing-only")}

	// Manually create an expired token
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    jwtIssuer,
			Subject:   "expired@example.com",
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(-2 * time.Hour)),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-1 * time.Hour)),
		},
		Email:     "expired@example.com",
		TokenType: "access",
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, err := token.SignedString(issuer.key)
	if err != nil {
		t.Fatal(err)
	}

	_, err = issuer.Validate(tokenStr)
	if err == nil {
		t.Fatal("expected error for expired token")
	}
}

func TestJWTSecretOverride(t *testing.T) {
	issuer, err := NewJWTIssuer(t.TempDir(), "my-custom-secret")
	if err != nil {
		t.Fatal(err)
	}

	access, _, err := issuer.Issue("override@example.com")
	if err != nil {
		t.Fatal(err)
	}

	claims, err := issuer.Validate(access)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Email != "override@example.com" {
		t.Fatalf("got email %q, want override@example.com", claims.Email)
	}
}

func TestJWTKeyPersistence(t *testing.T) {
	dir := t.TempDir()

	issuer1, err := NewJWTIssuer(dir, "")
	if err != nil {
		t.Fatal(err)
	}

	access, _, err := issuer1.Issue("persist@example.com")
	if err != nil {
		t.Fatal(err)
	}

	// Create second issuer from same dir — should load persisted key
	issuer2, err := NewJWTIssuer(dir, "")
	if err != nil {
		t.Fatal(err)
	}

	claims, err := issuer2.Validate(access)
	if err != nil {
		t.Fatal("second issuer could not validate token from first issuer:", err)
	}
	if claims.Email != "persist@example.com" {
		t.Fatalf("got email %q, want persist@example.com", claims.Email)
	}
}
