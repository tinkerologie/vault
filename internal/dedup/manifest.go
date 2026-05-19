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
