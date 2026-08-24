package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	containertypes "github.com/moby/moby/api/types/container"
	imagetypes "github.com/moby/moby/api/types/image"
	mounttypes "github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/ruaan-deysel/vault/internal/dedup"
)

// newRunningMock builds a mockDockerClient wired with a single bind mount
// pointing at a temp dir with one small file, and a container in the
// specified running state.
func newRunningMock(t *testing.T, running bool) *mockDockerClient {
	t.Helper()
	volSrc := t.TempDir()
	if err := os.WriteFile(filepath.Join(volSrc, "data.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	return &mockDockerClient{
		inspectResp: client.ContainerInspectResult{
			Container: containertypes.InspectResponse{
				ID:   "abc123",
				Name: "/test",
				Config: &containertypes.Config{
					Image: "nginx:latest",
				},
				State: &containertypes.State{Running: running},
				Mounts: []containertypes.MountPoint{
					{
						Type:        mounttypes.TypeBind,
						Source:      volSrc,
						Destination: "/data",
					},
				},
			},
		},
		imageResp: client.ImageInspectResult{
			InspectResponse: imagetypes.InspectResponse{
				RepoDigests: []string{"nginx@sha256:0000000000000000000000000000000000000000000000000000000000000000"},
			},
		},
	}
}

// noopProgress is a no-op ProgressFunc for tests where progress callbacks
// are not under test but must be non-nil (runWithRestart calls progress
// when shouldRestart is true).
func noopProgress(_ string, _ int, _ string) {}

// TestBackupChunked_DifferentialProducesCompleteManifest is the regression
// test for issue #320 (dedup path). A differential/incremental chunked
// container backup must produce a COMPLETE volume sub-manifest — unchanged
// files must be retained — because the dedup restore path restores the
// selected manifest alone and assumes it is complete. Before the fix,
// FolderHandler.BackupChunked honoured changed_since and dropped old.txt,
// so a differential restore lost the base files.
func TestBackupChunked_DifferentialProducesCompleteManifest(t *testing.T) {
	t.Parallel()

	changedSince := time.Now().Add(-1 * time.Hour).Truncate(time.Second)

	writeOld := func(dir string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "old.txt"), []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(filepath.Join(dir, "old.txt"), changedSince.Add(-time.Hour), changedSince.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
	}

	// Full backup: a single volume containing only old.txt.
	fullVol := t.TempDir()
	writeOld(fullVol)
	fullMock := &mockDockerClient{
		inspectResp: client.ContainerInspectResult{
			Container: containertypes.InspectResponse{
				ID:     "abc123",
				Name:   "/test",
				Config: &containertypes.Config{Image: "nginx:latest"},
				State:  &containertypes.State{Running: false},
				Mounts: []containertypes.MountPoint{
					{Type: mounttypes.TypeBind, Source: fullVol, Destination: "/data"},
				},
			},
		},
		imageResp: client.ImageInspectResult{
			InspectResponse: imagetypes.InspectResponse{
				RepoDigests: []string{"nginx@sha256:0000000000000000000000000000000000000000000000000000000000000000"},
			},
		},
	}

	r, _, cleanup := dedup.NewTestRepoForEngine(t)
	defer cleanup()

	fh := &ContainerHandler{cli: fullMock}
	fullID, err := fh.BackupChunked(context.Background(), BackupItem{
		Name:     "test",
		Type:     "container",
		Settings: map[string]any{"id": "abc123"},
	}, r, nil, noopProgress)
	if err != nil {
		t.Fatalf("full BackupChunked() error = %v", err)
	}
	if err := r.Flush(); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}
	parent, err := r.GetManifest(fullID)
	if err != nil {
		t.Fatalf("GetManifest(parent) error = %v", err)
	}

	// Differential backup: the same volume now holds old.txt (unchanged) plus
	// a freshly-written new.txt.
	diffVol := t.TempDir()
	writeOld(diffVol)
	if err := os.WriteFile(filepath.Join(diffVol, "new.txt"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	diffMock := &mockDockerClient{
		inspectResp: client.ContainerInspectResult{
			Container: containertypes.InspectResponse{
				ID:     "abc123",
				Name:   "/test",
				Config: &containertypes.Config{Image: "nginx:latest"},
				State:  &containertypes.State{Running: false},
				Mounts: []containertypes.MountPoint{
					{Type: mounttypes.TypeBind, Source: diffVol, Destination: "/data"},
				},
			},
		},
		imageResp: client.ImageInspectResult{
			InspectResponse: imagetypes.InspectResponse{
				RepoDigests: []string{"nginx@sha256:0000000000000000000000000000000000000000000000000000000000000000"},
			},
		},
	}

	dh := &ContainerHandler{cli: diffMock}
	manifestID, err := dh.BackupChunked(context.Background(), BackupItem{
		Name: "test",
		Type: "container",
		Settings: map[string]any{
			"id":            "abc123",
			"changed_since": changedSince.UTC().Format(time.RFC3339),
		},
	}, r, &parent, noopProgress)
	if err != nil {
		t.Fatalf("differential BackupChunked() error = %v", err)
	}
	if err := r.Flush(); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}

	m, err := r.GetManifest(manifestID)
	if err != nil {
		t.Fatalf("GetManifest() error = %v", err)
	}

	volEntry, ok := m.Files[containerVolPrefix+"/data"]
	if !ok {
		t.Fatalf("manifest missing %s/data entry", containerVolPrefix)
	}
	if volEntry.Size == volumeSkippedSize {
		t.Fatalf("volume was skipped (Size == volumeSkippedSize); expected a complete sub-manifest")
	}
	if len(volEntry.Chunks) != 1 {
		t.Fatalf("volume sub-manifest chunks = %d, want 1", len(volEntry.Chunks))
	}

	sub, err := r.GetManifest(volEntry.Chunks[0])
	if err != nil {
		t.Fatalf("GetManifest(sub) error = %v", err)
	}
	if _, ok := sub.Files["old.txt"]; !ok {
		t.Errorf("sub-manifest missing old.txt — unchanged files must be retained for single-point restore")
	}
	if _, ok := sub.Files["new.txt"]; !ok {
		t.Errorf("sub-manifest missing new.txt")
	}
}

// TestBackupChunked_NewVolumeChunkedFully is the regression test for the
// "new items missing" data-loss class in issue #320. A differential backup of
// a container with a newly-added volume (no matching entry in the parent
// manifest) must chunk that volume FULLY — even when the volume's files and
// directory predate changed_since — rather than recording an empty
// sub-manifest. Before the fix, changed_since was applied with a nil parent
// and every old-mtime file was silently dropped.
func TestBackupChunked_NewVolumeChunkedFully(t *testing.T) {
	t.Parallel()

	changedSince := time.Now().Add(-1 * time.Hour).Truncate(time.Second)
	writeOldFile := func(dir string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "old.txt"), []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(filepath.Join(dir, "old.txt"), changedSince.Add(-time.Hour), changedSince.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
	}

	// Full backup: a container with a single volume "/data" holding old.txt.
	fullVol := t.TempDir()
	writeOldFile(fullVol)
	fullMock := &mockDockerClient{
		inspectResp: client.ContainerInspectResult{
			Container: containertypes.InspectResponse{
				ID:     "abc123",
				Name:   "/test",
				Config: &containertypes.Config{Image: "nginx:latest"},
				State:  &containertypes.State{Running: false},
				Mounts: []containertypes.MountPoint{
					{Type: mounttypes.TypeBind, Source: fullVol, Destination: "/data"},
				},
			},
		},
		imageResp: client.ImageInspectResult{
			InspectResponse: imagetypes.InspectResponse{
				RepoDigests: []string{"nginx@sha256:0000000000000000000000000000000000000000000000000000000000000000"},
			},
		},
	}

	r, _, cleanup := dedup.NewTestRepoForEngine(t)
	defer cleanup()

	fullID, err := (&ContainerHandler{cli: fullMock}).BackupChunked(context.Background(), BackupItem{
		Name:     "test",
		Type:     "container",
		Settings: map[string]any{"id": "abc123"},
	}, r, nil, noopProgress)
	if err != nil {
		t.Fatalf("full BackupChunked() error = %v", err)
	}
	if err := r.Flush(); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}
	parent, err := r.GetManifest(fullID)
	if err != nil {
		t.Fatalf("GetManifest(parent) error = %v", err)
	}

	// Differential backup: the container now exposes a DIFFERENT, newly-added
	// volume "/newdata". Its only file (and the volume dir) predate
	// changed_since, so it looks "unchanged" — but there is no parent entry to
	// carry forward from, so it must be chunked fully.
	newVol := t.TempDir()
	file := filepath.Join(newVol, "fresh_old.txt")
	if err := os.WriteFile(file, []byte("kept"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{file, newVol} {
		if err := os.Chtimes(p, changedSince.Add(-time.Hour), changedSince.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	diffMock := &mockDockerClient{
		inspectResp: client.ContainerInspectResult{
			Container: containertypes.InspectResponse{
				ID:     "abc123",
				Name:   "/test",
				Config: &containertypes.Config{Image: "nginx:latest"},
				State:  &containertypes.State{Running: false},
				Mounts: []containertypes.MountPoint{
					{Type: mounttypes.TypeBind, Source: newVol, Destination: "/newdata"},
				},
			},
		},
		imageResp: client.ImageInspectResult{
			InspectResponse: imagetypes.InspectResponse{
				RepoDigests: []string{"nginx@sha256:0000000000000000000000000000000000000000000000000000000000000000"},
			},
		},
	}

	manifestID, err := (&ContainerHandler{cli: diffMock}).BackupChunked(context.Background(), BackupItem{
		Name: "test",
		Type: "container",
		Settings: map[string]any{
			"id":            "abc123",
			"changed_since": changedSince.UTC().Format(time.RFC3339),
		},
	}, r, &parent, noopProgress)
	if err != nil {
		t.Fatalf("differential BackupChunked() error = %v", err)
	}
	if err := r.Flush(); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}

	m, err := r.GetManifest(manifestID)
	if err != nil {
		t.Fatalf("GetManifest() error = %v", err)
	}

	volEntry, ok := m.Files[containerVolPrefix+"/newdata"]
	if !ok {
		t.Fatalf("manifest missing %s/newdata entry", containerVolPrefix)
	}
	if volEntry.Size == volumeSkippedSize {
		t.Fatalf("new volume was skipped (Size == volumeSkippedSize); expected a complete sub-manifest")
	}
	if len(volEntry.Chunks) != 1 {
		t.Fatalf("volume sub-manifest chunks = %d, want 1", len(volEntry.Chunks))
	}

	sub, err := r.GetManifest(volEntry.Chunks[0])
	if err != nil {
		t.Fatalf("GetManifest(sub) error = %v", err)
	}
	if _, ok := sub.Files["fresh_old.txt"]; !ok {
		t.Errorf("new volume sub-manifest missing fresh_old.txt — a new volume with old-mtime files must be chunked fully")
	}
}

// chunkedMockWithVolume builds a stopped mockDockerClient exposing a single
// /data bind mount backed by volSrc, with a valid image inspect response.
func chunkedMockWithVolume(t *testing.T, volSrc string) *mockDockerClient {
	t.Helper()
	return &mockDockerClient{
		inspectResp: client.ContainerInspectResult{
			Container: containertypes.InspectResponse{
				ID:     "abc123",
				Name:   "/test",
				Config: &containertypes.Config{Image: "nginx:latest"},
				State:  &containertypes.State{Running: false},
				Mounts: []containertypes.MountPoint{
					{Type: mounttypes.TypeBind, Source: volSrc, Destination: "/data"},
				},
			},
		},
		imageResp: client.ImageInspectResult{
			InspectResponse: imagetypes.InspectResponse{
				RepoDigests: []string{"nginx@sha256:0000000000000000000000000000000000000000000000000000000000000000"},
			},
		},
	}
}

// TestBackupChunked_UnchangedVolumeCarryForward is the regression test for two
// silent data-loss gaps in the dedup container flow (issue #320):
//   - a NEW file with a stale mtime (cp -a / rsync -a) in an otherwise
//     mtime-unchanged volume must still be chunked, not dropped by the
//     volume-level mtime gate;
//   - a pre-PR parent entry that recorded an unchanged volume as the -1 skip
//     sentinel must be re-chunked fully, not carried forward and skipped on
//     restore.
func TestBackupChunked_UnchangedVolumeCarryForward(t *testing.T) {
	t.Parallel()

	reference := time.Now().Add(-1 * time.Hour).Truncate(time.Second)
	stale := reference.Add(-time.Hour)

	writeFile := func(t *testing.T, dir, name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// pinOld stamps the files AND the volume root dir with a stale mtime so
	// pathChangedSince reports the volume unchanged — the root dir's mtime is
	// itself a change signal (pathChangedSinceWithPrev checks it), and writing
	// the files bumps it.
	pinOld := func(t *testing.T, dir string, names []string) {
		t.Helper()
		for _, name := range names {
			if err := os.Chtimes(filepath.Join(dir, name), stale, stale); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Chtimes(dir, stale, stale); err != nil {
			t.Fatal(err)
		}
	}

	cases := []struct {
		name      string
		parent    string   // "submanifest" (a real full-backup parent) or "sentinel" (a pre-PR -1 entry)
		diffFiles []string // files written into the differential volume, all with stale mtimes
		wantFiles []string // files that must appear in the resulting sub-manifest
	}{
		{
			name:      "stale-mtime new file in an unchanged volume is chunked",
			parent:    "submanifest",
			diffFiles: []string{"old.txt", "new_stale.txt"},
			wantFiles: []string{"old.txt", "new_stale.txt"},
		},
		{
			name:      "pre-PR sentinel parent entry is re-chunked fully",
			parent:    "sentinel",
			diffFiles: []string{"old.txt"},
			wantFiles: []string{"old.txt"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, _, cleanup := dedup.NewTestRepoForEngine(t)
			defer cleanup()

			var parent dedup.Manifest
			switch tc.parent {
			case "submanifest":
				fullVol := t.TempDir()
				writeFile(t, fullVol, "old.txt")
				pinOld(t, fullVol, []string{"old.txt"})
				fullID, err := (&ContainerHandler{cli: chunkedMockWithVolume(t, fullVol)}).BackupChunked(context.Background(), BackupItem{
					Name: "test", Type: "container", Settings: map[string]any{"id": "abc123"},
				}, r, nil, noopProgress)
				if err != nil {
					t.Fatalf("full BackupChunked() error = %v", err)
				}
				if err := r.Flush(); err != nil {
					t.Fatalf("Flush() error = %v", err)
				}
				parent, err = r.GetManifest(fullID)
				if err != nil {
					t.Fatalf("GetManifest(parent) error = %v", err)
				}
			case "sentinel":
				parent = dedup.Manifest{
					Item:  "test",
					Files: map[string]dedup.ManifestEntry{containerVolPrefix + "/data": {Size: volumeSkippedSize}},
				}
			}

			diffVol := t.TempDir()
			for _, name := range tc.diffFiles {
				writeFile(t, diffVol, name)
			}
			pinOld(t, diffVol, tc.diffFiles)

			manifestID, err := (&ContainerHandler{cli: chunkedMockWithVolume(t, diffVol)}).BackupChunked(context.Background(), BackupItem{
				Name: "test", Type: "container",
				Settings: map[string]any{
					"id":            "abc123",
					"changed_since": reference.UTC().Format(time.RFC3339),
				},
			}, r, &parent, noopProgress)
			if err != nil {
				t.Fatalf("differential BackupChunked() error = %v", err)
			}
			if err := r.Flush(); err != nil {
				t.Fatalf("Flush() error = %v", err)
			}

			m, err := r.GetManifest(manifestID)
			if err != nil {
				t.Fatalf("GetManifest() error = %v", err)
			}
			entry, ok := m.Files[containerVolPrefix+"/data"]
			if !ok {
				t.Fatalf("manifest missing %s/data entry", containerVolPrefix)
			}
			if entry.Size == volumeSkippedSize {
				t.Fatalf("volume was skipped (Size == volumeSkippedSize); expected a complete sub-manifest")
			}
			if len(entry.Chunks) != 1 {
				t.Fatalf("volume sub-manifest chunks = %d, want 1", len(entry.Chunks))
			}
			sub, err := r.GetManifest(entry.Chunks[0])
			if err != nil {
				t.Fatalf("GetManifest(sub) error = %v", err)
			}
			for _, want := range tc.wantFiles {
				if _, ok := sub.Files[want]; !ok {
					t.Errorf("sub-manifest missing %s", want)
				}
			}
		})
	}
}

// TestBackupChunked_StopRestartBehaviour consolidates the stop/restart
// lifecycle tests for BackupChunked into a single table-driven test.
// Each case varies the container running state, no_stop setting, and
// optional mock error, then asserts whether ContainerStop and
// ContainerStart were called.
func TestBackupChunked_StopRestartBehaviour(t *testing.T) {
	cases := []struct {
		name          string
		running       bool
		noStop        bool
		stopErr       error
		wantStopCall  bool
		wantStartCall bool
		wantErr       bool
	}{
		{
			name:          "running container is stopped and restarted",
			running:       true,
			noStop:        false,
			wantStopCall:  true,
			wantStartCall: true,
		},
		{
			name:          "no_stop skips stop and start",
			running:       true,
			noStop:        true,
			wantStopCall:  false,
			wantStartCall: false,
		},
		{
			name:          "already stopped container is left alone",
			running:       false,
			noStop:        false,
			wantStopCall:  false,
			wantStartCall: false,
		},
		{
			name:          "ContainerStop error propagates and does not restart",
			running:       true,
			noStop:        false,
			stopErr:       errors.New("mock stop failure"),
			wantStopCall:  true,
			wantStartCall: false,
			wantErr:       true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := newRunningMock(t, tc.running)
			mock.stopErr = tc.stopErr

			r, _, cleanup := dedup.NewTestRepoForEngine(t)
			defer cleanup()

			h := &ContainerHandler{cli: mock}
			settings := map[string]any{"id": "abc123"}
			if tc.noStop {
				settings["no_stop"] = true
			}
			item := BackupItem{Name: "test", Type: "container", Settings: settings}

			// Use non-nil progress when shouldRestart may be true
			// (runWithRestart calls progress on restart).
			var progress ProgressFunc = noopProgress
			if !tc.running || tc.noStop {
				progress = nil
			}

			_, err := h.BackupChunked(context.Background(), item, r, nil, progress)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
			} else if err != nil {
				t.Fatalf("BackupChunked() unexpected error = %v", err)
			}

			if mock.stopCalled != tc.wantStopCall {
				t.Errorf("stopCalled = %v, want %v", mock.stopCalled, tc.wantStopCall)
			}
			if mock.startCalled != tc.wantStartCall {
				t.Errorf("startCalled = %v, want %v", mock.startCalled, tc.wantStartCall)
			}
		})
	}
}

// TestBackupChunked_RestartsOnBackupError verifies that ContainerStart
// is called even when the volume backup loop returns an error, which is
// the core safety guarantee provided by runWithRestart.
//
// Note: This test relies on FolderHandler.BackupChunked failing when the
// mount source directory does not exist. That coupling is intentional but
// fragile — if FolderHandler.BackupChunked ever gains a graceful skip for
// missing directories, this test would stop exercising the error path and
// silently pass without validating the restart guarantee. Consider switching
// to a mock FolderHandler if the coupling breaks.
func TestBackupChunked_RestartsOnBackupError(t *testing.T) {
	volSrc := t.TempDir()
	// Point the mount at a nonexistent directory to force a backup failure.
	nonexistent := filepath.Join(volSrc, "nosuchdir")

	mock := &mockDockerClient{
		inspectResp: client.ContainerInspectResult{
			Container: containertypes.InspectResponse{
				ID:   "fail-id",
				Name: "/fail-test",
				Config: &containertypes.Config{
					Image: "nginx:latest",
				},
				State: &containertypes.State{Running: true},
				Mounts: []containertypes.MountPoint{
					{
						Type:        mounttypes.TypeBind,
						Source:      nonexistent,
						Destination: "/data",
					},
				},
			},
		},
		imageResp: client.ImageInspectResult{
			InspectResponse: imagetypes.InspectResponse{
				RepoDigests: []string{"nginx@sha256:0000000000000000000000000000000000000000000000000000000000000000"},
			},
		},
	}

	r, _, cleanup := dedup.NewTestRepoForEngine(t)
	defer cleanup()

	h := &ContainerHandler{cli: mock}
	item := BackupItem{
		Name:     "fail-test",
		Type:     "container",
		Settings: map[string]any{"id": "fail-id"},
	}

	// BackupChunked should fail because the mount source doesn't exist,
	// but the container must still be restarted.
	_, err := h.BackupChunked(context.Background(), item, r, nil, noopProgress)
	if err == nil {
		t.Fatal("expected BackupChunked to fail for nonexistent mount source")
	}

	if !mock.stopCalled {
		t.Error("expected ContainerStop to be called before backup attempt")
	}
	if !mock.startCalled {
		t.Error("expected ContainerStart to be called even though backup failed (runWithRestart guarantee)")
	}
}

// newChunkedMockForChangedSince builds a mockDockerClient with a single
// bind mount whose file tree (file and containing dir) is pinned to
// volMtime via os.Chtimes, letting changed_since tests control whether
// pathChangedSince sees the volume as changed.
func newChunkedMockForChangedSince(t *testing.T, volMtime time.Time) *mockDockerClient {
	t.Helper()
	volSrc := t.TempDir()
	file := filepath.Join(volSrc, "data.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{file, volSrc} {
		if err := os.Chtimes(p, volMtime, volMtime); err != nil {
			t.Fatal(err)
		}
	}
	return &mockDockerClient{
		inspectResp: client.ContainerInspectResult{
			Container: containertypes.InspectResponse{
				ID:     "abc123",
				Name:   "/test",
				Config: &containertypes.Config{Image: "nginx:latest"},
				State:  &containertypes.State{Running: true},
				Mounts: []containertypes.MountPoint{
					{Type: mounttypes.TypeBind, Source: volSrc, Destination: "/data"},
				},
			},
		},
		imageResp: client.ImageInspectResult{
			InspectResponse: imagetypes.InspectResponse{
				RepoDigests: []string{"nginx@sha256:0000000000000000000000000000000000000000000000000000000000000000"},
			},
		},
	}
}

// TestBackupChunked_DifferentialStopBehaviour consolidates the differential
// pre-check tests for BackupChunked into a single table-driven test.
// Each case varies whether the volume's files are older or newer than the
// changed_since reference, then asserts whether ContainerStop and
// ContainerStart were called. The unchanged case additionally verifies the
// manifest records a complete sub-manifest for the unchanged volume (rather
// than a volumeSkippedSize sentinel).
func TestBackupChunked_DifferentialStopBehaviour(t *testing.T) {
	reference := time.Now().Add(-1 * time.Hour)

	cases := []struct {
		name                     string
		volMtime                 time.Time // mtime applied to the volume's file and dir
		wantStopCall             bool
		wantStartCall            bool
		checkCompleteSubManifest bool // when true, verify the volume was NOT skipped (a complete sub-manifest was recorded)
	}{
		{
			name:                     "no_changes_skips_stop",
			volMtime:                 time.Now().Add(-2 * time.Hour),
			wantStopCall:             false,
			wantStartCall:            false,
			checkCompleteSubManifest: true,
		},
		{
			name:          "volume_changed_stops_and_restarts",
			volMtime:      time.Now(), // fresh — after the reference
			wantStopCall:  true,
			wantStartCall: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := newChunkedMockForChangedSince(t, tc.volMtime)

			r, _, cleanup := dedup.NewTestRepoForEngine(t)
			defer cleanup()

			h := &ContainerHandler{cli: mock}
			item := BackupItem{
				Name: "test",
				Type: "container",
				Settings: map[string]any{
					"id":            "abc123",
					"changed_since": reference.UTC().Format(time.RFC3339),
				},
			}

			manifestID, err := h.BackupChunked(context.Background(), item, r, nil, noopProgress)
			if err != nil {
				t.Fatalf("BackupChunked() error = %v", err)
			}
			if mock.stopCalled != tc.wantStopCall {
				t.Errorf("stopCalled = %v, want %v", mock.stopCalled, tc.wantStopCall)
			}
			if mock.startCalled != tc.wantStartCall {
				t.Errorf("startCalled = %v, want %v", mock.startCalled, tc.wantStartCall)
			}

			if tc.checkCompleteSubManifest {
				if err := r.Flush(); err != nil {
					t.Fatalf("Flush() error = %v", err)
				}
				m, err := r.GetManifest(manifestID)
				if err != nil {
					t.Fatalf("GetManifest() error = %v", err)
				}
				entry, ok := m.Files[containerVolPrefix+"/data"]
				if !ok {
					t.Fatalf("manifest missing %s/data entry", containerVolPrefix)
				}
				if entry.Size == volumeSkippedSize {
					t.Errorf("volume entry was skipped (Size == volumeSkippedSize); expected a complete sub-manifest")
				}
				if len(entry.Chunks) != 1 {
					t.Errorf("volume sub-manifest chunks = %d, want 1", len(entry.Chunks))
				}
			}
		})
	}
}

// TestBackupChunked_AllMountsExcluded guards the dedup path against the same
// silent data-loss trap the classic path already refuses: every backup-eligible
// mount excluded, producing a "successful" restore point that holds no volume
// data and cannot reconstruct the container. Before the guard, BackupChunked
// recorded each excluded mount as skipped and committed the manifest anyway.
//
// The container must NOT be stopped for a run that cannot produce anything.
func TestBackupChunked_AllMountsExcluded(t *testing.T) {
	mock := newRunningMock(t, true)

	r, _, cleanup := dedup.NewTestRepoForEngine(t)
	defer cleanup()

	h := &ContainerHandler{cli: mock}
	item := BackupItem{
		Name: "all-excluded",
		Type: "container",
		Settings: map[string]any{
			"id": "abc123",
			// newRunningMock's only eligible mount is /data.
			"exclude_paths": []any{"/data"},
		},
	}

	_, err := h.BackupChunked(context.Background(), item, r, nil, noopProgress)
	if err == nil {
		t.Fatal("BackupChunked succeeded with every eligible mount excluded — " +
			"that commits a restore point holding no volume data")
	}
	if !strings.Contains(err.Error(), "excluded") {
		t.Errorf("error should name exclusion as the cause, got: %v", err)
	}
	if mock.stopCalled {
		t.Error("container was stopped for a run that cannot produce a backup — " +
			"the guard must run before the stop")
	}
}

// TestBackupChunkedStopDecisionPrevAware is the regression test for the dedup
// stop-decision half of issue #320. A running container whose only change
// since the parent restore point is a NEW file with a stale mtime must still
// be stopped for a consistent backup. The chunked stop pre-check derives the
// per-volume previous listing from the parent manifest's sub-manifest
// (chunkedPrevBySource), so an otherwise-unchanged volume that gained a
// stale-mtime file flips to changed — mirroring the classic Backup path.
func TestBackupChunkedStopDecisionPrevAware(t *testing.T) {
	changedSince := time.Now().Add(-1 * time.Hour).Truncate(time.Second)
	old := changedSince.Add(-time.Hour)

	writePinned := func(t *testing.T, path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name       string
		mutateDiff func(t *testing.T, diffVol string)
		mutatePar  func(t *testing.T, parent *dedup.Manifest)
		wantStop   bool
	}{
		{
			name: "stale-mtime new file stops a running container",
			mutateDiff: func(t *testing.T, diffVol string) {
				writePinned(t, filepath.Join(diffVol, "new.txt"), "new")
			},
			wantStop: true,
		},
		{
			name:       "unchanged volume does not stop a running container",
			mutateDiff: func(t *testing.T, diffVol string) {},
			wantStop:   false,
		},
		{
			// Regression (issue #320 review follow-up): a parent manifest
			// without an entry for the current volume must still stop the
			// container. chunkedPrevBySource maps such a volume to an EMPTY
			// set, so old.txt reports as absent-from-parent (changed) and
			// needsStop stays set — the archiving loop will take the
			// full-chunk fallback, which must not run against a live
			// container.
			name:       "parent manifest missing the volume entry stops a running container",
			mutateDiff: func(t *testing.T, diffVol string) {},
			mutatePar: func(t *testing.T, parent *dedup.Manifest) {
				delete(parent.Files, containerVolPrefix+"/data")
			},
			wantStop: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, _, cleanup := dedup.NewTestRepoForEngine(t)
			defer cleanup()

			// Parent (full) backup: not running, single /data volume holding
			// only old.txt.
			fullVol := t.TempDir()
			writePinned(t, filepath.Join(fullVol, "old.txt"), "old")
			fullMock := &mockDockerClient{
				inspectResp: client.ContainerInspectResult{
					Container: containertypes.InspectResponse{
						ID:     "abc123",
						Name:   "/test",
						Config: &containertypes.Config{Image: "nginx:latest"},
						State:  &containertypes.State{Running: false},
						Mounts: []containertypes.MountPoint{
							{Type: mounttypes.TypeBind, Source: fullVol, Destination: "/data"},
						},
					},
				},
				imageResp: client.ImageInspectResult{
					InspectResponse: imagetypes.InspectResponse{
						RepoDigests: []string{"nginx@sha256:0000000000000000000000000000000000000000000000000000000000000000"},
					},
				},
			}
			fullID, err := (&ContainerHandler{cli: fullMock}).BackupChunked(context.Background(), BackupItem{
				Name:     "test",
				Type:     "container",
				Settings: map[string]any{"id": "abc123"},
			}, r, nil, noopProgress)
			if err != nil {
				t.Fatalf("full BackupChunked() error = %v", err)
			}
			if err := r.Flush(); err != nil {
				t.Fatalf("Flush() error = %v", err)
			}
			parent, err := r.GetManifest(fullID)
			if err != nil {
				t.Fatalf("GetManifest(parent) error = %v", err)
			}
			if tt.mutatePar != nil {
				tt.mutatePar(t, &parent)
			}

			// Differential backup: running container, same volume plus the
			// scenario's mutation. The dir mtime is pinned back to `old` so the
			// prev-aware check (not a bumped dir mtime) is the only change
			// signal.
			diffVol := t.TempDir()
			writePinned(t, filepath.Join(diffVol, "old.txt"), "old")
			tt.mutateDiff(t, diffVol)
			if err := os.Chtimes(diffVol, old, old); err != nil {
				t.Fatal(err)
			}
			diffMock := &mockDockerClient{
				inspectResp: client.ContainerInspectResult{
					Container: containertypes.InspectResponse{
						ID:     "abc123",
						Name:   "/test",
						Config: &containertypes.Config{Image: "nginx:latest"},
						State:  &containertypes.State{Running: true},
						Mounts: []containertypes.MountPoint{
							{Type: mounttypes.TypeBind, Source: diffVol, Destination: "/data"},
						},
					},
				},
				imageResp: client.ImageInspectResult{
					InspectResponse: imagetypes.InspectResponse{
						RepoDigests: []string{"nginx@sha256:0000000000000000000000000000000000000000000000000000000000000000"},
					},
				},
			}
			if _, err := (&ContainerHandler{cli: diffMock}).BackupChunked(context.Background(), BackupItem{
				Name: "test",
				Type: "container",
				Settings: map[string]any{
					"id":            "abc123",
					"changed_since": changedSince.UTC().Format(time.RFC3339),
				},
			}, r, &parent, noopProgress); err != nil {
				t.Fatalf("differential BackupChunked() error = %v", err)
			}

			if diffMock.stopCalled != tt.wantStop {
				t.Errorf("stopCalled = %v, want %v", diffMock.stopCalled, tt.wantStop)
			}
		})
	}
}

// TestChunkedPrevBySource verifies the per-volume previous-listing derivation
// used by the chunked stop decision: it resolves each mount's parent
// sub-manifest (via the containerVolPrefix+destination key) into a
// volume-relative path set keyed by mount source. A volume with no usable
// parent entry (absent key, skip sentinel, or unreadable sub-manifest) gets an
// EMPTY set rather than being omitted — every pre-existing file then reports
// as changed, which keeps needsStop set for the archiving loop's full-chunk
// fallback (issue #320 review follow-up). Only a nil parent/repo returns nil.
func TestChunkedPrevBySource(t *testing.T) {
	r, _, cleanup := dedup.NewTestRepoForEngine(t)
	defer cleanup()

	subID, err := r.PutManifest("sub", dedup.Manifest{
		Files: map[string]dedup.ManifestEntry{
			"old.txt":        {},
			"sub/nested.txt": {},
		},
	})
	if err != nil {
		t.Fatalf("PutManifest(sub) error = %v", err)
	}
	if err := r.Flush(); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}
	// An ID that was never written: GetManifest must fail on it.
	var missingID dedup.ID
	for i := range missingID {
		missingID[i] = 0xFF
	}

	dataMount := containertypes.MountPoint{Type: mounttypes.TypeBind, Source: "/s", Destination: "/data"}

	tests := []struct {
		name   string
		parent *dedup.Manifest
		mounts []containertypes.MountPoint
		want   map[string]map[string]struct{}
	}{
		{
			name:   "nil parent returns nil",
			parent: nil,
			mounts: []containertypes.MountPoint{dataMount},
			want:   nil,
		},
		{
			name: "sub-manifest resolves to per-volume paths keyed by source",
			parent: &dedup.Manifest{Files: map[string]dedup.ManifestEntry{
				containerVolPrefix + "/data": {Chunks: []dedup.ID{subID}},
			}},
			mounts: []containertypes.MountPoint{dataMount},
			want:   map[string]map[string]struct{}{"/s": {"old.txt": {}, "sub/nested.txt": {}}},
		},
		{
			name:   "mount destination absent from parent yields an empty set",
			parent: &dedup.Manifest{Files: map[string]dedup.ManifestEntry{}},
			mounts: []containertypes.MountPoint{dataMount},
			want:   map[string]map[string]struct{}{"/s": {}},
		},
		{
			name: "skipped volume (empty chunks sentinel) yields an empty set",
			parent: &dedup.Manifest{Files: map[string]dedup.ManifestEntry{
				containerVolPrefix + "/data": {Size: volumeSkippedSize},
			}},
			mounts: []containertypes.MountPoint{dataMount},
			want:   map[string]map[string]struct{}{"/s": {}},
		},
		{
			name: "unreadable sub-manifest yields an empty set",
			parent: &dedup.Manifest{Files: map[string]dedup.ManifestEntry{
				containerVolPrefix + "/data": {Chunks: []dedup.ID{missingID}},
			}},
			mounts: []containertypes.MountPoint{dataMount},
			want:   map[string]map[string]struct{}{"/s": {}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := chunkedPrevBySource(tt.parent, tt.mounts, r)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("chunkedPrevBySource() = %#v, want %#v", got, tt.want)
			}
		})
	}
}
