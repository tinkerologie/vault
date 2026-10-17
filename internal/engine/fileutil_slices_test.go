package engine

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"testing/iotest"
)

// setCopySliceSize shrinks copySlices' slice for one test so slice-level
// progress and cancellation can be observed on small fixtures.
func setCopySliceSize(t *testing.T, n int64) {
	t.Helper()
	old := copySliceSize
	copySliceSize = n
	t.Cleanup(func() { copySliceSize = old })
}

// readerFromSpy is a destination that implements io.ReaderFrom, standing in
// for *os.File, whose ReadFrom is the kernel copy path.
type readerFromSpy struct {
	buf     bytes.Buffer
	sources []io.Reader
	onSlice func()
}

func (s *readerFromSpy) Write(p []byte) (int, error) { return s.buf.Write(p) }

func (s *readerFromSpy) ReadFrom(r io.Reader) (int64, error) {
	s.sources = append(s.sources, r)
	n, err := s.buf.ReadFrom(r)
	if s.onSlice != nil {
		s.onSlice()
	}
	return n, err
}

// TestCopySlicesReachesReadFrom pins the #500 contract: each slice reaches
// dst.ReadFrom with an io.LimitedReader over the unwrapped source, and
// progress is cumulative.
func TestCopySlicesReachesReadFrom(t *testing.T) {
	setCopySliceSize(t, 1<<20)
	data := bytes.Repeat([]byte{0x42}, 2<<20+5)
	src := bytes.NewReader(data)
	dst := &readerFromSpy{}

	var progress []int64
	if err := copySlices(context.Background(), dst, src, func(n int64) { progress = append(progress, n) }); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(dst.buf.Bytes(), data) {
		t.Fatal("copied content differs from source")
	}
	if len(dst.sources) == 0 {
		t.Fatal("ReadFrom was never called")
	}
	for i, r := range dst.sources {
		lr, ok := r.(*io.LimitedReader)
		if !ok {
			t.Fatalf("slice %d: ReadFrom got %T, want *io.LimitedReader", i, r)
		}
		if lr.R != io.Reader(src) {
			t.Fatalf("slice %d: LimitedReader wraps %T, want the source itself", i, lr.R)
		}
	}
	want := []int64{1 << 20, 2 << 20, 2<<20 + 5}
	if len(progress) != len(want) {
		t.Fatalf("progress = %v, want %v", progress, want)
	}
	for i := range want {
		if progress[i] != want[i] {
			t.Fatalf("progress = %v, want %v", progress, want)
		}
	}
}

func TestCopySlicesStopsBetweenSlicesOnCancel(t *testing.T) {
	setCopySliceSize(t, 1<<20)
	ctx, cancel := context.WithCancel(context.Background())
	dst := &readerFromSpy{onSlice: cancel}

	err := copySlices(ctx, dst, bytes.NewReader(make([]byte, 4<<20)), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if got := len(dst.sources); got != 1 {
		t.Fatalf("ReadFrom called %d times after cancel, want 1", got)
	}
}

func TestCopySlicesEmptySource(t *testing.T) {
	called := false
	if err := copySlices(context.Background(), &readerFromSpy{}, bytes.NewReader(nil), func(int64) { called = true }); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("progress reported for an empty copy")
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk on fire") }

func TestCopySlicesReturnsWriteError(t *testing.T) {
	err := copySlices(context.Background(), failingWriter{}, bytes.NewReader([]byte("x")), nil)
	if err == nil || err.Error() != "disk on fire" {
		t.Fatalf("want write error, got %v", err)
	}
}

// A read error after some data still reports the bytes that landed and
// returns the error, so a failing source disk ends the restore with a cause.
func TestCopySlicesReturnsReadErrorAfterData(t *testing.T) {
	src := io.MultiReader(bytes.NewReader(make([]byte, 1000)), iotest.ErrReader(errors.New("bad sector")))
	var progress []int64
	var dst bytes.Buffer
	err := copySlices(context.Background(), &dst, src, func(n int64) { progress = append(progress, n) })
	if err == nil || err.Error() != "bad sector" {
		t.Fatalf("want the read error, got %v", err)
	}
	if dst.Len() != 1000 || len(progress) != 1 || progress[0] != 1000 {
		t.Fatalf("copied %d bytes, progress %v; want 1000 and [1000]", dst.Len(), progress)
	}
}
