package ws

import (
	"testing"
	"time"
)

// TestBroadcastDoesNotBlockWhenBufferFull covers the producer side of the
// freeze in issue #256. Hub.Run is not started here, so nothing drains the
// 256-slot buffer — the old blocking send would wedge the caller (in
// production, the single backup goroutine) on message 257 forever.
func TestBroadcastDoesNotBlockWhenBufferFull(t *testing.T) {
	h := NewHub()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 10*cap(h.broadcast); i++ {
			h.Broadcast([]byte("progress"))
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Broadcast blocked with a full buffer and no consumer")
	}

	if got := len(h.broadcast); got != cap(h.broadcast) {
		t.Fatalf("buffered %d messages, want the buffer saturated at %d", got, cap(h.broadcast))
	}
}

// TestBroadcastStillDeliversToClients confirms the non-blocking send did not
// break normal delivery when a consumer is running.
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
