package engine

import (
	"strings"

	"github.com/ruaan-deysel/vault/internal/dedup"
)

// The container dedup manifest's Files map holds only synthetic keys — see the
// key contract documented above BackupChunked in container.go. Backup, restore,
// and the restore-point browsing API all need to agree on what each key means;
// these exported helpers are the single authority so a reader can never invent
// its own rules and drift from what the engine wrote.

// IsSyntheticContainerKey reports whether a container manifest key holds engine
// metadata rather than restorable file content: __inspect, __image_meta, and
// the two database-dump keys a database_dump-enabled item adds.
//
// Such entries must never surface in a file picker. __inspect and __image_meta
// are internal JSON blobs whose sizes describe the blob rather than any
// restored file; __dbdump__ carries the logical dump, which restore replays
// into the live server rather than extracting to a path; and
// __dbdump_replay__ is a zero-value marker that would otherwise render as a
// spurious "0 B" file (issue #333).
func IsSyntheticContainerKey(key string) bool {
	switch key {
	case containerInspectKey, containerImageMetaKey, ContainerDBDumpKey, ContainerDBReplayKey:
		return true
	}
	return false
}

// ContainerVolumeDest returns the container-internal destination path a
// __vol__<destination> key refers to, and whether the key is a volume entry.
func ContainerVolumeDest(key string) (string, bool) {
	if !strings.HasPrefix(key, containerVolPrefix) {
		return "", false
	}
	return strings.TrimPrefix(key, containerVolPrefix), true
}

// IsSkippedVolumeEntry reports whether a __vol__ entry records a volume that
// was deliberately not backed up (shouldSkipVolume returned true, or the
// volume was excluded from the job).
//
// Backup preserves such entries for diagnostics with the volumeSkippedSize
// sentinel and no chunks, so any consumer that presents volumes to a user must
// filter them out — otherwise an excluded mount appears restorable and its
// sentinel size renders as "-1 B" (issue #333).
func IsSkippedVolumeEntry(e dedup.ManifestEntry) bool {
	return e.Size == volumeSkippedSize || len(e.Chunks) == 0
}

// ChunkedManifestUnchanged reports whether a freshly written chunked manifest
// captured no content that differs from its parent.
//
// Chunk and sub-manifest IDs are content addresses, so two runs that read the
// same bytes produce the same IDs — including for a container's __vol__ keys,
// whose single "chunk" is the ID of the volume's sub-manifest. Equal key sets
// with equal chunk lists therefore mean nothing changed.
//
// __inspect is deliberately excluded: it is re-encoded from a live `docker
// inspect` on every run and carries volatile state (start time, health, IDs),
// so it always differs even for a container nobody touched. It is engine
// metadata rather than backed-up content, and counting it would make every
// incremental run look like a fresh backup — the whole of issue #326.
//
// Manifest.Installer is compared too: a plugin's .plg payload lives outside
// Files (see PluginHandler.BackupChunked), so comparing Files alone would call
// a plugin unchanged when only its installer had been replaced.
//
// A nil parent means there is nothing to compare against (a full backup, or
// the first run of a chain), which is never "unchanged".
func ChunkedManifestUnchanged(current, parent *dedup.Manifest) bool {
	if current == nil || parent == nil {
		return false
	}
	if !sameManifestEntry(current.Installer, parent.Installer) {
		return false
	}
	comparable := func(m *dedup.Manifest) map[string][]dedup.ID {
		out := make(map[string][]dedup.ID, len(m.Files))
		for key, entry := range m.Files {
			if key == containerInspectKey {
				continue
			}
			out[key] = entry.Chunks
		}
		return out
	}
	cur, prev := comparable(current), comparable(parent)
	if len(cur) != len(prev) {
		return false
	}
	for key, chunks := range cur {
		other, ok := prev[key]
		if !ok || !sameChunks(chunks, other) {
			return false
		}
	}
	return true
}

// sameManifestEntry compares two out-of-tree payload entries by content
// address. Both absent counts as equal; one absent does not.
func sameManifestEntry(a, b *dedup.ManifestEntry) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return sameChunks(a.Chunks, b.Chunks)
}

func sameChunks(a, b []dedup.ID) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
