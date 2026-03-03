package auth

import (
	"context"
	"fmt"
	"time"
)

// RefreshIfNeeded checks if the credentials need refreshing and refreshes them.
// Returns the (possibly updated) credentials.
func RefreshIfNeeded(ctx context.Context, credsPath, coordHTTPURL string) (*Credentials, error) {
	creds, err := LoadCredentials(credsPath)
	if err != nil {
		return nil, err
	}

	if !creds.NeedsRefresh() {
		return creds, nil
	}

	result, err := RefreshAccessToken(ctx, coordHTTPURL, creds.RefreshToken)
	if err != nil {
		return nil, fmt.Errorf("refresh token: %w", err)
	}

	creds.AccessToken = result.AccessToken
	creds.RefreshToken = result.RefreshToken
	creds.ExpiresAt = time.Now().Add(time.Duration(result.ExpiresIn) * time.Second).Unix()
	if result.Email != "" {
		creds.Email = result.Email
	}

	if err := SaveCredentials(credsPath, creds); err != nil {
		return nil, fmt.Errorf("save refreshed credentials: %w", err)
	}

	return creds, nil
}
