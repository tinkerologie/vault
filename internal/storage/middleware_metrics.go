package storage

import (
	"context"
	"io"
	"sync"
	"time"
)

// OpStat holds aggregate counters for one operation kind on one destination.
type OpStat struct {
	Calls      int64
	Errors     int64
	TotalNanos int64
}

type metricsAdapter struct {
	inner Adapter
	dest  string
	mu    sync.Mutex
	stats map[string]*OpStat
}

// withMetrics wraps inner with a metrics-collecting adapter tagged by dest.
func withMetrics(inner Adapter, dest string) Adapter {
	return &metricsAdapter{inner: inner, dest: dest, stats: map[string]*OpStat{}}
}

func (m *metricsAdapter) record(op string, start time.Time, err error) {
	m.mu.Lock()
	s := m.stats[op]
	if s == nil {
		s = &OpStat{}
		m.stats[op] = s
	}
	s.Calls++
	if err != nil {
		s.Errors++
	}
	s.TotalNanos += time.Since(start).Nanoseconds()
	m.mu.Unlock()
}

// Snapshot returns a copy of the current stats, keyed by operation.
func (m *metricsAdapter) Snapshot() map[string]OpStat {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]OpStat, len(m.stats))
	for k, v := range m.stats {
		out[k] = *v
	}
	return out
}

func (m *metricsAdapter) Write(p string, r io.Reader) error {
	start := time.Now()
	err := m.inner.Write(p, r)
	m.record("write", start, err)
	return err
}

func (m *metricsAdapter) WriteFrom(p string, open func() (io.ReadCloser, error)) error {
	start := time.Now()
	err := m.inner.WriteFrom(p, open)
	m.record("write", start, err)
	return err
}

func (m *metricsAdapter) Read(p string) (io.ReadCloser, error) {
	start := time.Now()
	rc, err := m.inner.Read(p)
	m.record("read", start, err)
	return rc, err
}

func (m *metricsAdapter) ReadRange(p string, off, length int64) (io.ReadCloser, error) {
	start := time.Now()
	rc, err := m.inner.ReadRange(p, off, length)
	m.record("read", start, err)
	return rc, err
}

func (m *metricsAdapter) Delete(p string) error {
	start := time.Now()
	err := m.inner.Delete(p)
	m.record("delete", start, err)
	return err
}

func (m *metricsAdapter) List(p string) ([]FileInfo, error) {
	start := time.Now()
	out, err := m.inner.List(p)
	m.record("list", start, err)
	return out, err
}

func (m *metricsAdapter) Stat(p string) (FileInfo, error) {
	start := time.Now()
	fi, err := m.inner.Stat(p)
	m.record("stat", start, err)
	return fi, err
}

func (m *metricsAdapter) TestConnection() error {
	return m.inner.TestConnection()
}

func (m *metricsAdapter) GetCapacity(ctx context.Context) (Capacity, error) {
	return m.inner.GetCapacity(ctx)
}

func (m *metricsAdapter) Usage() (int64, int64, error) { return m.inner.Usage() }
