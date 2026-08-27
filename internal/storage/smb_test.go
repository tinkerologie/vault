package storage

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"testing"
	"time"

	"github.com/cloudsoda/go-smb2"
)

// fakeSMBFileFsInfo implements smb2.FileFsInfo for unit-testing the
// FileFsInfo → Capacity conversion without a live SMB server.
type fakeSMBFileFsInfo struct {
	blockSize, fragmentSize, totalBlocks, freeBlocks, availBlocks uint64
}

func (f *fakeSMBFileFsInfo) BlockSize() uint64           { return f.blockSize }
func (f *fakeSMBFileFsInfo) FragmentSize() uint64        { return f.fragmentSize }
func (f *fakeSMBFileFsInfo) TotalBlockCount() uint64     { return f.totalBlocks }
func (f *fakeSMBFileFsInfo) FreeBlockCount() uint64      { return f.freeBlocks }
func (f *fakeSMBFileFsInfo) AvailableBlockCount() uint64 { return f.availBlocks }

// Compile-time assertion that the fake satisfies the real interface.
var _ smb2.FileFsInfo = (*fakeSMBFileFsInfo)(nil)

func TestSMBFileFsInfoToCapacityHappyPath(t *testing.T) {
	t.Parallel()
	info := &fakeSMBFileFsInfo{
		blockSize:   4096,
		totalBlocks: 100 << 20, // 400 GiB total
		freeBlocks:  30 << 20,  // not used (we use AvailableBlockCount)
		availBlocks: 25 << 20,  // 100 GiB available
	}
	now := time.Now().UTC()
	cap, err := smbFileFsInfoToCapacity(info, now)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if cap.Source != "smb-fsctl" {
		t.Errorf("source = %q, want %q", cap.Source, "smb-fsctl")
	}
	if want := int64(100<<20) * 4096; cap.TotalBytes != want {
		t.Errorf("total = %d, want %d", cap.TotalBytes, want)
	}
	if want := int64(25<<20) * 4096; cap.FreeBytes != want {
		t.Errorf("free = %d, want %d", cap.FreeBytes, want)
	}
	if cap.UsedBytes != cap.TotalBytes-cap.FreeBytes {
		t.Errorf("used = %d, expected total-free = %d", cap.UsedBytes, cap.TotalBytes-cap.FreeBytes)
	}
	if cap.ProbedAt != now {
		t.Errorf("probed_at = %v, want %v", cap.ProbedAt, now)
	}
}

func TestSMBFileFsInfoToCapacityZeroBlockSize(t *testing.T) {
	t.Parallel()
	_, err := smbFileFsInfoToCapacity(&fakeSMBFileFsInfo{blockSize: 0, totalBlocks: 1000}, time.Now().UTC())
	if err == nil {
		t.Error("expected error for zero BlockSize")
	}
}

func TestSMBFileFsInfoToCapacityNilInput(t *testing.T) {
	t.Parallel()
	_, err := smbFileFsInfoToCapacity(nil, time.Now().UTC())
	if err == nil {
		t.Error("expected error for nil FileFsInfo")
	}
}

func TestSMBGetCapacityContextCancelled(t *testing.T) {
	t.Parallel()
	a, err := NewSMBAdapter(SMBConfig{Host: "127.0.0.1", Port: 1, User: "x", Password: "y", Share: "s"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = a.GetCapacity(ctx)
	if err == nil {
		t.Fatal("expected cancelled-context error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestIsSMBNotFound(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"fs.ErrNotExist", fs.ErrNotExist, true},
		{"os.ErrNotExist", os.ErrNotExist, true},
		{"wrapped fs.ErrNotExist", fmt.Errorf("read: %w", fs.ErrNotExist), true},
		{"wrapped os.ErrNotExist", &os.PathError{Op: "open", Path: "/missing", Err: os.ErrNotExist}, true},
		{"smb STATUS_OBJECT_NAME_NOT_FOUND", &smb2.ResponseError{Code: 0xC0000034}, true},
		{"smb STATUS_OBJECT_PATH_NOT_FOUND", &smb2.ResponseError{Code: 0xC000003A}, true},
		{"smb STATUS_NO_SUCH_FILE", &smb2.ResponseError{Code: 0xC000000F}, true},
		{"other smb ResponseError", &smb2.ResponseError{Code: 0xC0000022}, false}, // STATUS_ACCESS_DENIED
		{"other generic error", errors.New("connection failed"), false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isSMBNotFound(tt.err); got != tt.want {
				t.Errorf("isSMBNotFound(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestWrapSMBListError(t *testing.T) {
	t.Parallel()

	if err := wrapSMBListError(nil, "/test"); err != nil {
		t.Errorf("wrapSMBListError(nil) = %v, want nil", err)
	}

	notFoundErr := wrapSMBListError(fs.ErrNotExist, "/missing")
	if notFoundErr == nil || !IsNotExist(notFoundErr) {
		t.Errorf("wrapSMBListError(fs.ErrNotExist) = %v, want ErrNotExist", notFoundErr)
	}

	smbNotFoundErr := wrapSMBListError(&smb2.ResponseError{Code: 0xC0000034}, "/missing")
	if smbNotFoundErr == nil || !IsNotExist(smbNotFoundErr) {
		t.Errorf("wrapSMBListError(STATUS_OBJECT_NAME_NOT_FOUND) = %v, want ErrNotExist", smbNotFoundErr)
	}

	genericErr := wrapSMBListError(errors.New("smb network drop"), "/path")
	if genericErr == nil || IsNotExist(genericErr) {
		t.Errorf("wrapSMBListError(generic) = %v, want non-ErrNotExist", genericErr)
	}
}
