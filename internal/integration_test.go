package internal

import (
	"context"
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
	"github.com/alexanderfrey/tailbus/internal/handle"
	"github.com/alexanderfrey/tailbus/internal/identity"
	"github.com/alexanderfrey/tailbus/internal/session"
	"github.com/alexanderfrey/tailbus/internal/daemon"
	"github.com/alexanderfrey/tailbus/internal/transport"
	"google.golang.org/grpc"
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

	coordSrv := coord.NewServer(store, logger.With("component", "coord"))
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

	// --- Create transports ---
	tp1 := transport.NewGRPCTransport(logger.With("component", "transport-1"))
	tp2 := transport.NewGRPCTransport(logger.With("component", "transport-2"))

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

	// --- Create agent servers with routers ---
	agentSrv1 := daemon.NewAgentServer(sessions1, nil, activity1, logger.With("component", "agent-1"))
	router1 := daemon.NewMessageRouter(resolver1, tp1, agentSrv1, activity1, logger.With("component", "router-1"))
	agentSrv1.SetRouter(router1)

	agentSrv2 := daemon.NewAgentServer(sessions2, nil, activity2, logger.With("component", "agent-2"))
	router2 := daemon.NewMessageRouter(resolver2, tp2, agentSrv2, activity2, logger.With("component", "router-2"))
	agentSrv2.SetRouter(router2)

	// Wire transport receive to local delivery
	tp1.OnReceive(func(env *messagepb.Envelope) {
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

	coordConn, _ := grpc.NewClient(coordAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
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
		"marketing": {NodeID: "node-1", PublicKey: kp1.Public, AdvertiseAddr: tp1Lis.Addr().String()},
		"sales":     {NodeID: "node-2", PublicKey: kp2.Public, AdvertiseAddr: tp2Lis.Addr().String()},
	})
	resolver2.UpdatePeerMap(map[string]handle.PeerInfo{
		"marketing": {NodeID: "node-1", PublicKey: kp1.Public, AdvertiseAddr: tp1Lis.Addr().String()},
		"sales":     {NodeID: "node-2", PublicKey: kp2.Public, AdvertiseAddr: tp2Lis.Addr().String()},
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

	t.Log("End-to-end test passed!")
}
