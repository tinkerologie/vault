package storage

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// rec458Adapter is a full Adapter (plus Close and RemoveEmptyDir) that counts
// calls and returns errs[op] for each operation, so every middleware method
// can be driven through both its success and error branch.
type rec458Adapter struct {
	errs  map[string]error
	calls map[string]int
}

func newRec458() *rec458Adapter {
	return &rec458Adapter{errs: map[string]error{}, calls: map[string]int{}}
}

func (r *rec458Adapter) hit(op string) error { r.calls[op]++; return r.errs[op] }

func (r *rec458Adapter) Write(_ string, rd io.Reader) error {
	_, _ = io.Copy(io.Discard, rd)
	return r.hit("write")
}

func (r *rec458Adapter) WriteFrom(_ string, open func() (io.ReadCloser, error)) error {
	rc, err := open()
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, rc)
	_ = rc.Close()
	return r.hit("writefrom")
}

func (r *rec458Adapter) Read(string) (io.ReadCloser, error) {
	if err := r.hit("read"); err != nil {
		return nil, err
	}
	return io.NopCloser(strings.NewReader("data")), nil
}

func (r *rec458Adapter) ReadRange(string, int64, int64) (io.ReadCloser, error) {
	if err := r.hit("readrange"); err != nil {
		return nil, err
	}
	return io.NopCloser(strings.NewReader("da")), nil
}

func (r *rec458Adapter) Delete(string) error { return r.hit("delete") }

func (r *rec458Adapter) List(string) ([]FileInfo, error) {
	if err := r.hit("list"); err != nil {
		return nil, err
	}
	return []FileInfo{{Path: "a"}}, nil
}

func (r *rec458Adapter) Stat(p string) (FileInfo, error) {
	if err := r.hit("stat"); err != nil {
		return FileInfo{}, err
	}
	return FileInfo{Path: p, Size: 4}, nil
}

func (r *rec458Adapter) TestConnection() error { return r.hit("test") }

func (r *rec458Adapter) GetCapacity(context.Context) (Capacity, error) {
	return Capacity{TotalBytes: 10}, r.hit("capacity")
}

func (r *rec458Adapter) Close() error { return r.hit("close") }

func (r *rec458Adapter) RemoveEmptyDir(string) error { return r.hit("rmdir") }

// exercise458 calls every Adapter method on a and returns the errors seen.
func exercise458(t *testing.T, a Adapter) []error {
	t.Helper()
	var errs []error
	add := func(err error) { errs = append(errs, err) }
	add(a.Write("p", strings.NewReader("x")))
	add(a.WriteFrom("p", func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("x")), nil }))
	rc, err := a.Read("p")
	if rc != nil {
		_, _ = io.ReadAll(rc)
		_ = rc.Close()
	}
	add(err)
	rc, err = a.ReadRange("p", 0, 2)
	if rc != nil {
		_, _ = io.ReadAll(rc)
		_ = rc.Close()
	}
	add(err)
	add(a.Delete("p"))
	_, err = a.List("")
	add(err)
	_, err = a.Stat("p")
	add(err)
	add(a.TestConnection())
	_, err = a.GetCapacity(context.Background())
	add(err)
	add(a.(io.Closer).Close())
	add(a.(dirRemover).RemoveEmptyDir("d"))
	return errs
}

var allOps458 = []string{"write", "writefrom", "read", "readrange", "delete", "list", "stat", "test", "capacity", "close", "rmdir"}

func TestMiddleware458_PassThroughSuccessAndError(t *testing.T) {
	t.Parallel()
	wrappers := map[string]func(Adapter) Adapter{
		"logging-on":  func(a Adapter) Adapter { return withLogging(a, "dest", true) },
		"logging-off": func(a Adapter) Adapter { return withLogging(a, "dest", false) },
		"metrics":     func(a Adapter) Adapter { return withMetrics(a, "dest") },
		"retry":       func(a Adapter) Adapter { return withRetry(a, RetryPolicy{MaxAttempts: 0}, nil) },
		"throttle":    func(a Adapter) Adapter { return WrapThrottled(a, 1000) },
		"auto":        wrapAutoThrottled,
	}
	for name, wrap := range wrappers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			inner := newRec458()
			a := wrap(inner)
			for i, err := range exercise458(t, a) {
				if err != nil {
					t.Errorf("success pass op %d: %v", i, err)
				}
			}
			boom := errors.New("403 forbidden") // fatal: never retried
			for _, op := range allOps458 {
				inner.errs[op] = boom
			}
			for i, err := range exercise458(t, a) {
				if !errors.Is(err, boom) {
					t.Errorf("error pass op %d: want boom, got %v", i, err)
				}
			}
			for _, op := range allOps458 {
				if inner.calls[op] != 2 {
					t.Errorf("%s: inner calls = %d, want 2", op, inner.calls[op])
				}
			}
		})
	}
}

func TestMiddleware458_MetricsSnapshotCountsErrors(t *testing.T) {
	t.Parallel()
	inner := newRec458()
	m := withMetrics(inner, "d").(*metricsAdapter)
	inner.errs["delete"] = errors.New("x")
	_ = exercise458(t, m)
	snap := m.Snapshot()
	if snap["write"].Calls != 2 || snap["read"].Calls != 2 {
		t.Errorf("write/read calls = %d/%d, want 2/2", snap["write"].Calls, snap["read"].Calls)
	}
	if snap["delete"].Errors != 1 || snap["stat"].Calls != 1 || snap["list"].Calls != 1 {
		t.Errorf("snapshot = %+v", snap)
	}
}

func TestMiddleware458_CloseOnNonCloserInner(t *testing.T) {
	t.Parallel()
	for _, a := range []Adapter{
		withLogging(mockAdapter{}, "d", true),
		withMetrics(mockAdapter{}, "d"),
		withRetry(mockAdapter{}, RetryPolicy{}, nil),
		WrapThrottled(mockAdapter{}, 1),
	} {
		if err := a.(io.Closer).Close(); err != nil {
			t.Errorf("%T Close with non-closer inner = %v, want nil", a, err)
		}
	}
}

func TestMiddleware458_RetryRetriesTransientReads(t *testing.T) {
	t.Parallel()
	inner := newRec458()
	transient := &httpStatusError{code: http.StatusServiceUnavailable}
	var slept []time.Duration
	r := withRetry(inner, RetryPolicy{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond}, nil).(*retryAdapter)
	r.sleep = func(d time.Duration) { slept = append(slept, d) }
	for _, op := range []string{"readrange", "delete", "list", "stat"} {
		inner.errs[op] = transient
	}
	if _, err := r.ReadRange("p", 0, 1); !errors.Is(err, transient) {
		t.Errorf("ReadRange = %v", err)
	}
	if err := r.Delete("p"); !errors.Is(err, transient) {
		t.Errorf("Delete = %v", err)
	}
	if _, err := r.List(""); !errors.Is(err, transient) {
		t.Errorf("List = %v", err)
	}
	if _, err := r.Stat("p"); !errors.Is(err, transient) {
		t.Errorf("Stat = %v", err)
	}
	for _, op := range []string{"readrange", "delete", "list", "stat"} {
		if inner.calls[op] != 3 {
			t.Errorf("%s calls = %d, want 3", op, inner.calls[op])
		}
	}
	if len(slept) != 8 {
		t.Errorf("sleeps = %d, want 8", len(slept))
	}
	for _, d := range slept {
		if d < 0 || d > 2*time.Millisecond {
			t.Errorf("backoff %v outside [0, MaxDelay]", d)
		}
	}
}

func TestMiddleware458_JitteredBackoffEdges(t *testing.T) {
	t.Parallel()
	// MaxDelay unset falls back to 30s cap.
	if d := jitteredBackoff(RetryPolicy{BaseDelay: time.Hour}, 3); d < 0 || d > 30*time.Second {
		t.Errorf("default cap: got %v", d)
	}
	// A sub-nanosecond product truncates to zero.
	if d := jitteredBackoff(RetryPolicy{BaseDelay: 1, MaxDelay: 1}, 1); d < 0 || d > 1 {
		t.Errorf("tiny: got %v", d)
	}
}

func TestErrors458_ClassifierErrorStrings(t *testing.T) {
	t.Parallel()
	if got := (&httpStatusError{code: http.StatusBadGateway}).Error(); got != http.StatusText(http.StatusBadGateway) {
		t.Errorf("httpStatusError without inner = %q", got)
	}
	if got := (&httpStatusError{code: 500, err: errors.New("inner")}).Error(); got != "inner" {
		t.Errorf("httpStatusError with inner = %q", got)
	}
	if got := (&retryableError{err: errors.New("again")}).Error(); got != "again" {
		t.Errorf("retryableError = %q", got)
	}
}

func TestStreamWriteFrom458_OpenError(t *testing.T) {
	t.Parallel()
	boom := errors.New("open failed")
	if err := streamWriteFrom(newRec458(), "p", func() (io.ReadCloser, error) { return nil, boom }); !errors.Is(err, boom) {
		t.Errorf("streamWriteFrom = %v", err)
	}
}
