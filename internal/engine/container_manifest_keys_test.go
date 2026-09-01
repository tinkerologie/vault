package engine

import (
	"testing"

	"github.com/ruaan-deysel/vault/internal/dedup"
)

// The picker's correctness rests entirely on this helper agreeing with what
// BackupChunked writes, so every synthetic key the engine can produce is
// pinned here — including the two a database_dump-enabled item adds, which
// leaked into the file picker as a sized "__dbdump__" file and a spurious
// "__dbdump_replay__" 0 B entry until they were recognised (issue #333).
func TestIsSyntheticContainerKey(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		key  string
		want bool
	}{
		{containerInspectKey, true},
		{containerImageMetaKey, true},
		{ContainerDBDumpKey, true},
		{ContainerDBReplayKey, true},
		{"__vol__/config", false},
		{"config/settings.yml", false},
		{"__inspect.bak", false},
		{"", false},
	} {
		if got := IsSyntheticContainerKey(tc.key); got != tc.want {
			t.Errorf("IsSyntheticContainerKey(%q) = %v, want %v", tc.key, got, tc.want)
		}
	}
}

func TestContainerVolumeDest(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		key      string
		wantDest string
		wantOK   bool
	}{
		{"__vol__/config", "/config", true},
		{"__vol__/var/lib/mysql", "/var/lib/mysql", true},
		{"__vol__", "", true}, // prefix with no destination still parses as a volume key
		{containerInspectKey, "", false},
		{"config/__vol__/x", "", false},
	} {
		dest, ok := ContainerVolumeDest(tc.key)
		if ok != tc.wantOK || dest != tc.wantDest {
			t.Errorf("ContainerVolumeDest(%q) = (%q, %v), want (%q, %v)", tc.key, dest, ok, tc.wantDest, tc.wantOK)
		}
	}
}

func TestIsSkippedVolumeEntry(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		entry dedup.ManifestEntry
		want  bool
	}{
		{"skip sentinel", dedup.ManifestEntry{Size: volumeSkippedSize}, true},
		{"no chunks", dedup.ManifestEntry{Size: 100}, true},
		{"backed up", dedup.ManifestEntry{Size: 100, Chunks: []dedup.ID{{1}}}, false},
	} {
		if got := IsSkippedVolumeEntry(tc.entry); got != tc.want {
			t.Errorf("%s: IsSkippedVolumeEntry = %v, want %v", tc.name, got, tc.want)
		}
	}
}
