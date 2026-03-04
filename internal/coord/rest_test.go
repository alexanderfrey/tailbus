package coord

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func noopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func setupRESTTest(t *testing.T) (*RESTHandler, *JWTIssuer, *httptest.Server) {
	t.Helper()
	store, err := NewStore(":memory:")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	issuer, err := NewJWTIssuer(t.TempDir(), "")
	if err != nil {
		t.Fatalf("new jwt issuer: %v", err)
	}

	handler := NewRESTHandler(store, issuer, noopLogger())
	ts := httptest.NewServer(handler.Handler())
	t.Cleanup(ts.Close)
	return handler, issuer, ts
}

func issueAccessToken(t *testing.T, issuer *JWTIssuer, email string) string {
	t.Helper()
	at, _, err := issuer.Issue(email)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	return at
}

func doRequest(t *testing.T, ts *httptest.Server, method, path, token string, body interface{}) *http.Response {
	t.Helper()
	var bodyReader *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		bodyReader = bytes.NewReader(b)
	} else {
		bodyReader = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, ts.URL+path, bodyReader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	return resp
}

func decodeJSON(t *testing.T, resp *http.Response) map[string]interface{} {
	t.Helper()
	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	return result
}

func TestRESTMe(t *testing.T) {
	_, issuer, ts := setupRESTTest(t)
	token := issueAccessToken(t, issuer, "alice@example.com")

	resp := doRequest(t, ts, "GET", "/api/v1/me", token, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body := decodeJSON(t, resp)
	if body["email"] != "alice@example.com" {
		t.Fatalf("expected alice@example.com, got %v", body["email"])
	}
}

func TestRESTNoAuth(t *testing.T) {
	_, _, ts := setupRESTTest(t)

	resp := doRequest(t, ts, "GET", "/api/v1/me", "", nil)
	if resp.StatusCode != 401 {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestRESTRefreshTokenRejected(t *testing.T) {
	_, issuer, ts := setupRESTTest(t)
	_, rt, err := issuer.Issue("alice@example.com")
	if err != nil {
		t.Fatal(err)
	}

	resp := doRequest(t, ts, "GET", "/api/v1/me", rt, nil)
	if resp.StatusCode != 401 {
		t.Fatalf("expected 401 for refresh token, got %d", resp.StatusCode)
	}
}

func TestRESTTeamCRUD(t *testing.T) {
	_, issuer, ts := setupRESTTest(t)
	token := issueAccessToken(t, issuer, "alice@example.com")

	// Create team
	resp := doRequest(t, ts, "POST", "/api/v1/teams", token, map[string]string{"name": "test-team"})
	if resp.StatusCode != 201 {
		t.Fatalf("create: expected 201, got %d", resp.StatusCode)
	}
	body := decodeJSON(t, resp)
	teamID := body["team_id"].(string)
	if teamID == "" {
		t.Fatal("team_id is empty")
	}

	// List teams
	resp = doRequest(t, ts, "GET", "/api/v1/teams", token, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("list: expected 200, got %d", resp.StatusCode)
	}
	body = decodeJSON(t, resp)
	teams := body["teams"].([]interface{})
	if len(teams) != 1 {
		t.Fatalf("expected 1 team, got %d", len(teams))
	}

	// Get members
	resp = doRequest(t, ts, "GET", "/api/v1/teams/"+teamID+"/members", token, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("members: expected 200, got %d", resp.StatusCode)
	}
	body = decodeJSON(t, resp)
	members := body["members"].([]interface{})
	if len(members) != 1 {
		t.Fatalf("expected 1 member, got %d", len(members))
	}

	// Delete team
	resp = doRequest(t, ts, "DELETE", "/api/v1/teams/"+teamID, token, nil)
	if resp.StatusCode != 204 {
		t.Fatalf("delete: expected 204, got %d", resp.StatusCode)
	}

	// Verify gone
	resp = doRequest(t, ts, "GET", "/api/v1/teams", token, nil)
	body = decodeJSON(t, resp)
	teams = body["teams"].([]interface{})
	if len(teams) != 0 {
		t.Fatalf("expected 0 teams after delete, got %d", len(teams))
	}
}

func TestRESTInviteFlow(t *testing.T) {
	_, issuer, ts := setupRESTTest(t)
	ownerToken := issueAccessToken(t, issuer, "owner@example.com")
	memberToken := issueAccessToken(t, issuer, "member@example.com")

	// Create team
	resp := doRequest(t, ts, "POST", "/api/v1/teams", ownerToken, map[string]string{"name": "invite-team"})
	body := decodeJSON(t, resp)
	teamID := body["team_id"].(string)

	// Create invite
	resp = doRequest(t, ts, "POST", "/api/v1/teams/"+teamID+"/invites", ownerToken, map[string]int{"max_uses": 5})
	if resp.StatusCode != 201 {
		t.Fatalf("create invite: expected 201, got %d", resp.StatusCode)
	}
	body = decodeJSON(t, resp)
	code := body["code"].(string)
	if code == "" {
		t.Fatal("invite code is empty")
	}

	// Accept invite
	resp = doRequest(t, ts, "POST", "/api/v1/invites/accept", memberToken, map[string]string{"code": code})
	if resp.StatusCode != 200 {
		t.Fatalf("accept invite: expected 200, got %d", resp.StatusCode)
	}
	body = decodeJSON(t, resp)
	if body["team_id"] != teamID {
		t.Fatalf("expected team_id %s, got %v", teamID, body["team_id"])
	}

	// Verify member is now in team
	resp = doRequest(t, ts, "GET", "/api/v1/teams/"+teamID+"/members", memberToken, nil)
	body = decodeJSON(t, resp)
	members := body["members"].([]interface{})
	if len(members) != 2 {
		t.Fatalf("expected 2 members, got %d", len(members))
	}
}

func TestRESTOwnerOnlyOps(t *testing.T) {
	_, issuer, ts := setupRESTTest(t)
	ownerToken := issueAccessToken(t, issuer, "owner@example.com")
	memberToken := issueAccessToken(t, issuer, "member@example.com")

	// Create team and add member
	resp := doRequest(t, ts, "POST", "/api/v1/teams", ownerToken, map[string]string{"name": "auth-team"})
	body := decodeJSON(t, resp)
	teamID := body["team_id"].(string)

	// Create invite, accept as member
	resp = doRequest(t, ts, "POST", "/api/v1/teams/"+teamID+"/invites", ownerToken, nil)
	inviteBody := decodeJSON(t, resp)
	code := inviteBody["code"].(string)
	doRequest(t, ts, "POST", "/api/v1/invites/accept", memberToken, map[string]string{"code": code})

	// Member cannot create invites
	resp = doRequest(t, ts, "POST", "/api/v1/teams/"+teamID+"/invites", memberToken, nil)
	if resp.StatusCode != 403 {
		t.Fatalf("member invite: expected 403, got %d", resp.StatusCode)
	}

	// Member cannot remove members
	resp = doRequest(t, ts, "DELETE", "/api/v1/teams/"+teamID+"/members/owner@example.com", memberToken, nil)
	if resp.StatusCode != 403 {
		t.Fatalf("member remove: expected 403, got %d", resp.StatusCode)
	}

	// Member cannot change roles
	resp = doRequest(t, ts, "PUT", "/api/v1/teams/"+teamID+"/members/owner@example.com/role", memberToken, map[string]string{"role": "member"})
	if resp.StatusCode != 403 {
		t.Fatalf("member role change: expected 403, got %d", resp.StatusCode)
	}

	// Member cannot delete team
	resp = doRequest(t, ts, "DELETE", "/api/v1/teams/"+teamID, memberToken, nil)
	if resp.StatusCode != 403 {
		t.Fatalf("member delete team: expected 403, got %d", resp.StatusCode)
	}
}

func TestRESTUpdateRole(t *testing.T) {
	_, issuer, ts := setupRESTTest(t)
	ownerToken := issueAccessToken(t, issuer, "owner@example.com")
	memberToken := issueAccessToken(t, issuer, "member@example.com")

	// Create team and add member
	resp := doRequest(t, ts, "POST", "/api/v1/teams", ownerToken, map[string]string{"name": "role-team"})
	body := decodeJSON(t, resp)
	teamID := body["team_id"].(string)

	resp = doRequest(t, ts, "POST", "/api/v1/teams/"+teamID+"/invites", ownerToken, nil)
	inviteBody := decodeJSON(t, resp)
	doRequest(t, ts, "POST", "/api/v1/invites/accept", memberToken, map[string]string{"code": inviteBody["code"].(string)})

	// Promote member to owner
	resp = doRequest(t, ts, "PUT", "/api/v1/teams/"+teamID+"/members/member@example.com/role", ownerToken, map[string]string{"role": "owner"})
	if resp.StatusCode != 200 {
		t.Fatalf("promote: expected 200, got %d", resp.StatusCode)
	}

	// Demote self (now there are 2 owners, so this should work)
	resp = doRequest(t, ts, "PUT", "/api/v1/teams/"+teamID+"/members/owner@example.com/role", ownerToken, map[string]string{"role": "member"})
	if resp.StatusCode != 200 {
		t.Fatalf("demote: expected 200, got %d", resp.StatusCode)
	}
}

func TestRESTLastOwnerProtection(t *testing.T) {
	_, issuer, ts := setupRESTTest(t)
	token := issueAccessToken(t, issuer, "solo@example.com")

	resp := doRequest(t, ts, "POST", "/api/v1/teams", token, map[string]string{"name": "solo-team"})
	body := decodeJSON(t, resp)
	teamID := body["team_id"].(string)

	// Try to demote sole owner
	resp = doRequest(t, ts, "PUT", "/api/v1/teams/"+teamID+"/members/solo@example.com/role", token, map[string]string{"role": "member"})
	if resp.StatusCode != 400 {
		t.Fatalf("last owner demote: expected 400, got %d", resp.StatusCode)
	}
}

func TestRESTSelfRemoveBlocked(t *testing.T) {
	_, issuer, ts := setupRESTTest(t)
	token := issueAccessToken(t, issuer, "owner@example.com")

	resp := doRequest(t, ts, "POST", "/api/v1/teams", token, map[string]string{"name": "self-team"})
	body := decodeJSON(t, resp)
	teamID := body["team_id"].(string)

	resp = doRequest(t, ts, "DELETE", "/api/v1/teams/"+teamID+"/members/owner@example.com", token, nil)
	if resp.StatusCode != 400 {
		t.Fatalf("self remove: expected 400, got %d", resp.StatusCode)
	}
}

func TestRESTNonMemberAccess(t *testing.T) {
	_, issuer, ts := setupRESTTest(t)
	ownerToken := issueAccessToken(t, issuer, "owner@example.com")
	outsiderToken := issueAccessToken(t, issuer, "outsider@example.com")

	resp := doRequest(t, ts, "POST", "/api/v1/teams", ownerToken, map[string]string{"name": "private-team"})
	body := decodeJSON(t, resp)
	teamID := body["team_id"].(string)

	// Non-member cannot see members
	resp = doRequest(t, ts, "GET", "/api/v1/teams/"+teamID+"/members", outsiderToken, nil)
	if resp.StatusCode != 403 {
		t.Fatalf("non-member members: expected 403, got %d", resp.StatusCode)
	}

	// Non-member cannot see nodes
	resp = doRequest(t, ts, "GET", "/api/v1/teams/"+teamID+"/nodes", outsiderToken, nil)
	if resp.StatusCode != 403 {
		t.Fatalf("non-member nodes: expected 403, got %d", resp.StatusCode)
	}
}

func TestRESTNodes(t *testing.T) {
	handler, issuer, ts := setupRESTTest(t)
	token := issueAccessToken(t, issuer, "owner@example.com")

	// Create team
	resp := doRequest(t, ts, "POST", "/api/v1/teams", token, map[string]string{"name": "node-team"})
	body := decodeJSON(t, resp)
	teamID := body["team_id"].(string)

	// Add a node to the team
	err := handler.store.UpsertNode(&NodeRecord{
		NodeID:        "node-1",
		PublicKey:     []byte("key"),
		AdvertiseAddr: "10.0.0.1:9443",
		Handles:       []string{"echo", "calc"},
		TeamID:        teamID,
	})
	if err != nil {
		t.Fatal(err)
	}

	resp = doRequest(t, ts, "GET", "/api/v1/teams/"+teamID+"/nodes", token, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("get nodes: expected 200, got %d", resp.StatusCode)
	}
	body = decodeJSON(t, resp)
	nodes := body["nodes"].([]interface{})
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(nodes))
	}
	node := nodes[0].(map[string]interface{})
	if node["node_id"] != "node-1" {
		t.Fatalf("expected node-1, got %v", node["node_id"])
	}
}
