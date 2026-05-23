package release

import (
	"strings"
	"testing"
)

func TestParseWellFormed(t *testing.T) {
	md := strings.TrimSpace(`
# Changelog

## [Unreleased]

### Added

- Should NOT appear in output

## [v2026.05.03] - 2026-05-23

### Added

- Friendly retry editor

### Fixed

- Toggle persistence

## [v2026.05.02] - 2026-05-15

### Added

- Resilience hardening
`)
	got, err := Parse(md)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d releases, want 2 (Unreleased excluded)", len(got))
	}
	if got[0].Version != "v2026.05.03" {
		t.Errorf("got[0].Version = %q, want v2026.05.03", got[0].Version)
	}
	if got[0].Date != "2026-05-23" {
		t.Errorf("got[0].Date = %q, want 2026-05-23", got[0].Date)
	}
	if added := got[0].Sections["Added"]; len(added) != 1 || added[0] != "Friendly retry editor" {
		t.Errorf("got[0].Sections[Added] = %v", added)
	}
	if fixed := got[0].Sections["Fixed"]; len(fixed) != 1 || fixed[0] != "Toggle persistence" {
		t.Errorf("got[0].Sections[Fixed] = %v", fixed)
	}
	if got[1].Version != "v2026.05.02" {
		t.Errorf("got[1].Version = %q, want v2026.05.02", got[1].Version)
	}
}

func TestParseMissingDate(t *testing.T) {
	md := `## [v1.0.0]

### Added

- foo
`
	got, err := Parse(md)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got) != 1 || got[0].Version != "v1.0.0" {
		t.Fatalf("got %+v", got)
	}
	if got[0].Date != "" {
		t.Errorf("expected empty date, got %q", got[0].Date)
	}
}

func TestParseUnreleasedOnly(t *testing.T) {
	md := `## [Unreleased]

### Added

- foo
`
	got, err := Parse(md)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Unreleased should be excluded; got %d releases", len(got))
	}
}

func TestParseMalformedDoesNotPanic(t *testing.T) {
	cases := []string{"", "garbage", "## []", "## [v1.0.0]\n### \n- foo"}
	for _, in := range cases {
		got, err := Parse(in)
		if err != nil {
			t.Errorf("Parse(%q) unexpected error: %v", in, err)
		}
		_ = got
	}
}

func TestParseAgainstRealChangelog(t *testing.T) {
	// Ensures the embedded CHANGELOG.md still parses cleanly. If this
	// test fails after a CHANGELOG edit, the format drifted from
	// Keep-a-Changelog (likely a stray heading or bullet style).
	releases, err := Parse(Raw())
	if err != nil {
		t.Fatalf("Parse(Raw()): %v", err)
	}
	if len(releases) == 0 {
		t.Skip("no releases parsed — repo CHANGELOG.md only has [Unreleased]; not a regression")
	}
	for _, r := range releases {
		if r.Version == "" {
			t.Errorf("release with empty version: %+v", r)
		}
		if len(r.Sections) == 0 {
			t.Logf("release %s has no recognised sections", r.Version)
		}
	}
}
