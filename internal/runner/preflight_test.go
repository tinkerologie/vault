package runner

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/storage"
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

// Compile-time: stub satisfies the interface.
var _ storage.Adapter = (*stubAdapter)(nil)

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
