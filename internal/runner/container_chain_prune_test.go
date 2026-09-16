package runner

import (
	"archive/tar"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ruaan-deysel/vault/internal/engine"
)

// tarTree archives every entry under src into a plain tar at dest using
// source-relative entry names, the same naming the engine's volume archives
// use (directories keep their trailing slash, via tar.FileInfoHeader).
func tarTree(t *testing.T, src, dest string) {
	t.Helper()
	out, err := os.Create(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	tw := tar.NewWriter(out)
	walkErr := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil || rel == "." {
			return err
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			if link, err = os.Readlink(p); err != nil {
				return err
			}
		}
		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if info.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
	if walkErr != nil {
		t.Fatal(walkErr)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
}

// writeVolumeStep materialises one chain step directory the way
// stageContainerChainMerged would have staged it: the volume's tar archive,
// its tar-index sidecar, optionally its effective-listing sidecar, and a
// volumes.json naming the archive.
func writeVolumeStep(t *testing.T, stepDir, archiveName, source string, files map[string]string, withListing bool) {
	t.Helper()
	if err := os.MkdirAll(stepDir, 0o750); err != nil {
		t.Fatal(err)
	}

	tree := t.TempDir()
	for name, content := range files {
		p := filepath.Join(tree, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		// A "->target" value makes the entry a symlink, so a step can carry
		// the entry type the prune has to tell apart.
		if target, isLink := strings.CutPrefix(content, "->"); isLink {
			if err := os.Symlink(target, p); err != nil {
				t.Skipf("filesystem does not support symlinks: %v", err)
			}
			continue
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	archivePath := filepath.Join(stepDir, archiveName)
	tarTree(t, tree, archivePath)
	if err := engine.WriteTarIndex(archivePath); err != nil {
		t.Fatal(err)
	}
	if withListing {
		if err := engine.WriteEffectiveListing(tree, archivePath, nil); err != nil {
			t.Fatal(err)
		}
	}

	manifest := []map[string]any{{
		"index": 0, "source": source, "destination": "/data",
		"backed_up": true, "archive": archiveName,
	}}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stepDir, "volumes.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeMergedConfig writes the merged directory's config.json and volumes.json
// — the inputs ContainerHandler.Restore (and therefore the prune's target
// resolution) reads.
func writeMergedConfig(t *testing.T, mergedDir, archiveName, source string) {
	t.Helper()
	if err := os.MkdirAll(mergedDir, 0o750); err != nil {
		t.Fatal(err)
	}
	config := map[string]any{
		"Name": "/test",
		"Mounts": []map[string]any{
			{"Type": "bind", "Source": source, "Destination": "/data"},
		},
	}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mergedDir, "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := []map[string]any{{
		"index": 0, "source": source, "destination": "/data",
		"backed_up": true, "archive": archiveName,
	}}
	mData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mergedDir, "volumes.json"), mData, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestPruneContainerChainResurrected is the regression test for issue #345.
// The classic container chain merge is a pure union, so a file deleted from a
// volume after the base full reappears when a later differential is restored.
// The prune must remove it while leaving everything the newest point's
// authoritative listing still holds — and must leave files the user put there
// themselves alone.
func TestPruneContainerChainResurrected(t *testing.T) {
	cases := []struct {
		name string
		// fullFiles is the base full's content; diffFiles is the newest
		// step's content AND its authoritative listing (what still existed).
		fullFiles  map[string]string
		diffFiles  map[string]string
		onDisk     map[string]string // what the union restore produced
		wantGone   []string
		wantKept   []string
		newestList bool
	}{
		{
			name:       "file deleted after the base full is not resurrected",
			fullFiles:  map[string]string{"keep.txt": "keep", "delete-me.txt": "gone"},
			diffFiles:  map[string]string{"keep.txt": "keep"},
			onDisk:     map[string]string{"keep.txt": "keep", "delete-me.txt": "gone"},
			wantGone:   []string{"delete-me.txt"},
			wantKept:   []string{"keep.txt"},
			newestList: true,
		},
		{
			name:       "nested deleted file and its emptied directory are removed",
			fullFiles:  map[string]string{"keep.txt": "keep", "sub/old.txt": "old"},
			diffFiles:  map[string]string{"keep.txt": "keep"},
			onDisk:     map[string]string{"keep.txt": "keep", "sub/old.txt": "old"},
			wantGone:   []string{"sub/old.txt"},
			wantKept:   []string{"keep.txt"},
			newestList: true,
		},
		{
			// The ownership guard: a file at a path the base full also held,
			// but with different content, was not written by this restore.
			name:       "a same-named file of a different size is left alone",
			fullFiles:  map[string]string{"keep.txt": "keep", "delete-me.txt": "gone"},
			diffFiles:  map[string]string{"keep.txt": "keep"},
			onDisk:     map[string]string{"keep.txt": "keep", "delete-me.txt": "a much longer user-authored replacement"},
			wantGone:   nil,
			wantKept:   []string{"keep.txt", "delete-me.txt"},
			newestList: true,
		},
		{
			// A symlink's tar header records size 0 while its Lstat size is
			// the length of its target, so the size guard can never match
			// one. Being a symlink at the archived path is the ownership
			// evidence instead.
			name:       "a deleted symlink is pruned despite never matching on size",
			fullFiles:  map[string]string{"keep.txt": "keep", "link": "->keep.txt"},
			diffFiles:  map[string]string{"keep.txt": "keep"},
			onDisk:     map[string]string{"keep.txt": "keep", "link": "->keep.txt"},
			wantGone:   []string{"link"},
			wantKept:   []string{"keep.txt"},
			newestList: true,
		},
		{
			// Degradation: without the newest step's authoritative listing
			// there is no keep set, so the pure-union behaviour is preserved
			// rather than guessed at.
			name:       "no listing in the newest step skips the prune",
			fullFiles:  map[string]string{"keep.txt": "keep", "delete-me.txt": "gone"},
			diffFiles:  map[string]string{"keep.txt": "keep"},
			onDisk:     map[string]string{"keep.txt": "keep", "delete-me.txt": "gone"},
			wantGone:   nil,
			wantKept:   []string{"keep.txt", "delete-me.txt"},
			newestList: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()

			// No destination override, so the volume is restored back over
			// its own mount source — the target the prune must operate on.
			source := filepath.Join(tmpDir, "appdata")
			target := source
			if err := os.MkdirAll(target, 0o750); err != nil {
				t.Fatal(err)
			}
			for name, content := range tc.onDisk {
				p := filepath.Join(target, name)
				if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
					t.Fatal(err)
				}
				if linkTarget, isLink := strings.CutPrefix(content, "->"); isLink {
					if err := os.Symlink(linkTarget, p); err != nil {
						t.Skipf("filesystem does not support symlinks: %v", err)
					}
					continue
				}
				if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			archive := "volume_abcdef0123456789.tar"
			step0 := filepath.Join(tmpDir, "step_0")
			step1 := filepath.Join(tmpDir, "step_1")
			writeVolumeStep(t, step0, archive, source, tc.fullFiles, false)
			writeVolumeStep(t, step1, archive, source, tc.diffFiles, tc.newestList)

			mergedDir := filepath.Join(tmpDir, "merged")
			writeMergedConfig(t, mergedDir, archive, source)

			r := &Runner{}
			r.pruneContainerChainResurrected([]string{step0, step1}, mergedDir, "", 2)

			for _, rel := range tc.wantGone {
				if _, err := os.Stat(filepath.Join(target, rel)); err == nil {
					t.Errorf("%s was resurrected — prune did not remove it", rel)
				}
			}
			for _, rel := range tc.wantKept {
				if _, err := os.Stat(filepath.Join(target, rel)); err != nil {
					t.Errorf("%s should have survived the prune: %v", rel, err)
				}
			}
		})
	}
}

// TestPruneContainerChainResurrectedSingleStep asserts the prune is a no-op
// for a chain with nothing earlier to have resurrected anything.
func TestPruneContainerChainResurrectedSingleStep(t *testing.T) {
	t.Parallel()

	r := &Runner{}
	// No panic, no I/O, no error: fewer than two steps means there is no
	// union to prune.
	r.pruneContainerChainResurrected([]string{t.TempDir()}, t.TempDir(), "", 1)
}
