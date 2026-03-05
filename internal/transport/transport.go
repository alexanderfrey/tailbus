package transport

import (
	transportpb "github.com/alexanderfrey/tailbus/api/transportpb"
)

// Transport defines the interface for P2P communication between daemons.
type Transport interface {
	// Send sends a transport message to a peer at the given address.
	Send(addr string, msg *transportpb.TransportMessage) error

	// OnReceive registers a callback for incoming transport messages.
	OnReceive(fn func(*transportpb.TransportMessage))

	// Close shuts down the transport.
	Close() error
}
