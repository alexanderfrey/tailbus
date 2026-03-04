package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// DefaultCredentialFile returns the default path for credential storage.
func DefaultCredentialFile() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".tailbus", "credentials.json")
	}
	return filepath.Join(home, ".tailbus", "credentials.json")
}

// Credentials holds persisted OAuth credentials.
type Credentials struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Email        string `json:"email"`
	CoordAddr    string `json:"coord_addr"`
	ExpiresAt    int64  `json:"expires_at"` // Unix timestamp
	TeamID       string `json:"team_id,omitempty"`
	TeamName     string `json:"team_name,omitempty"`
}

// NeedsRefresh returns true if the access token is expired or will expire within 5 minutes.
func (c *Credentials) NeedsRefresh() bool {
	return time.Now().Unix() >= c.ExpiresAt-300
}

// IsExpired returns true if the access token is fully expired.
func (c *Credentials) IsExpired() bool {
	return time.Now().Unix() >= c.ExpiresAt
}

// LoadCredentials reads credentials from the given path.
func LoadCredentials(path string) (*Credentials, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read credentials: %w", err)
	}
	var creds Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("parse credentials: %w", err)
	}
	return &creds, nil
}

// SaveCredentials writes credentials to the given path with mode 0600.
func SaveCredentials(path string, creds *Credentials) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create credentials dir: %w", err)
	}
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal credentials: %w", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("write credentials: %w", err)
	}
	return nil
}

// RemoveCredentials deletes the credentials file.
func RemoveCredentials(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
