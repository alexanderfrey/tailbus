package coord

import (
	"log/slog"
	"sync"
	"sync/atomic"

	pb "github.com/alexanderfrey/tailbus/api/coordpb"
)

// PeerMap builds and distributes peer maps to watching nodes.
type PeerMap struct {
	store   *Store
	logger  *slog.Logger
	version atomic.Int64

	mu       sync.RWMutex
	watchers map[string]chan *pb.PeerMapUpdate // node_id -> update channel
}

// NewPeerMap creates a new peer map manager.
func NewPeerMap(store *Store, logger *slog.Logger) *PeerMap {
	return &PeerMap{
		store:    store,
		logger:   logger,
		watchers: make(map[string]chan *pb.PeerMapUpdate),
	}
}

// Build reads all nodes from the store and creates a PeerMapUpdate.
func (pm *PeerMap) Build() (*pb.PeerMapUpdate, error) {
	nodes, err := pm.store.GetAllNodes()
	if err != nil {
		return nil, err
	}

	var peers []*pb.PeerInfo
	for _, n := range nodes {
		peers = append(peers, &pb.PeerInfo{
			NodeId:              n.NodeID,
			PublicKey:           n.PublicKey,
			AdvertiseAddr:       n.AdvertiseAddr,
			Handles:             n.Handles,
			LastHeartbeatUnix:   n.LastHeartbeat.Unix(),
			HandleDescriptions:  n.HandleDescriptions,
		})
	}

	ver := pm.version.Add(1)
	return &pb.PeerMapUpdate{
		Peers:   peers,
		Version: ver,
	}, nil
}

// AddWatcher registers a node to receive peer map updates.
func (pm *PeerMap) AddWatcher(nodeID string) chan *pb.PeerMapUpdate {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	ch := make(chan *pb.PeerMapUpdate, 8)
	pm.watchers[nodeID] = ch
	pm.logger.Info("watcher added", "node_id", nodeID)
	return ch
}

// RemoveWatcher removes a node from receiving peer map updates.
func (pm *PeerMap) RemoveWatcher(nodeID string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if ch, ok := pm.watchers[nodeID]; ok {
		close(ch)
		delete(pm.watchers, nodeID)
		pm.logger.Info("watcher removed", "node_id", nodeID)
	}
}

// Broadcast sends a peer map update to all watchers.
func (pm *PeerMap) Broadcast() error {
	update, err := pm.Build()
	if err != nil {
		return err
	}

	pm.mu.RLock()
	defer pm.mu.RUnlock()

	for nodeID, ch := range pm.watchers {
		select {
		case ch <- update:
		default:
			pm.logger.Warn("watcher channel full, dropping update", "node_id", nodeID)
		}
	}
	return nil
}
