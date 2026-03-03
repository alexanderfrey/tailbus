package coord

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	pb "github.com/alexanderfrey/tailbus/api/coordpb"
	messagepb "github.com/alexanderfrey/tailbus/api/messagepb"
	"github.com/alexanderfrey/tailbus/internal/identity"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// Server is the coordination gRPC server.
type Server struct {
	pb.UnimplementedCoordinationAPIServer

	store     *Store
	registry  *Registry
	peerMap   *PeerMap
	admission *Admission
	jwtIssuer *JWTIssuer
	oauth     *OAuthServer
	logger    *slog.Logger
	grpc      *grpc.Server
}

// NewServer creates a new coordination server.
// If kp is non-nil, mTLS is enabled: the server presents its cert and
// requires valid client certs with an Ed25519 pubkey in Organization[0].
func NewServer(store *Store, logger *slog.Logger, kp *identity.Keypair) (*Server, error) {
	registry := NewRegistry(store, logger)
	peerMap := NewPeerMap(store, logger)

	admission := NewAdmission(store, logger)

	s := &Server{
		store:     store,
		registry:  registry,
		peerMap:   peerMap,
		admission: admission,
		logger:    logger,
	}

	var serverOpts []grpc.ServerOption
	if kp != nil {
		cert, err := identity.SelfSignedCert(kp)
		if err != nil {
			return nil, fmt.Errorf("generate coord TLS cert: %w", err)
		}
		tlsCfg := &tls.Config{
			Certificates: []tls.Certificate{cert},
			ClientAuth:   tls.RequireAnyClientCert,
			VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
				if len(rawCerts) == 0 {
					return nil
				}
				// Verify the client cert has a valid pubkey in Organization[0]
				_, err := identity.PubKeyFromCert(rawCerts[0])
				return err
			},
		}
		serverOpts = append(serverOpts, grpc.Creds(credentials.NewTLS(tlsCfg)))
	}

	gs := grpc.NewServer(serverOpts...)
	pb.RegisterCoordinationAPIServer(gs, s)
	s.grpc = gs
	return s, nil
}

// Admission returns the server's admission controller for token seeding.
func (s *Server) Admission() *Admission {
	return s.admission
}

// SetJWT configures JWT issuing/validation on the server and admission controller.
func (s *Server) SetJWT(issuer *JWTIssuer) {
	s.jwtIssuer = issuer
	s.admission.SetJWT(issuer)
}

// SetOAuth configures the OAuth device flow server.
func (s *Server) SetOAuth(oauth *OAuthServer) {
	s.oauth = oauth
}

// HTTPHandler returns an http.Handler for the OAuth routes, or nil if OAuth is not configured.
func (s *Server) HTTPHandler() http.Handler {
	if s.oauth == nil {
		return nil
	}
	return s.oauth.Handler()
}

// Serve starts the gRPC server on the given listener.
func (s *Server) Serve(lis net.Listener) error {
	s.logger.Info("coordination server listening", "addr", lis.Addr())
	return s.grpc.Serve(lis)
}

// StartReaper starts a background goroutine that removes nodes whose last
// heartbeat is older than ttl. It sweeps every interval and broadcasts the
// peer map when stale nodes are removed. The goroutine exits when ctx is cancelled.
func (s *Server) StartReaper(ctx context.Context, ttl, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cutoff := time.Now().Add(-ttl)
				n, err := s.store.RemoveStaleNodes(cutoff)
				if err != nil {
					s.logger.Error("reaper: failed to remove stale nodes", "error", err)
					continue
				}
				if n > 0 {
					s.logger.Info("reaper: evicted stale nodes", "count", n)
					if err := s.peerMap.ForceBroadcast(); err != nil {
						s.logger.Error("reaper: failed to broadcast peer map", "error", err)
					}
				}
			}
		}
	}()
}

// GracefulStop stops the server gracefully.
func (s *Server) GracefulStop() {
	s.grpc.GracefulStop()
}

// synthesizeManifests creates HandleManifests from deprecated HandleDescriptions
// for backward compatibility with old clients.
func synthesizeManifests(descriptions map[string]string) map[string]*messagepb.ServiceManifest {
	if len(descriptions) == 0 {
		return nil
	}
	manifests := make(map[string]*messagepb.ServiceManifest, len(descriptions))
	for h, d := range descriptions {
		if d != "" {
			manifests[h] = &messagepb.ServiceManifest{Description: d}
		}
	}
	return manifests
}

// RegisterNode handles node registration.
func (s *Server) RegisterNode(_ context.Context, req *pb.RegisterNodeRequest) (*pb.RegisterNodeResponse, error) {
	// Admission control: check auth token before allowing registration
	result, err := s.admission.ValidateRegistration(req.AuthToken, req.NodeId)
	if err != nil {
		return &pb.RegisterNodeResponse{Ok: false, Error: err.Error()}, nil
	}

	manifests := req.HandleManifests
	if len(manifests) == 0 {
		manifests = synthesizeManifests(req.HandleDescriptions)
	}

	if err := s.registry.RegisterNode(req.NodeId, req.PublicKey, req.AdvertiseAddr, req.Handles, manifests, req.IsRelay); err != nil {
		return &pb.RegisterNodeResponse{Ok: false, Error: err.Error()}, nil
	}

	// Broadcast updated peer map to all watchers
	if err := s.peerMap.Broadcast(); err != nil {
		s.logger.Error("failed to broadcast peer map", "error", err)
	}

	resp := &pb.RegisterNodeResponse{Ok: true}
	if result != nil && result.Email != "" {
		resp.UserEmail = result.Email
	}

	return resp, nil
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

	// Build deprecated descriptions from manifests
	descs := make(map[string]string, len(rec.HandleManifests))
	for h, m := range rec.HandleManifests {
		if m != nil && m.Description != "" {
			descs[h] = m.Description
		}
	}

	return &pb.LookupHandleResponse{
		Found: true,
		Peer: &pb.PeerInfo{
			NodeId:             rec.NodeID,
			PublicKey:          rec.PublicKey,
			AdvertiseAddr:      rec.AdvertiseAddr,
			Handles:            rec.Handles,
			LastHeartbeatUnix:  rec.LastHeartbeat.Unix(),
			HandleDescriptions: descs,
			HandleManifests:    rec.HandleManifests,
		},
	}, nil
}

// Heartbeat handles node heartbeats.
func (s *Server) Heartbeat(_ context.Context, req *pb.HeartbeatRequest) (*pb.HeartbeatResponse, error) {
	manifests := req.HandleManifests
	if len(manifests) == 0 {
		manifests = synthesizeManifests(req.HandleDescriptions)
	}

	if err := s.registry.Heartbeat(req.NodeId, req.Handles, manifests); err != nil {
		return &pb.HeartbeatResponse{Ok: false}, nil
	}

	// Broadcast in case handles changed
	if err := s.peerMap.Broadcast(); err != nil {
		s.logger.Error("failed to broadcast peer map", "error", err)
	}

	return &pb.HeartbeatResponse{Ok: true}, nil
}
