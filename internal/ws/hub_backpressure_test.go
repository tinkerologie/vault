package ws

import (
	"testing"
	"time"
)

// TestBroadcastLossyDoesNotBlockWhenBufferFull covers the producer side of the
// freeze in issue #256. Hub.Run is not started here, so nothing drains the
// 256-slot buffer — a blocking send would wedge the caller (in production, the
// single backup goroutine) on message 257 forever.
func TestBroadcastLossyDoesNotBlockWhenBufferFull(t *testing.T) {
	h := NewHub()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 10*cap(h.broadcast); i++ {
			h.BroadcastLossy([]byte("progress"))
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("BroadcastLossy blocked with a full buffer and no consumer")
	}

	if got := len(h.broadcast); got != cap(h.broadcast) {
		t.Fatalf("buffered %d messages, want the buffer saturated at %d", got, cap(h.broadcast))
	}
}

// TestBroadcastDeliversAfterProgressFlood is the guard against "fixing" the
// freeze by making every event droppable. Terminal events (run completion,
// item failures, queue changes) cannot be reconstructed by waiting, so losing
// one leaves the UI showing a finished job as still running — the same
// symptom issue #256 reported. A progress flood must not cost the completion
// event that follows it.
func TestBroadcastDeliversAfterProgressFlood(t *testing.T) {
	h := NewHub()

	// Saturate the queue with droppable progress before any consumer runs.
	for i := 0; i < 4*cap(h.broadcast); i++ {
		h.BroadcastLossy([]byte("progress"))
	}
	if len(h.broadcast) != cap(h.broadcast) {
		t.Fatalf("setup: buffer holds %d, want it saturated at %d", len(h.broadcast), cap(h.broadcast))
	}

	go h.Run()
	c := &Client{hub: h, send: make(chan []byte, 8*cap(h.broadcast))}
	h.Register(c)

	// Run's select picks pseudo-randomly between register and the already-
	// buffered broadcasts, so wait until the client is observably registered.
	// Otherwise the terminal event can be fanned out to an empty client set
	// and the test fails for a reason it is not testing.
	regDeadline := time.After(5 * time.Second)
	for {
		h.mu.RLock()
		registered := len(h.clients)
		h.mu.RUnlock()
		if registered == 1 {
			break
		}
		select {
		case <-regDeadline:
			t.Fatal("client never registered")
		default:
			time.Sleep(2 * time.Millisecond)
		}
	}

	sent := make(chan struct{})
	go func() {
		h.Broadcast([]byte("job_run_completed"))
		close(sent)
	}()
	select {
	case <-sent:
	case <-time.After(5 * time.Second):
		t.Fatal("Broadcast never queued the terminal event after a progress flood")
	}

	deadline := time.After(5 * time.Second)
	for {
		select {
		case msg := <-c.send:
			if string(msg) == "job_run_completed" {
				return // delivered — the terminal event survived the flood
			}
		case <-deadline:
			t.Fatal("terminal event was dropped after a progress flood")
		}
	}
}

// TestBroadcastStillDeliversToClients confirms ordinary delivery is intact.
func TestBroadcastStillDeliversToClients(t *testing.T) {
	h := NewHub()
	go h.Run()

	c := &Client{hub: h, send: make(chan []byte, 8)}
	h.Register(c)

	h.Broadcast([]byte("hello"))

	select {
	case msg := <-c.send:
		if string(msg) != "hello" {
			t.Fatalf("got %q, want %q", msg, "hello")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("registered client never received the broadcast")
	}
}
