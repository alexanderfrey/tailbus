package coord

import (
	"context"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"

	pb "github.com/alexanderfrey/tailbus/api/coordpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func testServer(t *testing.T) (pb.CoordinationAPIClient, func()) {
	t.Helper()
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	store, err := NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}

	srv := NewServer(store, logger)

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

	return client, cleanup
}

func TestRegisterAndLookup(t *testing.T) {
	client, cleanup := testServer(t)
	defer cleanup()

	ctx := context.Background()

	// Register a node with handles
	resp, err := client.RegisterNode(ctx, &pb.RegisterNodeRequest{
		NodeId:        "node-1",
		PublicKey:     []byte("pubkey1"),
		AdvertiseAddr: "10.0.0.1:9443",
		Handles:       []string{"marketing", "sales"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Ok {
		t.Fatalf("registration failed: %s", resp.Error)
	}

	// Lookup handle
	lr, err := client.LookupHandle(ctx, &pb.LookupHandleRequest{Handle: "marketing"})
	if err != nil {
		t.Fatal(err)
	}
	if !lr.Found {
		t.Fatal("handle not found")
	}
	if lr.Peer.NodeId != "node-1" {
		t.Errorf("node_id = %q, want node-1", lr.Peer.NodeId)
	}
	if lr.Peer.AdvertiseAddr != "10.0.0.1:9443" {
		t.Errorf("addr = %q", lr.Peer.AdvertiseAddr)
	}

	// Lookup non-existent
	lr, err = client.LookupHandle(ctx, &pb.LookupHandleRequest{Handle: "nonexistent"})
	if err != nil {
		t.Fatal(err)
	}
	if lr.Found {
		t.Error("expected not found")
	}
}

func TestWatchPeerMap(t *testing.T) {
	client, cleanup := testServer(t)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Register first node
	_, err := client.RegisterNode(ctx, &pb.RegisterNodeRequest{
		NodeId:        "node-1",
		PublicKey:     []byte("pk1"),
		AdvertiseAddr: "10.0.0.1:9443",
		Handles:       []string{"marketing"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Start watching
	stream, err := client.WatchPeerMap(ctx, &pb.WatchPeerMapRequest{NodeId: "node-1"})
	if err != nil {
		t.Fatal(err)
	}

	// Should get initial peer map
	update, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if len(update.Peers) != 1 {
		t.Fatalf("expected 1 peer, got %d", len(update.Peers))
	}
	if update.Peers[0].NodeId != "node-1" {
		t.Errorf("peer node_id = %q", update.Peers[0].NodeId)
	}

	// Register second node — should trigger update
	_, err = client.RegisterNode(ctx, &pb.RegisterNodeRequest{
		NodeId:        "node-2",
		PublicKey:     []byte("pk2"),
		AdvertiseAddr: "10.0.0.2:9443",
		Handles:       []string{"sales"},
	})
	if err != nil {
		t.Fatal(err)
	}

	update, err = stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if len(update.Peers) != 2 {
		t.Fatalf("expected 2 peers after second registration, got %d", len(update.Peers))
	}
}

func TestHeartbeat(t *testing.T) {
	client, cleanup := testServer(t)
	defer cleanup()

	ctx := context.Background()

	// Register
	_, err := client.RegisterNode(ctx, &pb.RegisterNodeRequest{
		NodeId:        "node-1",
		PublicKey:     []byte("pk1"),
		AdvertiseAddr: "10.0.0.1:9443",
		Handles:       []string{"marketing"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Heartbeat with updated handles
	hr, err := client.Heartbeat(ctx, &pb.HeartbeatRequest{
		NodeId:  "node-1",
		Handles: []string{"marketing", "analytics"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !hr.Ok {
		t.Error("heartbeat failed")
	}

	// Verify updated handles
	lr, err := client.LookupHandle(ctx, &pb.LookupHandleRequest{Handle: "analytics"})
	if err != nil {
		t.Fatal(err)
	}
	if !lr.Found {
		t.Error("analytics handle not found after heartbeat update")
	}
}
