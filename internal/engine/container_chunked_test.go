package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

			_, err := h.BackupChunked(context.Background(), item, r, progress)
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
	_, err := h.BackupChunked(context.Background(), item, r, noopProgress)
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
// ContainerStart were called. The unchanged case additionally verifies
// the manifest records the volume as skipped.
func TestBackupChunked_DifferentialStopBehaviour(t *testing.T) {
	reference := time.Now().Add(-1 * time.Hour)

	cases := []struct {
		name           string
		volMtime       time.Time // mtime applied to the volume's file and dir
		wantStopCall   bool
		wantStartCall  bool
		checkSkipEntry bool // when true, verify manifest has volumeSkippedSize entry
	}{
		{
			name:           "no_changes_skips_stop",
			volMtime:       time.Now().Add(-2 * time.Hour),
			wantStopCall:   false,
			wantStartCall:  false,
			checkSkipEntry: true,
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

			manifestID, err := h.BackupChunked(context.Background(), item, r, noopProgress)
			if err != nil {
				t.Fatalf("BackupChunked() error = %v", err)
			}
			if mock.stopCalled != tc.wantStopCall {
				t.Errorf("stopCalled = %v, want %v", mock.stopCalled, tc.wantStopCall)
			}
			if mock.startCalled != tc.wantStartCall {
				t.Errorf("startCalled = %v, want %v", mock.startCalled, tc.wantStartCall)
			}

			if tc.checkSkipEntry {
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
				if entry.Size != volumeSkippedSize {
					t.Errorf("volume entry Size = %d, want volumeSkippedSize (%d)", entry.Size, volumeSkippedSize)
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

	_, err := h.BackupChunked(context.Background(), item, r, noopProgress)
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
