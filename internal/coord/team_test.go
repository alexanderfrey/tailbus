package coord

import (
	"context"
	"log/slog"
	"net"
	"os"
	"testing"
	"time"

	pb "github.com/alexanderfrey/tailbus/api/coordpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// --- Store-level team tests ---

func TestStoreTeamCRUD(t *testing.T) {
	store, cleanup := testStore(t)
	defer cleanup()

	// Create a team
	if err := store.CreateTeam("team-1", "acme-corp", "alice@example.com"); err != nil {
		t.Fatal(err)
	}

	// Duplicate name should fail
	if err := store.CreateTeam("team-2", "acme-corp", "bob@example.com"); err == nil {
		t.Fatal("expected error on duplicate team name")
	}

	// Get team by name
	id, creator, err := store.GetTeamByName("acme-corp")
	if err != nil {
		t.Fatal(err)
	}
	if id != "team-1" || creator != "alice@example.com" {
		t.Fatalf("got id=%q creator=%q", id, creator)
	}

	// Non-existent team
	id, _, err = store.GetTeamByName("nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if id != "" {
		t.Fatalf("expected empty id for nonexistent team, got %q", id)
	}

	// Creator should be owner
	role, err := store.GetUserTeamRole("team-1", "alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if role != "owner" {
		t.Fatalf("expected owner role, got %q", role)
	}

	// Non-member role should be empty
	role, err = store.GetUserTeamRole("team-1", "stranger@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if role != "" {
		t.Fatalf("expected empty role for non-member, got %q", role)
	}
}

func TestStoreTeamMembers(t *testing.T) {
	store, cleanup := testStore(t)
	defer cleanup()

	store.CreateTeam("team-1", "acme", "alice@example.com")
	store.AddTeamMember("team-1", "bob@example.com", "member")

	members, err := store.GetTeamMembers("team-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 2 {
		t.Fatalf("expected 2 members, got %d", len(members))
	}

	// List user teams
	teams, err := store.ListUserTeams("bob@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(teams) != 1 || teams[0].Name != "acme" || teams[0].Role != "member" {
		t.Fatalf("unexpected teams: %+v", teams)
	}
}

func TestStoreTeamInvite(t *testing.T) {
	store, cleanup := testStore(t)
	defer cleanup()

	store.CreateTeam("team-1", "acme", "alice@example.com")

	expires := time.Now().Add(24 * time.Hour)
	if err := store.CreateTeamInvite("ABCD-1234", "team-1", "alice@example.com", expires, 2); err != nil {
		t.Fatal(err)
	}

	// First consume should succeed
	teamID, err := store.ConsumeTeamInvite("ABCD-1234")
	if err != nil {
		t.Fatal(err)
	}
	if teamID != "team-1" {
		t.Fatalf("expected team-1, got %q", teamID)
	}

	// Second consume should succeed (max_uses=2)
	teamID, err = store.ConsumeTeamInvite("ABCD-1234")
	if err != nil {
		t.Fatal(err)
	}
	if teamID != "team-1" {
		t.Fatalf("expected team-1, got %q", teamID)
	}

	// Third consume should fail (max_uses=2, use_count=2)
	_, err = store.ConsumeTeamInvite("ABCD-1234")
	if err == nil {
		t.Fatal("expected error after max uses exceeded")
	}

	// Invalid code
	_, err = store.ConsumeTeamInvite("INVALID")
	if err == nil {
		t.Fatal("expected error for invalid code")
	}
}

func TestStoreTeamInviteExpired(t *testing.T) {
	store, cleanup := testStore(t)
	defer cleanup()

	store.CreateTeam("team-1", "acme", "alice@example.com")

	// Create an already-expired invite
	expires := time.Now().Add(-1 * time.Hour)
	if err := store.CreateTeamInvite("EXPIRED", "team-1", "alice@example.com", expires, 10); err != nil {
		t.Fatal(err)
	}

	_, err := store.ConsumeTeamInvite("EXPIRED")
	if err == nil {
		t.Fatal("expected error for expired invite")
	}
}

func TestStoreGetNodesByTeam(t *testing.T) {
	store, cleanup := testStore(t)
	defer cleanup()

	// Team A nodes
	store.UpsertNode(&NodeRecord{
		NodeID: "node-a1", PublicKey: []byte("pk"), AdvertiseAddr: "10.0.0.1:9443",
		Handles: []string{"calculator"}, LastHeartbeat: time.Now(), TeamID: "team-a",
	})
	store.UpsertNode(&NodeRecord{
		NodeID: "node-a2", PublicKey: []byte("pk"), AdvertiseAddr: "10.0.0.2:9443",
		Handles: []string{"echo"}, LastHeartbeat: time.Now(), TeamID: "team-a",
	})

	// Team B node
	store.UpsertNode(&NodeRecord{
		NodeID: "node-b1", PublicKey: []byte("pk"), AdvertiseAddr: "10.0.0.3:9443",
		Handles: []string{"calculator"}, LastHeartbeat: time.Now(), TeamID: "team-b",
	})

	// Relay (no team)
	store.UpsertNode(&NodeRecord{
		NodeID: "relay-1", PublicKey: []byte("pk"), AdvertiseAddr: "10.0.0.100:7443",
		LastHeartbeat: time.Now(), IsRelay: true,
	})

	// Team A should see 2 nodes + relay
	nodesA, err := store.GetNodesByTeam("team-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(nodesA) != 3 {
		t.Fatalf("team-a: expected 3 nodes (2 + relay), got %d", len(nodesA))
	}

	// Team B should see 1 node + relay
	nodesB, err := store.GetNodesByTeam("team-b")
	if err != nil {
		t.Fatal(err)
	}
	if len(nodesB) != 2 {
		t.Fatalf("team-b: expected 2 nodes (1 + relay), got %d", len(nodesB))
	}

	// Verify handles are loaded
	for _, n := range nodesA {
		if n.NodeID == "node-a1" && len(n.Handles) != 1 {
			t.Fatalf("expected 1 handle for node-a1, got %d", len(n.Handles))
		}
	}
}

func TestStoreLookupHandleInTeam(t *testing.T) {
	store, cleanup := testStore(t)
	defer cleanup()

	// Two teams, both have "calculator"
	store.UpsertNode(&NodeRecord{
		NodeID: "node-a1", PublicKey: []byte("pk"), AdvertiseAddr: "10.0.0.1:9443",
		Handles: []string{"calculator"}, LastHeartbeat: time.Now(), TeamID: "team-a",
	})
	store.UpsertNode(&NodeRecord{
		NodeID: "node-b1", PublicKey: []byte("pk"), AdvertiseAddr: "10.0.0.2:9443",
		Handles: []string{"calculator"}, LastHeartbeat: time.Now(), TeamID: "team-b",
	})

	// Lookup in team-a
	rec, err := store.LookupHandleInTeam("calculator", "team-a")
	if err != nil {
		t.Fatal(err)
	}
	if rec == nil || rec.NodeID != "node-a1" {
		t.Fatalf("expected node-a1, got %v", rec)
	}

	// Lookup in team-b
	rec, err = store.LookupHandleInTeam("calculator", "team-b")
	if err != nil {
		t.Fatal(err)
	}
	if rec == nil || rec.NodeID != "node-b1" {
		t.Fatalf("expected node-b1, got %v", rec)
	}

	// Lookup without team (personal mode) should find one of them
	rec, err = store.LookupHandleInTeam("calculator", "")
	if err != nil {
		t.Fatal(err)
	}
	if rec == nil {
		t.Fatal("expected to find calculator in personal mode")
	}

	// Non-existent handle
	rec, err = store.LookupHandleInTeam("nonexistent", "team-a")
	if err != nil {
		t.Fatal(err)
	}
	if rec != nil {
		t.Fatal("expected nil for nonexistent handle")
	}
}

// --- JWT team tests ---

func TestJWTTeamClaims(t *testing.T) {
	issuer, err := NewJWTIssuer(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}

	access, refresh, err := issuer.IssueWithTeam("alice@example.com", "team-123")
	if err != nil {
		t.Fatal(err)
	}

	claims, err := issuer.Validate(access)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Email != "alice@example.com" {
		t.Fatalf("expected alice@example.com, got %q", claims.Email)
	}
	if claims.TeamID != "team-123" {
		t.Fatalf("expected team-123, got %q", claims.TeamID)
	}

	// Refresh should preserve team
	newAccess, _, err := issuer.Refresh(refresh)
	if err != nil {
		t.Fatal(err)
	}
	newClaims, err := issuer.Validate(newAccess)
	if err != nil {
		t.Fatal(err)
	}
	if newClaims.TeamID != "team-123" {
		t.Fatalf("refresh should preserve team, got %q", newClaims.TeamID)
	}
}

func TestJWTNoTeamBackwardCompat(t *testing.T) {
	issuer, err := NewJWTIssuer(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}

	// Issue without team (standard Issue method)
	access, _, err := issuer.Issue("bob@example.com")
	if err != nil {
		t.Fatal(err)
	}

	claims, err := issuer.Validate(access)
	if err != nil {
		t.Fatal(err)
	}
	if claims.TeamID != "" {
		t.Fatalf("expected empty team_id for no-team token, got %q", claims.TeamID)
	}
}

// --- Admission team tests ---

func TestAdmissionJWTWithTeam(t *testing.T) {
	store, cleanup := testStore(t)
	defer cleanup()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	issuer, err := NewJWTIssuer(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}

	adm := NewAdmission(store, logger)
	adm.SetJWT(issuer)

	access, _, err := issuer.IssueWithTeam("alice@example.com", "team-abc")
	if err != nil {
		t.Fatal(err)
	}

	result, err := adm.ValidateRegistration(access, "node-1")
	if err != nil {
		t.Fatal(err)
	}
	if result.TeamID != "team-abc" {
		t.Fatalf("expected team-abc, got %q", result.TeamID)
	}
}

// --- PeerMap team filtering tests ---

func TestPeerMapBuildForTeam(t *testing.T) {
	store, cleanup := testStore(t)
	defer cleanup()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	pm := NewPeerMap(store, logger)

	// Team A nodes
	store.UpsertNode(&NodeRecord{
		NodeID: "node-a1", PublicKey: []byte("pk"), AdvertiseAddr: "10.0.0.1:9443",
		Handles: []string{"svc-a"}, LastHeartbeat: time.Now(), TeamID: "team-a",
	})
	// Team B nodes
	store.UpsertNode(&NodeRecord{
		NodeID: "node-b1", PublicKey: []byte("pk"), AdvertiseAddr: "10.0.0.2:9443",
		Handles: []string{"svc-b"}, LastHeartbeat: time.Now(), TeamID: "team-b",
	})
	// Relay
	store.UpsertNode(&NodeRecord{
		NodeID: "relay-1", PublicKey: []byte("pk"), AdvertiseAddr: "10.0.0.100:7443",
		LastHeartbeat: time.Now(), IsRelay: true,
	})

	// Build for team-a: should see node-a1 as peer + relay
	update, err := pm.BuildForTeam("team-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(update.Peers) != 1 {
		t.Fatalf("team-a: expected 1 peer, got %d", len(update.Peers))
	}
	if update.Peers[0].NodeId != "node-a1" {
		t.Fatalf("expected node-a1, got %s", update.Peers[0].NodeId)
	}
	if len(update.Relays) != 1 {
		t.Fatalf("expected 1 relay, got %d", len(update.Relays))
	}

	// Build for team-b: should see node-b1 + relay
	update, err = pm.BuildForTeam("team-b")
	if err != nil {
		t.Fatal(err)
	}
	if len(update.Peers) != 1 || update.Peers[0].NodeId != "node-b1" {
		t.Fatalf("team-b: unexpected peers: %v", update.Peers)
	}

	// Build for personal mode (empty team): sees everything
	update, err = pm.BuildForTeam("")
	if err != nil {
		t.Fatal(err)
	}
	if len(update.Peers) != 2 {
		t.Fatalf("personal mode: expected 2 peers, got %d", len(update.Peers))
	}
	if len(update.Relays) != 1 {
		t.Fatalf("personal mode: expected 1 relay, got %d", len(update.Relays))
	}
}

func TestPeerMapTeamScopedBroadcast(t *testing.T) {
	store, cleanup := testStore(t)
	defer cleanup()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	pm := NewPeerMap(store, logger)

	// Register watchers for two teams
	chA := pm.AddWatcher("watcher-a", "team-a")
	defer pm.RemoveWatcher("watcher-a")
	chB := pm.AddWatcher("watcher-b", "team-b")
	defer pm.RemoveWatcher("watcher-b")

	// Add a node to team-a
	store.UpsertNode(&NodeRecord{
		NodeID: "node-a1", PublicKey: []byte("pk"), AdvertiseAddr: "10.0.0.1:9443",
		Handles: []string{"svc-a"}, LastHeartbeat: time.Now(), TeamID: "team-a",
	})

	if err := pm.Broadcast(); err != nil {
		t.Fatal(err)
	}

	// Watcher A should get update with 1 peer
	select {
	case update := <-chA:
		if len(update.Peers) != 1 || update.Peers[0].NodeId != "node-a1" {
			t.Fatalf("watcher-a should see node-a1, got %v", update.Peers)
		}
	default:
		t.Fatal("watcher-a should have received broadcast")
	}

	// Watcher B should also get an update (even if empty, because it's the first broadcast)
	select {
	case update := <-chB:
		if len(update.Peers) != 0 {
			t.Fatalf("watcher-b should see 0 peers, got %d", len(update.Peers))
		}
	default:
		t.Fatal("watcher-b should have received broadcast (first time)")
	}
}

// --- Server-level team RPC tests ---

func testServerWithJWT(t *testing.T) (*Server, pb.CoordinationAPIClient, *JWTIssuer, func()) {
	t.Helper()
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	store, err := NewStore(dir + "/test.db")
	if err != nil {
		t.Fatal(err)
	}

	srv, err := NewServer(store, logger, nil) // nil keypair = insecure for tests
	if err != nil {
		t.Fatal(err)
	}

	issuer, err := NewJWTIssuer(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	srv.SetJWT(issuer)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(lis)

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	client := pb.NewCoordinationAPIClient(conn)

	cleanup := func() {
		conn.Close()
		srv.GracefulStop()
		store.Close()
	}

	return srv, client, issuer, cleanup
}

func TestTeamCreateAndList(t *testing.T) {
	_, client, issuer, cleanup := testServerWithJWT(t)
	defer cleanup()

	ctx := context.Background()
	token, _, _ := issuer.Issue("alice@example.com")

	// Create a team
	resp, err := client.CreateTeam(ctx, &pb.CreateTeamRequest{
		AuthToken: token,
		Name:      "acme-corp",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error != "" {
		t.Fatalf("create team error: %s", resp.Error)
	}
	if resp.Name != "acme-corp" {
		t.Fatalf("expected name acme-corp, got %q", resp.Name)
	}
	teamID := resp.TeamId

	// List teams
	listResp, err := client.ListTeams(ctx, &pb.ListTeamsRequest{AuthToken: token})
	if err != nil {
		t.Fatal(err)
	}
	if len(listResp.Teams) != 1 {
		t.Fatalf("expected 1 team, got %d", len(listResp.Teams))
	}
	if listResp.Teams[0].TeamId != teamID || listResp.Teams[0].Role != "owner" {
		t.Fatalf("unexpected team: %+v", listResp.Teams[0])
	}
}

func TestTeamInviteAndJoin(t *testing.T) {
	_, client, issuer, cleanup := testServerWithJWT(t)
	defer cleanup()

	ctx := context.Background()
	aliceToken, _, _ := issuer.Issue("alice@example.com")
	bobToken, _, _ := issuer.Issue("bob@example.com")

	// Alice creates a team
	createResp, _ := client.CreateTeam(ctx, &pb.CreateTeamRequest{
		AuthToken: aliceToken,
		Name:      "acme-corp",
	})
	teamID := createResp.TeamId

	// Alice creates an invite
	invResp, err := client.CreateTeamInvite(ctx, &pb.CreateTeamInviteRequest{
		AuthToken:  aliceToken,
		TeamId:     teamID,
		MaxUses:    5,
		TtlSeconds: 3600,
	})
	if err != nil {
		t.Fatal(err)
	}
	if invResp.Error != "" {
		t.Fatalf("invite error: %s", invResp.Error)
	}
	code := invResp.Code

	// Bob joins with the invite
	joinResp, err := client.AcceptTeamInvite(ctx, &pb.AcceptTeamInviteRequest{
		AuthToken: bobToken,
		Code:      code,
	})
	if err != nil {
		t.Fatal(err)
	}
	if joinResp.Error != "" {
		t.Fatalf("join error: %s", joinResp.Error)
	}
	if joinResp.TeamId != teamID {
		t.Fatalf("expected team %s, got %s", teamID, joinResp.TeamId)
	}

	// Verify Bob can see members
	memResp, err := client.GetTeamMembers(ctx, &pb.GetTeamMembersRequest{
		AuthToken: bobToken,
		TeamId:    teamID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(memResp.Members) != 2 {
		t.Fatalf("expected 2 members, got %d", len(memResp.Members))
	}
}

func TestTeamInviteNonOwnerRejected(t *testing.T) {
	_, client, issuer, cleanup := testServerWithJWT(t)
	defer cleanup()

	ctx := context.Background()
	aliceToken, _, _ := issuer.Issue("alice@example.com")
	bobToken, _, _ := issuer.Issue("bob@example.com")

	// Alice creates a team
	createResp, _ := client.CreateTeam(ctx, &pb.CreateTeamRequest{
		AuthToken: aliceToken,
		Name:      "acme-corp",
	})

	// Bob (non-member) tries to create an invite — should fail
	invResp, err := client.CreateTeamInvite(ctx, &pb.CreateTeamInviteRequest{
		AuthToken: bobToken,
		TeamId:    createResp.TeamId,
	})
	if err != nil {
		t.Fatal(err)
	}
	if invResp.Error == "" {
		t.Fatal("expected error for non-owner invite creation")
	}
}

func TestTeamScopedRegistration(t *testing.T) {
	_, client, issuer, cleanup := testServerWithJWT(t)
	defer cleanup()

	ctx := context.Background()
	aliceToken, _, _ := issuer.Issue("alice@example.com")

	// Create a team
	createResp, _ := client.CreateTeam(ctx, &pb.CreateTeamRequest{
		AuthToken: aliceToken,
		Name:      "acme",
	})
	teamID := createResp.TeamId

	// Register a node with team scope
	regResp, err := client.RegisterNode(ctx, &pb.RegisterNodeRequest{
		NodeId:        "node-1",
		PublicKey:     []byte("pk1"),
		AdvertiseAddr: "10.0.0.1:9443",
		Handles:       []string{"calculator"},
		AuthToken:     aliceToken,
		TeamId:        teamID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !regResp.Ok {
		t.Fatalf("registration failed: %s", regResp.Error)
	}
	if regResp.TeamId != teamID {
		t.Fatalf("expected team_id %s, got %s", teamID, regResp.TeamId)
	}

	// Non-member should be rejected
	bobToken, _, _ := issuer.Issue("bob@example.com")
	regResp2, err := client.RegisterNode(ctx, &pb.RegisterNodeRequest{
		NodeId:        "node-2",
		PublicKey:     []byte("pk2"),
		AdvertiseAddr: "10.0.0.2:9443",
		AuthToken:     bobToken,
		TeamId:        teamID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if regResp2.Ok {
		t.Fatal("expected rejection for non-member")
	}
}

func TestTeamScopedLookup(t *testing.T) {
	_, client, issuer, cleanup := testServerWithJWT(t)
	defer cleanup()

	ctx := context.Background()

	// Create two teams
	aliceToken, _, _ := issuer.Issue("alice@example.com")
	bobToken, _, _ := issuer.Issue("bob@example.com")

	crA, _ := client.CreateTeam(ctx, &pb.CreateTeamRequest{AuthToken: aliceToken, Name: "team-a"})
	crB, _ := client.CreateTeam(ctx, &pb.CreateTeamRequest{AuthToken: bobToken, Name: "team-b"})

	// Register "calculator" in both teams
	client.RegisterNode(ctx, &pb.RegisterNodeRequest{
		NodeId: "node-a", PublicKey: []byte("pk"), AdvertiseAddr: "10.0.0.1:9443",
		Handles: []string{"calculator"}, AuthToken: aliceToken, TeamId: crA.TeamId,
	})
	client.RegisterNode(ctx, &pb.RegisterNodeRequest{
		NodeId: "node-b", PublicKey: []byte("pk"), AdvertiseAddr: "10.0.0.2:9443",
		Handles: []string{"calculator"}, AuthToken: bobToken, TeamId: crB.TeamId,
	})

	// Lookup in team-a
	lr, err := client.LookupHandle(ctx, &pb.LookupHandleRequest{Handle: "calculator", TeamId: crA.TeamId})
	if err != nil {
		t.Fatal(err)
	}
	if !lr.Found || lr.Peer.NodeId != "node-a" {
		t.Fatalf("expected node-a for team-a lookup, got %v", lr.Peer)
	}

	// Lookup in team-b
	lr, err = client.LookupHandle(ctx, &pb.LookupHandleRequest{Handle: "calculator", TeamId: crB.TeamId})
	if err != nil {
		t.Fatal(err)
	}
	if !lr.Found || lr.Peer.NodeId != "node-b" {
		t.Fatalf("expected node-b for team-b lookup, got %v", lr.Peer)
	}
}

func TestTeamRemoveMember(t *testing.T) {
	_, client, issuer, cleanup := testServerWithJWT(t)
	defer cleanup()

	ctx := context.Background()
	aliceToken, _, _ := issuer.Issue("alice@example.com")
	bobToken, _, _ := issuer.Issue("bob@example.com")

	// Alice creates team, invites Bob
	cr, _ := client.CreateTeam(ctx, &pb.CreateTeamRequest{AuthToken: aliceToken, Name: "acme"})
	inv, _ := client.CreateTeamInvite(ctx, &pb.CreateTeamInviteRequest{
		AuthToken: aliceToken, TeamId: cr.TeamId, MaxUses: 1, TtlSeconds: 3600,
	})
	client.AcceptTeamInvite(ctx, &pb.AcceptTeamInviteRequest{AuthToken: bobToken, Code: inv.Code})

	// Bob (member) can't remove Alice
	resp, _ := client.RemoveTeamMember(ctx, &pb.RemoveTeamMemberRequest{
		AuthToken: bobToken, TeamId: cr.TeamId, Email: "alice@example.com",
	})
	if resp.Error == "" {
		t.Fatal("member should not be able to remove others")
	}

	// Alice can't remove herself (last owner)
	resp, _ = client.RemoveTeamMember(ctx, &pb.RemoveTeamMemberRequest{
		AuthToken: aliceToken, TeamId: cr.TeamId, Email: "alice@example.com",
	})
	if resp.Error == "" {
		t.Fatal("owner should not be able to remove themselves")
	}

	// Alice removes Bob
	resp, _ = client.RemoveTeamMember(ctx, &pb.RemoveTeamMemberRequest{
		AuthToken: aliceToken, TeamId: cr.TeamId, Email: "bob@example.com",
	})
	if resp.Error != "" {
		t.Fatalf("remove bob failed: %s", resp.Error)
	}

	// Verify Bob is gone
	mem, _ := client.GetTeamMembers(ctx, &pb.GetTeamMembersRequest{AuthToken: aliceToken, TeamId: cr.TeamId})
	if len(mem.Members) != 1 {
		t.Fatalf("expected 1 member after removal, got %d", len(mem.Members))
	}
}

func TestTeamUpdateRole(t *testing.T) {
	_, client, issuer, cleanup := testServerWithJWT(t)
	defer cleanup()

	ctx := context.Background()
	aliceToken, _, _ := issuer.Issue("alice@example.com")
	bobToken, _, _ := issuer.Issue("bob@example.com")

	cr, _ := client.CreateTeam(ctx, &pb.CreateTeamRequest{AuthToken: aliceToken, Name: "acme"})
	inv, _ := client.CreateTeamInvite(ctx, &pb.CreateTeamInviteRequest{
		AuthToken: aliceToken, TeamId: cr.TeamId, MaxUses: 1, TtlSeconds: 3600,
	})
	client.AcceptTeamInvite(ctx, &pb.AcceptTeamInviteRequest{AuthToken: bobToken, Code: inv.Code})

	// Promote Bob to owner
	resp, _ := client.UpdateTeamMemberRole(ctx, &pb.UpdateTeamMemberRoleRequest{
		AuthToken: aliceToken, TeamId: cr.TeamId, Email: "bob@example.com", Role: "owner",
	})
	if resp.Error != "" {
		t.Fatalf("promote bob failed: %s", resp.Error)
	}

	// Invalid role should fail
	resp, _ = client.UpdateTeamMemberRole(ctx, &pb.UpdateTeamMemberRoleRequest{
		AuthToken: aliceToken, TeamId: cr.TeamId, Email: "bob@example.com", Role: "admin",
	})
	if resp.Error == "" {
		t.Fatal("invalid role should be rejected")
	}

	// Alice can now demote herself (Bob is also owner)
	resp, _ = client.UpdateTeamMemberRole(ctx, &pb.UpdateTeamMemberRoleRequest{
		AuthToken: aliceToken, TeamId: cr.TeamId, Email: "alice@example.com", Role: "member",
	})
	if resp.Error != "" {
		t.Fatalf("demote alice failed (bob is also owner): %s", resp.Error)
	}

	// Bob is now sole owner — cannot demote self
	resp, _ = client.UpdateTeamMemberRole(ctx, &pb.UpdateTeamMemberRoleRequest{
		AuthToken: bobToken, TeamId: cr.TeamId, Email: "bob@example.com", Role: "member",
	})
	if resp.Error == "" {
		t.Fatal("last owner should not be able to demote themselves")
	}
}

func TestTeamDelete(t *testing.T) {
	_, client, issuer, cleanup := testServerWithJWT(t)
	defer cleanup()

	ctx := context.Background()
	aliceToken, _, _ := issuer.Issue("alice@example.com")
	bobToken, _, _ := issuer.Issue("bob@example.com")

	cr, _ := client.CreateTeam(ctx, &pb.CreateTeamRequest{AuthToken: aliceToken, Name: "acme"})
	inv, _ := client.CreateTeamInvite(ctx, &pb.CreateTeamInviteRequest{
		AuthToken: aliceToken, TeamId: cr.TeamId, MaxUses: 1, TtlSeconds: 3600,
	})
	client.AcceptTeamInvite(ctx, &pb.AcceptTeamInviteRequest{AuthToken: bobToken, Code: inv.Code})

	// Bob (member) can't delete
	resp, _ := client.DeleteTeam(ctx, &pb.DeleteTeamRequest{AuthToken: bobToken, TeamId: cr.TeamId})
	if resp.Error == "" {
		t.Fatal("member should not be able to delete team")
	}

	// Alice (owner) deletes
	resp, _ = client.DeleteTeam(ctx, &pb.DeleteTeamRequest{AuthToken: aliceToken, TeamId: cr.TeamId})
	if resp.Error != "" {
		t.Fatalf("delete team failed: %s", resp.Error)
	}

	// Verify team is gone
	list, _ := client.ListTeams(ctx, &pb.ListTeamsRequest{AuthToken: aliceToken})
	if len(list.Teams) != 0 {
		t.Fatalf("expected 0 teams after deletion, got %d", len(list.Teams))
	}
}
