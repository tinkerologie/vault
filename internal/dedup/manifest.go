package dedup

import "encoding/json"

// ManifestVersion bumps on incompatible manifest schema change. Consumers
// must refuse manifests with a Version higher than the constant they were
// compiled against.
const ManifestVersion = 1

// Manifest describes one backed-up item — a file tree + per-file chunk list.
// Stored in the repo as just another chunk (PutManifest writes it via Put,
// returns the manifest's chunk ID for restore_point.manifest_id).
type Manifest struct {
	Version int                      `json:"version"`
	Item    string                   `json:"item"`
	Files   map[string]ManifestEntry `json:"files"` // key = path relative to source root
}

// ManifestEntry is one path's metadata + chunks. For directories, Chunks is
// nil and IsDir is true. For regular files, Chunks lists the content chunks
// in order; concatenating their plaintexts reproduces the file body.
type ManifestEntry struct {
	Mode    uint32 `json:"mode"`
	ModTime string `json:"modtime"` // RFC3339
	Size    int64  `json:"size"`
	IsDir   bool   `json:"is_dir,omitempty"`
	Chunks  []ID   `json:"chunks,omitempty"`
}

// EncodeJSON returns the canonical JSON form. Stored as a chunk via
// Repo.PutManifest.
func (m Manifest) EncodeJSON() ([]byte, error) { return json.Marshal(m) }

// DecodeManifest parses a Manifest blob produced by EncodeJSON.
func DecodeManifest(b []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// ManifestSegmentSize is the threshold above which a serialised manifest is
// split into multiple chunk-sized segments before storage. Manifests at or
// below this size are stored as a single chunk (v1 layout). 4 MiB aligns with
// ChunkMax so every segment is a normal-sized chunk, well under the packer's
// safety bound.
const ManifestSegmentSize = 4 * 1024 * 1024

// segmentedManifestType is the Type discriminator value carried by a
// SegmentedManifest envelope. A v1 Manifest blob has no "type" field, so its
// presence unambiguously identifies the segmented layout.
const segmentedManifestType = "segmented"

// SegmentedManifest is the small envelope stored in place of an oversized
// manifest. Segments lists the chunk IDs of the manifest-JSON pieces in order;
// concatenating their plaintexts and JSON-decoding the result reproduces the
// Manifest.
type SegmentedManifest struct {
	Type     string `json:"type"`
	Segments []ID   `json:"segments"`
}

// isSegmentedManifest reports whether data is a SegmentedManifest envelope (as
// opposed to a v1 Manifest blob). It probe-decodes only the "type"
// discriminator, so it is cheap and tolerant of the larger fields. Malformed
// or empty input returns false — the caller then attempts a v1 decode, which
// surfaces a precise JSON error.
func isSegmentedManifest(data []byte) bool {
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return false
	}
	return probe.Type == segmentedManifestType
}
