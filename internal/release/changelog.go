package release

import (
	"regexp"
	"strings"
)

// Release is one parsed entry from CHANGELOG.md.
type Release struct {
	Version  string              `json:"version"`        // e.g. "v2026.05.03"
	Date     string              `json:"date,omitempty"` // "YYYY-MM-DD" or empty
	Sections map[string][]string `json:"sections"`       // {"Added": [...], "Fixed": [...]}
}

// sectionWhitelist limits which Keep-a-Changelog headings the parser
// surfaces. Anything else under a version block is ignored silently.
var sectionWhitelist = map[string]bool{
	"Added":    true,
	"Changed":  true,
	"Fixed":    true,
	"Removed":  true,
	"Security": true,
}

// versionLine matches lines of the form:
//
//	## [v1.2.3]
//	## [v1.2.3] - 2026-05-23
//	## [Unreleased]
var versionLine = regexp.MustCompile(`^##\s+\[([^\]]+)\](?:\s+-\s+(\d{4}-\d{2}-\d{2}))?\s*$`)

// sectionLine matches "### Added" / "### Fixed" / etc.
var sectionLine = regexp.MustCompile(`^###\s+(\w+)\s*$`)

// Parse converts a CHANGELOG.md (Keep-a-Changelog format) into a slice
// of Release entries, newest first. The Unreleased section, if present,
// is dropped — only shipped versions are surfaced to the user.
//
// The parser is defensive: malformed input never panics or returns an
// error; it returns whatever it could extract.
func Parse(md string) ([]Release, error) {
	var out []Release
	var cur *Release
	var section string

	for _, line := range strings.Split(md, "\n") {
		if m := versionLine.FindStringSubmatch(line); m != nil {
			// Flush previous, start new.
			if cur != nil && cur.Version != "Unreleased" && cur.Version != "" {
				out = append(out, *cur)
			}
			cur = &Release{
				Version:  m[1],
				Date:     m[2],
				Sections: make(map[string][]string),
			}
			section = ""
			continue
		}
		if cur == nil {
			continue
		}
		if m := sectionLine.FindStringSubmatch(line); m != nil {
			if sectionWhitelist[m[1]] {
				section = m[1]
			} else {
				section = ""
			}
			continue
		}
		if section == "" {
			continue
		}
		if strings.HasPrefix(line, "- ") {
			cur.Sections[section] = append(cur.Sections[section], strings.TrimSpace(line[2:]))
		}
	}
	// Final flush.
	if cur != nil && cur.Version != "Unreleased" && cur.Version != "" {
		out = append(out, *cur)
	}
	return out, nil
}
