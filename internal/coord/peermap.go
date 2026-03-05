package coord

import (
	"crypto/sha256"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	pb "github.com/alexanderfrey/tailbus/api/coordpb"
)

// watcherInfo tracks a watcher's channel and team scope.
type watcherInfo struct {
	ch     chan *pb.PeerMapUpdate
	teamID string
}

// PeerMap builds and distributes peer maps to watching nodes.
type PeerMap struct {
	store   *Store
	logger  *slog.Logger
	version atomic.Int64

	mu        sync.RWMutex
	watchers  map[string]*watcherInfo // node_id -> watcher info
	lastHash  map[string]string       // team_id -> last hash ("" key = personal mode)
}

// NewPeerMap creates a new peer map manager.
func NewPeerMap(store *Store, logger *slog.Logger) *PeerMap {
	return &PeerMap{
		store:    store,
		logger:   logger,
		watchers: make(map[string]*watcherInfo),
		lastHash: make(map[string]string),
	}
}

// Build reads all nodes from the store and creates a PeerMapUpdate.
func (pm *PeerMap) Build() (*pb.PeerMapUpdate, error) {
	nodes, err := pm.store.GetAllNodes()
	if err != nil {
		return nil, err
	}
	rooms, err := pm.store.ListRoomsForHandle("", "")
	if err != nil {
		return nil, err
	}

	var peers []*pb.PeerInfo
	var relays []*pb.RelayInfo
	for _, n := range nodes {
		if n.IsRelay {
			relays = append(relays, &pb.RelayInfo{
				NodeId:    n.NodeID,
				PublicKey: n.PublicKey,
				Addr:      n.AdvertiseAddr,
			})
			continue
		}
		// Build deprecated HandleDescriptions from manifests for backward compat
		descs := make(map[string]string, len(n.HandleManifests))
		for h, m := range n.HandleManifests {
			if m != nil && m.Description != "" {
				descs[h] = m.Description
			}
		}
		peers = append(peers, &pb.PeerInfo{
			NodeId:             n.NodeID,
			PublicKey:          n.PublicKey,
			AdvertiseAddr:      n.AdvertiseAddr,
			Handles:            n.Handles,
			LastHeartbeatUnix:  n.LastHeartbeat.Unix(),
			HandleDescriptions: descs,
			HandleManifests:    n.HandleManifests,
		})
	}

	ver := pm.version.Add(1)
	return &pb.PeerMapUpdate{
		Peers:   peers,
		Relays:  relays,
		Rooms:   rooms,
		Version: ver,
	}, nil
}

// peerHash computes a hash of the peer topology (node IDs, addresses, handles)
// and relay info. Deliberately excludes heartbeat timestamps so that heartbeats
// alone don't trigger broadcasts.
func peerHash(peers []*pb.PeerInfo, relays []*pb.RelayInfo) string {
	// Sort by node ID for deterministic hashing
	sorted := make([]*pb.PeerInfo, len(peers))
	copy(sorted, peers)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].NodeId < sorted[j].NodeId
	})

	h := sha256.New()
	for _, p := range sorted {
		handles := make([]string, len(p.Handles))
		copy(handles, p.Handles)
		sort.Strings(handles)
		fmt.Fprintf(h, "%s|%s|%s\n", p.NodeId, p.AdvertiseAddr, strings.Join(handles, ","))
	}

	// Include relay info in hash
	sortedRelays := make([]*pb.RelayInfo, len(relays))
	copy(sortedRelays, relays)
	sort.Slice(sortedRelays, func(i, j int) bool {
		return sortedRelays[i].NodeId < sortedRelays[j].NodeId
	})
	for _, r := range sortedRelays {
		fmt.Fprintf(h, "relay|%s|%s\n", r.NodeId, r.Addr)
	}

	return fmt.Sprintf("%x", h.Sum(nil))
}

// BuildForTeam reads nodes scoped to a team and creates a PeerMapUpdate.
// If teamID is empty (personal mode), returns all nodes.
func (pm *PeerMap) BuildForTeam(teamID string) (*pb.PeerMapUpdate, error) {
	var nodes []*NodeRecord
	var err error
	if teamID == "" {
		nodes, err = pm.store.GetAllNodes()
	} else {
		nodes, err = pm.store.GetNodesByTeam(teamID)
	}
	if err != nil {
		return nil, err
	}
	rooms, err := pm.store.ListRoomsForHandle("", teamID)
	if err != nil {
		return nil, err
	}

	var peers []*pb.PeerInfo
	var relays []*pb.RelayInfo
	for _, n := range nodes {
		if n.IsRelay {
			relays = append(relays, &pb.RelayInfo{
				NodeId:    n.NodeID,
				PublicKey: n.PublicKey,
				Addr:      n.AdvertiseAddr,
			})
			continue
		}
		descs := make(map[string]string, len(n.HandleManifests))
		for h, m := range n.HandleManifests {
			if m != nil && m.Description != "" {
				descs[h] = m.Description
			}
		}
		peers = append(peers, &pb.PeerInfo{
			NodeId:             n.NodeID,
			PublicKey:          n.PublicKey,
			AdvertiseAddr:      n.AdvertiseAddr,
			Handles:            n.Handles,
			LastHeartbeatUnix:  n.LastHeartbeat.Unix(),
			HandleDescriptions: descs,
			HandleManifests:    n.HandleManifests,
		})
	}

	ver := pm.version.Add(1)
	return &pb.PeerMapUpdate{
		Peers:   peers,
		Relays:  relays,
		Rooms:   rooms,
		Version: ver,
	}, nil
}

// AddWatcher registers a node to receive peer map updates scoped to a team.
func (pm *PeerMap) AddWatcher(nodeID string, teamID string) chan *pb.PeerMapUpdate {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	ch := make(chan *pb.PeerMapUpdate, 8)
	pm.watchers[nodeID] = &watcherInfo{ch: ch, teamID: teamID}
	pm.logger.Info("watcher added", "node_id", nodeID, "team_id", teamID)
	return ch
}

// RemoveWatcher removes a node from receiving peer map updates.
func (pm *PeerMap) RemoveWatcher(nodeID string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if w, ok := pm.watchers[nodeID]; ok {
		close(w.ch)
		delete(pm.watchers, nodeID)
		pm.logger.Info("watcher removed", "node_id", nodeID)
	}
}

// Broadcast builds per-team peer map updates and sends to watchers only if
// the topology has changed for that team since the last broadcast.
func (pm *PeerMap) Broadcast() error {
	pm.mu.RLock()
	// Collect unique team IDs from watchers
	teamIDs := make(map[string]struct{})
	for _, w := range pm.watchers {
		teamIDs[w.teamID] = struct{}{}
	}
	pm.mu.RUnlock()

	// Build one update per unique team
	updates := make(map[string]*pb.PeerMapUpdate)
	hashes := make(map[string]string)
	for tid := range teamIDs {
		update, err := pm.BuildForTeam(tid)
		if err != nil {
			return err
		}
		updates[tid] = update
		hashes[tid] = peerHash(update.Peers, update.Relays)
	}

	pm.mu.Lock()
	// Determine which teams actually changed
	changed := make(map[string]bool)
	for tid, hash := range hashes {
		if hash != pm.lastHash[tid] {
			pm.lastHash[tid] = hash
			changed[tid] = true
		}
	}
	pm.mu.Unlock()

	pm.mu.RLock()
	defer pm.mu.RUnlock()
	for nodeID, w := range pm.watchers {
		if !changed[w.teamID] {
			continue
		}
		update := updates[w.teamID]
		select {
		case w.ch <- update:
		default:
			pm.logger.Warn("watcher channel full, dropping update", "node_id", nodeID)
		}
	}
	return nil
}

// ForceBroadcast sends a peer map update unconditionally (bypasses hash check).
// Used by the reaper when it knows data has changed.
func (pm *PeerMap) ForceBroadcast() error {
	pm.mu.RLock()
	teamIDs := make(map[string]struct{})
	for _, w := range pm.watchers {
		teamIDs[w.teamID] = struct{}{}
	}
	pm.mu.RUnlock()

	updates := make(map[string]*pb.PeerMapUpdate)
	for tid := range teamIDs {
		update, err := pm.BuildForTeam(tid)
		if err != nil {
			return err
		}
		updates[tid] = update
		hash := peerHash(update.Peers, update.Relays)
		pm.mu.Lock()
		pm.lastHash[tid] = hash
		pm.mu.Unlock()
	}

	pm.mu.RLock()
	defer pm.mu.RUnlock()
	for nodeID, w := range pm.watchers {
		update := updates[w.teamID]
		select {
		case w.ch <- update:
		default:
			pm.logger.Warn("watcher channel full, dropping update", "node_id", nodeID)
		}
	}
	return nil
}
