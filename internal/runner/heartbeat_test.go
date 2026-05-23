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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hb.Start(ctx)

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
	dir := t.TempDir()
	path := filepath.Join(dir, "hb")
	hb := NewHeartbeat(path, "v", 20*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	hb.Start(ctx)
	time.Sleep(60 * time.Millisecond)
	cancel()

	// Wait for the in-flight tick to finish + a margin.
	time.Sleep(40 * time.Millisecond)
	fi1, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	time.Sleep(80 * time.Millisecond)
	fi2, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat 2: %v", err)
	}
	if fi2.ModTime().After(fi1.ModTime()) {
		t.Errorf("heartbeat continued after cancel")
	}
}
