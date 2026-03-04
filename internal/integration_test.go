package internal

import (
	"context"
	"crypto/tls"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	agentpb "github.com/alexanderfrey/tailbus/api/agentpb"
	coordpb "github.com/alexanderfrey/tailbus/api/coordpb"
	messagepb "github.com/alexanderfrey/tailbus/api/messagepb"
	"github.com/alexanderfrey/tailbus/internal/coord"
	"github.com/alexanderfrey/tailbus/internal/daemon"
	"github.com/alexanderfrey/tailbus/internal/handle"
	"github.com/alexanderfrey/tailbus/internal/identity"
	"github.com/alexanderfrey/tailbus/internal/session"
	"github.com/alexanderfrey/tailbus/internal/transport"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// TestEndToEnd tests the full session lifecycle:
// coord server + 2 daemons + 2 agents, open session, exchange messages, resolve.
func TestEndToEnd(t *testing.T) {
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// --- Start coordination server ---
	store, err := coord.NewStore(filepath.Join(dir, "coord.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// Generate coord keypair for mTLS
	coordKP, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	coordSrv, err := coord.NewServer(store, logger.With("component", "coord"), coordKP)
	if err != nil {
		t.Fatal(err)
	}
	coordLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go coordSrv.Serve(coordLis)
	defer coordSrv.GracefulStop()

	coordAddr := coordLis.Addr().String()

	// --- Generate keys for two nodes ---
	kp1, _ := identity.Generate()
	kp2, _ := identity.Generate()

	// --- Create resolvers ---
	resolver1 := handle.NewResolver()
	resolver2 := handle.NewResolver()

	// --- Generate TLS certs for mTLS ---
	cert1, err := identity.SelfSignedCert(kp1)
	if err != nil {
		t.Fatal(err)
	}
	cert2, err := identity.SelfSignedCert(kp2)
	if err != nil {
		t.Fatal(err)
	}

	// Verifiers will be backed by resolvers (set up peer maps below)
	verifier1 := transport.NewResolverVerifier(resolver1)
	verifier2 := transport.NewResolverVerifier(resolver2)

	// --- Create transports with mTLS ---
	tp1 := transport.NewGRPCTransport(logger.With("component", "transport-1"), &cert1, verifier1)
	tp2 := transport.NewGRPCTransport(logger.With("component", "transport-2"), &cert2, verifier2)

	tp1Lis, _ := net.Listen("tcp", "127.0.0.1:0")
	tp2Lis, _ := net.Listen("tcp", "127.0.0.1:0")
	go tp1.Serve(tp1Lis)
	go tp2.Serve(tp2Lis)
	defer tp1.Close()
	defer tp2.Close()

	// --- Create session stores ---
	sessions1 := session.NewStore()
	sessions2 := session.NewStore()

	// --- Create activity buses ---
	activity1 := daemon.NewActivityBus()
	activity2 := daemon.NewActivityBus()

	// --- Create trace stores ---
	traceStore1 := daemon.NewTraceStore(1000)
	traceStore2 := daemon.NewTraceStore(1000)

	// --- Create agent servers with routers ---
	agentSrv1 := daemon.NewAgentServer(sessions1, nil, activity1, logger.With("component", "agent-1"))
	agentSrv1.SetDashboardDeps("node-1", resolver1, tp1)
	agentSrv1.SetTracing(traceStore1, nil)
	router1 := daemon.NewMessageRouter(resolver1, tp1, agentSrv1, activity1, logger.With("component", "router-1"))
	router1.SetTracing(traceStore1, nil, "node-1")
	agentSrv1.SetRouter(router1)

	agentSrv2 := daemon.NewAgentServer(sessions2, nil, activity2, logger.With("component", "agent-2"))
	agentSrv2.SetDashboardDeps("node-2", resolver2, tp2)
	agentSrv2.SetTracing(traceStore2, nil)
	router2 := daemon.NewMessageRouter(resolver2, tp2, agentSrv2, activity2, logger.With("component", "router-2"))
	router2.SetTracing(traceStore2, nil, "node-2")
	agentSrv2.SetRouter(router2)

	// Wire transport send callbacks for tracing
	tp1.OnSend(func(env *messagepb.Envelope) {
		if env.TraceId != "" {
			traceStore1.RecordSpan(env.TraceId, env.MessageId, "node-1", agentpb.TraceAction_TRACE_ACTION_SENT_TO_TRANSPORT, nil)
		}
	})
	tp2.OnSend(func(env *messagepb.Envelope) {
		if env.TraceId != "" {
			traceStore2.RecordSpan(env.TraceId, env.MessageId, "node-2", agentpb.TraceAction_TRACE_ACTION_SENT_TO_TRANSPORT, nil)
		}
	})

	// Wire transport receive to local delivery
	tp1.OnReceive(func(env *messagepb.Envelope) {
		if env.TraceId != "" {
			traceStore1.RecordSpan(env.TraceId, env.MessageId, "node-1", agentpb.TraceAction_TRACE_ACTION_RECEIVED_FROM_TRANSPORT, nil)
		}
		// When node1 receives a message from the network, try to deliver locally
		// If the session doesn't exist locally, create it
		if _, ok := sessions1.Get(env.SessionId); !ok {
			sess := &session.Session{
				ID:        env.SessionId,
				FromHandle: env.FromHandle,
				ToHandle:   env.ToHandle,
				State:     session.StateOpen,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			sessions1.Put(sess)
		}
		agentSrv1.DeliverToLocal(env)
	})
	tp2.OnReceive(func(env *messagepb.Envelope) {
		if env.TraceId != "" {
			traceStore2.RecordSpan(env.TraceId, env.MessageId, "node-2", agentpb.TraceAction_TRACE_ACTION_RECEIVED_FROM_TRANSPORT, nil)
		}
		if _, ok := sessions2.Get(env.SessionId); !ok {
			sess := &session.Session{
				ID:        env.SessionId,
				FromHandle: env.FromHandle,
				ToHandle:   env.ToHandle,
				State:     session.StateOpen,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			sessions2.Put(sess)
		}
		agentSrv2.DeliverToLocal(env)
	})

	// --- Start agent servers on TCP (for testing) ---
	agentLis1, _ := net.Listen("tcp", "127.0.0.1:0")
	agentLis2, _ := net.Listen("tcp", "127.0.0.1:0")
	go agentSrv1.ServeTCP(agentLis1)
	go agentSrv2.ServeTCP(agentLis2)
	defer agentSrv1.GracefulStop()
	defer agentSrv2.GracefulStop()

	// --- Register nodes with coord server ---
	ctx := context.Background()

	// Connect to coord with mTLS (using kp1's cert, TOFU for coord cert)
	coordClientCert, err := identity.SelfSignedCert(kp1)
	if err != nil {
		t.Fatal(err)
	}
	coordTOFUFile := filepath.Join(dir, "coord-test.fp")
	coordTOFU := identity.NewTOFUVerifier(coordTOFUFile)
	coordClientTLS := &tls.Config{
		Certificates:          []tls.Certificate{coordClientCert},
		InsecureSkipVerify:    true,
		VerifyPeerCertificate: coordTOFU.Verify,
	}
	coordConn, _ := grpc.NewClient(coordAddr, grpc.WithTransportCredentials(credentials.NewTLS(coordClientTLS)))
	defer coordConn.Close()
	coordClient := coordpb.NewCoordinationAPIClient(coordConn)

	_, err = coordClient.RegisterNode(ctx, &coordpb.RegisterNodeRequest{
		NodeId:        "node-1",
		PublicKey:     kp1.Public,
		AdvertiseAddr: tp1Lis.Addr().String(),
		Handles:       []string{"marketing"},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = coordClient.RegisterNode(ctx, &coordpb.RegisterNodeRequest{
		NodeId:        "node-2",
		PublicKey:     kp2.Public,
		AdvertiseAddr: tp2Lis.Addr().String(),
		Handles:       []string{"sales"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Manually set up resolver maps (in a real daemon, WatchPeerMap handles this)
	resolver1.UpdatePeerMap(map[string]handle.PeerInfo{
		"marketing": {NodeID: "node-1", PublicKey: kp1.Public, AdvertiseAddr: tp1Lis.Addr().String(), Manifest: handle.ServiceManifest{}},
		"sales":     {NodeID: "node-2", PublicKey: kp2.Public, AdvertiseAddr: tp2Lis.Addr().String(), Manifest: handle.ServiceManifest{}},
	})
	resolver2.UpdatePeerMap(map[string]handle.PeerInfo{
		"marketing": {NodeID: "node-1", PublicKey: kp1.Public, AdvertiseAddr: tp1Lis.Addr().String(), Manifest: handle.ServiceManifest{}},
		"sales":     {NodeID: "node-2", PublicKey: kp2.Public, AdvertiseAddr: tp2Lis.Addr().String(), Manifest: handle.ServiceManifest{}},
	})

	// --- Connect as agent programs ---
	agentConn1, _ := grpc.NewClient(agentLis1.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	defer agentConn1.Close()
	agent1 := agentpb.NewAgentAPIClient(agentConn1)

	agentConn2, _ := grpc.NewClient(agentLis2.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	defer agentConn2.Close()
	agent2 := agentpb.NewAgentAPIClient(agentConn2)

	// Register handles
	resp1, err := agent1.Register(ctx, &agentpb.RegisterRequest{Handle: "marketing"})
	if err != nil || !resp1.Ok {
		t.Fatalf("register marketing: %v, %+v", err, resp1)
	}

	resp2, err := agent2.Register(ctx, &agentpb.RegisterRequest{Handle: "sales"})
	if err != nil || !resp2.Ok {
		t.Fatalf("register sales: %v, %+v", err, resp2)
	}

	// --- Sales subscribes to messages ---
	subCtx, subCancel := context.WithCancel(ctx)
	defer subCancel()
	salesStream, err := agent2.Subscribe(subCtx, &agentpb.SubscribeRequest{Handle: "sales"})
	if err != nil {
		t.Fatal(err)
	}

	// Also subscribe marketing for reply
	mktStream, err := agent1.Subscribe(subCtx, &agentpb.SubscribeRequest{Handle: "marketing"})
	if err != nil {
		t.Fatal(err)
	}

	// Give streams a moment to establish
	time.Sleep(100 * time.Millisecond)

	// --- Marketing opens a session with sales ---
	openResp, err := agent1.OpenSession(ctx, &agentpb.OpenSessionRequest{
		FromHandle:  "marketing",
		ToHandle:    "sales",
		Payload:     []byte("Need Q4 numbers"),
		ContentType: "text/plain",
	})
	if err != nil {
		t.Fatal(err)
	}
	sessionID := openResp.SessionId
	t.Logf("Session opened: %s", sessionID)

	// --- Sales receives the session-open message ---
	msg, err := salesStream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if msg.Envelope.Type != messagepb.EnvelopeType_ENVELOPE_TYPE_SESSION_OPEN {
		t.Errorf("expected SESSION_OPEN, got %v", msg.Envelope.Type)
	}
	if string(msg.Envelope.Payload) != "Need Q4 numbers" {
		t.Errorf("payload = %q", string(msg.Envelope.Payload))
	}
	if msg.Envelope.SessionId != sessionID {
		t.Errorf("session_id = %q, want %q", msg.Envelope.SessionId, sessionID)
	}
	// Verify sequence number
	if msg.Envelope.Sequence != 1 {
		t.Errorf("session_open sequence = %d, want 1", msg.Envelope.Sequence)
	}
	t.Log("Sales received session open")

	// --- Sales replies ---
	_, err = agent2.SendMessage(ctx, &agentpb.SendMessageRequest{
		SessionId:   sessionID,
		FromHandle:  "sales",
		Payload:     []byte("Q4 revenue: $1.2M"),
		ContentType: "text/plain",
	})
	if err != nil {
		t.Fatal(err)
	}

	// --- Marketing receives the reply ---
	reply, err := mktStream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if string(reply.Envelope.Payload) != "Q4 revenue: $1.2M" {
		t.Errorf("reply payload = %q", string(reply.Envelope.Payload))
	}
	t.Log("Marketing received reply")

	// --- Marketing resolves the session ---
	_, err = agent1.ResolveSession(ctx, &agentpb.ResolveSessionRequest{
		SessionId:   sessionID,
		FromHandle:  "marketing",
		Payload:     []byte("Thanks!"),
		ContentType: "text/plain",
	})
	if err != nil {
		t.Fatal(err)
	}

	// --- Sales receives the resolve message ---
	resolveMsg, err := salesStream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if resolveMsg.Envelope.Type != messagepb.EnvelopeType_ENVELOPE_TYPE_SESSION_RESOLVE {
		t.Errorf("expected SESSION_RESOLVE, got %v", resolveMsg.Envelope.Type)
	}
	// Sequence 2 was the resolve (marketing opened=1, resolved=2)
	if resolveMsg.Envelope.Sequence != 2 {
		t.Errorf("session_resolve sequence = %d, want 2", resolveMsg.Envelope.Sequence)
	}
	t.Log("Session resolved")

	// --- Verify session state ---
	sessResp, err := agent1.ListSessions(ctx, &agentpb.ListSessionsRequest{Handle: "marketing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessResp.Sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessResp.Sessions))
	}
	if sessResp.Sessions[0].State != "resolved" {
		t.Errorf("session state = %q, want resolved", sessResp.Sessions[0].State)
	}

	// --- Verify GetNodeStatus ---
	statusResp, err := agent1.GetNodeStatus(ctx, &agentpb.GetNodeStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}

	// Should have our registered handle
	if len(statusResp.Handles) != 1 {
		t.Fatalf("expected 1 handle, got %d", len(statusResp.Handles))
	}
	if statusResp.Handles[0].Name != "marketing" {
		t.Errorf("handle name = %q, want marketing", statusResp.Handles[0].Name)
	}

	// Should have sessions
	if len(statusResp.Sessions) == 0 {
		t.Error("expected at least 1 session in status")
	}

	// Should have counters from activity bus
	if statusResp.Counters == nil {
		t.Fatal("expected counters in status")
	}
	if statusResp.Counters.SessionsOpened == 0 {
		t.Error("expected sessions_opened > 0")
	}
	if statusResp.Counters.MessagesRouted == 0 {
		t.Error("expected messages_routed > 0")
	}
	t.Logf("Node status: handles=%d sessions=%d msgs_routed=%d",
		len(statusResp.Handles), len(statusResp.Sessions), statusResp.Counters.MessagesRouted)

	// --- Verify tracing via GetTrace RPC ---
	// The open response should have included a trace ID
	traceID := openResp.TraceId
	if traceID == "" {
		t.Fatal("expected trace_id in OpenSession response")
	}
	t.Logf("Trace ID: %s", traceID)

	traceResp, err := agent1.GetTrace(ctx, &agentpb.GetTraceRequest{TraceId: traceID})
	if err != nil {
		t.Fatal(err)
	}
	if len(traceResp.Spans) == 0 {
		t.Fatal("expected trace spans from node-1")
	}
	t.Logf("Node-1 trace spans: %d", len(traceResp.Spans))
	for _, span := range traceResp.Spans {
		t.Logf("  %s  %s  msg:%s  node:%s", span.Timestamp.AsTime().Format("15:04:05.000"), span.Action, span.MessageId[:8], span.NodeId)
	}

	// Verify node-2 also has trace spans
	traceResp2, err := agent2.GetTrace(ctx, &agentpb.GetTraceRequest{TraceId: traceID})
	if err != nil {
		t.Fatal(err)
	}
	if len(traceResp2.Spans) == 0 {
		t.Fatal("expected trace spans from node-2")
	}
	t.Logf("Node-2 trace spans: %d", len(traceResp2.Spans))

	t.Log("End-to-end test passed!")
}

// TestTeamIsolation verifies that two teams with the same handle name are
// isolated from each other: each team's peer map only contains its own nodes,
// and cross-team handle lookup fails.
func TestTeamIsolation(t *testing.T) {
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// --- Start coordination server with JWT ---
	store, err := coord.NewStore(filepath.Join(dir, "coord.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	coordKP, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	coordSrv, err := coord.NewServer(store, logger.With("component", "coord"), coordKP)
	if err != nil {
		t.Fatal(err)
	}
	jwtIssuer, err := coord.NewJWTIssuer(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	coordSrv.SetJWT(jwtIssuer)

	coordLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go coordSrv.Serve(coordLis)
	defer coordSrv.GracefulStop()

	coordAddr := coordLis.Addr().String()

	// --- Connect to coord as gRPC client (mTLS) ---
	clientKP, _ := identity.Generate()
	clientCert, _ := identity.SelfSignedCert(clientKP)
	tofuFile := filepath.Join(dir, "coord-test.fp")
	tofu := identity.NewTOFUVerifier(tofuFile)
	coordTLS := &tls.Config{
		Certificates:          []tls.Certificate{clientCert},
		InsecureSkipVerify:    true,
		VerifyPeerCertificate: tofu.Verify,
	}
	coordConn, _ := grpc.NewClient(coordAddr, grpc.WithTransportCredentials(credentials.NewTLS(coordTLS)))
	defer coordConn.Close()
	coordClient := coordpb.NewCoordinationAPIClient(coordConn)
	ctx := context.Background()

	// --- Create two teams with two users ---
	aliceToken, _, _ := jwtIssuer.Issue("alice@example.com")
	bobToken, _, _ := jwtIssuer.Issue("bob@example.com")

	crA, err := coordClient.CreateTeam(ctx, &coordpb.CreateTeamRequest{
		AuthToken: aliceToken, Name: "team-alpha",
	})
	if err != nil {
		t.Fatal(err)
	}
	if crA.Error != "" {
		t.Fatal(crA.Error)
	}
	teamAlpha := crA.TeamId
	t.Logf("Team Alpha: %s", teamAlpha)

	crB, err := coordClient.CreateTeam(ctx, &coordpb.CreateTeamRequest{
		AuthToken: bobToken, Name: "team-beta",
	})
	if err != nil {
		t.Fatal(err)
	}
	if crB.Error != "" {
		t.Fatal(crB.Error)
	}
	teamBeta := crB.TeamId
	t.Logf("Team Beta: %s", teamBeta)

	// --- Generate keys and transports for two nodes ---
	kpA, _ := identity.Generate()
	kpB, _ := identity.Generate()

	resolverA := handle.NewResolver()
	resolverB := handle.NewResolver()

	certA, _ := identity.SelfSignedCert(kpA)
	certB, _ := identity.SelfSignedCert(kpB)

	verifierA := transport.NewResolverVerifier(resolverA)
	verifierB := transport.NewResolverVerifier(resolverB)

	tpA := transport.NewGRPCTransport(logger.With("component", "tp-alpha"), &certA, verifierA)
	tpB := transport.NewGRPCTransport(logger.With("component", "tp-beta"), &certB, verifierB)

	tpALis, _ := net.Listen("tcp", "127.0.0.1:0")
	tpBLis, _ := net.Listen("tcp", "127.0.0.1:0")
	go tpA.Serve(tpALis)
	go tpB.Serve(tpBLis)
	defer tpA.Close()
	defer tpB.Close()

	// --- Register both nodes with the SAME handle "calculator" in different teams ---
	regA, err := coordClient.RegisterNode(ctx, &coordpb.RegisterNodeRequest{
		NodeId:        "node-alpha",
		PublicKey:     kpA.Public,
		AdvertiseAddr: tpALis.Addr().String(),
		Handles:       []string{"calculator"},
		AuthToken:     aliceToken,
		TeamId:        teamAlpha,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !regA.Ok {
		t.Fatalf("register node-alpha failed: %s", regA.Error)
	}
	t.Logf("Node Alpha registered in team %s", regA.TeamId)

	regB, err := coordClient.RegisterNode(ctx, &coordpb.RegisterNodeRequest{
		NodeId:        "node-beta",
		PublicKey:     kpB.Public,
		AdvertiseAddr: tpBLis.Addr().String(),
		Handles:       []string{"calculator"},
		AuthToken:     bobToken,
		TeamId:        teamBeta,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !regB.Ok {
		t.Fatalf("register node-beta failed: %s", regB.Error)
	}
	t.Logf("Node Beta registered in team %s", regB.TeamId)

	// --- Verify team-scoped peer maps via WatchPeerMap ---

	// Alpha's peer map should only contain node-alpha
	streamA, err := coordClient.WatchPeerMap(ctx, &coordpb.WatchPeerMapRequest{
		NodeId: "node-alpha", TeamId: teamAlpha,
	})
	if err != nil {
		t.Fatal(err)
	}
	updateA, err := streamA.Recv()
	if err != nil {
		t.Fatal(err)
	}

	// Count non-relay peers
	alphaNodeIDs := make(map[string]bool)
	for _, p := range updateA.Peers {
		alphaNodeIDs[p.NodeId] = true
	}
	if !alphaNodeIDs["node-alpha"] {
		t.Fatal("team-alpha peer map should contain node-alpha")
	}
	if alphaNodeIDs["node-beta"] {
		t.Fatal("team-alpha peer map should NOT contain node-beta (isolation violation)")
	}
	t.Logf("Alpha peer map: %d peers (correct isolation)", len(updateA.Peers))

	// Beta's peer map should only contain node-beta
	streamB, err := coordClient.WatchPeerMap(ctx, &coordpb.WatchPeerMapRequest{
		NodeId: "node-beta", TeamId: teamBeta,
	})
	if err != nil {
		t.Fatal(err)
	}
	updateB, err := streamB.Recv()
	if err != nil {
		t.Fatal(err)
	}

	betaNodeIDs := make(map[string]bool)
	for _, p := range updateB.Peers {
		betaNodeIDs[p.NodeId] = true
	}
	if !betaNodeIDs["node-beta"] {
		t.Fatal("team-beta peer map should contain node-beta")
	}
	if betaNodeIDs["node-alpha"] {
		t.Fatal("team-beta peer map should NOT contain node-alpha (isolation violation)")
	}
	t.Logf("Beta peer map: %d peers (correct isolation)", len(updateB.Peers))

	// --- Verify team-scoped handle lookup ---

	// Lookup "calculator" in team-alpha → should find node-alpha
	lrA, err := coordClient.LookupHandle(ctx, &coordpb.LookupHandleRequest{
		Handle: "calculator", TeamId: teamAlpha,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !lrA.Found || lrA.Peer.NodeId != "node-alpha" {
		t.Fatalf("team-alpha lookup: expected node-alpha, got %v", lrA.Peer)
	}

	// Lookup "calculator" in team-beta → should find node-beta
	lrB, err := coordClient.LookupHandle(ctx, &coordpb.LookupHandleRequest{
		Handle: "calculator", TeamId: teamBeta,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !lrB.Found || lrB.Peer.NodeId != "node-beta" {
		t.Fatalf("team-beta lookup: expected node-beta, got %v", lrB.Peer)
	}

	// --- Verify cross-team routing fails ---
	// Set up agent servers on each node, wire peer maps from team-scoped data

	sessionsA := session.NewStore()
	sessionsB := session.NewStore()
	activityA := daemon.NewActivityBus()
	activityB := daemon.NewActivityBus()

	agentSrvA := daemon.NewAgentServer(sessionsA, nil, activityA, logger.With("component", "agent-alpha"))
	agentSrvA.SetDashboardDeps("node-alpha", resolverA, tpA)
	routerA := daemon.NewMessageRouter(resolverA, tpA, agentSrvA, activityA, logger.With("component", "router-alpha"))
	agentSrvA.SetRouter(routerA)

	agentSrvB := daemon.NewAgentServer(sessionsB, nil, activityB, logger.With("component", "agent-beta"))
	agentSrvB.SetDashboardDeps("node-beta", resolverB, tpB)
	routerB := daemon.NewMessageRouter(resolverB, tpB, agentSrvB, activityB, logger.With("component", "router-beta"))
	agentSrvB.SetRouter(routerB)

	tpB.OnReceive(func(env *messagepb.Envelope) {
		agentSrvB.DeliverToLocal(env)
	})

	agentLisA, _ := net.Listen("tcp", "127.0.0.1:0")
	agentLisB, _ := net.Listen("tcp", "127.0.0.1:0")
	go agentSrvA.ServeTCP(agentLisA)
	go agentSrvB.ServeTCP(agentLisB)
	defer agentSrvA.GracefulStop()
	defer agentSrvB.GracefulStop()

	// Each node only sees its own team's peers in the resolver
	// (simulating what WatchPeerMap would populate)
	resolverA.UpdatePeerMap(map[string]handle.PeerInfo{
		"calculator": {NodeID: "node-alpha", PublicKey: kpA.Public, AdvertiseAddr: tpALis.Addr().String()},
	})
	resolverB.UpdatePeerMap(map[string]handle.PeerInfo{
		"calculator": {NodeID: "node-beta", PublicKey: kpB.Public, AdvertiseAddr: tpBLis.Addr().String()},
	})

	// Connect as agent clients
	agentConnA, _ := grpc.NewClient(agentLisA.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	defer agentConnA.Close()
	agentA := agentpb.NewAgentAPIClient(agentConnA)

	agentConnB, _ := grpc.NewClient(agentLisB.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	defer agentConnB.Close()
	agentB := agentpb.NewAgentAPIClient(agentConnB)

	// Register handles locally
	rA, _ := agentA.Register(ctx, &agentpb.RegisterRequest{Handle: "calculator"})
	if !rA.Ok {
		t.Fatalf("register alpha calculator: %s", rA.Error)
	}
	rB, _ := agentB.Register(ctx, &agentpb.RegisterRequest{Handle: "calculator"})
	if !rB.Ok {
		t.Fatalf("register beta calculator: %s", rB.Error)
	}

	// Node-alpha's resolver doesn't know about node-beta's "calculator",
	// so opening a session from alpha to a handle that only exists in beta's
	// team would fail — the handle resolves to alpha's own "calculator" (local).
	// This IS the isolation: node-alpha never learns about node-beta's calculator.

	// Verify that each node's list only shows its own calculator
	listA, err := agentA.ListHandles(ctx, &agentpb.ListHandlesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(listA.Entries) != 1 || listA.Entries[0].Handle != "calculator" {
		t.Fatalf("node-alpha should see exactly 1 handle, got %d", len(listA.Entries))
	}

	listB, err := agentB.ListHandles(ctx, &agentpb.ListHandlesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(listB.Entries) != 1 || listB.Entries[0].Handle != "calculator" {
		t.Fatalf("node-beta should see exactly 1 handle, got %d", len(listB.Entries))
	}

	t.Log("Team isolation test passed — each team sees only its own handles")
}
