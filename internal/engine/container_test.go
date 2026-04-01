package engine

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTarDirectory(t *testing.T) {
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "file1.txt"), []byte("hello"), 0644)
	os.MkdirAll(filepath.Join(src, "subdir"), 0755)
	os.WriteFile(filepath.Join(src, "subdir", "file2.txt"), []byte("world"), 0644)

	dst := filepath.Join(t.TempDir(), "test.tar.gz")
	if err := tarDirectory(src, dst, nil); err != nil {
		t.Fatalf("tarDirectory() error = %v", err)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("tar file not created: %v", err)
	}
	if info.Size() == 0 {
		t.Error("tar file is empty")
	}
}

func TestTarAndUntarRoundtrip(t *testing.T) {
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "data.txt"), []byte("vault backup"), 0644)

	tarPath := filepath.Join(t.TempDir(), "backup.tar.gz")
	if err := tarDirectory(src, tarPath, nil); err != nil {
		t.Fatalf("tarDirectory() error = %v", err)
	}

	restored := t.TempDir()
	if err := untarDirectory(tarPath, restored); err != nil {
		t.Fatalf("untarDirectory() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(restored, "data.txt"))
	if err != nil {
		t.Fatalf("restored file not found: %v", err)
	}
	if string(data) != "vault backup" {
		t.Errorf("data = %q, want %q", string(data), "vault backup")
	}
}

func TestUntarDirectoryRejectsTraversal(t *testing.T) {
	t.Parallel()

	archivePath := filepath.Join(t.TempDir(), "bad.tar.gz")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	defer file.Close()

	gzw := gzip.NewWriter(file)
	tw := tar.NewWriter(gzw)
	if err := tw.WriteHeader(&tar.Header{Name: "../evil.txt", Mode: 0o644, Size: int64(len("oops"))}); err != nil {
		t.Fatalf("WriteHeader() error = %v", err)
	}
	if _, err := io.WriteString(tw, "oops"); err != nil {
		t.Fatalf("WriteString() error = %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close error = %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("gzip close error = %v", err)
	}

	if err := untarDirectory(archivePath, t.TempDir()); err == nil {
		t.Fatal("untarDirectory() should reject traversal archive entries")
	}
}

func TestShouldSkipVolume(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		source     string
		wantSkip   bool
		wantReason string
	}{
		// Appdata — always backed up.
		{"appdata cache", "/mnt/cache/appdata/plex", false, ""},
		{"appdata user share", "/mnt/user/appdata/radarr", false, ""},
		{"boot config", "/boot/config/plugins/vault", false, ""},

		// Shared data — always skipped.
		{"media movies", "/mnt/user/media/movies", true, "shared data volume (/mnt/user/media)"},
		{"media tv", "/mnt/user/media/tv", true, "shared data volume (/mnt/user/media)"},
		{"downloads", "/mnt/user/downloads/complete", true, "shared data volume (/mnt/user/downloads)"},
		{"isos", "/mnt/user/isos", true, "shared data volume (/mnt/user/isos)"},
		{"domains", "/mnt/user/domains/win10", true, "shared data volume (/mnt/user/domains)"},
		{"backups", "/mnt/user/backups/vault", true, "shared data volume (/mnt/user/backups)"},
		{"remotes", "/mnt/remotes/nas", true, "shared data volume (/mnt/remotes)"},

		// Direct disk access — skipped.
		{"disk1", "/mnt/disk1/share", true, "direct disk volume"},
		{"disk12", "/mnt/disk12/data", true, "direct disk volume"},

		// Root /mnt — skipped.
		{"root mnt", "/mnt", true, "root /mnt mount"},

		// Device and virtual filesystem paths — skipped.
		{"dev rtc", "/dev/rtc", true, "device/virtual path (/dev)"},
		{"dev dri", "/dev/dri", true, "device/virtual path (/dev)"},
		{"proc", "/proc/self/fd", true, "device/virtual path (/proc)"},
		{"sys", "/sys/class/net", true, "device/virtual path (/sys)"},
		{"run", "/run/udev", true, "device/virtual path (/run)"},

		// Other system paths — backed up.
		{"tmp", "/tmp/something", false, ""},
		{"etc", "/etc/localtime", false, ""},
		{"custom", "/opt/myapp/config", false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotSkip, gotReason := shouldSkipVolume(tt.source)
			if gotSkip != tt.wantSkip {
				t.Errorf("shouldSkipVolume(%q) skip = %v, want %v", tt.source, gotSkip, tt.wantSkip)
			}
			if tt.wantReason != "" && gotReason != tt.wantReason {
				t.Errorf("shouldSkipVolume(%q) reason = %q, want %q", tt.source, gotReason, tt.wantReason)
			}
		})
	}
}

func TestRunWithRestartRestartsAfterBackupFailure(t *testing.T) {
	t.Parallel()

	backupErr := errors.New("backup failed")
	var restartCalled bool

	err := runWithRestart(true, "plex", func(string, int, string) {}, func() error {
		return backupErr
	}, func() error {
		restartCalled = true
		return nil
	})

	if !restartCalled {
		t.Fatal("expected restart to be attempted after backup failure")
	}
	if !errors.Is(err, backupErr) {
		t.Fatalf("expected backup error, got %v", err)
	}
}

func TestRunWithRestartJoinsRestartError(t *testing.T) {
	t.Parallel()

	backupErr := errors.New("backup failed")
	restartErr := errors.New("start failed")

	err := runWithRestart(true, "plex", func(string, int, string) {}, func() error {
		return backupErr
	}, func() error {
		return restartErr
	})

	if !errors.Is(err, backupErr) {
		t.Fatalf("expected joined error to include backup failure, got %v", err)
	}
	if !errors.Is(err, restartErr) {
		t.Fatalf("expected joined error to include restart failure, got %v", err)
	}
}

func TestRunWithRestartSkipsRestartWhenNotNeeded(t *testing.T) {
	t.Parallel()

	var restartCalled bool

	err := runWithRestart(false, "plex", func(string, int, string) {}, func() error {
		return nil
	}, func() error {
		restartCalled = true
		return nil
	})

	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if restartCalled {
		t.Fatal("expected restart to be skipped")
	}
}

func TestTarFileAndUntarFileRoundtrip(t *testing.T) {
	t.Parallel()

	// Create a source file to archive.
	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "hook_file")
	content := []byte("tailscale container hook content")
	if err := os.WriteFile(srcFile, content, 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	// Archive the single file.
	tarPath := filepath.Join(t.TempDir(), "volume.tar.gz")
	if err := tarFile(srcFile, tarPath); err != nil {
		t.Fatalf("tarFile() error = %v", err)
	}

	info, err := os.Stat(tarPath)
	if err != nil {
		t.Fatalf("tar file not created: %v", err)
	}
	if info.Size() == 0 {
		t.Error("tar file is empty")
	}

	// Verify the archive contains exactly one file with the correct name.
	f, err := os.Open(tarPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip.NewReader() error = %v", err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	header, err := tr.Next()
	if err != nil {
		t.Fatalf("tar.Next() error = %v", err)
	}
	if header.Name != "hook_file" {
		t.Errorf("tar entry name = %q, want %q", header.Name, "hook_file")
	}
	if _, err := tr.Next(); err != io.EOF {
		t.Error("expected exactly one entry in tar archive")
	}

	// Restore the file.
	restorePath := filepath.Join(t.TempDir(), "restored_hook")
	if err := untarFile(tarPath, restorePath); err != nil {
		t.Fatalf("untarFile() error = %v", err)
	}

	restored, err := os.ReadFile(restorePath)
	if err != nil {
		t.Fatalf("restored file not found: %v", err)
	}
	if string(restored) != string(content) {
		t.Errorf("restored content = %q, want %q", string(restored), string(content))
	}
}

func TestShouldExcludePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		relPath    string
		exclusions []string
		want       bool
	}{
		// Prefix matching.
		{"exact dir match", "Cache", []string{"Cache"}, true},
		{"nested under excluded dir", "Cache/Transcode/file.mp4", []string{"Cache"}, true},
		{"no match", "config/settings.xml", []string{"Cache"}, false},
		{"empty exclusions", "anything", nil, false},
		{"empty exclusions slice", "anything", []string{}, false},

		// Leading slash normalization.
		{"exclusion with leading slash", "Cache/foo", []string{"/Cache"}, true},
		{"exclusion without leading slash", "Cache/foo", []string{"Cache"}, true},

		// Glob matching.
		{"glob star-dot-log matches file", "app.log", []string{"*.log"}, true},
		{"glob star-dot-log matches nested", "subdir/debug.log", []string{"*.log"}, true},
		{"glob no match", "app.txt", []string{"*.log"}, false},
		{"glob question mark", "file1.tmp", []string{"file?.tmp"}, true},

		// Mixed prefix and glob.
		{"prefix and glob combined", "logs/app.log", []string{"Cache", "*.log"}, true},
		{"prefix match in mixed", "Cache/data", []string{"Cache", "*.log"}, true},
		{"no match in mixed", "config/app.conf", []string{"Cache", "*.log"}, false},

		// Edge cases.
		{"root path dot", ".", []string{"Cache"}, false},
		{"empty relpath", "", []string{"Cache"}, false},
		{"trailing slash on exclusion", "Cache/foo", []string{"Cache/"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := shouldExcludePath(tt.relPath, tt.exclusions)
			if got != tt.want {
				t.Errorf("shouldExcludePath(%q, %v) = %v, want %v", tt.relPath, tt.exclusions, got, tt.want)
			}
		})
	}
}

func TestTarFilePreservesPermissions(t *testing.T) {
	t.Parallel()

	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "executable")
	if err := os.WriteFile(srcFile, []byte("#!/bin/sh"), 0755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	tarPath := filepath.Join(t.TempDir(), "exec.tar.gz")
	if err := tarFile(srcFile, tarPath); err != nil {
		t.Fatalf("tarFile() error = %v", err)
	}

	restorePath := filepath.Join(t.TempDir(), "restored_exec")
	if err := untarFile(tarPath, restorePath); err != nil {
		t.Fatalf("untarFile() error = %v", err)
	}

	info, err := os.Stat(restorePath)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if info.Mode().Perm()&0111 == 0 {
		t.Errorf("expected executable permission bits, got %v", info.Mode().Perm())
	}
}

func TestMapExclusionsToVolume(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		exclusions       []string
		mountDestination string
		want             []string
	}{
		{
			"matching prefix stripped",
			[]string{"/config/Cache", "/config/Logs"},
			"/config",
			[]string{"Cache", "Logs"},
		},
		{
			"non-matching prefix dropped",
			[]string{"/data/stuff"},
			"/config",
			nil,
		},
		{
			"glob patterns pass through",
			[]string{"*.log", "/config/Cache"},
			"/config",
			[]string{"*.log", "Cache"},
		},
		{
			"mixed match and non-match",
			[]string{"/config/Cache", "/data/stuff", "*.tmp"},
			"/config",
			[]string{"Cache", "*.tmp"},
		},
		{
			"empty exclusions",
			nil,
			"/config",
			nil,
		},
		{
			"root mount",
			[]string{"/config/Cache"},
			"/",
			[]string{"config/Cache"},
		},
		{
			"exact mount match excluded",
			[]string{"/config"},
			"/config",
			[]string{"."},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := mapExclusionsToVolume(tt.exclusions, tt.mountDestination)
			if len(got) != len(tt.want) {
				t.Fatalf("mapExclusionsToVolume() = %v (len %d), want %v (len %d)", got, len(got), tt.want, len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("mapExclusionsToVolume()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestTarDirectoryWithExclusions(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "keep.txt"), []byte("keep"), 0644)
	os.MkdirAll(filepath.Join(src, "Cache"), 0755)
	os.WriteFile(filepath.Join(src, "Cache", "temp.dat"), []byte("temp"), 0644)
	os.MkdirAll(filepath.Join(src, "logs"), 0755)
	os.WriteFile(filepath.Join(src, "logs", "app.log"), []byte("log"), 0644)
	os.WriteFile(filepath.Join(src, "debug.log"), []byte("debug"), 0644)

	dst := filepath.Join(t.TempDir(), "test.tar.gz")
	if err := tarDirectory(src, dst, []string{"Cache", "*.log"}); err != nil {
		t.Fatalf("tarDirectory() error = %v", err)
	}

	names := tarEntryNames(t, dst)

	if !containsEntry(names, "keep.txt") {
		t.Error("expected keep.txt in archive")
	}
	if !containsEntry(names, "logs") {
		t.Error("expected logs/ directory in archive")
	}
	for _, name := range names {
		if strings.HasPrefix(name, "Cache") {
			t.Errorf("unexpected Cache entry in archive: %s", name)
		}
	}
	for _, name := range names {
		if strings.HasSuffix(name, ".log") {
			t.Errorf("unexpected .log file in archive: %s", name)
		}
	}
}

func TestTarDirectoryNilExclusions(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "file.txt"), []byte("data"), 0644)

	dst := filepath.Join(t.TempDir(), "test.tar.gz")
	if err := tarDirectory(src, dst, nil); err != nil {
		t.Fatalf("tarDirectory() with nil exclusions error = %v", err)
	}

	names := tarEntryNames(t, dst)
	if !containsEntry(names, "file.txt") {
		t.Error("expected file.txt in archive with nil exclusions")
	}
}

func tarEntryNames(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip.NewReader() error = %v", err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	var names []string
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar.Next() error = %v", err)
		}
		names = append(names, header.Name)
	}
	return names
}

func containsEntry(names []string, target string) bool {
	for _, n := range names {
		if n == target || strings.TrimSuffix(n, "/") == target {
			return true
		}
	}
	return false
}
