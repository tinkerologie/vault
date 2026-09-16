package engine

import (
	"context"
	"strings"
	"testing"

	containertypes "github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/ruaan-deysel/vault/internal/dedup"
)

// zeroMountMock is a container with no bind mounts and no named volumes —
// the shape issue #305 reports as failing to back up.
func zeroMountMock() *mockDockerClient {
	return &mockDockerClient{
		inspectResp: client.ContainerInspectResult{
			Container: containertypes.InspectResponse{
				ID:     "deadbeef",
				Name:   "/stateless",
				Image:  "nginx:latest",
				Config: &containertypes.Config{Image: "nginx:latest"},
				State:  &containertypes.State{Running: false},
				Mounts: nil,
			},
		},
	}
}

// A container with nothing mounted still has an image, a configuration and a
// template, and those are worth a restore point on their own — the user does
// not have to keep their compose file safe separately.
func TestContainerBackupWithoutVolumes(t *testing.T) {
	h := &ContainerHandler{cli: zeroMountMock()}
	destDir := t.TempDir()

	result, err := h.Backup(context.Background(), BackupItem{
		Name: "stateless", Settings: map[string]any{"id": "deadbeef"},
	}, destDir, func(string, int, string) {})
	if err != nil {
		t.Fatalf("a container without volumes should still back up: %v", err)
	}
	if result == nil || !result.Success {
		t.Fatalf("result = %+v, want a successful backup", result)
	}

	var names []string
	for _, f := range result.Files {
		names = append(names, f.Name)
	}
	joined := strings.Join(names, " ")
	if !strings.Contains(joined, "config.json") {
		t.Errorf("the container configuration must be captured, got %v", names)
	}
	if strings.Contains(joined, "volume_") || strings.Contains(joined, "volumes.json") {
		t.Errorf("no volume artefacts should be produced, got %v", names)
	}
}

func TestContainerBackupChunkedWithoutVolumes(t *testing.T) {
	repo, _, cleanup := dedup.NewTestRepoForEngine(t)
	defer cleanup()

	h := &ContainerHandler{cli: zeroMountMock()}
	manifestID, err := h.BackupChunked(context.Background(), BackupItem{
		Name: "stateless", Settings: map[string]any{"id": "deadbeef"},
	}, repo, nil, func(string, int, string) {})
	if err != nil {
		t.Fatalf("a container without volumes should still back up: %v", err)
	}
	if err := repo.Flush(); err != nil {
		t.Fatal(err)
	}

	m, err := repo.GetManifest(manifestID)
	if err != nil {
		t.Fatalf("get manifest: %v", err)
	}
	if _, ok := m.Files[containerInspectKey]; !ok {
		t.Error("the manifest must carry the container's inspect JSON")
	}
	for k := range m.Files {
		if strings.HasPrefix(k, containerVolPrefix) || strings.HasPrefix(k, containerVolFilePrefix) {
			t.Errorf("unexpected volume entry %q for a container with no mounts", k)
		}
	}
}
