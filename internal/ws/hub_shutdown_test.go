package ws

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
)

func TestStopReleasesBlockedBroadcast(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := NewHub()
		for range cap(h.broadcast) {
			h.Broadcast([]byte("queued"))
		}
		sent := make(chan struct{})
		go func() {
			h.Broadcast([]byte("waiting for room"))
			close(sent)
		}()
		synctest.Wait()
		select {
		case <-sent:
			t.Fatal("reliable broadcast should wait while the hub queue is full")
		default:
		}
		h.Stop()
		h.Stop()
		<-sent
		h.Broadcast([]byte("discard after stop"))
	})
}

func TestStoppedHubRefusesNewClients(t *testing.T) {
	h := NewHub()
	h.Stop()
	h.Run()
	if h.Register(&Client{send: make(chan []byte)}) {
		t.Fatal("a stopped hub admitted a client")
	}
	rec := httptest.NewRecorder()
	h.HandleWS(rec, httptest.NewRequest(http.MethodGet, "/ws", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("upgrade status = %d, want 503", rec.Code)
	}
	h.BroadcastLossy([]byte("discard"))
	if len(h.broadcast) != 0 {
		t.Fatal("a stopped hub queued a lossy broadcast")
	}
}
