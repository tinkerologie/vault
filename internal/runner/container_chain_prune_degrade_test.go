package runner

import (
	"archive/tar"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ruaan-deysel/vault/internal/engine"
)

// writeManifest writes a volumes.json naming a single volume, so a step can
// be given a manifest that deliberately disagrees with the merged directory.
func writeManifest(t *testing.T, dir, archive, source string, backedUp, isFile bool) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal([]map[string]any{{
		"index": 0, "source": source, "destination": "/data",
		"backed_up": backedUp, "is_file": isFile, "archive": archive,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "volumes.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// The prune is best-effort by design: anything it cannot read with certainty
// leaves that volume — or the whole chain — exactly as the union restore
// produced it. These tests pin each of those degradations, because silently
// pruning on a guess would delete a user's files.

// TestPruneChainSkipsOnUnresolvableTargets covers the cases where the prune
// gives up before it reaches any volume.
func TestPruneChainSkipsOnUnresolvableTargets(t *testing.T) {
	archive := "volume_abcdef0123456789.tar"

	t.Run("merged directory without a config", func(t *testing.T) {
		tmpDir := t.TempDir()
		source := filepath.Join(tmpDir, "appdata")
		step0 := filepath.Join(tmpDir, "step_0")
		step1 := filepath.Join(tmpDir, "step_1")
		writeVolumeStep(t, step0, archive, source, map[string]string{"gone.txt": "x"}, false)
		writeVolumeStep(t, step1, archive, source, nil, true)
		if err := os.MkdirAll(source, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(source, "gone.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}

		// mergedDir exists but holds no config.json, so the targets cannot be
		// resolved at all.
		merged := filepath.Join(tmpDir, "merged")
		if err := os.MkdirAll(merged, 0o750); err != nil {
			t.Fatal(err)
		}

		(&Runner{}).pruneContainerChainResurrected([]string{step0, step1}, merged, "", 2)
		if _, err := os.Stat(filepath.Join(source, "gone.txt")); err != nil {
			t.Errorf("nothing should have been pruned without resolvable targets: %v", err)
		}
	})

	t.Run("chain step with an unreadable manifest", func(t *testing.T) {
		tmpDir := t.TempDir()
		source := filepath.Join(tmpDir, "appdata")
		step0 := filepath.Join(tmpDir, "step_0")
		step1 := filepath.Join(tmpDir, "step_1")
		writeVolumeStep(t, step0, archive, source, map[string]string{"gone.txt": "x"}, false)
		writeVolumeStep(t, step1, archive, source, nil, true)
		if err := os.MkdirAll(source, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(source, "gone.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(step0, "volumes.json"), []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}

		merged := filepath.Join(tmpDir, "merged")
		writeMergedConfig(t, merged, archive, source)

		(&Runner{}).pruneContainerChainResurrected([]string{step0, step1}, merged, "", 2)
		if _, err := os.Stat(filepath.Join(source, "gone.txt")); err != nil {
			t.Errorf("a corrupt step manifest must abort the whole prune: %v", err)
		}
	})
}

// TestPruneContainerVolumeDegradations drives pruneContainerVolume directly,
// because each of these is a per-volume decision the chain-level entry point
// reports only as "nothing happened".
func TestPruneContainerVolumeDegradations(t *testing.T) {
	const source = "/mnt/user/appdata/plex"
	const archive = "volume_abcdef0123456789.tar"

	t.Run("newest step never archived this volume", func(t *testing.T) {
		vol := engine.ContainerVolumeTarget{Source: source, Archive: archive, BackedUp: true, Target: t.TempDir()}
		steps := []string{t.TempDir(), t.TempDir()}
		archives := []map[string]string{{source: archive}, {}}

		if _, ok := (&Runner{}).pruneContainerVolume(vol, steps, archives, steps[1]); ok {
			t.Error("a volume absent from the newest step's manifest must not be pruned")
		}
	})

	t.Run("newest step has no effective listing", func(t *testing.T) {
		tmpDir := t.TempDir()
		newest := filepath.Join(tmpDir, "step_1")
		writeVolumeStep(t, newest, archive, source, map[string]string{"a.txt": "a"}, false)
		vol := engine.ContainerVolumeTarget{Source: source, Archive: archive, BackedUp: true, Target: t.TempDir()}
		steps := []string{t.TempDir(), newest}
		archives := []map[string]string{{source: archive}, {source: archive}}

		if _, ok := (&Runner{}).pruneContainerVolume(vol, steps, archives, newest); ok {
			t.Error("without an authoritative listing there is no keep set, so the prune must not run")
		}
	})

	t.Run("earlier step with an unreadable tar index", func(t *testing.T) {
		tmpDir := t.TempDir()
		older := filepath.Join(tmpDir, "step_0")
		newest := filepath.Join(tmpDir, "step_1")
		writeVolumeStep(t, older, archive, source, map[string]string{"a.txt": "a"}, false)
		writeVolumeStep(t, newest, archive, source, nil, true)
		if err := os.Remove(filepath.Join(older, archive+engine.IndexSuffix)); err != nil {
			t.Fatal(err)
		}

		vol := engine.ContainerVolumeTarget{Source: source, Archive: archive, BackedUp: true, Target: t.TempDir()}
		steps := []string{older, newest}
		archives := []map[string]string{{source: archive}, {source: archive}}

		if _, ok := (&Runner{}).pruneContainerVolume(vol, steps, archives, newest); ok {
			t.Error("an unreadable index means the writes are unknown, so the prune must not run")
		}
	})

	t.Run("no earlier step archived this volume", func(t *testing.T) {
		tmpDir := t.TempDir()
		newest := filepath.Join(tmpDir, "step_1")
		writeVolumeStep(t, newest, archive, source, nil, true)

		vol := engine.ContainerVolumeTarget{Source: source, Archive: archive, BackedUp: true, Target: t.TempDir()}
		steps := []string{t.TempDir(), newest}
		archives := []map[string]string{{}, {source: archive}}

		pruned, ok := (&Runner{}).pruneContainerVolume(vol, steps, archives, newest)
		if !ok || pruned != 0 {
			t.Errorf("an empty written set is a clean no-op prune; got pruned=%d ok=%v", pruned, ok)
		}
	})

	t.Run("restore target that does not exist", func(t *testing.T) {
		tmpDir := t.TempDir()
		older := filepath.Join(tmpDir, "step_0")
		newest := filepath.Join(tmpDir, "step_1")
		writeVolumeStep(t, older, archive, source, map[string]string{"a.txt": "a"}, false)
		writeVolumeStep(t, newest, archive, source, nil, true)

		vol := engine.ContainerVolumeTarget{
			Source: source, Archive: archive, BackedUp: true,
			Target: filepath.Join(tmpDir, "never-restored"),
		}
		steps := []string{older, newest}
		archives := []map[string]string{{source: archive}, {source: archive}}

		if _, ok := (&Runner{}).pruneContainerVolume(vol, steps, archives, newest); ok {
			t.Error("a target that was never written cannot be pruned")
		}
	})
}

// TestPruneChainSkipsVolumesItNeverWrote covers the two per-volume kinds the
// prune declines outright: one that was not backed up (nothing was extracted)
// and a single-file mount (overwritten wholesale, never merged).
func TestPruneChainSkipsVolumesItNeverWrote(t *testing.T) {
	for _, tc := range []struct {
		name     string
		backedUp bool
		isFile   bool
	}{
		{"volume that was not backed up", false, false},
		{"single-file mount", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			source := filepath.Join(tmpDir, "appdata")
			archive := "volume_abcdef0123456789.tar"
			step0 := filepath.Join(tmpDir, "step_0")
			step1 := filepath.Join(tmpDir, "step_1")
			writeVolumeStep(t, step0, archive, source, map[string]string{"gone.txt": "x"}, false)
			writeVolumeStep(t, step1, archive, source, nil, true)
			if err := os.MkdirAll(source, 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(source, "gone.txt"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}

			merged := filepath.Join(tmpDir, "merged")
			writeMergedConfig(t, merged, archive, source)
			// The merged manifest is what decides the volume's kind.
			writeManifest(t, merged, archive, source, tc.backedUp, tc.isFile)

			(&Runner{}).pruneContainerChainResurrected([]string{step0, step1}, merged, "", 2)
			if _, err := os.Stat(filepath.Join(source, "gone.txt")); err != nil {
				t.Errorf("this volume kind must be left alone: %v", err)
			}
		})
	}
}

// TestPruneChainRemovesNestedEmptiedDirectories pins the deepest-first
// ordering: an outer directory can only be removed once the inner one it
// holds has gone, so the order the written set happens to iterate in must not
// decide the outcome.
func TestPruneChainRemovesNestedEmptiedDirectories(t *testing.T) {
	tmpDir := t.TempDir()
	source := filepath.Join(tmpDir, "appdata")
	archive := "volume_abcdef0123456789.tar"
	step0 := filepath.Join(tmpDir, "step_0")
	step1 := filepath.Join(tmpDir, "step_1")

	writeVolumeStep(t, step0, archive, source, map[string]string{
		"keep.txt": "keep", "a/b/c/old.txt": "old",
	}, false)
	writeVolumeStep(t, step1, archive, source, map[string]string{"keep.txt": "keep"}, true)

	for name, content := range map[string]string{"keep.txt": "keep", "a/b/c/old.txt": "old"} {
		p := filepath.Join(source, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	merged := filepath.Join(tmpDir, "merged")
	writeMergedConfig(t, merged, archive, source)

	(&Runner{}).pruneContainerChainResurrected([]string{step0, step1}, merged, "", 2)

	if _, err := os.Stat(filepath.Join(source, "a")); err == nil {
		t.Error("the whole emptied directory chain should have been removed")
	}
	if _, err := os.Stat(filepath.Join(source, "keep.txt")); err != nil {
		t.Errorf("keep.txt should have survived: %v", err)
	}
}

// TestReadLocalSidecar pins the two-spelling lookup: a manifest names the
// archive with its compression suffix, but the merge re-tars under the plain
// base, so the sidecar can be filed under either name.
func TestReadLocalSidecar(t *testing.T) {
	t.Parallel()

	t.Run("falls back to the plain tar base name", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeVolumeStep(t, dir, "volume_abcdef0123456789.tar", "/src", map[string]string{"a.txt": "a"}, false)

		idx, ok := readLocalSidecar(dir, "volume_abcdef0123456789.tar.zst", engine.IndexSuffix)
		if !ok {
			t.Fatal("the sidecar written under the plain base name should still be found")
		}
		if len(idx.Files) == 0 {
			t.Error("the index should carry the archived entries")
		}
	})

	t.Run("a corrupt first candidate does not mask the second", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeVolumeStep(t, dir, "volume_abcdef0123456789.tar", "/src", map[string]string{"a.txt": "a"}, false)
		// The suffixed spelling exists but is unreadable; the plain base is
		// intact and must win.
		corrupt := filepath.Join(dir, "volume_abcdef0123456789.tar.zst"+engine.IndexSuffix)
		if err := os.WriteFile(corrupt, []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}

		if _, ok := readLocalSidecar(dir, "volume_abcdef0123456789.tar.zst", engine.IndexSuffix); !ok {
			t.Error("a corrupt candidate should be skipped, not fail the lookup")
		}
	})

	t.Run("no sidecar at all", func(t *testing.T) {
		t.Parallel()
		if _, ok := readLocalSidecar(t.TempDir(), "volume_abcdef0123456789.tar", engine.IndexSuffix); ok {
			t.Error("a step without sidecars must report a miss")
		}
	})
}

// TestWriteTarIndexSkipsTarInternalRecords pins that pax/global metadata
// records never enter the index: they are not files, and a prune driven off
// them would try to remove paths that were never extracted.
func TestWriteTarIndexSkipsTarInternalRecords(t *testing.T) {
	t.Parallel()
	archivePath := filepath.Join(t.TempDir(), "volume_abcdef0123456789.tar")

	out, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(out)
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeXGlobalHeader, Name: "pax_global_header",
		PAXRecords: map[string]string{"comment": "written by another tar"},
		Format:     tar.FormatPAX,
	}); err != nil {
		t.Fatal(err)
	}
	body := []byte("real")
	if err := tw.WriteHeader(&tar.Header{Name: "real.txt", Size: int64(len(body)), Mode: 0o644}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}

	if err := engine.WriteTarIndex(archivePath); err != nil {
		t.Fatal(err)
	}
	idx, ok := readLocalSidecar(filepath.Dir(archivePath), filepath.Base(archivePath), engine.IndexSuffix)
	if !ok {
		t.Fatal("the index should have been written")
	}
	if len(idx.Files) != 1 || idx.Files[0].Path != "real.txt" {
		t.Errorf("only real entries belong in the index, got %+v", idx.Files)
	}
}
