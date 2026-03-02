package coord

import (
	"context"
	"fmt"
	"log/slog"
	"net"

	pb "github.com/alexanderfrey/tailbus/api/coordpb"
	"google.golang.org/grpc"
)

// Server is the coordination gRPC server.
type Server struct {
	pb.UnimplementedCoordinationAPIServer

	registry *Registry
	peerMap  *PeerMap
	logger   *slog.Logger
	grpc     *grpc.Server
}

// NewServer creates a new coordination server.
func NewServer(store *Store, logger *slog.Logger) *Server {
	registry := NewRegistry(store, logger)
	peerMap := NewPeerMap(store, logger)

	s := &Server{
		registry: registry,
		peerMap:  peerMap,
		logger:   logger,
	}

	gs := grpc.NewServer()
	pb.RegisterCoordinationAPIServer(gs, s)
	s.grpc = gs
	return s
}

// Serve starts the gRPC server on the given listener.
func (s *Server) Serve(lis net.Listener) error {
	s.logger.Info("coordination server listening", "addr", lis.Addr())
	return s.grpc.Serve(lis)
}

// GracefulStop stops the server gracefully.
func (s *Server) GracefulStop() {
	s.grpc.GracefulStop()
}

// RegisterNode handles node registration.
func (s *Server) RegisterNode(_ context.Context, req *pb.RegisterNodeRequest) (*pb.RegisterNodeResponse, error) {
	if err := s.registry.RegisterNode(req.NodeId, req.PublicKey, req.AdvertiseAddr, req.Handles); err != nil {
		return &pb.RegisterNodeResponse{Ok: false, Error: err.Error()}, nil
	}

	// Broadcast updated peer map to all watchers
	if err := s.peerMap.Broadcast(); err != nil {
		s.logger.Error("failed to broadcast peer map", "error", err)
	}

	return &pb.RegisterNodeResponse{Ok: true}, nil
}

// WatchPeerMap streams peer map updates to a node.
func (s *Server) WatchPeerMap(req *pb.WatchPeerMapRequest, stream pb.CoordinationAPI_WatchPeerMapServer) error {
	// Send current peer map immediately
	current, err := s.peerMap.Build()
	if err != nil {
		return fmt.Errorf("build peer map: %w", err)
	}
	if err := stream.Send(current); err != nil {
		return err
	}

	// Watch for updates
	ch := s.peerMap.AddWatcher(req.NodeId)
	defer s.peerMap.RemoveWatcher(req.NodeId)

	for {
		select {
		case update, ok := <-ch:
			if !ok {
				return nil
			}
			if err := stream.Send(update); err != nil {
				return err
			}
		case <-stream.Context().Done():
			return stream.Context().Err()
		}
	}
}

// LookupHandle looks up which node serves a handle.
func (s *Server) LookupHandle(_ context.Context, req *pb.LookupHandleRequest) (*pb.LookupHandleResponse, error) {
	rec, err := s.registry.LookupHandle(req.Handle)
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return &pb.LookupHandleResponse{Found: false}, nil
	}
	return &pb.LookupHandleResponse{
		Found: true,
		Peer: &pb.PeerInfo{
			NodeId:            rec.NodeID,
			PublicKey:         rec.PublicKey,
			AdvertiseAddr:     rec.AdvertiseAddr,
			Handles:           rec.Handles,
			LastHeartbeatUnix: rec.LastHeartbeat.Unix(),
		},
	}, nil
}

// Heartbeat handles node heartbeats.
func (s *Server) Heartbeat(_ context.Context, req *pb.HeartbeatRequest) (*pb.HeartbeatResponse, error) {
	if err := s.registry.Heartbeat(req.NodeId, req.Handles); err != nil {
		return &pb.HeartbeatResponse{Ok: false}, nil
	}

	// Broadcast in case handles changed
	if err := s.peerMap.Broadcast(); err != nil {
		s.logger.Error("failed to broadcast peer map", "error", err)
	}

	return &pb.HeartbeatResponse{Ok: true}, nil
}
