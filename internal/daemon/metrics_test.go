package daemon

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"log/slog"
	"os"

	"github.com/prometheus/client_golang/prometheus"
)

func TestMetrics_CollectorReadsFromActivityBus(t *testing.T) {
	bus := NewActivityBus()
	m := NewMetrics(bus)

	// Simulate some activity
	bus.EmitMessageRouted("s1", "a", "b", false, "t1", "m1")
	bus.EmitMessageRouted("s1", "a", "b", true, "t1", "m2")
	bus.EmitSessionOpened("s1", "a", "b")
	bus.EmitSessionResolved("s1", "a")
	bus.MessagesReceivedRemote.Add(5)

	// Register and collect
	reg := prometheus.NewRegistry()
	if err := reg.Register(m); err != nil {
		t.Fatal(err)
	}

	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}

	expected := map[string]float64{
		"tailbus_messages_routed_total":          2,
		"tailbus_messages_delivered_local_total":  1,
		"tailbus_messages_sent_remote_total":     1,
		"tailbus_messages_received_remote_total": 5,
		"tailbus_sessions_opened_total":          1,
		"tailbus_sessions_resolved_total":        1,
	}

	found := make(map[string]bool)
	for _, fam := range families {
		if val, ok := expected[fam.GetName()]; ok {
			got := fam.GetMetric()[0].GetCounter().GetValue()
			if got != val {
				t.Errorf("%s = %f, want %f", fam.GetName(), got, val)
			}
			found[fam.GetName()] = true
		}
	}

	for name := range expected {
		if !found[name] {
			t.Errorf("metric %q not found in gathered families", name)
		}
	}
}

func TestMetrics_HistogramObserve(t *testing.T) {
	bus := NewActivityBus()
	m := NewMetrics(bus)

	(*m.MessageRoutingDuration).(prometheus.Histogram).Observe(0.005)
	(*m.MessageRoutingDuration).(prometheus.Histogram).Observe(0.010)
	(*m.SessionLifetime).(prometheus.Histogram).Observe(30.0)

	reg := prometheus.NewRegistry()
	reg.MustRegister(m)

	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}

	foundRouting := false
	foundSession := false
	for _, fam := range families {
		switch fam.GetName() {
		case "tailbus_message_routing_duration_seconds":
			h := fam.GetMetric()[0].GetHistogram()
			if h.GetSampleCount() != 2 {
				t.Errorf("routing histogram count = %d, want 2", h.GetSampleCount())
			}
			foundRouting = true
		case "tailbus_session_lifetime_seconds":
			h := fam.GetMetric()[0].GetHistogram()
			if h.GetSampleCount() != 1 {
				t.Errorf("session histogram count = %d, want 1", h.GetSampleCount())
			}
			foundSession = true
		}
	}

	if !foundRouting {
		t.Error("routing duration histogram not found")
	}
	if !foundSession {
		t.Error("session lifetime histogram not found")
	}
}

func TestMetrics_HTTPEndpoint(t *testing.T) {
	bus := NewActivityBus()
	m := NewMetrics(bus)

	bus.EmitMessageRouted("s1", "a", "b", false, "", "")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	go m.Serve(ctx, "127.0.0.1:0", logger)

	// The Serve method uses a fixed addr so we need to use a port; let's use a higher port
	// Actually, since Serve uses ListenAndServe we need to pick a free port. Let's use a different approach.
	cancel() // Cancel the above

	// Use a specific port for testing
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()

	addr := "127.0.0.1:19091"
	go m.Serve(ctx2, addr, logger)

	// Wait for server to start
	var resp *http.Response
	var err error
	for i := 0; i < 20; i++ {
		time.Sleep(50 * time.Millisecond)
		resp, err = http.Get("http://" + addr + "/metrics")
		if err == nil {
			break
		}
	}
	if err != nil {
		t.Fatalf("failed to reach metrics endpoint: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "tailbus_messages_routed_total") {
		t.Error("response does not contain tailbus_messages_routed_total")
	}
	if !strings.Contains(bodyStr, "tailbus_message_routing_duration_seconds") {
		t.Error("response does not contain tailbus_message_routing_duration_seconds")
	}
}
