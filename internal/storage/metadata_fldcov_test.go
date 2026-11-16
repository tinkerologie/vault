package storage

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestBoundedLocalListingRefusesUnusableDirectories(t *testing.T) {
	root := t.TempDir()
	a := NewLocalAdapter(root)
	if _, err := a.listBounded(t.Context(), "../outside", 4); err == nil {
		t.Fatal("listing escaped the storage root")
	}
	if _, err := a.listBounded(t.Context(), "missing", 4); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing prefix: %v", err)
	}
	if os.Getuid() == 0 {
		t.Skip("root can read a directory without permissions")
	}
	locked := filepath.Join(root, "locked")
	if err := os.Mkdir(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	if _, err := a.listBounded(t.Context(), "locked", 4); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("unreadable prefix: %v", err)
	}
}

func TestBoundedMetadataRefusesEscapingS3Keys(t *testing.T) {
	a, err := NewS3Adapter(S3Config{Bucket: "b", Region: "r", AccessKey: "a", SecretKey: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.listBounded(t.Context(), "../outside", 4); err == nil {
		t.Fatal("S3 listing escaped its prefix")
	}
	if _, err := a.statBounded(t.Context(), "../outside"); err == nil {
		t.Fatal("S3 stat escaped its prefix")
	}
}

func TestBoundedMetadataNFSMountFailure(t *testing.T) {
	originalMount := mountExport
	t.Cleanup(func() { mountExport = originalMount })
	mountExport = func(...string) ([]byte, error) { return nil, errors.New("fixture mount refused") }
	a, err := NewAdapterWithOptions("nfs", `{"host":"fixture","export":"/exports"}`, Options{MetadataProbeLimit: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { CloseAdapter(a) })
	if _, err := a.List("point"); err == nil {
		t.Fatal("listing succeeded without a mounted export")
	}
}
