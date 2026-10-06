package engine

import (
	"path"
	"strings"
)

// tarIncludeSet is the path-filter used by untarDirectoryFiltered to select
// only the tar entries explicitly requested by a partial restore. An empty
// set means "extract everything" — the legacy behaviour callers fall back
// to when no file-paths filter is supplied.
type tarIncludeSet struct {
	// exact holds the literal entry names (forward-slash separated, no
	// leading slash) the user picked from the file tree.
	exact map[string]struct{}
	// dirs holds those exact entries that look like directory prefixes
	// (always treated as such for the "include descendants" rule below).
	dirs []string
	// ancestors holds every strict parent directory of an exact entry.
	ancestors map[string]struct{}
}

// newIncludeSet builds a tarIncludeSet from a list of paths. Empty input
// yields a permissive set whose matches() always returns true.
func newIncludeSet(paths []string) tarIncludeSet {
	if len(paths) == 0 {
		return tarIncludeSet{}
	}
	set := tarIncludeSet{
		exact:     make(map[string]struct{}, len(paths)),
		ancestors: make(map[string]struct{}),
	}
	for _, p := range paths {
		p = strings.Trim(strings.ReplaceAll(p, "\\", "/"), "/")
		if p == "" {
			continue
		}
		set.exact[p] = struct{}{}
		// If the path was explicitly added with a trailing slash in the
		// caller's intent, or if it looks like an enclosing directory
		// (no extension and no segments below it), we also include any
		// descendant entry whose name has this as a prefix.
		set.dirs = append(set.dirs, p+"/")
		for parent := path.Dir(p); parent != "." && parent != "/"; parent = path.Dir(parent) {
			set.ancestors[parent] = struct{}{}
		}
	}
	return set
}

// matches reports whether a tar entry's Name should be extracted. The empty
// set always matches (legacy whole-archive extract).
func (s tarIncludeSet) matches(name string) bool {
	if s.exact == nil {
		return true
	}
	clean := strings.Trim(strings.ReplaceAll(name, "\\", "/"), "/")
	if clean == "" {
		return false
	}
	if _, ok := s.exact[clean]; ok {
		return true
	}
	// Allow descendants of any directory the caller explicitly picked.
	for _, prefix := range s.dirs {
		if strings.HasPrefix(clean+"/", prefix) || strings.HasPrefix(clean, prefix) {
			return true
		}
	}
	return false
}

// isAncestorOf reports whether name is a strict ancestor directory of an
// explicitly requested path. Partial restores select leaf files, so the
// directories containing them never match; a caller that sees one of these
// directory entries can still restore its recorded metadata (#442) without
// pulling in any sibling content.
func (s tarIncludeSet) isAncestorOf(name string) bool {
	clean := strings.Trim(strings.ReplaceAll(name, "\\", "/"), "/")
	_, ok := s.ancestors[clean]
	return ok
}
