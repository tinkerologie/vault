package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
)

func TestDiagStatsNilIsNoOp(t *testing.T) {
	var d *DiagStats
	d.recordTransient("Read", "a/b", 1, 5, errors.New("x"), true)
	d.recordResume("a/b", 1, 5, errors.New("x"))
	d.recordPartRestart("a/b", 1, 5, errors.New("x"), 10)
	d.AddBytesRead(10)
	if got := d.Snapshot(); got.Retries != 0 || got.BytesRead != 0 || got.TransientByOp != nil {
		t.Fatalf("nil Snapshot = %+v, want zero", got)
	}
	if DiagStatsFrom(context.Background()) != nil {
		t.Fatal("DiagStatsFrom(empty ctx) != nil")
	}
}

func TestDiagStatsContextRoundTrip(t *testing.T) {
	d := NewDiagStats(nil, 0)
	if got := DiagStatsFrom(WithDiagStats(t.Context(), d)); got != d {
		t.Fatalf("DiagStatsFrom = %p, want %p", got, d)
	}
	if d.cap != DefaultDiagEventCap {
		t.Fatalf("cap = %d, want default %d", d.cap, DefaultDiagEventCap)
	}
}

func TestDiagStatsConcurrentCounters(t *testing.T) {
	d := NewDiagStats(nil, 1000)
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			for range 100 {
				d.recordTransient("Read", "x", 1, 5, errors.New("reset"), true)
				d.AddBytesRead(3)
			}
		})
	}
	wg.Wait()
	s := d.Snapshot()
	if s.Retries != 5000 || s.TransientErrors != 5000 || s.BytesRead != 15000 || s.TransientByOp["Read"] != 5000 {
		t.Fatalf("snapshot = %+v, want 5000 retries/transient, 15000 bytes", s)
	}
}

func TestDiagStatsEventCapAndBaseName(t *testing.T) {
	var events []DiagEvent
	d := NewDiagStats(func(e DiagEvent) { events = append(events, e) }, 2)
	for i := range 5 {
		d.recordTransient("ReadRange", "vault/job-1/secret/path/data.tar.zst", i+1, 5, errors.New("reset"), true)
	}
	d.recordTransient("Stat", "x", 5, 5, errors.New("final"), false) // final attempt: counted, not emitted
	if len(events) != 3 || !events[2].Suppressed {
		t.Fatalf("forwarded %d events (%+v), want 2 plus one suppression notice", len(events), events)
	}
	if events[0].Object != "data.tar.zst" || events[0].Op != "ReadRange" || events[0].Attempt != 1 || events[0].MaxAttempts != 5 || events[0].Err != "reset" {
		t.Errorf("event = %+v, want base name and attempt detail", events[0])
	}
	s := d.Snapshot()
	if s.SuppressedEvents != 3 || s.Retries != 5 || s.TransientErrors != 6 {
		t.Errorf("snapshot = %+v, want 3 suppressed, 5 retries, 6 transient", s)
	}
}

func TestRetryRecordsIntoDiag(t *testing.T) {
	f := &fakeAdapter{writeErrs: []error{io.ErrUnexpectedEOF, io.ErrUnexpectedEOF}}
	d := NewDiagStats(nil, 0)
	a := withRetry(f, RetryPolicy{MaxAttempts: 5}, d)
	if err := a.WriteFrom("dir/obj", func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader([]byte("data"))), nil
	}); err != nil {
		t.Fatalf("WriteFrom: %v", err)
	}
	if s := d.Snapshot(); s.Retries != 2 || s.TransientByOp["WriteFrom"] != 2 {
		t.Fatalf("snapshot = %+v, want 2 WriteFrom retries", s)
	}
}

func TestRetryExhaustedCountsFinalTransientWithoutRetry(t *testing.T) {
	f := &fakeAdapter{writeErrs: []error{io.ErrUnexpectedEOF, io.ErrUnexpectedEOF, io.ErrUnexpectedEOF}}
	d := NewDiagStats(nil, 0)
	a := withRetry(f, RetryPolicy{MaxAttempts: 3}, d)
	if err := a.WriteFrom("obj", func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(nil)), nil }); err == nil {
		t.Fatal("WriteFrom succeeded, want exhausted error")
	}
	if s := d.Snapshot(); s.Retries != 2 || s.TransientErrors != 3 {
		t.Fatalf("snapshot = %+v, want 2 retries, 3 transient errors", s)
	}
}

func TestNewAdapterWithOptionsWiresDiag(t *testing.T) {
	d := NewDiagStats(nil, 0)
	a, err := NewAdapterWithOptions("local", `{"path":"`+t.TempDir()+`"}`, Options{Diag: d})
	if err != nil {
		t.Fatalf("NewAdapterWithOptions: %v", err)
	}
	_, _ = a.Stat("missing") // not transient: nothing recorded
	if s := d.Snapshot(); s.TransientErrors != 0 {
		t.Fatalf("snapshot = %+v, want no transient errors", s)
	}
}

func TestResumableReaderRecordsResumeAndBytes(t *testing.T) {
	want := bytes.Repeat([]byte("vault-restore-"), 5000)
	fa := &flakyAdapter{data: want, failAfter: 1000, failTimes: 1}
	var events []DiagEvent
	d := NewDiagStats(func(e DiagEvent) { events = append(events, e) }, 0)
	rr := NewResumableReader(WithDiagStats(t.Context(), d), fa, "dir/obj", int64(len(want)), RetryPolicy{MaxAttempts: 5})
	got, err := io.ReadAll(rr)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("ReadAll = %d bytes, %v", len(got), err)
	}
	s := d.Snapshot()
	if s.Resumes != 1 || s.BytesRead != int64(len(want)) {
		t.Fatalf("snapshot = %+v, want 1 resume and %d bytes", s, len(want))
	}
	if len(events) != 1 || events[0].Op != "resume" || events[0].Object != "obj" {
		t.Fatalf("events = %+v, want one resume of obj", events)
	}
}

func TestParallelDownloadRecordsRestartsAndNetBytes(t *testing.T) {
	orig := parallelDownloadPolicy
	parallelDownloadPolicy = RetryPolicy{MaxAttempts: 5}
	t.Cleanup(func() { parallelDownloadPolicy = orig })

	data := bytes.Repeat([]byte("Z"), 30000)
	a := &rangeAdapter{data: data, failAtOffset: 10000}
	d := NewDiagStats(nil, 0)
	var progress int64
	var mu sync.Mutex
	out := &memWriterAt{}
	err := ParallelRangeDownload(WithDiagStats(t.Context(), d), a, "obj", out, int64(len(data)), 10000, 3, func(n int64) {
		mu.Lock()
		progress += n
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("ParallelRangeDownload: %v", err)
	}
	s := d.Snapshot()
	if s.PartRestarts != 1 || s.RefetchedBytes != 5000 {
		t.Errorf("snapshot = %+v, want 1 restart refetching 5000 bytes", s)
	}
	if s.BytesRead != int64(len(data)) {
		t.Errorf("BytesRead = %d, want %d (net of the retried part)", s.BytesRead, len(data))
	}
	if progress != int64(len(data))+5000 {
		t.Errorf("onBytes total = %d, want unchanged over-count %d", progress, len(data)+5000)
	}
}

// memWriterAt is an in-memory io.WriterAt.
type memWriterAt struct {
	mu  sync.Mutex
	buf []byte
}

func (m *memWriterAt) WriteAt(p []byte, off int64) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if end := int(off) + len(p); end > len(m.buf) {
		m.buf = append(m.buf, make([]byte, end-len(m.buf))...)
	}
	copy(m.buf[off:], p)
	return len(p), nil
}

// openFailAdapter fails the first ReadRange open with a transient error.
type openFailAdapter struct {
	mockAdapter
	data  []byte
	calls int
}

func (a *openFailAdapter) ReadRange(_ string, offset, length int64) (io.ReadCloser, error) {
	a.calls++
	if a.calls == 1 {
		return nil, &retryableError{err: errors.New("connection refused")}
	}
	return io.NopCloser(bytes.NewReader(a.data[offset : offset+length])), nil
}

func TestResumableReaderRecordsFailedOpen(t *testing.T) {
	want := []byte("payload")
	var events []DiagEvent
	d := NewDiagStats(func(e DiagEvent) { events = append(events, e) }, 0)
	rr := NewResumableReader(WithDiagStats(t.Context(), d), &openFailAdapter{data: want}, "dir/obj", int64(len(want)), RetryPolicy{MaxAttempts: 3})
	got, err := io.ReadAll(rr)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("ReadAll = %q, %v", got, err)
	}
	if s := d.Snapshot(); s.Retries != 1 || s.TransientByOp["ReadRange"] != 1 {
		t.Fatalf("snapshot = %+v, want one recorded ReadRange retry", s)
	}
	if len(events) != 1 || events[0].Op != "ReadRange" || events[0].Object != "obj" || events[0].Attempt != 1 {
		t.Fatalf("events = %+v, want one ReadRange retry of obj", events)
	}
}

// TestNestedRetryCountsExhaustedFailureOnce guards Copilot's review on #491:
// when the retry middleware exhausts its attempts on a ReadRange open and the
// resumable reader retries, the failure must not be counted twice.
func TestNestedRetryCountsExhaustedFailureOnce(t *testing.T) {
	want := []byte("payload")
	inner := &failingOpens{remaining: 2, data: want} // both middleware attempts fail once
	d := NewDiagStats(nil, 0)
	chain := withRetry(inner, RetryPolicy{MaxAttempts: 2}, d)
	rr := NewResumableReader(WithDiagStats(t.Context(), d), chain, "obj", int64(len(want)), RetryPolicy{MaxAttempts: 3})
	got, err := io.ReadAll(rr)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("ReadAll = %q, %v", got, err)
	}
	s := d.Snapshot()
	// Middleware: 2 transient failures, 1 retry. Reader: 1 retry, no new transient.
	if s.TransientErrors != 2 || s.Retries != 2 {
		t.Fatalf("snapshot = %+v, want 2 transient errors and 2 retries (no double count)", s)
	}
	if err := (&recordedError{io.ErrUnexpectedEOF}); !errors.Is(err, io.ErrUnexpectedEOF) || err.Error() != io.ErrUnexpectedEOF.Error() || !classify(err) {
		t.Fatal("recordedError must stay transparent to errors.Is, Error and classify")
	}
}

type failingOpens struct {
	mockAdapter
	remaining int
	data      []byte
}

func (f *failingOpens) ReadRange(_ string, offset, length int64) (io.ReadCloser, error) {
	if f.remaining > 0 {
		f.remaining--
		return nil, &retryableError{err: errors.New("connection refused")}
	}
	return io.NopCloser(bytes.NewReader(f.data[offset : offset+length])), nil
}
