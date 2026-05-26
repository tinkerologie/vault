package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	containertypes "github.com/moby/moby/api/types/container"
	mounttypes "github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// DetectSocketMounts: with a non-empty name and a mock that returns one
// or more bind mounts whose source ends in .sock, the function must
// return a sorted, deduplicated list of in-container destination paths.
// This drives the body of the function past the early-return guard.
func TestDetectSocketMountsReturnsSocketDestinations(t *testing.T) {
	t.Parallel()

	mock := &mockDockerClient{
		inspectResp: client.ContainerInspectResult{
			Container: containertypes.InspectResponse{
				ID:   "deadbeef",
				Name: "/plex",
				Mounts: []containertypes.MountPoint{
					{Type: mounttypes.TypeBind, Source: "/var/run/docker.sock", Destination: "/var/run/docker.sock"},
					{Type: mounttypes.TypeBind, Source: "/usr/share/data", Destination: "/data"},
					{Type: mounttypes.TypeBind, Source: "/var/run/docker-shim.sock", Destination: "/var/run/docker-shim.sock"},
					// Dedup: same destination as the first entry — should be merged.
					{Type: mounttypes.TypeBind, Source: "/var/run/docker.sock", Destination: "/var/run/docker.sock"},
				},
			},
		},
	}
	h := &ContainerHandler{cli: mock}

	paths, err := h.DetectSocketMounts(context.Background(), "plex")
	if err != nil {
		t.Fatalf("DetectSocketMounts error = %v", err)
	}
	if len(paths) != 2 {
		t.Fatalf("expected 2 unique socket paths, got %v", paths)
	}
	// Sorted output: docker-shim.sock < docker.sock alphabetically.
	if paths[0] != "/var/run/docker-shim.sock" || paths[1] != "/var/run/docker.sock" {
		t.Fatalf("paths not sorted/deduped: %v", paths)
	}
}

// DetectSocketMounts: empty-destination fallback. The function falls back
// to the source path when destination is empty so the caller still gets
// a usable exclusion path.
func TestDetectSocketMountsFallsBackToSourceWhenDestEmpty(t *testing.T) {
	t.Parallel()

	mock := &mockDockerClient{
		inspectResp: client.ContainerInspectResult{
			Container: containertypes.InspectResponse{
				ID:   "deadbeef",
				Name: "/x",
				Mounts: []containertypes.MountPoint{
					{Type: mounttypes.TypeBind, Source: "/var/run/foo.sock", Destination: ""},
				},
			},
		},
	}
	h := &ContainerHandler{cli: mock}
	paths, err := h.DetectSocketMounts(context.Background(), "x")
	if err != nil {
		t.Fatalf("DetectSocketMounts error = %v", err)
	}
	if len(paths) != 1 || paths[0] != "/var/run/foo.sock" {
		t.Fatalf("expected fallback to source path, got %v", paths)
	}
}

// DetectSocketMounts: inspect failure must surface as an error. This
// drives the error wrap that lets the caller distinguish "no sockets"
// from "inspect failed".
func TestDetectSocketMountsInspectError(t *testing.T) {
	t.Parallel()

	mock := &mockDockerClient{
		inspectErr: errInspectMissing,
	}
	h := &ContainerHandler{cli: mock}

	_, err := h.DetectSocketMounts(context.Background(), "plex")
	if err == nil {
		t.Fatal("expected inspect error to be surfaced")
	}
}

// errInspectMissing is a sentinel used by the mock to simulate a
// "container not found" condition.
var errInspectMissing = &simpleErr{msg: "mock: container not found"}

type simpleErr struct{ msg string }

func (e *simpleErr) Error() string { return e.msg }

// listMockDockerClient is a narrower mock used by ListItems tests. It only
// implements the ContainerList method; everything else panics so any
// unintended call surfaces in test output.
type listMockDockerClient struct {
	mockDockerClient
	listResp client.ContainerListResult
	listErr  error
}

func (m *listMockDockerClient) ContainerList(_ context.Context, _ client.ContainerListOptions) (client.ContainerListResult, error) {
	return m.listResp, m.listErr
}

// ListItems projects ContainerList results into BackupItems. The
// projection rules are exercised here:
//   - prefer the name (with leading slash stripped) over the ID prefix
//   - fall back to a 12-char ID prefix when names are empty
//   - settings includes id/image/state
func TestContainerListItemsProjectsContainers(t *testing.T) {
	t.Parallel()

	mock := &listMockDockerClient{
		listResp: client.ContainerListResult{
			Items: []containertypes.Summary{
				{
					ID:    "0123456789abcdef0123",
					Names: []string{"/plex"},
					Image: "linuxserver/plex:latest",
					State: containertypes.StateRunning,
				},
				{
					ID:    "fedcba9876543210fedc",
					Names: nil, // exercise the ID-prefix fallback
					Image: "redis:7",
					State: containertypes.StateExited,
				},
			},
		},
	}
	h := &ContainerHandler{cli: mock}

	items, err := h.ListItems()
	if err != nil {
		t.Fatalf("ListItems error = %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].Name != "plex" {
		t.Fatalf("items[0].Name = %q, want %q", items[0].Name, "plex")
	}
	if items[1].Name != "fedcba987654" {
		t.Fatalf("items[1].Name = %q, want 12-char ID prefix", items[1].Name)
	}
	if items[0].Settings["image"] != "linuxserver/plex:latest" {
		t.Fatalf("items[0].Settings[image] = %v, want linuxserver/plex:latest", items[0].Settings["image"])
	}
}

// Backup: inspect succeeds (container is stopped, no mounts), but the
// mock's ImageSave stub returns an error. This drives Backup well past
// the early-validation guards, through config-write, into the image-save
// step, exercising the runWithRestart wrapper and the error-wrap on
// `saving image`.
func TestContainerBackupSurfacesImageSaveError(t *testing.T) {
	t.Parallel()

	mock := &mockDockerClient{
		inspectResp: client.ContainerInspectResult{
			Container: containertypes.InspectResponse{
				ID:    "deadbeef",
				Name:  "/plex",
				Image: "linuxserver/plex:latest",
				Config: &containertypes.Config{
					Image: "linuxserver/plex:latest",
				},
				State: &containertypes.State{Running: false},
			},
		},
	}
	h := &ContainerHandler{cli: mock}

	item := BackupItem{
		Name:     "plex",
		Settings: map[string]any{"id": "deadbeef"},
	}
	destDir := t.TempDir()
	result, err := h.Backup(context.Background(), item, destDir, func(string, int, string) {})
	if err == nil {
		t.Fatal("expected ImageSave failure to be surfaced")
	}
	if result != nil {
		t.Fatalf("expected nil result on ImageSave failure, got %#v", result)
	}
	// config.json must already have been written before ImageSave was attempted.
	if _, statErr := os.Stat(filepath.Join(destDir, "config.json")); statErr != nil {
		t.Fatalf("expected config.json to be written before ImageSave: %v", statErr)
	}
}

// Backup with a running container drives the "stop" branch — the mock's
// ContainerStop returns an error, which Backup surfaces as "stopping
// container". This adds coverage for the stop-and-restart wrapper that
// only fires when wasRunning && !no_stop.
func TestContainerBackupSurfacesStopError(t *testing.T) {
	t.Parallel()

	mock := &mockDockerClient{
		inspectResp: client.ContainerInspectResult{
			Container: containertypes.InspectResponse{
				ID:    "deadbeef",
				Name:  "/plex",
				Image: "linuxserver/plex:latest",
				Config: &containertypes.Config{
					Image: "linuxserver/plex:latest",
				},
				State: &containertypes.State{Running: true},
			},
		},
	}
	h := &ContainerHandler{cli: mock}

	item := BackupItem{
		Name:     "plex",
		Settings: map[string]any{"id": "deadbeef"},
	}
	_, err := h.Backup(context.Background(), item, t.TempDir(), func(string, int, string) {})
	if err == nil {
		t.Fatal("expected ContainerStop error to surface for running container")
	}
}

// Backup with no_stop=true on a running container skips the stop call.
// The mock then fails at ImageSave (the next step), confirming we
// progressed past the stop branch without invoking it.
func TestContainerBackupNoStopSkipsStop(t *testing.T) {
	t.Parallel()

	mock := &mockDockerClient{
		inspectResp: client.ContainerInspectResult{
			Container: containertypes.InspectResponse{
				ID:    "deadbeef",
				Name:  "/plex",
				Image: "linuxserver/plex:latest",
				Config: &containertypes.Config{
					Image: "linuxserver/plex:latest",
				},
				State: &containertypes.State{Running: true},
			},
		},
	}
	h := &ContainerHandler{cli: mock}

	item := BackupItem{
		Name: "plex",
		Settings: map[string]any{
			"id":      "deadbeef",
			"no_stop": true,
		},
	}
	_, err := h.Backup(context.Background(), item, t.TempDir(), func(string, int, string) {})
	if err == nil {
		t.Fatal("expected ImageSave error to surface (stop skipped, save attempted)")
	}
}

// Restore: with a valid image.tar on disk but no daemon (mock ImageLoad
// returns an error), the function must surface "loading image" wrapped
// error. This drives Restore past findArchive, past the file open, past
// detectingReader (plain bytes pass through), and into the ImageLoad call.
func TestContainerRestoreSurfacesImageLoadError(t *testing.T) {
	t.Parallel()

	sourceDir := t.TempDir()
	// Plain bytes — detectingReader will pass them through to the mock
	// ImageLoad, which in turn returns the sentinel error.
	if err := os.WriteFile(filepath.Join(sourceDir, "image.tar"), []byte("not actually a tar"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	mock := &mockDockerClient{}
	h := &ContainerHandler{cli: mock}
	err := h.Restore(context.Background(), BackupItem{Name: "plex"}, sourceDir, func(string, int, string) {})
	if err == nil {
		t.Fatal("expected ImageLoad failure to be surfaced")
	}
}

// Backup: when the container ID is stale and the name-based fallback
// inspect also fails, the function returns the wrapped inspect error.
// This drives the function past the early ID validation into the actual
// inspect retry logic.
func TestContainerBackupSurfacesInspectFailure(t *testing.T) {
	t.Parallel()

	mock := &mockDockerClient{
		inspectErr: errInspectMissing, // both the ID and name attempts fail
	}
	h := &ContainerHandler{cli: mock}

	item := BackupItem{
		Name:     "plex",
		Settings: map[string]any{"id": "deadbeef"},
	}
	result, err := h.Backup(context.Background(), item, t.TempDir(), func(string, int, string) {})
	if err == nil {
		t.Fatal("expected inspect error to be surfaced")
	}
	if result != nil {
		t.Fatalf("expected nil result on inspect failure, got %#v", result)
	}
}

// ListItems must surface daemon-side failures to the caller with an error
// wrap rather than silently returning an empty slice.
func TestContainerListItemsSurfacesDaemonError(t *testing.T) {
	t.Parallel()

	mock := &listMockDockerClient{
		listErr: errInspectMissing, // any non-nil error suffices
	}
	h := &ContainerHandler{cli: mock}

	_, err := h.ListItems()
	if err == nil {
		t.Fatal("expected error from failing ContainerList to be surfaced")
	}
}
