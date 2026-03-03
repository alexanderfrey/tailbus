package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestRequestDeviceCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/device/code" || r.Method != "POST" {
			http.Error(w, "not found", 404)
			return
		}
		json.NewEncoder(w).Encode(DeviceAuthResponse{
			DeviceCode:      "dc_test",
			UserCode:        "ABCD-EFGH",
			VerificationURI: "http://localhost/verify",
			ExpiresIn:       900,
			Interval:        5,
		})
	}))
	defer srv.Close()

	resp, err := RequestDeviceCode(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if resp.DeviceCode != "dc_test" {
		t.Fatalf("got device_code %q, want dc_test", resp.DeviceCode)
	}
	if resp.UserCode != "ABCD-EFGH" {
		t.Fatalf("got user_code %q, want ABCD-EFGH", resp.UserCode)
	}
}

func TestPollForToken(t *testing.T) {
	var pollCount atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/device/token" {
			http.Error(w, "not found", 404)
			return
		}
		count := pollCount.Add(1)
		if count < 3 {
			json.NewEncoder(w).Encode(TokenResponse{Error: "authorization_pending"})
			return
		}
		json.NewEncoder(w).Encode(TokenResponse{
			AccessToken:  "at_test",
			RefreshToken: "rt_test",
			Email:        "alice@example.com",
			ExpiresIn:    3600,
		})
	}))
	defer srv.Close()

	resp, err := PollForToken(context.Background(), srv.URL, "dc_test", 1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.AccessToken != "at_test" {
		t.Fatalf("got access_token %q, want at_test", resp.AccessToken)
	}
	if resp.Email != "alice@example.com" {
		t.Fatalf("got email %q, want alice@example.com", resp.Email)
	}
}

func TestPollForTokenExpired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(TokenResponse{Error: "expired_token"})
	}))
	defer srv.Close()

	_, err := PollForToken(context.Background(), srv.URL, "dc_test", 1)
	if err == nil {
		t.Fatal("expected error for expired token")
	}
}
