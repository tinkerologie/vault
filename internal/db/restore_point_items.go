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

// ItemSizes returns the per-item byte sizes recorded when the backup ran, and
// whether any were found.
//
// Both classic and dedup backups record item_sizes: the chunked path reports a
// synthetic result whose size is the item's logical byte total, which the
// shared completion path writes here alongside item_manifests. Sizes are
// therefore absent only for restore points written before this metadata
// existed, and for items whose backup result reported no size at all. A zero
// is recorded explicitly, so an item that backed up nothing restores as 0
// rather than falling back to the whole restore point's size.
//
// Callers must treat a missing entry as "unknown" rather than as zero — a
// restore reporting 0 bytes would be as wrong as one reporting the whole
// backup's size (issue #334).
func (rp RestorePoint) ItemSizes() (map[string]int64, bool) {
	if rp.Metadata == "" {
		return nil, false
	}
	var meta struct {
		ItemSizes map[string]int64 `json:"item_sizes"`
	}
	if err := json.Unmarshal([]byte(rp.Metadata), &meta); err != nil {
		return nil, false
	}
	if len(meta.ItemSizes) == 0 {
		return nil, false
	}
	return meta.ItemSizes, true
}
