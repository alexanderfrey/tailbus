package handle

import (
	"fmt"
	"sync"
)

// PeerInfo holds the network info for a node that serves a given handle.
type PeerInfo struct {
	NodeID        string
	PublicKey     []byte
	AdvertiseAddr string
}

// Resolver resolves handle names to peer info using a cached peer map.
type Resolver struct {
	mu       sync.RWMutex
	handleTo map[string]PeerInfo // handle name -> peer info
}

// NewResolver creates a new resolver.
func NewResolver() *Resolver {
	return &Resolver{
		handleTo: make(map[string]PeerInfo),
	}
}

// UpdatePeerMap replaces the cached peer map with fresh data.
func (r *Resolver) UpdatePeerMap(entries map[string]PeerInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handleTo = entries
}

// Resolve looks up a handle in the cached peer map.
func (r *Resolver) Resolve(handle string) (PeerInfo, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	info, ok := r.handleTo[handle]
	if !ok {
		return PeerInfo{}, fmt.Errorf("handle %q not found in peer map", handle)
	}
	return info, nil
}
