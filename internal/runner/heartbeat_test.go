package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHeartbeatWritesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "heartbeat")
	hb := NewHeartbeat(path, "test-v1", 50*time.Millisecond)

	ctx, cancel := context.WithCancel(t.Context())
	done := hb.Start(ctx)
	t.Cleanup(func() {
		cancel()
		waitHeartbeatDone(t, done)
	})

	// Wait for first write.
	deadline := time.Now().Add(500 * time.Millisecond)
	var data []byte
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
			data = b
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(data) == 0 {
		t.Fatalf("heartbeat file never written")
	}
	if !strings.Contains(string(data), "test-v1") {
		t.Errorf("heartbeat missing version: %q", string(data))
	}

	// Capture mtime, wait for next tick, expect mtime to advance.
	fi1, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	time.Sleep(120 * time.Millisecond)
	fi2, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat 2: %v", err)
	}
	if !fi2.ModTime().After(fi1.ModTime()) {
		t.Errorf("heartbeat mtime did not advance")
	}
}

func TestHeartbeatStopsOnContextCancel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hb")
	hb := NewHeartbeat(path, "v", 20*time.Millisecond)
	ticked := make(chan struct{})
	writes := 0
	hb.writeFile = func(path string, data []byte, mode os.FileMode) error {
		err := os.WriteFile(path, data, mode)
		writes++
		if writes == 2 {
			close(ticked)
		}
		return err
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := hb.Start(ctx)
	t.Cleanup(func() {
		cancel()
		waitHeartbeatDone(t, done)
	})
	select {
	case <-ticked:
	case <-time.After(2 * time.Second):
		t.Fatal("heartbeat ticker never wrote before cancellation")
	}
	cancel()
	waitHeartbeatDone(t, done)
}
