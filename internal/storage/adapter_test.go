package storage

import (
	"context"
	"errors"
	"io"
	"testing"
)

// noCloseAdapter is an Adapter that does NOT implement io.Closer.
type noCloseAdapter struct{}

func (noCloseAdapter) Write(string, io.Reader) error      { return nil }
func (noCloseAdapter) Read(string) (io.ReadCloser, error) { return nil, nil }
func (noCloseAdapter) ReadRange(string, int64, int64) (io.ReadCloser, error) {
	return nil, nil
}
func (noCloseAdapter) Delete(string) error                           { return nil }
func (noCloseAdapter) List(string) ([]FileInfo, error)               { return nil, nil }
func (noCloseAdapter) Stat(string) (FileInfo, error)                 { return FileInfo{}, nil }
func (noCloseAdapter) TestConnection() error                         { return nil }
func (noCloseAdapter) GetCapacity(context.Context) (Capacity, error) { return Capacity{}, nil }
func (noCloseAdapter) Usage() (int64, int64, error)                  { return 0, 0, ErrUsageNotSupported }

// closableAdapter implements both Adapter and io.Closer, with a flag.
type closableAdapter struct {
	noCloseAdapter
	closed   bool
	closeErr error
}

func (c *closableAdapter) Close() error {
	c.closed = true
	return c.closeErr
}

func TestCloseAdapter_NoCloser(t *testing.T) {
	t.Parallel()
	// Must not panic when adapter has no Close method.
	CloseAdapter(noCloseAdapter{})
}

func TestCloseAdapter_NilAdapter(t *testing.T) {
	t.Parallel()
	// io.Closer type assertion on a nil interface returns ok=false; this
	// must not panic. Use an explicit typed-nil to also cover the case
	// where the interface holds a non-nil type with a nil concrete value.
	CloseAdapter(nil)
}

func TestCloseAdapter_CallsCloseWhenImplemented(t *testing.T) {
	t.Parallel()
	c := &closableAdapter{}
	CloseAdapter(c)
	if !c.closed {
		t.Error("Close() was not invoked")
	}
}

func TestCloseAdapter_SwallowsCloseError(t *testing.T) {
	t.Parallel()
	c := &closableAdapter{closeErr: errors.New("nope")}
	// CloseAdapter intentionally discards the error (best-effort cleanup),
	// so the test only asserts no panic and that Close did get called.
	CloseAdapter(c)
	if !c.closed {
		t.Error("Close() was not invoked")
	}
}

// TestRangeReader_NilCloserIsNoOp covers the early-return branch of
// rangeReader.Close when no closer was supplied. The local, sftp, smb
// adapters always set closer, but the contract allows a nil one.
func TestRangeReader_NilCloserIsNoOp(t *testing.T) {
	t.Parallel()
	rr := &rangeReader{Reader: nil, closer: nil}
	if err := rr.Close(); err != nil {
		t.Errorf("Close with nil closer returned %v, want nil", err)
	}
}
