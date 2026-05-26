package runner

import (
	"path/filepath"
	"testing"
)

// TestWriteDBOnce_OpenFailure drives the os.Open error branch by pointing
// dbPath at a file that doesn't exist.
func TestWriteDBOnce_OpenFailure(t *testing.T) {
	t.Parallel()

	adapter := newRecordingAdapter()
	missing := filepath.Join(t.TempDir(), "no-such.db")

	if err := writeDBOnce(adapter, missing, "_vault/x", ""); err == nil {
		t.Fatal("expected open error for missing db file")
	}
}

// TestWriteDBOnce_AdapterWriteFails drives the adapter.Write error branch:
// the writeDBOnce succeeds at opening + reading, then the adapter returns
// an error. We do not assert details — just that the wrapper bubbles it.
func TestWriteDBOnce_AdapterWriteFails(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "vault.db")
	if err := writeFileSafe(t, dbPath, []byte("payload")); err != nil {
		t.Fatalf("setup: %v", err)
	}

	adapter := newRecordingAdapter()
	adapter.err = errInjected

	err := writeDBOnce(adapter, dbPath, "_vault/x", "")
	if err == nil {
		t.Fatal("expected error from adapter.Write")
	}
}

// errInjected is a sentinel used by the adapter mock to trigger the
// per-call error path.
var errInjected = injectedErr{"injected adapter write error"}

type injectedErr struct{ msg string }

func (e injectedErr) Error() string { return e.msg }
