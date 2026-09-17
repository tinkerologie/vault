package engine

import (
	"archive/tar"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/dedup"
)

func TestRestoreSecurity_UntarGuards(t *testing.T) {
	ctx := context.Background()

	// untarFile rejects suspicious destination path
	err := untarFile(ctx, "/fake.tar", "../suspicious")
	if err == nil {
		t.Fatal("untarFile accepted suspicious destination path")
	}

	// untarDirectoryFiltered rejects suspicious destination directory
	err = untarDirectoryFiltered(ctx, "/fake.tar", "../suspicious", nil)
	if err == nil {
		t.Fatal("untarDirectoryFiltered accepted suspicious destination directory")
	}

	// removeExistingNonDir rejects suspicious target path
	removeExistingNonDir("../suspicious")
	removeExistingNonDir("..\\suspicious")

	// removeExistingNonDir removes real file
	f := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(f, []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
	removeExistingNonDir(f)
	if _, err := os.Stat(f); err == nil {
		t.Fatal("removeExistingNonDir failed to remove real file")
	}
}

func TestRestoreSecurity_MkdirRestoredSymlink(t *testing.T) {
	dir := t.TempDir()

	// 1. Rejects suspicious path
	err := mkdirRestored("../outside", 0o755)
	if err == nil {
		t.Fatal("mkdirRestored accepted traversal path")
	}

	// 2. Rejects backslash traversal
	err = mkdirRestored("..\\outside", 0o755)
	if err == nil {
		t.Fatal("mkdirRestored accepted backslash traversal path")
	}

	// 3. Rejects existing symlink
	realDir := filepath.Join(dir, "real_dir")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	symlinkDir := filepath.Join(dir, "symlink_dir")
	if err := os.Symlink(realDir, symlinkDir); err == nil {
		err = mkdirRestored(symlinkDir, 0o755)
		if err == nil {
			t.Fatal("mkdirRestored created through symlink without error")
		}
	}
}

func TestRestoreSecurity_FolderRestoreUnsafeDestination(t *testing.T) {
	h := &FolderHandler{}
	item := BackupItem{
		Name: "test-folder",
		Settings: map[string]any{
			"restore_destination": "../suspicious",
		},
	}
	err := h.Restore(context.Background(), item, t.TempDir(), func(item string, pct int, msg string) {})
	if err == nil {
		t.Fatal("FolderHandler.Restore accepted suspicious destination")
	}
}

func TestRestoreSecurity_VMRestoreUnsafeDestination(t *testing.T) {
	h := &VMHandler{}
	dir := t.TempDir()
	xmlPath := filepath.Join(dir, "domain.xml")
	if err := os.WriteFile(xmlPath, []byte("<domain type='kvm'><name>test-vm</name></domain>"), 0o644); err != nil {
		t.Fatal(err)
	}
	item := BackupItem{
		Name: "test-vm",
		Settings: map[string]any{
			"restore_destination": "../suspicious",
		},
	}
	err := h.Restore(context.Background(), item, dir, func(item string, pct int, msg string) {})
	if err == nil {
		t.Fatal("VMHandler.Restore accepted suspicious destination")
	}
}

func createTestTar(t *testing.T, tarPath string) {
	t.Helper()
	f, err := os.Create(tarPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	tw := tar.NewWriter(f)
	defer tw.Close()

	// 1. Directory
	dirHdr := &tar.Header{
		Name:     "folder/",
		Mode:     0o755,
		Typeflag: tar.TypeDir,
		ModTime:  time.Now(),
	}
	if err := tw.WriteHeader(dirHdr); err != nil {
		t.Fatal(err)
	}

	// 2. Regular file
	fileContent := []byte("hello vault")
	fileHdr := &tar.Header{
		Name:     "folder/file.txt",
		Mode:     0o644,
		Size:     int64(len(fileContent)),
		Typeflag: tar.TypeReg,
		ModTime:  time.Now(),
	}
	if err := tw.WriteHeader(fileHdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(fileContent); err != nil {
		t.Fatal(err)
	}

	// 3. Symlink
	symHdr := &tar.Header{
		Name:     "folder/link.txt",
		Linkname: "file.txt",
		Typeflag: tar.TypeSymlink,
		ModTime:  time.Now(),
	}
	if err := tw.WriteHeader(symHdr); err != nil {
		t.Fatal(err)
	}

	// 4. Hard link
	hardHdr := &tar.Header{
		Name:     "folder/hardlink.txt",
		Linkname: "folder/file.txt",
		Typeflag: tar.TypeLink,
		ModTime:  time.Now(),
	}
	if err := tw.WriteHeader(hardHdr); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreSecurity_UntarArchiveTypes(t *testing.T) {
	dir := t.TempDir()
	tarPath := filepath.Join(dir, "archive.tar")
	createTestTar(t, tarPath)

	destDir := filepath.Join(dir, "dest")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	// Test untarDirectoryFiltered
	if err := untarDirectoryFiltered(ctx, tarPath, destDir, nil); err != nil {
		t.Fatalf("untarDirectoryFiltered failed: %v", err)
	}

	// Verify regular file extracted
	extractedFile := filepath.Join(destDir, "folder", "file.txt")
	if _, err := os.Stat(extractedFile); err != nil {
		t.Fatalf("extracted file missing: %v", err)
	}

	// Test untarFile
	singleDest := filepath.Join(destDir, "single.txt")
	if err := untarFile(ctx, tarPath, singleDest); err != nil {
		t.Fatalf("untarFile failed: %v", err)
	}
	if _, err := os.Stat(singleDest); err != nil {
		t.Fatalf("single extracted file missing: %v", err)
	}

	// Test untarDirectoryFiltered when destination file collides with a directory
	destCollisionDir := filepath.Join(dir, "dest_collision")
	_ = os.MkdirAll(filepath.Join(destCollisionDir, "folder", "file.txt"), 0o755)
	if err := untarDirectoryFiltered(ctx, tarPath, destCollisionDir, nil); err == nil {
		t.Fatal("expected error when regular file collides with existing directory")
	}

	// Test untarFile when destination file collides with a directory
	destFileCollisionDir := filepath.Join(dir, "single_collision")
	_ = os.MkdirAll(destFileCollisionDir, 0o755)
	if err := untarFile(ctx, tarPath, destFileCollisionDir); err == nil {
		t.Fatal("expected error when untarFile destination is an existing directory")
	}
}

func TestRestoreSecurity_DatabaseDumpAndVolumeFile(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// 1. preserveDatabaseDump success
	dumpSrc := filepath.Join(dir, "source.sql")
	if err := os.WriteFile(dumpSrc, []byte("SELECT 1;"), 0o644); err != nil {
		t.Fatal(err)
	}
	destBase := filepath.Join(dir, "dump_dest")
	res, err := preserveDatabaseDump(ctx, dumpSrc, destBase, restoreInspect{Name: "my_container"})
	if err != nil {
		t.Fatalf("preserveDatabaseDump failed: %v", err)
	}
	if _, err := os.Stat(res); err != nil {
		t.Fatalf("restored dump not found: %v", err)
	}

	// 2. preserveDatabaseDump suspicious base
	if _, err := preserveDatabaseDump(ctx, dumpSrc, "../escape", restoreInspect{Name: "my_container"}); err == nil {
		t.Fatal("preserveDatabaseDump accepted suspicious base")
	}

	// 3. restoreChunkedVolumeFile success
	mountTarget := filepath.Join(dir, "mount_dir", "mount.txt")
	if err := restoreChunkedVolumeFile(nil, dedup.ManifestEntry{Mode: 0o644}, mountTarget); err != nil {
		t.Fatalf("restoreChunkedVolumeFile failed: %v", err)
	}
	if _, err := os.Stat(mountTarget); err != nil {
		t.Fatalf("mountTarget not found: %v", err)
	}

	// 4. restoreChunkedVolumeFile suspicious target
	if err := restoreChunkedVolumeFile(nil, dedup.ManifestEntry{Mode: 0o644}, "../escape/mount.txt"); err == nil {
		t.Fatal("restoreChunkedVolumeFile accepted suspicious target")
	}
}
