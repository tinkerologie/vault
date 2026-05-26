package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pkg/sftp"
)

func TestSFTPStatVFSToCapacityHappyPath(t *testing.T) {
	t.Parallel()
	st := &sftp.StatVFS{
		Bsize:  4096,
		Frsize: 4096,
		Blocks: 100 << 20, // 100 Mi blocks * 4 KiB = 400 GiB
		Bavail: 25 << 20,  // 25 Mi blocks * 4 KiB = 100 GiB free
	}
	now := time.Now().UTC()
	cap, err := sftpStatVFSToCapacity(st, now)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if cap.Source != "sftp-statvfs" {
		t.Errorf("source = %q", cap.Source)
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
	if !cap.ProbedAt.Equal(now) {
		t.Errorf("ProbedAt = %v, want %v", cap.ProbedAt, now)
	}
}

func TestSFTPStatVFSToCapacityZeroFrsize(t *testing.T) {
	t.Parallel()
	_, err := sftpStatVFSToCapacity(&sftp.StatVFS{Frsize: 0, Blocks: 1000}, time.Now().UTC())
	if err == nil {
		t.Error("expected error for zero Frsize")
	}
}

func TestSFTPStatVFSToCapacityNilInput(t *testing.T) {
	t.Parallel()
	_, err := sftpStatVFSToCapacity(nil, time.Now().UTC())
	if err == nil {
		t.Error("expected error for nil StatVFS")
	}
}

// TestSFTPGetCapacityContextCancelled verifies the early ctx.Err()
// check shorts out before the connect() call. Uses an unreachable
// host (port 1 is reserved); if the ctx check works, no dial happens.
func TestSFTPGetCapacityContextCancelled(t *testing.T) {
	t.Parallel()
	a, err := NewSFTPAdapter(SFTPConfig{Host: "127.0.0.1", Port: 1, User: "x", Password: "y"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = a.GetCapacity(ctx)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	// The early ctx.Err() path returns context.Canceled directly (no wrap).
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}
