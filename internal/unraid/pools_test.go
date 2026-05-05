package unraid

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverPoolsIn(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(root string)
		want  []string // basenames in expected order
	}{
		{
			name:  "empty mnt returns no pools",
			setup: func(_ string) {},
			want:  nil,
		},
		{
			name: "cache sorts first",
			setup: func(root string) {
				for _, n := range []string{"nvme", "cache", "ssd"} {
					os.Mkdir(filepath.Join(root, n), 0o755)
				}
			},
			want: []string{"cache", "nvme", "ssd"},
		},
		{
			name: "excludes known dirs",
			setup: func(root string) {
				for _, n := range []string{"user", "user0", "disks", "remotes", "disk1", "disk23", "mypool"} {
					os.Mkdir(filepath.Join(root, n), 0o755)
				}
			},
			want: []string{"mypool"},
		},
		{
			name: "skips files",
			setup: func(root string) {
				os.WriteFile(filepath.Join(root, "notapool"), []byte("data"), 0o644)
				os.Mkdir(filepath.Join(root, "cache"), 0o755)
			},
			want: []string{"cache"},
		},
		{
			name:  "nonexistent root returns empty",
			setup: nil, // use a non-existent path below
			want:  nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var root string
			if tc.setup == nil {
				root = "/nonexistent/path"
			} else {
				root = t.TempDir()
				tc.setup(root)
			}

			pools := discoverPoolsIn(root)
			if len(tc.want) == 0 {
				if len(pools) != 0 {
					t.Errorf("expected no pools, got %v", pools)
				}
				return
			}
			if len(pools) != len(tc.want) {
				t.Fatalf("expected %d pools, got %v", len(tc.want), pools)
			}
			for i, want := range tc.want {
				if filepath.Base(pools[i]) != want {
					t.Errorf("pools[%d]: expected %s, got %s", i, want, filepath.Base(pools[i]))
				}
			}
		})
	}
}

func TestIsMountedPoolFrom(t *testing.T) {
	t.Parallel()

	// Create a fake mountinfo file.
	tmpDir := t.TempDir()
	mountInfoFile := filepath.Join(tmpDir, "mountinfo")
	content := `35 22 8:2 / /mnt/cache rw,relatime - btrfs /dev/sdb1 rw,space_cache
40 22 8:3 / /mnt/nvme rw,relatime - xfs /dev/nvme0n1p1 rw
50 22 8:4 / /mnt/pool\040name rw,relatime - btrfs /dev/sdc1 rw
`
	os.WriteFile(mountInfoFile, []byte(content), 0o644)

	tests := []struct {
		name     string
		poolPath string
		want     bool
	}{
		{"mounted cache pool", "/mnt/cache", true},
		{"mounted nvme pool", "/mnt/nvme", true},
		{"pool with space in name", "/mnt/pool name", true},
		{"not mounted pool", "/mnt/ssd", false},
		{"root is not a pool", "/", false},
		{"empty path", "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := isMountedPoolFrom(mountInfoFile, tc.poolPath)
			if got != tc.want {
				t.Errorf("isMountedPoolFrom(%q) = %v, want %v", tc.poolPath, got, tc.want)
			}
		})
	}
}

func TestIsMountedPoolFromMissingFile(t *testing.T) {
	t.Parallel()
	got := isMountedPoolFrom("/nonexistent/mountinfo", "/mnt/cache")
	if got {
		t.Error("expected false for nonexistent mountinfo file")
	}
}

func TestPreferredMountedPoolIn(t *testing.T) {
	t.Parallel()

	mountedSet := func(paths ...string) func(string) bool {
		set := make(map[string]bool, len(paths))
		for _, p := range paths {
			set[p] = true
		}
		return func(p string) bool { return set[p] }
	}

	t.Run("prefers mounted cache over mounted nvme", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		for _, n := range []string{"cache", "nvme"} {
			os.Mkdir(filepath.Join(root, n), 0o755)
		}
		cache := filepath.Join(root, "cache")
		nvme := filepath.Join(root, "nvme")
		got := preferredMountedPoolIn(root, mountedSet(cache, nvme))
		if got != cache {
			t.Errorf("got %s, want %s", got, cache)
		}
	})

	t.Run("skips unmounted cache and picks mounted nvme", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		for _, n := range []string{"cache", "nvme"} {
			os.Mkdir(filepath.Join(root, n), 0o755)
		}
		nvme := filepath.Join(root, "nvme")
		got := preferredMountedPoolIn(root, mountedSet(nvme))
		if got != nvme {
			t.Errorf("got %s, want %s (unmounted /mnt/cache must not win — issue #69)", got, nvme)
		}
	})

	t.Run("falls back to first discovered when none mounted", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		os.Mkdir(filepath.Join(root, "nvme"), 0o755)
		nvme := filepath.Join(root, "nvme")
		got := preferredMountedPoolIn(root, mountedSet())
		if got != nvme {
			t.Errorf("got %s, want %s", got, nvme)
		}
	})

	t.Run("returns empty when no pools discovered", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		got := preferredMountedPoolIn(root, mountedSet())
		if got != "" {
			t.Errorf("got %s, want empty", got)
		}
	})
}

func TestPublicWrappersWithOverriddenRoots(t *testing.T) {
	// Not parallel — these tests mutate package-level vars.
	origMnt := mntBase
	origInfo := mountInfoPath
	t.Cleanup(func() {
		mntBase = origMnt
		mountInfoPath = origInfo
	})

	root := t.TempDir()
	for _, n := range []string{"cache", "nvme", "user", "disk1"} {
		if err := os.Mkdir(filepath.Join(root, n), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	mntBase = root

	// DiscoverPools wrapper
	pools := DiscoverPools()
	if len(pools) != 2 {
		t.Fatalf("DiscoverPools returned %d pools, want 2 (got %v)", len(pools), pools)
	}
	if filepath.Base(pools[0]) != "cache" {
		t.Errorf("cache should sort first, got %v", pools)
	}

	// IsMountedPool wrapper — point mountInfoPath at a fake mountinfo
	mountInfo := filepath.Join(t.TempDir(), "mountinfo")
	cachePath := filepath.Join(root, "cache")
	content := "35 22 8:2 / " + cachePath + " rw,relatime - btrfs /dev/sdb1 rw\n"
	if err := os.WriteFile(mountInfo, []byte(content), 0o644); err != nil {
		t.Fatalf("write mountinfo: %v", err)
	}
	mountInfoPath = mountInfo
	if !IsMountedPool(cachePath) {
		t.Errorf("IsMountedPool(%s) = false, want true", cachePath)
	}
	if IsMountedPool(filepath.Join(root, "nvme")) {
		t.Error("IsMountedPool for unmounted path should be false")
	}

	// PreferredPool wrapper — should pick mounted cache
	if got := PreferredPool(); got != cachePath {
		t.Errorf("PreferredPool() = %s, want %s", got, cachePath)
	}
}
