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
func TestListMountsIncludesBackupableMountsAndFlagsAutoSkip(t *testing.T) {
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
					{Type: mounttypes.TypeVolume, Name: "some-named-volume", Source: "/var/lib/docker/volumes/some-named-volume/_data", Destination: "/data"},
					{Type: mounttypes.TypeVolume, Name: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", Source: "/var/lib/docker/volumes/anon/_data", Destination: "/anon"},
					{Type: mounttypes.TypeBind, Source: "/dev/rtc", Destination: "/dev/rtc"},
					{Type: mounttypes.TypeBind, Source: "/mnt/disk1/data", Destination: "/disk1"},
				},
			},
		},
	}
	h := &ContainerHandler{cli: mock}

	got, err := h.ListMounts(context.Background(), "sonarr", true)
	if err != nil {
		t.Fatalf("ListMounts() error = %v", err)
	}

	// Named volumes are now backed up alongside bind mounts (their Source is a
	// real host path under /var/lib/docker/volumes). Anonymous volumes (64-hex
	// name) are excluded — they can't be reliably restored. Sorted by destination.
	want := []MountInfo{
		{Source: "/mnt/cache/appdata/sonarr", Destination: "/config", Type: "bind", AutoSkip: false, SkipReason: "", Overridable: false},
		{Source: "/var/lib/docker/volumes/some-named-volume/_data", Destination: "/data", Type: "volume", AutoSkip: false, SkipReason: "", Overridable: false},
		{Source: "/dev/rtc", Destination: "/dev/rtc", Type: "bind", AutoSkip: true, SkipReason: "device/virtual path (/dev)", Overridable: false},
		{Source: "/mnt/disk1/data", Destination: "/disk1", Type: "bind", AutoSkip: true, SkipReason: "direct disk volume", Overridable: false},
		{Source: "/", Destination: "/rootfs", Type: "bind", AutoSkip: false, SkipReason: "", Overridable: false},
		{Source: "/mnt/user/media/tv", Destination: "/tv", Type: "bind", AutoSkip: true, SkipReason: "shared data volume (/mnt/user/media)", Overridable: true},
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

	if _, err := h.ListMounts(context.Background(), "ghost", true); err == nil {
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

// TestExtractIncludedMounts verifies parsing of the included_mounts setting
// across types (string slice, any slice from JSON, empty).
func TestExtractIncludedMounts(t *testing.T) {
	t.Parallel()

	if got := extractIncludedMounts(nil); got != nil {
		t.Errorf("extractIncludedMounts(nil) = %v, want nil", got)
	}
	if got := extractIncludedMounts(map[string]any{}); got != nil {
		t.Errorf("extractIncludedMounts(empty) = %v, want nil", got)
	}

	strMap := map[string]any{"included_mounts": []string{"/tv", "/data"}}
	if got := extractIncludedMounts(strMap); !reflect.DeepEqual(got, []string{"/tv", "/data"}) {
		t.Errorf("extractIncludedMounts([]string) = %v, want %v", got, []string{"/tv", "/data"})
	}

	anyMap := map[string]any{"included_mounts": []any{"/tv", "/data", ""}}
	if got := extractIncludedMounts(anyMap); !reflect.DeepEqual(got, []string{"/tv", "/data"}) {
		t.Errorf("extractIncludedMounts([]any) = %v, want %v", got, []string{"/tv", "/data"})
	}
}

// TestIsMountForceIncluded checks path cleaning and matching for force-included mounts.
func TestIsMountForceIncluded(t *testing.T) {
	t.Parallel()

	inc := []string{"/tv", "/data/"}
	if !isMountForceIncluded(inc, "/tv") {
		t.Errorf("expected /tv to match")
	}
	if !isMountForceIncluded(inc, "/data") {
		t.Errorf("expected /data to match /data/")
	}
	if isMountForceIncluded(inc, "/other") {
		t.Errorf("did not expect /other to match")
	}
	if isMountForceIncluded(nil, "/tv") {
		t.Errorf("did not expect match on nil list")
	}
}
