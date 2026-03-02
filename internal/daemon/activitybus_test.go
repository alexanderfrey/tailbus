package daemon

import (
	"sync"
	"testing"
	"time"

	agentpb "github.com/alexanderfrey/tailbus/api/agentpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestActivityBus_SubscribeEmitUnsubscribe(t *testing.T) {
	bus := NewActivityBus()

	ch := bus.Subscribe()

	event := &agentpb.ActivityEvent{
		Timestamp: timestamppb.Now(),
		Event: &agentpb.ActivityEvent_HandleRegistered{
			HandleRegistered: &agentpb.HandleRegisteredEvent{Handle: "test"},
		},
	}

	bus.Emit(event)

	select {
	case got := <-ch:
		reg := got.GetHandleRegistered()
		if reg == nil || reg.Handle != "test" {
			t.Fatalf("unexpected event: %v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}

	bus.Unsubscribe(ch)

	// Channel should be closed after unsubscribe
	_, ok := <-ch
	if ok {
		t.Fatal("channel should be closed after unsubscribe")
	}
}

func TestActivityBus_MultipleSubscribers(t *testing.T) {
	bus := NewActivityBus()

	ch1 := bus.Subscribe()
	ch2 := bus.Subscribe()

	bus.EmitHandleRegistered("multi")

	for _, ch := range []chan *agentpb.ActivityEvent{ch1, ch2} {
		select {
		case got := <-ch:
			if got.GetHandleRegistered().Handle != "multi" {
				t.Fatalf("unexpected: %v", got)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out")
		}
	}

	bus.Unsubscribe(ch1)
	bus.Unsubscribe(ch2)
}

func TestActivityBus_NonBlockingEmit(t *testing.T) {
	bus := NewActivityBus()

	ch := bus.Subscribe()

	// Fill the channel buffer (64 events)
	for i := 0; i < 100; i++ {
		bus.EmitHandleRegistered("flood")
	}

	// Should not block or panic — drain what we can
	count := 0
	for {
		select {
		case <-ch:
			count++
		default:
			goto done
		}
	}
done:
	if count == 0 {
		t.Fatal("expected at least some events")
	}
	if count > 64 {
		t.Fatalf("got %d events, expected at most 64 (buffer size)", count)
	}

	bus.Unsubscribe(ch)
}

func TestActivityBus_Counters(t *testing.T) {
	bus := NewActivityBus()

	bus.EmitSessionOpened("s1", "a", "b")
	bus.EmitSessionOpened("s2", "c", "d")
	bus.EmitSessionResolved("s1", "a")
	bus.EmitMessageRouted("s1", "a", "b", false)
	bus.EmitMessageRouted("s2", "c", "d", true)
	bus.EmitMessageRouted("s2", "c", "d", true)
	bus.MessagesReceivedRemote.Add(3)

	c := bus.Counters()

	if c.SessionsOpened != 2 {
		t.Errorf("sessions opened = %d, want 2", c.SessionsOpened)
	}
	if c.SessionsResolved != 1 {
		t.Errorf("sessions resolved = %d, want 1", c.SessionsResolved)
	}
	if c.MessagesRouted != 3 {
		t.Errorf("messages routed = %d, want 3", c.MessagesRouted)
	}
	if c.MessagesDeliveredLocal != 1 {
		t.Errorf("messages delivered local = %d, want 1", c.MessagesDeliveredLocal)
	}
	if c.MessagesSentRemote != 2 {
		t.Errorf("messages sent remote = %d, want 2", c.MessagesSentRemote)
	}
	if c.MessagesReceivedRemote != 3 {
		t.Errorf("messages received remote = %d, want 3", c.MessagesReceivedRemote)
	}
}

func TestActivityBus_ConcurrentEmit(t *testing.T) {
	bus := NewActivityBus()
	ch := bus.Subscribe()

	var wg sync.WaitGroup
	n := 100
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			bus.EmitHandleRegistered("concurrent")
		}()
	}
	wg.Wait()

	// Drain
	count := 0
	for {
		select {
		case <-ch:
			count++
		default:
			goto done
		}
	}
done:
	if count == 0 {
		t.Fatal("expected some events")
	}

	bus.Unsubscribe(ch)
}
