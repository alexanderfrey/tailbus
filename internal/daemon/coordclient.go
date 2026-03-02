package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	pb "github.com/alexanderfrey/tailbus/api/coordpb"
	"github.com/alexanderfrey/tailbus/internal/handle"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// CoordClient connects to the coordination server for registration and peer map updates.
type CoordClient struct {
	conn     *grpc.ClientConn
	client   pb.CoordinationAPIClient
	nodeID   string
	pubKey   []byte
	addr     string
	logger   *slog.Logger
	resolver *handle.Resolver
}

// NewCoordClient creates a new coordination client.
func NewCoordClient(coordAddr, nodeID string, pubKey []byte, advertiseAddr string, resolver *handle.Resolver, logger *slog.Logger) (*CoordClient, error) {
	conn, err := grpc.NewClient(coordAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("connect to coord server: %w", err)
	}

	return &CoordClient{
		conn:     conn,
		client:   pb.NewCoordinationAPIClient(conn),
		nodeID:   nodeID,
		pubKey:   pubKey,
		addr:     advertiseAddr,
		logger:   logger,
		resolver: resolver,
	}, nil
}

// Register registers this node with the coordination server.
func (c *CoordClient) Register(ctx context.Context, handles []string) error {
	resp, err := c.client.RegisterNode(ctx, &pb.RegisterNodeRequest{
		NodeId:        c.nodeID,
		PublicKey:     c.pubKey,
		AdvertiseAddr: c.addr,
		Handles:       handles,
	})
	if err != nil {
		return fmt.Errorf("register node: %w", err)
	}
	if !resp.Ok {
		return fmt.Errorf("registration rejected: %s", resp.Error)
	}
	c.logger.Info("registered with coordination server", "node_id", c.nodeID)
	return nil
}

// WatchPeerMap starts watching for peer map updates and updating the resolver.
// Blocks until the context is cancelled.
func (c *CoordClient) WatchPeerMap(ctx context.Context) error {
	stream, err := c.client.WatchPeerMap(ctx, &pb.WatchPeerMapRequest{NodeId: c.nodeID})
	if err != nil {
		return fmt.Errorf("watch peer map: %w", err)
	}

	for {
		update, err := stream.Recv()
		if err != nil {
			return fmt.Errorf("recv peer map: %w", err)
		}

		entries := make(map[string]handle.PeerInfo)
		for _, p := range update.Peers {
			info := handle.PeerInfo{
				NodeID:        p.NodeId,
				PublicKey:     p.PublicKey,
				AdvertiseAddr: p.AdvertiseAddr,
			}
			for _, h := range p.Handles {
				entries[h] = info
			}
		}
		c.resolver.UpdatePeerMap(entries)
		c.logger.Info("peer map updated", "version", update.Version, "peers", len(update.Peers))
	}
}

// Heartbeat sends periodic heartbeats to the coordination server.
// Blocks until the context is cancelled.
func (c *CoordClient) Heartbeat(ctx context.Context, getHandles func() []string, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			handles := getHandles()
			_, err := c.client.Heartbeat(ctx, &pb.HeartbeatRequest{
				NodeId:  c.nodeID,
				Handles: handles,
			})
			if err != nil {
				c.logger.Error("heartbeat failed", "error", err)
			}
		}
	}
}

// Close closes the connection.
func (c *CoordClient) Close() error {
	return c.conn.Close()
}
