package storage

import (
	"errors"
	"io"
	"testing"
)

type okWriter struct{ mockAdapter }

func (okWriter) Write(string, io.Reader) error { return nil }

func TestMetricsCountsCalls(t *testing.T) {
	m := withMetrics(okWriter{}, "dest-1")
	for i := 0; i < 3; i++ {
		_ = m.Write("p", nil)
	}
	snap := m.(*metricsAdapter).Snapshot()
	if snap["write"].Calls != 3 {
		t.Errorf("write calls = %d, want 3", snap["write"].Calls)
	}
	if snap["write"].Errors != 0 {
		t.Errorf("write errors = %d, want 0", snap["write"].Errors)
	}
}

type errLister struct{ mockAdapter }

func (errLister) List(string) ([]FileInfo, error) { return nil, errors.New("x") }

func TestMetricsCountsErrors(t *testing.T) {
	m := withMetrics(errLister{}, "dest-1")
	_, _ = m.List("p")
	snap := m.(*metricsAdapter).Snapshot()
	if snap["list"].Calls != 1 || snap["list"].Errors != 1 {
		t.Errorf("list = %+v, want calls=1 errors=1", snap["list"])
	}
}
