package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"time"

	messagepb "github.com/alexanderfrey/tailbus/api/messagepb"
	"github.com/alexanderfrey/tailbus/internal/config"
	"github.com/alexanderfrey/tailbus/internal/handle"
	"github.com/alexanderfrey/tailbus/internal/identity"
	"github.com/alexanderfrey/tailbus/internal/session"
	"github.com/alexanderfrey/tailbus/internal/transport"
)

// Daemon is the main node daemon that ties together all components.
type Daemon struct {
	cfg         *config.DaemonConfig
	logger      *slog.Logger
	keypair     *identity.Keypair
	resolver    *handle.Resolver
	sessions    *session.Store
	coordClient *CoordClient
	agentServer *AgentServer
	transport   *transport.GRPCTransport
	router      *MessageRouter
	activity    *ActivityBus
}

// New creates a new daemon from config.
func New(cfg *config.DaemonConfig, logger *slog.Logger) (*Daemon, error) {
	kp, err := identity.LoadOrGenerate(cfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load identity: %w", err)
	}

	resolver := handle.NewResolver()
	sessions := session.NewStore()

	// Create transport
	tp := transport.NewGRPCTransport(logger)

	activity := NewActivityBus()

	d := &Daemon{
		cfg:       cfg,
		logger:    logger,
		keypair:   kp,
		resolver:  resolver,
		sessions:  sessions,
		transport: tp,
		activity:  activity,
	}

	// Agent server needs router, but router needs agent server (for local delivery).
	// Create agent server first with nil router, then set router.
	agentSrv := NewAgentServer(sessions, nil, activity, logger)
	router := NewMessageRouter(resolver, tp, agentSrv, activity, logger)
	agentSrv.router = router
	d.agentServer = agentSrv
	d.router = router

	// Set up transport to deliver received envelopes through the agent server
	tp.OnReceive(func(env *messagepb.Envelope) {
		agentSrv.DeliverToLocal(env)
		activity.MessagesReceivedRemote.Add(1)
	})

	return d, nil
}

// Run starts the daemon and blocks until the context is cancelled.
func (d *Daemon) Run(ctx context.Context) error {
	// Wire dashboard dependencies
	d.agentServer.SetDashboardDeps(d.cfg.NodeID, d.resolver, d.transport)

	// Connect to coord server
	cc, err := NewCoordClient(d.cfg.CoordAddr, d.cfg.NodeID, d.keypair.Public, d.cfg.AdvertiseAddr, d.resolver, d.logger)
	if err != nil {
		return fmt.Errorf("create coord client: %w", err)
	}
	d.coordClient = cc
	defer cc.Close()

	// Register with coord
	if err := cc.Register(ctx, nil); err != nil {
		return fmt.Errorf("register with coord: %w", err)
	}

	// When local handles change, re-register with coord so peer map updates immediately
	d.agentServer.SetOnHandleChange(func(handles []string) {
		if err := cc.Register(ctx, handles); err != nil {
			d.logger.Error("failed to re-register handles with coord", "error", err)
		}
	})

	// Start P2P transport listener
	p2pLis, err := net.Listen("tcp", d.cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("listen P2P: %w", err)
	}
	go d.transport.Serve(p2pLis)

	// Start agent Unix socket server
	go func() {
		if err := d.agentServer.ServeUnix(d.cfg.SocketPath); err != nil {
			d.logger.Error("agent server error", "error", err)
		}
	}()

	// Watch peer map in background
	go func() {
		for {
			if err := cc.WatchPeerMap(ctx); err != nil {
				if ctx.Err() != nil {
					return
				}
				d.logger.Error("peer map watch error, retrying", "error", err)
				time.Sleep(5 * time.Second)
			}
		}
	}()

	// Heartbeat in background
	go cc.Heartbeat(ctx, d.agentServer.GetHandles, 30*time.Second)

	d.logger.Info("daemon running",
		"node_id", d.cfg.NodeID,
		"p2p_addr", d.cfg.ListenAddr,
		"socket", d.cfg.SocketPath,
	)

	<-ctx.Done()
	d.logger.Info("daemon shutting down")
	d.agentServer.GracefulStop()
	d.transport.Close()
	return nil
}

// AgentServer returns the agent server (used for testing).
func (d *Daemon) AgentServer() *AgentServer {
	return d.agentServer
}
