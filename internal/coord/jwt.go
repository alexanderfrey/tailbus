package coord

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	jwtIssuer             = "tailbus-coord"
	accessTokenDuration   = 1 * time.Hour
	refreshTokenDuration  = 30 * 24 * time.Hour
	jwtKeySize            = 32 // 256-bit HMAC key
)

// Claims represents the JWT claims for tailbus tokens.
type Claims struct {
	jwt.RegisteredClaims
	Email     string `json:"email"`
	TokenType string `json:"type"` // "access" or "refresh"
}

// JWTIssuer handles issuing and validating tailbus JWTs using HMAC-SHA256.
type JWTIssuer struct {
	key []byte
}

// NewJWTIssuer loads or generates an HMAC signing key from dataDir/jwt.key.
// If secretOverride is non-empty, it is used directly as the key material.
func NewJWTIssuer(dataDir, secretOverride string) (*JWTIssuer, error) {
	if secretOverride != "" {
		return &JWTIssuer{key: []byte(secretOverride)}, nil
	}

	keyPath := filepath.Join(dataDir, "jwt.key")
	key, err := os.ReadFile(keyPath)
	if err == nil && len(key) >= jwtKeySize {
		return &JWTIssuer{key: key}, nil
	}

	// Generate new key
	key = make([]byte, jwtKeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate JWT key: %w", err)
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	if err := os.WriteFile(keyPath, key, 0600); err != nil {
		return nil, fmt.Errorf("write JWT key: %w", err)
	}
	return &JWTIssuer{key: key}, nil
}

// Issue mints an access token (1h) and refresh token (30d) for the given email.
func (j *JWTIssuer) Issue(email string) (accessToken, refreshToken string, err error) {
	now := time.Now()

	accessClaims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    jwtIssuer,
			Subject:   email,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(accessTokenDuration)),
		},
		Email:     email,
		TokenType: "access",
	}
	at := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims)
	accessToken, err = at.SignedString(j.key)
	if err != nil {
		return "", "", fmt.Errorf("sign access token: %w", err)
	}

	refreshClaims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    jwtIssuer,
			Subject:   email,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(refreshTokenDuration)),
		},
		Email:     email,
		TokenType: "refresh",
	}
	rt := jwt.NewWithClaims(jwt.SigningMethodHS256, refreshClaims)
	refreshToken, err = rt.SignedString(j.key)
	if err != nil {
		return "", "", fmt.Errorf("sign refresh token: %w", err)
	}

	return accessToken, refreshToken, nil
}

// Validate verifies a token's signature and expiry, returning the claims.
func (j *JWTIssuer) Validate(tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return j.key, nil
	})
	if err != nil {
		return nil, fmt.Errorf("parse token: %w", err)
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token claims")
	}

	return claims, nil
}

// Refresh takes a valid refresh token and issues a new access + refresh token pair.
func (j *JWTIssuer) Refresh(refreshToken string) (newAccess, newRefresh string, err error) {
	claims, err := j.Validate(refreshToken)
	if err != nil {
		return "", "", fmt.Errorf("invalid refresh token: %w", err)
	}
	if claims.TokenType != "refresh" {
		return "", "", fmt.Errorf("token is not a refresh token")
	}
	return j.Issue(claims.Email)
}
