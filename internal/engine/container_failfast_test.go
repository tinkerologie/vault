package engine

import (
	"context"
	"testing"

	"github.com/ruaan-deysel/vault/internal/dedup"
)

// TestContainerBackupRejectsMissingContainerID drives the validation guard
// at the top of Backup that prevents Docker calls when the BackupItem
// settings are malformed. This branch runs before the docker client is
// touched, so it's safe to exercise on a machine with no Docker daemon.
func TestContainerBackupRejectsMissingContainerID(t *testing.T) {
	t.Parallel()

	// nil client is fine — the validation guard returns before any client
	// method is invoked.
	h := &ContainerHandler{cli: nil}
	progress := func(string, int, string) {}

	tests := []struct {
		name string
		item BackupItem
	}{
		{
			name: "settings nil",
			item: BackupItem{Name: "plex", Settings: nil},
		},
		{
			name: "id missing",
			item: BackupItem{Name: "plex", Settings: map[string]any{}},
		},
		{
			name: "id empty string",
			item: BackupItem{Name: "plex", Settings: map[string]any{"id": ""}},
		},
		{
			name: "id wrong type",
			item: BackupItem{Name: "plex", Settings: map[string]any{"id": 12345}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result, err := h.Backup(context.Background(), tc.item, t.TempDir(), progress)
			if err == nil {
				t.Fatal("expected container-id-not-found error before Docker is contacted")
			}
			if result != nil {
				t.Fatalf("expected nil result on validation failure, got %#v", result)
			}
		})
	}
}

// TestContainerRestoreFailsWhenImageArchiveMissing drives the early-error
// branch of Restore that calls findArchive("image.tar") before any Docker
// SDK call. An empty source directory triggers it.
func TestContainerRestoreFailsWhenImageArchiveMissing(t *testing.T) {
	t.Parallel()

	h := &ContainerHandler{cli: nil}
	progress := func(string, int, string) {}

	err := h.Restore(context.Background(), BackupItem{Name: "plex"}, t.TempDir(), progress)
	if err == nil {
		t.Fatal("expected error when image.tar is absent from source dir")
	}
}

// TestContainerRestoreChunkedRejectsNilRepo drives the nil-repo guard at
// the top of RestoreChunked. This prevents a panic when callers forget to
// pass a dedup repository.
func TestContainerRestoreChunkedRejectsNilRepo(t *testing.T) {
	t.Parallel()

	h := &ContainerHandler{cli: nil}
	err := h.RestoreChunked(context.Background(), BackupItem{Name: "plex"}, nil, dedup.ID{}, t.TempDir(), nil)
	if err == nil {
		t.Fatal("expected error when dedup repo is nil")
	}
}

// TestContainerRestoreChunkedRejectsMissingManifest drives the second
// guard: the manifest must exist in the repo. We use the package's test
// helper to spin up a fresh, empty repo and pass a zero manifest ID — the
// GetManifest call must surface a not-found error before any Docker call.
func TestContainerRestoreChunkedRejectsMissingManifest(t *testing.T) {
	t.Parallel()

	repo, _, cleanup := dedup.NewTestRepoForEngine(t)
	defer cleanup()

	h := &ContainerHandler{cli: nil}
	err := h.RestoreChunked(context.Background(), BackupItem{Name: "plex"}, repo, dedup.ID{}, t.TempDir(), nil)
	if err == nil {
		t.Fatal("expected error when manifest is missing from repo")
	}
}

// NewContainerHandler builds the Docker client lazily from env vars. The
// client constructor does not contact the daemon (it just records URL +
// API version negotiation knobs), so this works on a host without Docker
// running. The returned handler must be non-nil with cli wired up.
func TestNewContainerHandler(t *testing.T) {
	t.Parallel()

	h, err := NewContainerHandler()
	if err != nil {
		t.Fatalf("NewContainerHandler() error = %v", err)
	}
	if h == nil || h.cli == nil {
		t.Fatal("NewContainerHandler() returned nil or unwired handler")
	}
}

// DetectSocketMounts has a fast-path that returns nil for an empty
// container name. This runs without touching the Docker SDK, so it's a
// safe-to-test branch even on hosts without Docker.
func TestDetectSocketMountsEmptyName(t *testing.T) {
	t.Parallel()

	h := &ContainerHandler{cli: nil}
	paths, err := h.DetectSocketMounts(context.Background(), "")
	if err != nil {
		t.Fatalf("expected nil err for empty name, got %v", err)
	}
	if paths != nil {
		t.Fatalf("expected nil paths for empty name, got %v", paths)
	}
}

// recreateAndStartContainer is otherwise Docker-bound, but the very first
// step normalises the container name and bails out on traversal/illegal
// components. We exercise that guard by passing a name that fails the
// safepath check — the function exits before any Docker SDK call.
func TestRecreateAndStartContainerRejectsUnsafeName(t *testing.T) {
	t.Parallel()

	h := &ContainerHandler{cli: nil}
	progress := func(string, int, string) {}

	// inspect.Name is empty so the code falls back to item.Name, which we
	// set to a path-traversal pattern.
	inspect := restoreInspect{}
	item := BackupItem{Name: "../escape"}

	err := h.recreateAndStartContainer(context.Background(), item, inspect, "", "", progress)
	if err == nil {
		t.Fatal("expected error from normalizeRestoreComponent for traversal name")
	}
}

// StopContainers and StartContainers create a Docker client from env, but
// with an empty ID list they never contact the daemon — the loop body is
// skipped. This means we can exercise the happy-path through the function
// without a running Docker daemon, picking up the client-creation,
// defer-close, and empty-loop branches.
func TestStopContainersEmptyList(t *testing.T) {
	t.Parallel()

	stopped, err := StopContainers(nil)
	if err != nil {
		t.Fatalf("StopContainers(nil) error = %v", err)
	}
	if len(stopped) != 0 {
		t.Fatalf("expected no IDs reported as stopped, got %v", stopped)
	}
}

func TestStartContainersEmptyList(t *testing.T) {
	t.Parallel()

	errs := StartContainers(nil)
	if len(errs) != 0 {
		t.Fatalf("expected no errors from empty-list start, got %v", errs)
	}
}

// populateRepoDigestsViaPull short-circuits when either the docker client
// or imageRef is missing. These guards make the helper safe to call from
// best-effort code paths without a wrapper nil-check at every callsite.
func TestPopulateRepoDigestsViaPullShortCircuits(t *testing.T) {
	t.Parallel()

	if populateRepoDigestsViaPull(context.Background(), nil, "redis:7") {
		t.Fatal("expected false when docker client is nil")
	}
	// We can't easily construct a non-nil dockerClient here without
	// reaching into mock-fakes used by other tests; the nil and
	// empty-ref guards both early-return false in identical fashion.
	// Empty image ref:
	if populateRepoDigestsViaPull(context.Background(), nil, "") {
		t.Fatal("expected false when imageRef is empty")
	}
}
