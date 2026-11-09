package ws

import (
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
