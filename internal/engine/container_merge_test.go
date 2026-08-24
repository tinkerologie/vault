package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMergeContainerChainStagingOverlaysVolumes is the regression test for the
// classic-path half of issue #320. The full step's volume_0.tar holds old.txt;
// the differential step's volume_0.tar holds only new.txt. Merging must produce
// a single volume_0.tar containing BOTH, so the engine's ContainerHandler.Restore
// (which untars the merged archive once) restores the complete volume.
func TestMergeContainerChainStagingOverlaysVolumes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		setup      func(t *testing.T) (stepDirs []string, wantConfig string)
		wantVolume []string
	}{
		{
			name: "differential step's partial volume overlays the full step's",
			setup: func(t *testing.T) ([]string, string) {
				// Full step: old.txt only.
				fullDir := t.TempDir()
				fullVol := filepath.Join(fullDir, "volroot")
				if err := os.MkdirAll(fullVol, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(fullVol, "old.txt"), []byte("old"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := tarDirectory(context.Background(), fullVol, filepath.Join(fullDir, "volume_0.tar"), nil, CompressionNone); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(fullDir, "config.json"), []byte(`{"full":true}`), 0o600); err != nil {
					t.Fatal(err)
				}

				// Differential step: new.txt only.
				diffDir := t.TempDir()
				diffVol := filepath.Join(diffDir, "volroot")
				if err := os.MkdirAll(diffVol, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(diffVol, "new.txt"), []byte("new"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := tarDirectory(context.Background(), diffVol, filepath.Join(diffDir, "volume_0.tar"), nil, CompressionNone); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(diffDir, "config.json"), []byte(`{"diff":true}`), 0o600); err != nil {
					t.Fatal(err)
				}
				return []string{fullDir, diffDir}, `{"diff":true}`
			},
			wantVolume: []string{"old.txt", "new.txt"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stepDirs, wantConfig := tt.setup(t)
			outDir := t.TempDir()
			if err := MergeContainerChainStaging(context.Background(), stepDirs, outDir); err != nil {
				t.Fatalf("MergeContainerChainStaging() error = %v", err)
			}
			// config.json comes from the newest step.
			assertMergedFile(t, outDir, "config.json", wantConfig)
			assertMergedVolumeHas(t, outDir, tt.wantVolume...)
		})
	}
}

// TestMergeCopyFilePreservesMode verifies mergeCopyFile copies the source's
// permission bits rather than defaulting to 0644 (issue #320 review feedback).
func TestMergeCopyFilePreservesMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mode    os.FileMode
		content string
	}{
		{
			name:    "0600 source mode and content are copied",
			mode:    0o600,
			content: `{"a":1}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(src, []byte(tt.content), tt.mode); err != nil {
				t.Fatal(err)
			}

			dst := filepath.Join(t.TempDir(), "config.json")
			if err := mergeCopyFile(src, dst); err != nil {
				t.Fatalf("mergeCopyFile: %v", err)
			}

			info, err := os.Stat(dst)
			if err != nil {
				t.Fatalf("stat dst: %v", err)
			}
			if info.Mode().Perm() != tt.mode {
				t.Errorf("dst mode = %o, want %o", info.Mode().Perm(), tt.mode)
			}

			data, err := os.ReadFile(dst)
			if err != nil {
				t.Fatalf("read dst: %v", err)
			}
			if string(data) != tt.content {
				t.Errorf("dst content = %q, want %q", string(data), tt.content)
			}
		})
	}
}

// TestMergeContainerChainStagingSidecarsAndImage exercises the non-volume parts
// of MergeContainerChainStaging that the overlay regression test does not
// reach: plain sidecar files (config.json / template.xml / volumes.json)
// newest-step-wins, and image.tar located across steps regardless of its
// compression suffix (issue #320).
func TestMergeContainerChainStagingSidecarsAndImage(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(t *testing.T) (stepDirs []string, outDir string)
		verify func(t *testing.T, outDir string)
	}{
		{
			name: "all sidecars plus plain image copied, newest config wins",
			setup: func(t *testing.T) ([]string, string) {
				full := t.TempDir()
				diff := t.TempDir()
				for _, f := range []struct{ name, content string }{
					{"config.json", `{"full":true}`},
					{"template.xml", "<template/>"},
					{"volumes.json", `{"volumes":[]}`},
					{"image.tar", "image-bytes"},
				} {
					if err := os.WriteFile(filepath.Join(full, f.name), []byte(f.content), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(filepath.Join(diff, "config.json"), []byte(`{"diff":true}`), 0o600); err != nil {
					t.Fatal(err)
				}
				return []string{full, diff}, t.TempDir()
			},
			verify: func(t *testing.T, outDir string) {
				assertMergedFile(t, outDir, "config.json", `{"diff":true}`)
				assertMergedFile(t, outDir, "template.xml", "<template/>")
				assertMergedFile(t, outDir, "volumes.json", `{"volumes":[]}`)
				assertMergedFile(t, outDir, "image.tar", "image-bytes")
			},
		},
		{
			name: "gzip-suffixed image archive located via findArchive",
			setup: func(t *testing.T) ([]string, string) {
				full := t.TempDir()
				if err := os.WriteFile(filepath.Join(full, "image.tar.gz"), []byte("gz-image-bytes"), 0o600); err != nil {
					t.Fatal(err)
				}
				return []string{full}, t.TempDir()
			},
			verify: func(t *testing.T, outDir string) {
				assertMergedFile(t, outDir, "image.tar.gz", "gz-image-bytes")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stepDirs, outDir := tt.setup(t)
			if err := MergeContainerChainStaging(context.Background(), stepDirs, outDir); err != nil {
				t.Fatalf("MergeContainerChainStaging() error = %v", err)
			}
			tt.verify(t, outDir)
		})
	}
}

// TestMergeContainerChainStagingVolumeOverlay exercises the volume-archive
// overlay across chain steps: compression-suffixed volumes (volume_0.tar.gz)
// are normalised to their plain base name before merging, and a step that
// omits a volume archive (unchanged) is skipped rather than failing the merge
// (issue #320).
func TestMergeContainerChainStagingVolumeOverlay(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(t *testing.T) (stepDirs []string, outDir string)
		verify func(t *testing.T, outDir string)
	}{
		{
			name: "gzip-suffixed volumes overlay oldest-first",
			setup: func(t *testing.T) ([]string, string) {
				full := t.TempDir()
				diff := t.TempDir()
				writeTestVolume(t, full, "volume_0.tar.gz", CompressionGzip, map[string]string{"old.txt": "old"})
				writeTestVolume(t, diff, "volume_0.tar.gz", CompressionGzip, map[string]string{"new.txt": "new"})
				return []string{full, diff}, t.TempDir()
			},
			verify: func(t *testing.T, outDir string) {
				assertMergedVolumeHas(t, outDir, "old.txt", "new.txt")
			},
		},
		{
			name: "middle step omitting the volume is skipped",
			setup: func(t *testing.T) ([]string, string) {
				full := t.TempDir()
				mid := t.TempDir()
				diff := t.TempDir()
				writeTestVolume(t, full, "volume_0.tar", CompressionNone, map[string]string{"old.txt": "old"})
				writeTestVolume(t, diff, "volume_0.tar", CompressionNone, map[string]string{"new.txt": "new"})
				return []string{full, mid, diff}, t.TempDir()
			},
			verify: func(t *testing.T, outDir string) {
				assertMergedVolumeHas(t, outDir, "old.txt", "new.txt")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stepDirs, outDir := tt.setup(t)
			if err := MergeContainerChainStaging(context.Background(), stepDirs, outDir); err != nil {
				t.Fatalf("MergeContainerChainStaging() error = %v", err)
			}
			tt.verify(t, outDir)
		})
	}
}

// TestMergeContainerChainStagingErrors exercises the error paths of
// MergeContainerChainStaging: merge-dir creation failure, a sidecar that
// cannot be copied, a corrupt volume archive, and an unreadable step dir.
func TestMergeContainerChainStagingErrors(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T) (stepDirs []string, outDir string)
		wantErr string
	}{
		{
			name: "out dir creation fails",
			setup: func(t *testing.T) ([]string, string) {
				blocker := filepath.Join(t.TempDir(), "file")
				if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
				return []string{t.TempDir()}, filepath.Join(blocker, "sub", "dir")
			},
			wantErr: "creating merge dir",
		},
		{
			name: "sidecar copy fails",
			setup: func(t *testing.T) ([]string, string) {
				step := t.TempDir()
				if err := os.MkdirAll(filepath.Join(step, "config.json"), 0o755); err != nil {
					t.Fatal(err)
				}
				return []string{step}, t.TempDir()
			},
			wantErr: "merge sidecar config.json",
		},
		{
			name: "corrupt volume archive fails extraction",
			setup: func(t *testing.T) ([]string, string) {
				step := t.TempDir()
				if err := os.WriteFile(filepath.Join(step, "volume_0.tar"), []byte("this is not a tar archive"), 0o644); err != nil {
					t.Fatal(err)
				}
				return []string{step}, t.TempDir()
			},
			wantErr: "extracting volume_0.tar",
		},
		{
			name: "image archive copy fails",
			setup: func(t *testing.T) ([]string, string) {
				step := t.TempDir()
				if err := os.MkdirAll(filepath.Join(step, "image.tar"), 0o755); err != nil {
					t.Fatal(err)
				}
				return []string{step}, t.TempDir()
			},
			wantErr: "merge image archive",
		},
		{
			name: "work dir creation fails",
			setup: func(t *testing.T) ([]string, string) {
				step := t.TempDir()
				writeTestVolume(t, step, "volume_0.tar", CompressionNone, map[string]string{"old.txt": "old"})
				out := t.TempDir()
				if err := os.WriteFile(filepath.Join(out, "volume_0.tar.merge"), []byte("blocker"), 0o600); err != nil {
					t.Fatal(err)
				}
				return []string{step}, out
			},
			wantErr: "creating work dir",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stepDirs, outDir := tt.setup(t)
			err := MergeContainerChainStaging(context.Background(), stepDirs, outDir)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %q, want it to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

// TestMergeContainerChainStagingUnreadableStepDir verifies that a step
// directory that cannot be read is skipped (continue) rather than aborting the
// merge, while still merging the readable steps.
func TestMergeContainerChainStagingUnreadableStepDir(t *testing.T) {
	tests := []struct {
		name string
	}{
		{
			name: "unreadable step dir is skipped and readable steps still merge",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			full := t.TempDir()
			writeTestVolume(t, full, "volume_0.tar", CompressionNone, map[string]string{"old.txt": "old"})

			outDir := t.TempDir()
			err := MergeContainerChainStaging(context.Background(), []string{full, filepath.Join(t.TempDir(), "does-not-exist")}, outDir)
			if err != nil {
				t.Fatalf("MergeContainerChainStaging() error = %v", err)
			}
			assertMergedVolumeHas(t, outDir, "old.txt")
		})
	}
}

// TestMergeCopyFileErrors exercises the error branches of mergeCopyFile:
// a missing source, an unwritable destination, and a directory source (which
// fails during the copy and must clean up the partial destination).
func TestMergeCopyFileErrors(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T) (src, dst string)
		wantErr string
	}{
		{
			name: "missing source",
			setup: func(t *testing.T) (string, string) {
				return filepath.Join(t.TempDir(), "nope"), filepath.Join(t.TempDir(), "out")
			},
			wantErr: "no such file",
		},
		{
			name: "missing parent directory",
			setup: func(t *testing.T) (string, string) {
				src := filepath.Join(t.TempDir(), "src")
				if err := os.WriteFile(src, []byte("data"), 0o600); err != nil {
					t.Fatal(err)
				}
				return src, filepath.Join(t.TempDir(), "missing-parent", "out")
			},
			wantErr: "no such file",
		},
		{
			name: "source is a directory",
			setup: func(t *testing.T) (string, string) {
				return t.TempDir(), filepath.Join(t.TempDir(), "out")
			},
			wantErr: "is a directory",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src, dst := tt.setup(t)
			err := mergeCopyFile(src, dst)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %q, want it to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

// writeTestVolume writes a volume archive (plain or gzip-compressed) into dir,
// containing the given name→content files, by tarring a fresh source dir.
func writeTestVolume(t *testing.T, dir, name, compression string, files map[string]string) {
	t.Helper()
	root := t.TempDir()
	for n, c := range files {
		if err := os.WriteFile(filepath.Join(root, n), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarDirectory(context.Background(), root, filepath.Join(dir, name), nil, compression); err != nil {
		t.Fatal(err)
	}
}

// assertMergedFile asserts that outDir/name exists with the exact content want.
func assertMergedFile(t *testing.T, outDir, name, want string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(outDir, name))
	if err != nil {
		t.Fatalf("read merged %s: %v", name, err)
	}
	if string(data) != want {
		t.Fatalf("merged %s = %q, want %q", name, string(data), want)
	}
}

// assertMergedVolumeHas extracts outDir/volume_0.tar and asserts every wanted
// file name is present in the merged archive.
func assertMergedVolumeHas(t *testing.T, outDir string, want ...string) {
	t.Helper()
	extract := t.TempDir()
	if err := untarDirectory(context.Background(), filepath.Join(outDir, "volume_0.tar"), extract); err != nil {
		t.Fatalf("untar merged volume: %v", err)
	}
	for _, name := range want {
		if _, err := os.Stat(filepath.Join(extract, name)); err != nil {
			t.Errorf("merged volume missing %s: %v", name, err)
		}
	}
}
