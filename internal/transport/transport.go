package transport

import (
	messagepb "github.com/alexanderfrey/tailbus/api/messagepb"
)

// Transport defines the interface for P2P communication between daemons.
type Transport interface {
	// Send sends an envelope to a peer at the given address.
	Send(addr string, env *messagepb.Envelope) error

	// OnReceive registers a callback for incoming envelopes.
	OnReceive(fn func(*messagepb.Envelope))

	// Close shuts down the transport.
	Close() error
}
