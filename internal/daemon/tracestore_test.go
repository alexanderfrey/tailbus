package daemon

import (
	"fmt"
	"sync"
	"testing"

	agentpb "github.com/alexanderfrey/tailbus/api/agentpb"
)

func TestTraceStore_RecordAndGet(t *testing.T) {
	ts := NewTraceStore(100)

	ts.RecordSpan("trace-1", "msg-1", "node-a", agentpb.TraceAction_TRACE_ACTION_MESSAGE_CREATED, map[string]string{"from": "alice"})
	ts.RecordSpan("trace-1", "msg-1", "node-a", agentpb.TraceAction_TRACE_ACTION_ROUTED_LOCAL, nil)
	ts.RecordSpan("trace-2", "msg-2", "node-b", agentpb.TraceAction_TRACE_ACTION_MESSAGE_CREATED, nil)

	spans := ts.GetTrace("trace-1")
	if len(spans) != 2 {
		t.Fatalf("expected 2 spans for trace-1, got %d", len(spans))
	}
	if spans[0].Action != agentpb.TraceAction_TRACE_ACTION_MESSAGE_CREATED {
		t.Errorf("span[0] action = %v, want MESSAGE_CREATED", spans[0].Action)
	}
	if spans[1].Action != agentpb.TraceAction_TRACE_ACTION_ROUTED_LOCAL {
		t.Errorf("span[1] action = %v, want ROUTED_LOCAL", spans[1].Action)
	}
	if spans[0].Metadata["from"] != "alice" {
		t.Errorf("span[0] metadata[from] = %q, want alice", spans[0].Metadata["from"])
	}

	spans2 := ts.GetTrace("trace-2")
	if len(spans2) != 1 {
		t.Fatalf("expected 1 span for trace-2, got %d", len(spans2))
	}

	// Non-existent trace
	spans3 := ts.GetTrace("nonexistent")
	if len(spans3) != 0 {
		t.Fatalf("expected 0 spans for nonexistent, got %d", len(spans3))
	}
}

func TestTraceStore_RingBufferEviction(t *testing.T) {
	ts := NewTraceStore(5)

	// Fill buffer
	for i := 0; i < 5; i++ {
		ts.RecordSpan("old-trace", fmt.Sprintf("msg-%d", i), "node", agentpb.TraceAction_TRACE_ACTION_MESSAGE_CREATED, nil)
	}

	spans := ts.GetTrace("old-trace")
	if len(spans) != 5 {
		t.Fatalf("expected 5 spans, got %d", len(spans))
	}

	// Add 3 more, which should evict the first 3
	for i := 0; i < 3; i++ {
		ts.RecordSpan("new-trace", fmt.Sprintf("new-msg-%d", i), "node", agentpb.TraceAction_TRACE_ACTION_ROUTED_REMOTE, nil)
	}

	// old-trace should have 2 remaining spans
	oldSpans := ts.GetTrace("old-trace")
	if len(oldSpans) != 2 {
		t.Fatalf("expected 2 remaining spans for old-trace, got %d", len(oldSpans))
	}

	// new-trace should have all 3
	newSpans := ts.GetTrace("new-trace")
	if len(newSpans) != 3 {
		t.Fatalf("expected 3 spans for new-trace, got %d", len(newSpans))
	}
}

func TestTraceStore_FullEviction(t *testing.T) {
	ts := NewTraceStore(3)

	ts.RecordSpan("a", "m1", "n", agentpb.TraceAction_TRACE_ACTION_MESSAGE_CREATED, nil)
	ts.RecordSpan("a", "m2", "n", agentpb.TraceAction_TRACE_ACTION_ROUTED_LOCAL, nil)
	ts.RecordSpan("a", "m3", "n", agentpb.TraceAction_TRACE_ACTION_DELIVERED_TO_SUBSCRIBER, nil)

	// Completely overwrite all of trace "a"
	ts.RecordSpan("b", "m4", "n", agentpb.TraceAction_TRACE_ACTION_MESSAGE_CREATED, nil)
	ts.RecordSpan("b", "m5", "n", agentpb.TraceAction_TRACE_ACTION_ROUTED_LOCAL, nil)
	ts.RecordSpan("b", "m6", "n", agentpb.TraceAction_TRACE_ACTION_DELIVERED_TO_SUBSCRIBER, nil)

	// "a" should be completely gone
	if spans := ts.GetTrace("a"); len(spans) != 0 {
		t.Fatalf("expected 0 spans for evicted trace a, got %d", len(spans))
	}

	// "b" should have all 3
	if spans := ts.GetTrace("b"); len(spans) != 3 {
		t.Fatalf("expected 3 spans for trace b, got %d", len(spans))
	}
}

func TestTraceStore_ConcurrentAccess(t *testing.T) {
	ts := NewTraceStore(1000)
	var wg sync.WaitGroup

	// Concurrent writers
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			traceID := fmt.Sprintf("trace-%d", id)
			for j := 0; j < 50; j++ {
				ts.RecordSpan(traceID, fmt.Sprintf("msg-%d-%d", id, j), "node", agentpb.TraceAction_TRACE_ACTION_MESSAGE_CREATED, nil)
			}
		}(i)
	}

	// Concurrent readers
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				ts.GetTrace(fmt.Sprintf("trace-%d", id))
			}
		}(i)
	}

	wg.Wait()

	// Verify each trace has its spans (all 500 fit in capacity 1000)
	for i := 0; i < 10; i++ {
		spans := ts.GetTrace(fmt.Sprintf("trace-%d", i))
		if len(spans) != 50 {
			t.Errorf("trace-%d: expected 50 spans, got %d", i, len(spans))
		}
	}
}
