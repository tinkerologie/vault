package db

import "encoding/json"

// BackedUpItems reports which item names a restore point actually contains,
// derived from the per-item membership recorded in its metadata when the
// backup ran (item_sizes for classic backups, item_manifests for dedup).
//
// The second return value reports whether membership is *known*: legacy
// restore points produced before this metadata existed (and malformed or
// empty metadata) return (nil, false). Callers should treat unknown
// membership permissively — e.g. fall back to whole-archive behaviour —
// rather than assuming the restore point is empty.
func (rp RestorePoint) BackedUpItems() (map[string]struct{}, bool) {
	if rp.Metadata == "" {
		return nil, false
	}
	var meta struct {
		ItemSizes     map[string]json.RawMessage `json:"item_sizes"`
		ItemManifests map[string]json.RawMessage `json:"item_manifests"`
	}
	if err := json.Unmarshal([]byte(rp.Metadata), &meta); err != nil {
		return nil, false
	}
	if len(meta.ItemSizes) == 0 && len(meta.ItemManifests) == 0 {
		return nil, false
	}
	items := make(map[string]struct{}, len(meta.ItemSizes)+len(meta.ItemManifests))
	for name := range meta.ItemSizes {
		items[name] = struct{}{}
	}
	for name := range meta.ItemManifests {
		items[name] = struct{}{}
	}
	return items, true
}
