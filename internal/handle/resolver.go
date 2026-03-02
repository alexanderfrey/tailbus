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
	Description   string
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

// GetPeerMap returns a snapshot of the cached peer map.
func (r *Resolver) GetPeerMap() map[string]PeerInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make(map[string]PeerInfo, len(r.handleTo))
	for k, v := range r.handleTo {
		result[k] = v
	}
	return result
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

// GetDescription returns the description for a handle, if known.
func (r *Resolver) GetDescription(handle string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	info, ok := r.handleTo[handle]
	if !ok {
		return "", false
	}
	return info.Description, true
}
