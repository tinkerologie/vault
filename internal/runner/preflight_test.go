package runner

import (
	"context"
	"errors"
	"io"
	"testing"
	"testing/synctest"
	"time"

	"github.com/tinkerologie/vault/internal/storage"
)

type stubAdapter struct {
	testErr error
	delay   time.Duration
}

func (s *stubAdapter) Write(string, io.Reader) error                         { return nil }
func (s *stubAdapter) Read(string) (io.ReadCloser, error)                    { return nil, nil }
func (s *stubAdapter) ReadRange(string, int64, int64) (io.ReadCloser, error) { return nil, nil }
func (s *stubAdapter) Delete(string) error                                   { return nil }
func (s *stubAdapter) List(string) ([]storage.FileInfo, error)               { return nil, nil }
func (s *stubAdapter) Stat(string) (storage.FileInfo, error)                 { return storage.FileInfo{}, nil }
func (s *stubAdapter) TestConnection() error {
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	return s.testErr
}
func (s *stubAdapter) GetCapacity(_ context.Context) (storage.Capacity, error) {
	return storage.Capacity{}, nil
}
func (s *stubAdapter) WriteFrom(string, func() (io.ReadCloser, error)) error { return nil }

// Compile-time: stub satisfies the interface.
var _ storage.Adapter = (*stubAdapter)(nil)

func preflightAdapter(ctx context.Context, adapter storage.Adapter, timeout time.Duration) error {
	result := launchStorageProbe(adapter, func(check func()) { go check() })
	return preflightResult(ctx, result, timeout)
}

func TestPreflightSuccess(t *testing.T) {
	if err := preflightAdapter(context.Background(), &stubAdapter{}, 1*time.Second); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestPreflightFailure(t *testing.T) {
	adapter := &stubAdapter{testErr: errors.New("dial: connection refused")}
	err := preflightAdapter(context.Background(), adapter, 1*time.Second)
	if err == nil || !errors.Is(err, ErrPreflightFailed) {
		t.Errorf("expected ErrPreflightFailed wrap, got %v", err)
	}
}

func TestPreflightTimeout(t *testing.T) {
	adapter := &stubAdapter{delay: 200 * time.Millisecond}
	err := preflightAdapter(context.Background(), adapter, 50*time.Millisecond)
	if !errors.Is(err, ErrPreflightTimeout) {
		t.Errorf("expected ErrPreflightTimeout, got %v", err)
	}
}

type blockedStorageProbe struct {
	stubAdapter
	started chan struct{}
	release chan struct{}
	closed  chan struct{}
}

func (p *blockedStorageProbe) TestConnection() error {
	close(p.started)
	<-p.release
	return nil
}

func (p *blockedStorageProbe) Close() error {
	close(p.closed)
	return nil
}

func TestTimedOutStorageProbeRetainsOwnership(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		probe := &blockedStorageProbe{started: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{})}
		r := &Runner{}
		result := launchStorageProbe(probe, r.backgroundWork.Go)
		<-probe.started
		if err := preflightResult(t.Context(), result, time.Second); !errors.Is(err, ErrPreflightTimeout) {
			t.Errorf("probe error = %v, want timeout", err)
		}

		stopped := make(chan struct{})
		go func() {
			r.WaitForBackgroundWork()
			close(stopped)
		}()
		synctest.Wait()
		select {
		case <-probe.closed:
			t.Error("adapter closed while the timed-out probe was active")
		case <-stopped:
			t.Error("runner stopped while the timed-out probe was active")
		default:
		}
		close(probe.release)
		<-stopped
		select {
		case <-probe.closed:
		default:
			t.Fatal("runner returned without closing the adapter")
		}
	})
}
