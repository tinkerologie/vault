package maintlock

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHostLockModes(t *testing.T) {
	SetDirForTest(t.TempDir())
	sharedRetryEvery = time.Millisecond

	daemon, err := AcquireShared(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	replica, err := AcquireShared(context.Background())
	if err != nil {
		t.Fatalf("two shared holders must coexist: %v", err)
	}
	if _, err := AcquireExclusive(); !errors.Is(err, ErrBusy) {
		t.Fatalf("exclusive with a daemon running = %v, want ErrBusy", err)
	}
	daemon.Release()
	daemon.Release()
	if _, err := AcquireExclusive(); !errors.Is(err, ErrBusy) {
		t.Fatalf("exclusive with a replica running = %v, want ErrBusy", err)
	}
	replica.Release()

	gc, err := AcquireExclusive()
	if err != nil {
		t.Fatalf("exclusive with nothing running: %v", err)
	}
	if _, err := AcquireExclusive(); !errors.Is(err, ErrBusy) {
		t.Fatalf("second exclusive = %v, want ErrBusy", err)
	}

	// A daemon starting during maintenance waits, and can be cancelled.
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan error, 1)
	go func() {
		l, err := AcquireShared(ctx)
		if err == nil {
			l.Release()
		}
		got <- err
	}()
	cancel()
	if err := <-got; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled wait = %v", err)
	}

	go func() {
		l, err := AcquireShared(context.Background())
		if err == nil {
			l.Release()
		}
		got <- err
	}()
	time.Sleep(10 * time.Millisecond)
	gc.Release()
	if err := <-got; err != nil {
		t.Fatalf("wait after maintenance ended: %v", err)
	}
}

func TestUnusableLockFailsClosed(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	SetDirForTest(blocked)
	if _, err := AcquireExclusive(); err == nil || errors.Is(err, ErrBusy) {
		t.Fatalf("unusable lock = %v, want a hard error", err)
	}
	if _, err := AcquireShared(context.Background()); err == nil {
		t.Fatal("shared lock on an unusable path succeeded")
	}
}

func TestDestinationLock(t *testing.T) {
	release := HoldDestination(t, 7)
	if _, ok := TryExclusiveDestination(t, 7); ok {
		t.Fatal("deletion allowed during an adoption")
	}
	if rel, ok := TryExclusiveDestination(t, 8); !ok {
		t.Fatal("another destination must be unaffected")
	} else {
		rel()
	}
	release()
	rel, ok := TryExclusiveDestination(t, 7)
	if !ok {
		t.Fatal("deletion refused with no adoption running")
	}
	rel()
}
