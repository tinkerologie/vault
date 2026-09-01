package engine

import (
	"path"
	"strings"
)

// The restore file picker presents a container's contents as container-internal
// absolute paths — `/config/settings.yml` for the file `settings.yml` inside
// the volume mounted at `/config` (see dedupManifestToTarIndex and the classic
// volume index merge in the jobs handler). Extraction, however, happens one
// volume at a time against paths relative to that volume's root. These helpers
// are the translation between the two, so classic and chunked restore agree on
// what a selection means (issue #275).

// ContainerVolumePath renders a volume-relative path the way the picker shows
// it: joined onto the volume's container-internal mount destination. The API's
// restore-point browsing uses it so the listing and the extraction filter below
// cannot drift into different spellings of the same file.
func ContainerVolumePath(destination, rel string) string {
	return path.Join("/", destination, rel)
}

// containerVolumeIncludes maps a picker selection onto one volume.
//
// It returns the volume-relative paths to extract and whether the volume is
// wanted at all. Three outcomes:
//
//   - (nil, true) — restore the whole volume. The selection is empty (no picker
//     filter: restore everything, the pre-#275 behaviour), or the user picked
//     the mount point itself, or they picked a directory this mount nests
//     inside.
//   - (paths, true) — restore only these paths within the volume.
//   - (nil, false) — the selection names nothing in this volume, so it must be
//     skipped entirely. Extracting it anyway is the bug in issue #275.
//
// Selecting a directory selects its contents: the returned paths are fed to
// the same tarIncludeSet / manifest filter the folder handler uses, which
// already treats a picked directory as covering its descendants.
//
// allDests carries every mount destination in the backup because mounts nest:
// a container can mount both /config and /config/cache, and the picker shows
// them as one tree. Without the full set, /config/cache/f.yml would be routed
// into /config as well — a mount whose archive never held it — so a selected
// path belongs only to the DEEPEST destination that contains it. Conversely a
// selected directory covers the mounts nested beneath it, which is why picking
// /config restores /config/cache too.
func containerVolumeIncludes(selection []string, destination string, allDests []string) ([]string, bool) {
	if len(selection) == 0 {
		return nil, true
	}
	dest := normaliseContainerPath(destination)
	var rels []string
	for _, sel := range selection {
		p := normaliseContainerPath(sel)
		if p == "" {
			continue
		}
		if p == dest {
			// The mount point itself: the whole volume, however deep the rest
			// of the selection goes.
			return nil, true
		}
		if containsPath(dest, p) {
			// A directory this mount nests inside was picked, so the mount
			// comes with it.
			return nil, true
		}
		if !containsPath(p, dest) {
			continue
		}
		if deepestDest(p, allDests, dest) != dest {
			// A deeper mount owns this path; that volume will extract it.
			continue
		}
		if dest == "/" {
			rels = append(rels, strings.TrimPrefix(p, "/"))
			continue
		}
		if rel, ok := strings.CutPrefix(p, dest+"/"); ok && rel != "" {
			rels = append(rels, rel)
		}
	}
	if len(rels) == 0 {
		return nil, false
	}
	return rels, true
}

// containsPath reports whether the normalised directory path parent contains
// child, comparing whole path segments so /configuration never counts as being
// inside /config.
func containsPath(child, parent string) bool {
	if parent == "/" {
		return child != "/"
	}
	return strings.HasPrefix(child, parent+"/")
}

// deepestDest returns the longest mount destination in dests that contains p.
// fallback is returned when dests is empty or names nothing containing p, so a
// caller with no mount list behaves exactly as it did before nesting was
// considered.
func deepestDest(p string, dests []string, fallback string) string {
	best := ""
	for _, d := range dests {
		nd := normaliseContainerPath(d)
		if nd == "" || (nd != p && !containsPath(p, nd)) {
			continue
		}
		if len(nd) > len(best) {
			best = nd
		}
	}
	if best == "" {
		return fallback
	}
	return best
}

// normaliseContainerPath puts a container-internal path into the single form
// the comparisons above rely on: cleaned, slash-separated, and rooted. An empty
// input stays empty so callers can drop it rather than read it as the root,
// which would select every volume.
func normaliseContainerPath(p string) string {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	if p == "" {
		return ""
	}
	return path.Clean("/" + p)
}
