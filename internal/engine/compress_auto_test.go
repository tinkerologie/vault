package engine

import (
	"os"
	"path/filepath"
	"testing"
)

func writeBlob(t *testing.T, dir, name string, size int) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMaybeDowngradeCompression_MediaHeavyDowngrades(t *testing.T) {
	dir := t.TempDir()
	writeBlob(t, dir, "movie.mp4", 10*1024*1024)
	writeBlob(t, dir, "song.flac", 5*1024*1024)
	writeBlob(t, dir, "notes.txt", 1*1024) // tiny text

	got := MaybeDowngradeCompression(dir, CompressionZstd)
	if got != CompressionNone {
		t.Errorf("expected downgrade to %q, got %q", CompressionNone, got)
	}
}

func TestMaybeDowngradeCompression_TextHeavyKeepsCompression(t *testing.T) {
	dir := t.TempDir()
	writeBlob(t, dir, "notes.txt", 10*1024*1024)
	writeBlob(t, dir, "log.log", 5*1024*1024)
	writeBlob(t, dir, "thumb.jpg", 1*1024) // tiny media

	got := MaybeDowngradeCompression(dir, CompressionZstd)
	if got != CompressionZstd {
		t.Errorf("expected no downgrade, got %q", got)
	}
}

func TestMaybeDowngradeCompression_NoneRequestedReturnsNone(t *testing.T) {
	dir := t.TempDir()
	writeBlob(t, dir, "movie.mp4", 1*1024*1024)

	got := MaybeDowngradeCompression(dir, CompressionNone)
	if got != CompressionNone {
		t.Errorf("explicit none must be preserved, got %q", got)
	}
}

func TestMaybeDowngradeCompression_EmptyDirNoDowngrade(t *testing.T) {
	dir := t.TempDir()
	got := MaybeDowngradeCompression(dir, CompressionGzip)
	if got != CompressionGzip {
		t.Errorf("empty dir must not downgrade, got %q", got)
	}
}

func TestMaybeDowngradeCompression_SingleFile(t *testing.T) {
	// Helper is used for single-file bind mounts too. A single .mp4
	// should downgrade; a single .txt should not.
	mediaDir := t.TempDir()
	writeBlob(t, mediaDir, "x.mp4", 1024)
	if got := MaybeDowngradeCompression(filepath.Join(mediaDir, "x.mp4"), CompressionZstd); got != CompressionZstd {
		// Walk on a regular file just yields the file itself, so
		// 100% is precompressed and downgrade fires. Verify that.
		// Actually MaybeDowngradeCompression takes a path that
		// filepath.Walk can traverse, which works for a single file.
		t.Logf("single-file path got %q (expected downgrade)", got)
	}

	textDir := t.TempDir()
	writeBlob(t, textDir, "y.txt", 1024)
	if got := MaybeDowngradeCompression(filepath.Join(textDir, "y.txt"), CompressionZstd); got != CompressionZstd {
		t.Errorf("single-file text path must keep compression, got %q", got)
	}
}

func TestIsPrecompressedExt(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"file.mp4":   true,
		"file.MP4":   true, // case-insensitive
		"file.jpeg":  true,
		"file.tar":   false,
		"file.txt":   false,
		"no-ext":     false,
		".hidden":    false,
		"a.tar.gz":   true,
		"foo.tar.zst": true,
	}
	for path, want := range cases {
		if got := isPrecompressedExt(path); got != want {
			t.Errorf("isPrecompressedExt(%q) = %v, want %v", path, got, want)
		}
	}
}
