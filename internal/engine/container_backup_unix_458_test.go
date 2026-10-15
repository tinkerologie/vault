//go:build unix

package engine

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"

	containertypes "github.com/moby/moby/api/types/container"
	mounttypes "github.com/moby/moby/api/types/mount"
)

// A bind mount of a non-regular inode (a FIFO here; sockets such as
// /var/run/docker.sock take the same path) is skipped with a reason rather
// than failing the backup, since archive/tar cannot record it.
func TestContainerBackupSkipsSpecialInode458(t *testing.T) {
	t.Parallel()

	keep := t.TempDir()
	writeFile458(t, filepath.Join(keep, "a"), "a")
	fifo := filepath.Join(t.TempDir(), "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	f := backupFake458(containertypes.InspectResponse{
		ID: "cid", Name: "/c", Config: &containertypes.Config{Image: "c"}, State: &containertypes.State{},
		Mounts: []containertypes.MountPoint{
			{Type: mounttypes.TypeBind, Source: keep, Destination: "/config"},
			{Type: mounttypes.TypeBind, Source: fifo, Destination: "/pipe"},
		},
	})
	dest := t.TempDir()
	if _, err := (&ContainerHandler{cli: f}).Backup(t.Context(), BackupItem{Name: "c", Settings: map[string]any{"id": "cid"}}, dest, noopProgress); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	for _, e := range readManifestForTest(t, dest) {
		if e.Destination == "/pipe" && (e.BackedUp || e.SkipReason == "") {
			t.Errorf("fifo entry = %+v, want skipped with a reason", e)
		}
	}
}

// A FIFO inside a volume is skipped by both archivers instead of failing the
// archive.
func TestTarDirectorySkipsSpecialFiles458(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	writeFile458(t, filepath.Join(src, "keep.txt"), "k")
	if err := syscall.Mkfifo(filepath.Join(src, "pipe"), 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	full := filepath.Join(t.TempDir(), "full.tar")
	if _, err := tarDirectoryReporting(t.Context(), src, full, nil, CompressionNone); err != nil {
		t.Fatalf("tarDirectoryReporting: %v", err)
	}
	filtered := filepath.Join(t.TempDir(), "filtered.tar")
	if _, err := tarDirectoryFilteredReporting(t.Context(), src, filtered, time.Time{}, nil, CompressionNone, nil); err != nil {
		t.Fatalf("tarDirectoryFilteredReporting: %v", err)
	}
	for _, archive := range []string{full, filtered} {
		names := listTarEntries(t, archive)
		if !containsName(names, "keep.txt") || containsName(names, "pipe") {
			t.Errorf("%s entries = %v", filepath.Base(archive), names)
		}
	}
}
