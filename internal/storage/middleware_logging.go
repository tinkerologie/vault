package storage

import (
	"context"
	"io"
	"log"
	"time"
)

type loggingAdapter struct { //nolint:unused // wired into factory in Task 7; used directly from tests
	inner   Adapter
	dest    string
	enabled bool
}

// withLogging wraps inner with a structured-logging adapter tagged by dest.
// When enabled is false the wrapper is a thin pass-through with no allocations.
func withLogging(inner Adapter, dest string, enabled bool) Adapter { //nolint:unused // wired into factory in Task 7; used directly from tests
	return &loggingAdapter{inner: inner, dest: dest, enabled: enabled}
}

func (l *loggingAdapter) trace(op, path string, start time.Time, err error) { //nolint:unused
	if !l.enabled {
		return
	}
	status := "ok"
	if err != nil {
		status = "err=" + err.Error()
	}
	log.Printf("[storage] %s %s %s %s %s",
		l.dest, op, path, time.Since(start).Truncate(time.Millisecond), status)
}

func (l *loggingAdapter) Write(p string, r io.Reader) error { //nolint:unused
	if !l.enabled {
		return l.inner.Write(p, r)
	}
	start := time.Now()
	err := l.inner.Write(p, r)
	l.trace("write", p, start, err)
	return err
}

func (l *loggingAdapter) WriteFrom(p string, open func() (io.ReadCloser, error)) error { //nolint:unused
	if !l.enabled {
		return l.inner.WriteFrom(p, open)
	}
	start := time.Now()
	err := l.inner.WriteFrom(p, open)
	l.trace("write", p, start, err)
	return err
}

func (l *loggingAdapter) Read(p string) (io.ReadCloser, error) { //nolint:unused
	if !l.enabled {
		return l.inner.Read(p)
	}
	start := time.Now()
	rc, err := l.inner.Read(p)
	l.trace("read", p, start, err)
	return rc, err
}

func (l *loggingAdapter) ReadRange(p string, off, length int64) (io.ReadCloser, error) { //nolint:unused
	if !l.enabled {
		return l.inner.ReadRange(p, off, length)
	}
	start := time.Now()
	rc, err := l.inner.ReadRange(p, off, length)
	l.trace("readrange", p, start, err)
	return rc, err
}

func (l *loggingAdapter) Delete(p string) error { //nolint:unused
	if !l.enabled {
		return l.inner.Delete(p)
	}
	start := time.Now()
	err := l.inner.Delete(p)
	l.trace("delete", p, start, err)
	return err
}

func (l *loggingAdapter) List(p string) ([]FileInfo, error) { //nolint:unused
	if !l.enabled {
		return l.inner.List(p)
	}
	start := time.Now()
	out, err := l.inner.List(p)
	l.trace("list", p, start, err)
	return out, err
}

func (l *loggingAdapter) Stat(p string) (FileInfo, error) { //nolint:unused
	if !l.enabled {
		return l.inner.Stat(p)
	}
	start := time.Now()
	fi, err := l.inner.Stat(p)
	l.trace("stat", p, start, err)
	return fi, err
}

func (l *loggingAdapter) TestConnection() error { //nolint:unused
	return l.inner.TestConnection()
}

func (l *loggingAdapter) GetCapacity(ctx context.Context) (Capacity, error) { //nolint:unused
	return l.inner.GetCapacity(ctx)
}

func (l *loggingAdapter) Usage() (int64, int64, error) { return l.inner.Usage() } //nolint:unused
