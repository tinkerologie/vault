package engine

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTarDirectory(t *testing.T) {
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "file1.txt"), []byte("hello"), 0644)
	os.MkdirAll(filepath.Join(src, "subdir"), 0755)
	os.WriteFile(filepath.Join(src, "subdir", "file2.txt"), []byte("world"), 0644)

	dst := filepath.Join(t.TempDir(), "test.tar.gz")
	if err := tarDirectory(src, dst); err != nil {
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
	if err := tarDirectory(src, tarPath); err != nil {
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
