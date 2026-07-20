package handlers

import (
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
)

func TestPathsOverlap(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		a, b string
		want bool
	}{
		{"equal", "/mnt/user/appdata", "/mnt/user/appdata", true},
		{"b under a", "/mnt/user", "/mnt/user/backups", true},
		{"a under b", "/mnt/user/appdata/plex", "/mnt/user/appdata", true},
		{"siblings", "/mnt/user/appdata", "/mnt/user/media", false},
		{"different roots", "/mnt/user/appdata", "/mnt/cache/appdata", false},
		{"prefix but not path parent", "/mnt/user/app", "/mnt/user/appdata", false},
		{"cleaned to equal", "/mnt/user/appdata/", "/mnt/user/appdata/plex/..", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := pathsOverlap(tc.a, tc.b); got != tc.want {
				t.Errorf("pathsOverlap(%q,%q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestNormalizeCompressionLevel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		compression string
		level       string
		want        string
	}{
		{"none clears level", "none", "best", ""},
		{"unknown compression clears level", "weird", "best", ""},
		{"default collapses to empty", "zstd", "default", ""},
		{"empty stays empty", "zstd", "", ""},
		{"unknown collapses to empty", "zstd", "wild", ""},
		{"valid best kept", "zstd", "best", "best"},
		{"valid fastest kept", "gzip", "fastest", "fastest"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeCompressionLevel(tc.compression, tc.level); got != tc.want {
				t.Errorf("normalizeCompressionLevel(%q,%q) = %q, want %q", tc.compression, tc.level, got, tc.want)
			}
		})
	}
}

func TestFolderSourceOverlap(t *testing.T) {
	t.Parallel()
	// Flash items are folder-typed (Type "folder" with preset "flash"), so the
	// "folder" guard covers them; a container item's path must be ignored.
	items := []db.JobItem{
		{ItemType: "folder", Settings: `{"path":"/mnt/user/documents"}`},
		{ItemType: "folder", ItemID: "/mnt/user/by-id"},
		{ItemType: "folder", ItemName: "/mnt/user/by-name"},
		{ItemType: "container", Settings: `{"path":"/mnt/user/backups"}`},
	}
	tests := []struct {
		name string
		dest string
		want bool
	}{
		{"dest inside folder source", "/mnt/user/documents/backups", true},
		{"item ID fallback", "/mnt/user/by-id/backups", true},
		{"item name fallback", "/mnt/user/by-name/backups", true},
		{"dest outside any source", "/mnt/user/backups", false},
		{"remote (empty) dest never overlaps", "", false},
		{"container path is not a folder source", "/mnt/user/backups/sub", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, bad := folderSourceOverlap(tc.dest, items); bad != tc.want {
				t.Errorf("folderSourceOverlap(%q) = %v, want %v", tc.dest, bad, tc.want)
			}
		})
	}
}
