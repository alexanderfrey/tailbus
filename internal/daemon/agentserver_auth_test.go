package daemon

import (
	"context"
	"log/slog"
	"net"
	"os"
	"testing"

	agentpb "github.com/alexanderfrey/tailbus/api/agentpb"
	"github.com/alexanderfrey/tailbus/internal/handle"
	"github.com/alexanderfrey/tailbus/internal/session"
	"github.com/alexanderfrey/tailbus/internal/transport"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// tokenCreds implements grpc.PerRPCCredentials for test clients.
type testTokenCreds struct{ token string }

func (t testTokenCreds) GetRequestMetadata(_ context.Context, _ ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + t.token}, nil
}

func (t testTokenCreds) RequireTransportSecurity() bool { return false }

func startAuthTestServer(t *testing.T, token string) (*AgentServer, string, func()) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	sessions := session.NewStore()
	activity := NewActivityBus()

	resolver := handle.NewResolver()
	tp := transport.NewGRPCTransport(logger, nil, nil)

	srv := NewAgentServer(sessions, nil, activity, logger)
	srv.SetDashboardDeps("test-node", resolver, tp)
	router := NewMessageRouter(resolver, tp, srv, activity, logger)
	srv.SetRouter(router)
	srv.SetAuthToken(token)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.ServeTCP(lis)

	cleanup := func() {
		srv.GracefulStop()
		tp.Close()
	}
	return srv, lis.Addr().String(), cleanup
}

func dialWithToken(t *testing.T, addr, token string) (agentpb.AgentAPIClient, func()) {
	t.Helper()
	opts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	if token != "" {
		opts = append(opts, grpc.WithPerRPCCredentials(testTokenCreds{token: token}))
	}
	conn, err := grpc.NewClient(addr, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return agentpb.NewAgentAPIClient(conn), func() { conn.Close() }
}

func TestAuthRejectWithoutToken(t *testing.T) {
	_, addr, cleanup := startAuthTestServer(t, "secret-token-123")
	defer cleanup()

	client, close := dialWithToken(t, addr, "")
	defer close()

	_, err := client.Register(context.Background(), &agentpb.RegisterRequest{Handle: "alice"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %v", err)
	}
}

func TestAuthAcceptCorrectToken(t *testing.T) {
	_, addr, cleanup := startAuthTestServer(t, "secret-token-123")
	defer cleanup()

	client, close := dialWithToken(t, addr, "secret-token-123")
	defer close()

	resp, err := client.Register(context.Background(), &agentpb.RegisterRequest{Handle: "alice"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Ok {
		t.Fatalf("expected Ok=true, got error: %s", resp.Error)
	}
}

func TestAuthRejectWrongToken(t *testing.T) {
	_, addr, cleanup := startAuthTestServer(t, "secret-token-123")
	defer cleanup()

	client, close := dialWithToken(t, addr, "wrong-token")
	defer close()

	_, err := client.Register(context.Background(), &agentpb.RegisterRequest{Handle: "alice"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %v", err)
	}
}

func TestAuthStreamRejection(t *testing.T) {
	_, addr, cleanup := startAuthTestServer(t, "secret-token-123")
	defer cleanup()

	// First register with correct token so the handle exists
	goodClient, closeGood := dialWithToken(t, addr, "secret-token-123")
	defer closeGood()
	resp, err := goodClient.Register(context.Background(), &agentpb.RegisterRequest{Handle: "bob"})
	if err != nil || !resp.Ok {
		t.Fatalf("register failed: %v %+v", err, resp)
	}

	// Try to subscribe without token
	badClient, closeBad := dialWithToken(t, addr, "")
	defer closeBad()
	stream, err := badClient.Subscribe(context.Background(), &agentpb.SubscribeRequest{Handle: "bob"})
	if err != nil {
		// Some gRPC versions return error on Subscribe call
		if s, ok := status.FromError(err); !ok || s.Code() != codes.Unauthenticated {
			t.Fatalf("expected Unauthenticated, got %v", err)
		}
		return
	}
	// Others return error on first Recv
	_, err = stream.Recv()
	if err == nil {
		t.Fatal("expected error on stream recv, got nil")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %v", err)
	}
}

func TestNoAuthWhenTokenEmpty(t *testing.T) {
	_, addr, cleanup := startAuthTestServer(t, "")
	defer cleanup()

	// No token on client, no token on server → should work
	client, close := dialWithToken(t, addr, "")
	defer close()

	resp, err := client.Register(context.Background(), &agentpb.RegisterRequest{Handle: "charlie"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Ok {
		t.Fatalf("expected Ok=true, got error: %s", resp.Error)
	}
}
