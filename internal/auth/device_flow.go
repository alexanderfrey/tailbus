package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// DeviceAuthResponse is returned by the device code request endpoint.
type DeviceAuthResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// TokenResponse is returned by the device token polling endpoint.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Email        string `json:"email"`
	Error        string `json:"error,omitempty"`
}

// RequestDeviceCode initiates the device authorization flow with the coord server.
func RequestDeviceCode(ctx context.Context, coordHTTPURL string) (*DeviceAuthResponse, error) {
	url := coordHTTPURL + "/oauth/device/code"

	req, err := http.NewRequestWithContext(ctx, "POST", url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request device code: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("device code request failed (HTTP %d): %s", resp.StatusCode, body)
	}

	var result DeviceAuthResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &result, nil
}

// PollForToken polls the coord server for token completion.
// It blocks until the user completes authentication, the code expires, or the context is cancelled.
func PollForToken(ctx context.Context, coordHTTPURL, deviceCode string, interval int) (*TokenResponse, error) {
	if interval < 1 {
		interval = 5
	}

	url := coordHTTPURL + "/oauth/device/token"
	ticker := time.NewTicker(time.Duration(interval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			body, _ := json.Marshal(map[string]string{"device_code": deviceCode})
			req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
			if err != nil {
				return nil, fmt.Errorf("create poll request: %w", err)
			}
			req.Header.Set("Content-Type", "application/json")

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				continue // transient network error, keep polling
			}

			var result TokenResponse
			json.NewDecoder(resp.Body).Decode(&result)
			resp.Body.Close()

			switch result.Error {
			case "authorization_pending":
				continue
			case "slow_down":
				// Back off per RFC 8628
				ticker.Reset(time.Duration(interval+5) * time.Second)
				continue
			case "expired_token":
				return nil, fmt.Errorf("device code expired — please try again")
			case "":
				// Success
				if result.AccessToken != "" {
					return &result, nil
				}
				continue
			default:
				return nil, fmt.Errorf("token error: %s", result.Error)
			}
		}
	}
}

// RefreshAccessToken uses a refresh token to obtain new access and refresh tokens.
func RefreshAccessToken(ctx context.Context, coordHTTPURL, refreshToken string) (*TokenResponse, error) {
	url := coordHTTPURL + "/oauth/refresh"
	body, _ := json.Marshal(map[string]string{"refresh_token": refreshToken})

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("refresh request: %w", err)
	}
	defer resp.Body.Close()

	var result TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode refresh response: %w", err)
	}

	if result.Error != "" {
		return nil, fmt.Errorf("refresh failed: %s", result.Error)
	}
	if result.AccessToken == "" {
		return nil, fmt.Errorf("refresh returned empty access token")
	}

	return &result, nil
}
