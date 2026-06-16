package engine

import (
	"context"
	"errors"
	"reflect"
	"testing"

	containertypes "github.com/moby/moby/api/types/container"
	mounttypes "github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// TestListMountsFiltersBindAndFlagsAutoSkip verifies that ListMounts returns
// only bind mounts, sorted by destination, and flags auto-skipped volumes via
// the same shouldSkipVolume rules the backup engine applies.
func TestListMountsFiltersBindAndFlagsAutoSkip(t *testing.T) {
	t.Parallel()
	mock := &mockDockerClient{
		inspectResp: client.ContainerInspectResult{
			Container: containertypes.InspectResponse{
				ID:   "deadbeef",
				Name: "/sonarr",
				Mounts: []containertypes.MountPoint{
					{Type: mounttypes.TypeBind, Source: "/mnt/user/media/tv", Destination: "/tv"},
					{Type: mounttypes.TypeBind, Source: "/mnt/cache/appdata/sonarr", Destination: "/config"},
					{Type: mounttypes.TypeBind, Source: "/", Destination: "/rootfs"},
					{Type: mounttypes.TypeVolume, Source: "some-named-volume", Destination: "/data"},
				},
			},
		},
	}
	h := &ContainerHandler{cli: mock}

	got, err := h.ListMounts(context.Background(), "sonarr")
	if err != nil {
		t.Fatalf("ListMounts() error = %v", err)
	}

	want := []MountInfo{
		{Source: "/mnt/cache/appdata/sonarr", Destination: "/config", Type: "bind", AutoSkip: false, SkipReason: ""},
		{Source: "/", Destination: "/rootfs", Type: "bind", AutoSkip: false, SkipReason: ""},
		{Source: "/mnt/user/media/tv", Destination: "/tv", Type: "bind", AutoSkip: true, SkipReason: "shared data volume (/mnt/user/media)"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ListMounts() =\n  %#v\nwant\n  %#v", got, want)
	}
}

// TestListMountsInspectError surfaces inspect failures to the caller.
func TestListMountsInspectError(t *testing.T) {
	t.Parallel()
	mock := &mockDockerClient{inspectErr: errors.New("no such container")}
	h := &ContainerHandler{cli: mock}

	if _, err := h.ListMounts(context.Background(), "ghost"); err == nil {
		t.Fatal("ListMounts() error = nil, want non-nil")
	}
}

// TestContainerExclusionsMergesExcludedMounts verifies that the per-container
// exclusion list combines free-text exclude_paths with checkbox-driven
// excluded_mounts.
func TestContainerExclusionsMergesExcludedMounts(t *testing.T) {
	t.Parallel()
	settings := map[string]any{
		"exclude_paths":   []any{"*.log", "/config/Cache"},
		"excluded_mounts": []any{"/tv", "/downloads"},
	}

	got := containerExclusions(settings)
	want := []string{"*.log", "/config/Cache", "/tv", "/downloads"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("containerExclusions() = %#v, want %#v", got, want)
	}
}

// TestContainerExclusionsOnlyMounts handles jobs that use mount toggles without
// any free-text exclusions.
func TestContainerExclusionsOnlyMounts(t *testing.T) {
	t.Parallel()
	settings := map[string]any{
		"excluded_mounts": []any{"/rootfs"},
	}
	got := containerExclusions(settings)
	want := []string{"/rootfs"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("containerExclusions() = %#v, want %#v", got, want)
	}
}
