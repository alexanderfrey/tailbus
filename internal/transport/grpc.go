package transport

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"

	messagepb "github.com/alexanderfrey/tailbus/api/messagepb"
	transportpb "github.com/alexanderfrey/tailbus/api/transportpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// GRPCTransport implements Transport using gRPC bidirectional streams.
type GRPCTransport struct {
	transportpb.UnimplementedNodeTransportServer

	logger    *slog.Logger
	grpcSrv   *grpc.Server
	receiveFn func(*messagepb.Envelope)

	mu    sync.RWMutex
	peers map[string]*peerConn // addr -> peer connection
}

type peerConn struct {
	conn   *grpc.ClientConn
	stream transportpb.NodeTransport_ExchangeClient
	mu     sync.Mutex
}

// NewGRPCTransport creates a new gRPC-based transport.
func NewGRPCTransport(logger *slog.Logger) *GRPCTransport {
	t := &GRPCTransport{
		logger: logger,
		peers:  make(map[string]*peerConn),
	}

	gs := grpc.NewServer()
	transportpb.RegisterNodeTransportServer(gs, t)
	t.grpcSrv = gs
	return t
}

// Serve starts listening for incoming peer connections.
func (t *GRPCTransport) Serve(lis net.Listener) error {
	t.logger.Info("P2P transport listening", "addr", lis.Addr())
	return t.grpcSrv.Serve(lis)
}

// OnReceive registers the callback for incoming envelopes.
func (t *GRPCTransport) OnReceive(fn func(*messagepb.Envelope)) {
	t.receiveFn = fn
}

// Send sends an envelope to a peer. Establishes connection lazily.
func (t *GRPCTransport) Send(addr string, env *messagepb.Envelope) error {
	pc, err := t.getOrConnect(addr)
	if err != nil {
		return err
	}

	pc.mu.Lock()
	defer pc.mu.Unlock()

	if err := pc.stream.Send(env); err != nil {
		// Connection broken, remove and retry once
		t.mu.Lock()
		delete(t.peers, addr)
		t.mu.Unlock()
		pc.conn.Close()

		pc2, err := t.connect(addr)
		if err != nil {
			return fmt.Errorf("reconnect to %s: %w", addr, err)
		}
		t.mu.Lock()
		t.peers[addr] = pc2
		t.mu.Unlock()

		pc2.mu.Lock()
		defer pc2.mu.Unlock()
		return pc2.stream.Send(env)
	}
	return nil
}

func (t *GRPCTransport) getOrConnect(addr string) (*peerConn, error) {
	t.mu.RLock()
	pc, ok := t.peers[addr]
	t.mu.RUnlock()
	if ok {
		return pc, nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	// Double-check
	if pc, ok := t.peers[addr]; ok {
		return pc, nil
	}

	pc, err := t.connect(addr)
	if err != nil {
		return nil, err
	}
	t.peers[addr] = pc
	return pc, nil
}

func (t *GRPCTransport) connect(addr string) (*peerConn, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}

	client := transportpb.NewNodeTransportClient(conn)
	stream, err := client.Exchange(context.Background())
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("open exchange stream to %s: %w", addr, err)
	}

	pc := &peerConn{conn: conn, stream: stream}

	// Start receiving on this outbound stream
	go t.recvLoop(addr, stream)

	t.logger.Info("connected to peer", "addr", addr)
	return pc, nil
}

func (t *GRPCTransport) recvLoop(addr string, stream transportpb.NodeTransport_ExchangeClient) {
	for {
		env, err := stream.Recv()
		if err != nil {
			t.logger.Debug("peer stream closed", "addr", addr, "error", err)
			return
		}
		if t.receiveFn != nil {
			t.receiveFn(env)
		}
	}
}

// Exchange handles incoming bidirectional streams from other daemons.
func (t *GRPCTransport) Exchange(stream transportpb.NodeTransport_ExchangeServer) error {
	for {
		env, err := stream.Recv()
		if err != nil {
			return err
		}
		if t.receiveFn != nil {
			t.receiveFn(env)
		}
	}
}

// ConnectedAddrs returns the list of currently connected peer addresses.
func (t *GRPCTransport) ConnectedAddrs() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	addrs := make([]string, 0, len(t.peers))
	for addr := range t.peers {
		addrs = append(addrs, addr)
	}
	return addrs
}

// Close shuts down the transport.
func (t *GRPCTransport) Close() error {
	// Close peer connections first so streams end
	t.mu.Lock()
	for addr, pc := range t.peers {
		pc.conn.Close()
		delete(t.peers, addr)
	}
	t.mu.Unlock()
	// Force stop the server (GracefulStop can hang with open streams)
	t.grpcSrv.Stop()
	return nil
}
