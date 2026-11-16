package notify

import (
	"context"
	"errors"
	"io/fs"
	"runtime"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func recordingUnraidSender(t *testing.T, wantArgs []string) unraidSender {
	t.Helper()
	called := false
	t.Cleanup(func() {
		if !called {
			t.Error("notify helper was not called")
		}
	})
	return unraidSender{
		goos: "linux",
		stat: func(path string) (fs.FileInfo, error) {
			if path != notifyScriptPath {
				t.Errorf("stat path = %q, want %q", path, notifyScriptPath)
			}
			return nil, nil
		},
		run: func(_ context.Context, path string, args []string) error {
			called = true
			if path != notifyScriptPath || !slices.Equal(args, wantArgs) {
				t.Errorf("helper call = %q %q, want %q %q", path, args, notifyScriptPath, wantArgs)
			}
			return nil
		},
	}
}

func TestUnraidSenderLowFreeAnomaly(t *testing.T) {
	t.Parallel()
	const summary = `"test-dest" is critically low on free space — only 2% left.`
	sender := recordingUnraidSender(t, []string{
		"-e", "Vault", "-s", "Anomaly detected: " + summary,
		"-d", "Free space: 1000 B of 48.8 KB", "-i", "alert",
	})
	if err := sender.sendAnomaly(summary, lowFreeDetails, "critical"); err != nil {
		t.Fatal(err)
	}
}

func TestUnraidSenderDescription(t *testing.T) {
	t.Parallel()
	const subject = `100% complete — "quotes", %s and %%`
	const supplied = "  50% left in \"C:\\test\"\nSize: 2× the limit  "
	for _, tc := range []struct {
		name, description, want string
	}{
		{"empty", "", "No additional details."},
		{"whitespace", " \t\n\u00a0", "No additional details."},
		{"verbatim", supplied, supplied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sender := recordingUnraidSender(t, []string{
				"-e", "Vault", "-s", subject, "-d", tc.want, "-i", "warning",
			})
			if err := sender.send("Vault", subject, tc.description, ImportanceWarning); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUnraidSenderHelperErrors(t *testing.T) {
	t.Parallel()
	execErr := errors.New("helper failed")
	for _, tc := range []struct {
		name, goos, wantMessage string
		statErr, wantErr        error
		wantStat, wantRun       bool
	}{
		{name: "non-Linux", goos: "darwin"},
		{name: "missing helper", goos: "linux", statErr: fs.ErrNotExist, wantStat: true},
		{name: "stat error", goos: "linux", statErr: fs.ErrPermission, wantStat: true, wantErr: fs.ErrPermission, wantMessage: "checking notify helper"},
		{name: "execution error", goos: "linux", wantStat: true, wantRun: true, wantErr: execErr, wantMessage: "send notification"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var statCalled, runCalled bool
			sender := unraidSender{
				goos: tc.goos,
				stat: func(string) (fs.FileInfo, error) {
					statCalled = true
					return nil, tc.statErr
				},
				run: func(context.Context, string, []string) error {
					runCalled = true
					return execErr
				},
			}
			err := sender.send("Vault", "Subject", "Details", ImportanceNormal)
			if !errors.Is(err, tc.wantErr) || (err != nil && !strings.HasPrefix(err.Error(), tc.wantMessage+":")) {
				t.Errorf("send error = %v, want %v with prefix %q", err, tc.wantErr, tc.wantMessage)
			}
			if statCalled != tc.wantStat || runCalled != tc.wantRun {
				t.Errorf("helper stat/run called = %v/%v, want %v/%v", statCalled, runCalled, tc.wantStat, tc.wantRun)
			}
		})
	}
}

func TestUnraidSenderTimeout(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		sender := unraidSender{
			goos: "linux",
			stat: func(string) (fs.FileInfo, error) { return nil, nil },
			run: func(ctx context.Context, _ string, _ []string) error {
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) != 30*time.Second {
					t.Fatalf("helper timeout = %v, want 30s", time.Until(deadline))
				}
				<-ctx.Done()
				return ctx.Err()
			},
		}
		err := sender.send("Vault", "Subject", "Details", ImportanceNormal)
		if !errors.Is(err, context.DeadlineExceeded) || !strings.HasPrefix(err.Error(), "notify helper timed out after 30s:") {
			t.Errorf("send timeout = %v, want wrapped deadline error", err)
		}
	})
}

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
