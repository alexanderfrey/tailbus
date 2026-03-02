package daemon

import (
	"context"
	"fmt"
	"log/slog"

	messagepb "github.com/alexanderfrey/tailbus/api/messagepb"
	"github.com/alexanderfrey/tailbus/internal/handle"
	"github.com/alexanderfrey/tailbus/internal/transport"
)

// MessageRouter routes envelopes to local agents or remote daemons.
type MessageRouter struct {
	resolver  *handle.Resolver
	transport transport.Transport
	local     LocalDeliverer
	logger    *slog.Logger
	activity  *ActivityBus
}

// LocalDeliverer delivers messages to local agents.
type LocalDeliverer interface {
	DeliverToLocal(env *messagepb.Envelope) bool
	HasHandle(h string) bool
}

// NewMessageRouter creates a new message router.
func NewMessageRouter(resolver *handle.Resolver, transport transport.Transport, local LocalDeliverer, activity *ActivityBus, logger *slog.Logger) *MessageRouter {
	return &MessageRouter{
		resolver:  resolver,
		transport: transport,
		local:     local,
		logger:    logger,
		activity:  activity,
	}
}

// Route routes an envelope to the appropriate destination.
func (r *MessageRouter) Route(_ context.Context, env *messagepb.Envelope) error {
	// Check if the destination is local
	if r.local.HasHandle(env.ToHandle) {
		if r.local.DeliverToLocal(env) {
			if r.activity != nil {
				r.activity.EmitMessageRouted(env.SessionId, env.FromHandle, env.ToHandle, false)
			}
			return nil
		}
		return fmt.Errorf("handle %q is local but has no subscribers", env.ToHandle)
	}

	// Resolve to a remote peer
	peer, err := r.resolver.Resolve(env.ToHandle)
	if err != nil {
		return fmt.Errorf("resolve handle %q: %w", env.ToHandle, err)
	}

	r.logger.Debug("routing to remote peer", "handle", env.ToHandle, "peer", peer.NodeID, "addr", peer.AdvertiseAddr)
	if err := r.transport.Send(peer.AdvertiseAddr, env); err != nil {
		return err
	}

	if r.activity != nil {
		r.activity.EmitMessageRouted(env.SessionId, env.FromHandle, env.ToHandle, true)
	}
	return nil
}
