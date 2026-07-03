package storage

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestRemoveEmptyDirThroughWrappedChain pins the #168 fix: the adapter
// returned by NewAdapter is wrapped in throttle/retry/metrics/logging, and
// the chain must still expose RemoveEmptyDir so the runner's empty-parent
// sweep can reach the provider. Before the fix the capability assertion
// failed on every wrapped adapter and the sweep silently no-oped.
func TestRemoveEmptyDirThroughWrappedChain(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	sub := filepath.Join(base, "job", "run")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	a, err := NewAdapter("local", `{"path":"`+base+`"}`)
	if err != nil {
		t.Fatalf("NewAdapter: %v", err)
	}
	defer CloseAdapter(a)

	dr, ok := a.(interface{ RemoveEmptyDir(string) error })
	if !ok {
		t.Fatal("wrapped adapter chain does not expose RemoveEmptyDir — the empty-dir sweep is dead again (#168)")
	}

	// Empty dir: removed.
	if err := dr.RemoveEmptyDir("job/run"); err != nil {
		t.Fatalf("RemoveEmptyDir(empty): %v", err)
	}
	if _, err := os.Stat(sub); !os.IsNotExist(err) {
		t.Fatal("empty directory was not removed through the chain")
	}

	// Non-empty dir: refused (the desired sweep guard).
	if err := os.WriteFile(filepath.Join(base, "job", "keep.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := dr.RemoveEmptyDir("job"); err == nil {
		t.Fatal("RemoveEmptyDir(non-empty) must fail")
	}
	if _, err := os.Stat(filepath.Join(base, "job")); err != nil {
		t.Fatal("non-empty directory must survive")
	}
}

// TestRemoveEmptyDirUnsupportedProvider verifies the sentinel path: a chain
// over a provider without the capability reports ErrDirRemovalUnsupported
// instead of pretending the capability is absent.
func TestRemoveEmptyDirUnsupportedProvider(t *testing.T) {
	t.Parallel()
	// S3 has no directory concept; construct the wrapped chain directly so
	// no network dial is needed for this assertion.
	provider, err := NewS3Adapter(S3Config{Bucket: "b", Region: "r", AccessKey: "a", SecretKey: "s"})
	if err != nil {
		t.Fatal(err)
	}
	chain := withLogging(withMetrics(withRetry(WrapThrottled(provider, 0), DefaultRetryPolicy), "t"), "t", false)

	dr, ok := chain.(interface{ RemoveEmptyDir(string) error })
	if !ok {
		t.Fatal("chain must expose RemoveEmptyDir even over unsupported providers")
	}
	if err := dr.RemoveEmptyDir("x"); !errors.Is(err, ErrDirRemovalUnsupported) {
		t.Fatalf("want ErrDirRemovalUnsupported, got %v", err)
	}
}
