package notify

import (
	"context"
	"runtime"
	"testing"
	"time"
)

func TestSendOnNonLinux(t *testing.T) {
	// On macOS, this should just log and return nil
	err := Send("Vault", "Test", "Test notification", ImportanceNormal)
	if err != nil {
		t.Errorf("Send() error = %v", err)
	}
}

func TestJobSuccess(t *testing.T) {
	err := JobSuccess("test-job", 5, 1073741824)
	if err != nil {
		t.Errorf("JobSuccess() error = %v", err)
	}
}

func TestJobFailed(t *testing.T) {
	err := JobFailed("test-job", "disk full")
	if err != nil {
		t.Errorf("JobFailed() error = %v", err)
	}
}

// TestRunNotifyCommandSuccess verifies that a fast command (/bin/sh -c "true")
// completes without error. Guards for linux/darwin where /bin/sh is available.
func TestRunNotifyCommandSuccess(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("test requires /bin/sh (linux or darwin)")
	}
	ctx := context.Background()
	if err := runNotifyCommand(ctx, "/bin/sh", []string{"-c", "true"}); err != nil {
		t.Errorf("runNotifyCommand fast-success: unexpected error: %v", err)
	}
}

// TestRunNotifyCommandTimeout verifies that a slow command (sleep 5) is killed
// well before the natural completion when the context deadline fires. This
// proves that CommandContext-based timeout actually cancels the process (fix
// for issue #112: notify helper with no timeout could block indefinitely).
func TestRunNotifyCommandTimeout(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("test requires /bin/sh (linux or darwin)")
	}

	const timeout = 200 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	start := time.Now()
	err := runNotifyCommand(ctx, "/bin/sh", []string{"-c", "sleep 5"})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("runNotifyCommand: expected error when context times out, got nil")
	}
	// Must return well before the 5s sleep completes — allow generous 2s headroom.
	const maxElapsed = 2 * time.Second
	if elapsed > maxElapsed {
		t.Errorf("runNotifyCommand did not respect context timeout: elapsed %v (want < %v)", elapsed, maxElapsed)
	}
}
