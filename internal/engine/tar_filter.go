package engine

import "strings"

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
}

// newIncludeSet builds a tarIncludeSet from a list of paths. Empty input
// yields a permissive set whose matches() always returns true.
func newIncludeSet(paths []string) tarIncludeSet {
	if len(paths) == 0 {
		return tarIncludeSet{}
	}
	set := tarIncludeSet{exact: make(map[string]struct{}, len(paths))}
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
