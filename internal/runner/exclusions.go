package runner

import (
	"encoding/json"
	"log"

	"github.com/ruaan-deysel/vault/internal/docsmeta"
)

// globalExcludePaths reads the global_exclude_paths setting.
//
// A malformed value yields no exclusions rather than an error: excluding
// nothing degrades to today's behaviour, whereas failing the run would take
// every backup offline over a mistyped setting. The problem is logged so it is
// still diagnosable.
func (r *Runner) globalExcludePaths() []string {
	// GetSetting returns the default alongside a real database error, so an
	// unlogged error here would be indistinguishable from "not configured" —
	// and failing open on exclusions means backing up paths meant to be
	// skipped. Logged rather than fatal, matching how every other setting in
	// the runner is read.
	raw, err := r.db.GetSetting("global_exclude_paths", docsmeta.DefaultFor("global_exclude_paths"))
	if err != nil {
		log.Printf("runner: reading global_exclude_paths (continuing without global exclusions): %v", err)
	}
	if raw == "" {
		return nil
	}
	var paths []string
	if err := json.Unmarshal([]byte(raw), &paths); err != nil {
		log.Printf("runner: invalid global_exclude_paths %q: %v", raw, err)
		return nil
	}
	return paths
}

// mergeExclusions unions the global exclusion list with an item's own.
//
// Merged rather than overridden: a global list is a floor, not a replacement,
// so an item can still add paths of its own. The item's entries come first so
// the more specific list leads in any operator-facing rendering, and duplicates
// are dropped so a path listed in both places is not matched twice.
//
// itemRaw arrives straight from the job's JSON settings blob, so it may be
// []any (decoded JSON) or []string (already normalised) — both are accepted,
// matching the engine's own extractExcludePaths.
func mergeExclusions(itemRaw any, global []string) []string {
	item := toStringSlice(itemRaw)
	if len(global) == 0 {
		return item
	}

	merged := make([]string, 0, len(item)+len(global))
	seen := make(map[string]struct{}, len(item)+len(global))
	for _, list := range [][]string{item, global} {
		for _, p := range list {
			if p == "" {
				continue
			}
			if _, dup := seen[p]; dup {
				continue
			}
			seen[p] = struct{}{}
			merged = append(merged, p)
		}
	}
	return merged
}

// toStringSlice normalises the shapes a settings value can take.
func toStringSlice(raw any) []string {
	switch v := raw.(type) {
	case nil:
		return nil
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
