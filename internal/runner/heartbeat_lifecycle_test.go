package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func waitHeartbeatDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("heartbeat writer did not finish")
	}
}

func TestHeartbeatCompletionWaitsForInflightWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "heartbeat")
	hb := NewHeartbeat(path, "joined", time.Hour)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	hb.writeFile = func(path string, data []byte, mode os.FileMode) error {
		close(entered)
		<-release
		return os.WriteFile(path, data, mode)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := hb.Start(ctx)
	t.Cleanup(func() {
		cancel()
		unblock()
		waitHeartbeatDone(t, done)
	})
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("heartbeat never entered the write")
	}
	cancel()
	select {
	case <-done:
		t.Fatal("heartbeat reported completion with a write still in flight")
	default:
	}
	unblock()
	waitHeartbeatDone(t, done)
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "vjoined") {
		t.Fatalf("joined heartbeat: data=%q, err=%v", data, err)
	}
}

func TestHeartbeatCancelledStartPublishesBeforeCompletion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "heartbeat")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	hb := NewHeartbeat(path, "cancelled", time.Hour)
	waitHeartbeatDone(t, hb.Start(ctx))
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "vcancelled") {
		t.Fatalf("initial heartbeat: data=%q, err=%v", data, err)
	}
}
