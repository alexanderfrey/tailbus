package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"

	agentpb "github.com/alexanderfrey/tailbus/api/agentpb"
	messagepb "github.com/alexanderfrey/tailbus/api/messagepb"
	"github.com/alexanderfrey/tailbus/internal/session"
	"github.com/google/uuid"
	"google.golang.org/grpc"
)

// Router is the interface the agent server uses to send outbound messages.
type Router interface {
	Route(ctx context.Context, env *messagepb.Envelope) error
}

// AgentServer is the local gRPC server that agent programs connect to via Unix socket.
type AgentServer struct {
	agentpb.UnimplementedAgentAPIServer

	logger   *slog.Logger
	sessions *session.Store
	router   Router
	grpc     *grpc.Server

	mu              sync.RWMutex
	handles         map[string]bool                          // registered handles on this node
	subscribers     map[string][]chan *agentpb.IncomingMessage // handle -> subscriber channels
	onHandleChange  func(handles []string)                   // called when handles change
}

// NewAgentServer creates a new agent server.
func NewAgentServer(sessions *session.Store, router Router, logger *slog.Logger) *AgentServer {
	s := &AgentServer{
		logger:      logger,
		sessions:    sessions,
		router:      router,
		handles:     make(map[string]bool),
		subscribers: make(map[string][]chan *agentpb.IncomingMessage),
	}

	gs := grpc.NewServer()
	agentpb.RegisterAgentAPIServer(gs, s)
	s.grpc = gs
	return s
}

// ServeUnix starts the gRPC server on a Unix socket.
func (s *AgentServer) ServeUnix(socketPath string) error {
	os.Remove(socketPath)
	lis, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen unix: %w", err)
	}
	s.logger.Info("agent server listening", "socket", socketPath)
	return s.grpc.Serve(lis)
}

// ServeTCP starts the gRPC server on a TCP address (for testing).
func (s *AgentServer) ServeTCP(lis net.Listener) error {
	return s.grpc.Serve(lis)
}

// SetRouter sets the router (used for breaking circular dependency during setup).
func (s *AgentServer) SetRouter(r Router) {
	s.router = r
}

// SetOnHandleChange sets a callback invoked when local handles change.
func (s *AgentServer) SetOnHandleChange(fn func(handles []string)) {
	s.onHandleChange = fn
}

// GracefulStop stops the server gracefully.
func (s *AgentServer) GracefulStop() {
	s.grpc.GracefulStop()
}

// GetHandles returns the list of currently registered handles.
func (s *AgentServer) GetHandles() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []string
	for h := range s.handles {
		result = append(result, h)
	}
	return result
}

// DeliverToLocal delivers an envelope to local subscribers. Returns true if delivered.
func (s *AgentServer) DeliverToLocal(env *messagepb.Envelope) bool {
	s.mu.RLock()
	subs := s.subscribers[env.ToHandle]
	s.mu.RUnlock()

	if len(subs) == 0 {
		return false
	}

	msg := &agentpb.IncomingMessage{Envelope: env}
	for _, ch := range subs {
		select {
		case ch <- msg:
		default:
			s.logger.Warn("subscriber channel full, dropping message", "handle", env.ToHandle)
		}
	}
	return true
}

// HasHandle returns true if the given handle is registered locally.
func (s *AgentServer) HasHandle(h string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.handles[h]
}

// Register registers an agent handle on this node.
func (s *AgentServer) Register(_ context.Context, req *agentpb.RegisterRequest) (*agentpb.RegisterResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.handles[req.Handle] {
		return &agentpb.RegisterResponse{Ok: false, Error: "handle already registered"}, nil
	}

	s.handles[req.Handle] = true
	s.logger.Info("agent registered", "handle", req.Handle)

	// Notify coord about handle change (must copy handles while holding lock)
	if s.onHandleChange != nil {
		handles := make([]string, 0, len(s.handles))
		for h := range s.handles {
			handles = append(handles, h)
		}
		go s.onHandleChange(handles)
	}

	return &agentpb.RegisterResponse{Ok: true}, nil
}

// OpenSession opens a new session and sends the opening message.
func (s *AgentServer) OpenSession(ctx context.Context, req *agentpb.OpenSessionRequest) (*agentpb.OpenSessionResponse, error) {
	sess := session.New(req.FromHandle, req.ToHandle)
	s.sessions.Put(sess)

	msgID := uuid.New().String()
	env := &messagepb.Envelope{
		MessageId:   msgID,
		SessionId:   sess.ID,
		FromHandle:  req.FromHandle,
		ToHandle:    req.ToHandle,
		Payload:     req.Payload,
		ContentType: req.ContentType,
		SentAtUnix:  sess.CreatedAt.Unix(),
		Type:        messagepb.EnvelopeType_ENVELOPE_TYPE_SESSION_OPEN,
	}

	if err := s.router.Route(ctx, env); err != nil {
		return nil, fmt.Errorf("route session open: %w", err)
	}

	s.logger.Info("session opened", "session_id", sess.ID, "from", req.FromHandle, "to", req.ToHandle)
	return &agentpb.OpenSessionResponse{SessionId: sess.ID, MessageId: msgID}, nil
}

// SendMessage sends a message within an existing session.
func (s *AgentServer) SendMessage(ctx context.Context, req *agentpb.SendMessageRequest) (*agentpb.SendMessageResponse, error) {
	sess, ok := s.sessions.Get(req.SessionId)
	if !ok {
		return nil, fmt.Errorf("session %q not found", req.SessionId)
	}

	// Determine recipient
	toHandle := sess.ToHandle
	if req.FromHandle == sess.ToHandle {
		toHandle = sess.FromHandle
	}

	msgID := uuid.New().String()
	env := &messagepb.Envelope{
		MessageId:   msgID,
		SessionId:   req.SessionId,
		FromHandle:  req.FromHandle,
		ToHandle:    toHandle,
		Payload:     req.Payload,
		ContentType: req.ContentType,
		SentAtUnix:  sess.UpdatedAt.Unix(),
		Type:        messagepb.EnvelopeType_ENVELOPE_TYPE_MESSAGE,
	}

	if err := s.router.Route(ctx, env); err != nil {
		return nil, fmt.Errorf("route message: %w", err)
	}

	return &agentpb.SendMessageResponse{MessageId: msgID}, nil
}

// Subscribe opens a stream of incoming messages for a handle.
func (s *AgentServer) Subscribe(req *agentpb.SubscribeRequest, stream agentpb.AgentAPI_SubscribeServer) error {
	ch := make(chan *agentpb.IncomingMessage, 64)

	s.mu.Lock()
	s.subscribers[req.Handle] = append(s.subscribers[req.Handle], ch)
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		subs := s.subscribers[req.Handle]
		for i, sub := range subs {
			if sub == ch {
				s.subscribers[req.Handle] = append(subs[:i], subs[i+1:]...)
				break
			}
		}
		s.mu.Unlock()
	}()

	for {
		select {
		case msg := <-ch:
			if err := stream.Send(msg); err != nil {
				return err
			}
		case <-stream.Context().Done():
			return stream.Context().Err()
		}
	}
}

// ResolveSession resolves (closes) a session with a final message.
func (s *AgentServer) ResolveSession(ctx context.Context, req *agentpb.ResolveSessionRequest) (*agentpb.ResolveSessionResponse, error) {
	sess, ok := s.sessions.Get(req.SessionId)
	if !ok {
		return nil, fmt.Errorf("session %q not found", req.SessionId)
	}

	toHandle := sess.ToHandle
	if req.FromHandle == sess.ToHandle {
		toHandle = sess.FromHandle
	}

	msgID := uuid.New().String()
	env := &messagepb.Envelope{
		MessageId:   msgID,
		SessionId:   req.SessionId,
		FromHandle:  req.FromHandle,
		ToHandle:    toHandle,
		Payload:     req.Payload,
		ContentType: req.ContentType,
		SentAtUnix:  sess.UpdatedAt.Unix(),
		Type:        messagepb.EnvelopeType_ENVELOPE_TYPE_SESSION_RESOLVE,
	}

	if err := s.router.Route(ctx, env); err != nil {
		return nil, fmt.Errorf("route resolve: %w", err)
	}

	if err := sess.Resolve(); err != nil {
		return nil, err
	}

	s.logger.Info("session resolved", "session_id", req.SessionId)
	return &agentpb.ResolveSessionResponse{MessageId: msgID}, nil
}

// ListSessions lists sessions involving a handle.
func (s *AgentServer) ListSessions(_ context.Context, req *agentpb.ListSessionsRequest) (*agentpb.ListSessionsResponse, error) {
	sessions := s.sessions.ListByHandle(req.Handle)
	var infos []*agentpb.SessionInfo
	for _, sess := range sessions {
		infos = append(infos, &agentpb.SessionInfo{
			SessionId:    sess.ID,
			FromHandle:   sess.FromHandle,
			ToHandle:     sess.ToHandle,
			State:        string(sess.State),
			CreatedAtUnix: sess.CreatedAt.Unix(),
			UpdatedAtUnix: sess.UpdatedAt.Unix(),
		})
	}
	return &agentpb.ListSessionsResponse{Sessions: infos}, nil
}
