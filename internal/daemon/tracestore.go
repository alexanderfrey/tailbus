package daemon

import (
	"sync"
	"time"

	agentpb "github.com/alexanderfrey/tailbus/api/agentpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TraceStore is a concurrent ring buffer that stores trace spans indexed by trace ID.
type TraceStore struct {
	mu       sync.RWMutex
	spans    []*agentpb.TraceSpan
	index    map[string][]int // trace_id -> positions in ring buffer
	capacity int
	head     int // next write position
	count    int // total items written (for detecting wraps)
}

// NewTraceStore creates a new trace store with the given capacity.
func NewTraceStore(capacity int) *TraceStore {
	return &TraceStore{
		spans:    make([]*agentpb.TraceSpan, capacity),
		index:    make(map[string][]int),
		capacity: capacity,
	}
}

// Record stores a trace span in the ring buffer.
func (ts *TraceStore) Record(span *agentpb.TraceSpan) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	// If we're overwriting an existing span, clean up its index entry
	if ts.count >= ts.capacity {
		old := ts.spans[ts.head]
		if old != nil {
			ts.removeIndexEntry(old.TraceId, ts.head)
		}
	}

	ts.spans[ts.head] = span
	ts.index[span.TraceId] = append(ts.index[span.TraceId], ts.head)

	ts.head = (ts.head + 1) % ts.capacity
	ts.count++
}

// RecordSpan is a convenience method that creates and records a TraceSpan.
func (ts *TraceStore) RecordSpan(traceID, messageID, nodeID string, action agentpb.TraceAction, metadata map[string]string) {
	span := &agentpb.TraceSpan{
		TraceId:   traceID,
		MessageId: messageID,
		NodeId:    nodeID,
		Action:    action,
		Timestamp: timestamppb.New(time.Now()),
		Metadata:  metadata,
	}
	ts.Record(span)
}

// GetTrace returns all spans for a given trace ID, ordered by their insertion order.
func (ts *TraceStore) GetTrace(traceID string) []*agentpb.TraceSpan {
	ts.mu.RLock()
	defer ts.mu.RUnlock()

	positions, ok := ts.index[traceID]
	if !ok {
		return nil
	}

	result := make([]*agentpb.TraceSpan, 0, len(positions))
	for _, pos := range positions {
		if ts.spans[pos] != nil && ts.spans[pos].TraceId == traceID {
			result = append(result, ts.spans[pos])
		}
	}
	return result
}

// removeIndexEntry removes a specific position from a trace ID's index.
func (ts *TraceStore) removeIndexEntry(traceID string, pos int) {
	positions := ts.index[traceID]
	for i, p := range positions {
		if p == pos {
			ts.index[traceID] = append(positions[:i], positions[i+1:]...)
			break
		}
	}
	if len(ts.index[traceID]) == 0 {
		delete(ts.index, traceID)
	}
}
