package engine

import "testing"

func TestTarIncludeSet_EmptyMatchesEverything(t *testing.T) {
	s := newIncludeSet(nil)
	for _, name := range []string{"a.txt", "config/app.yml", ""} {
		if !s.matches(name) {
			t.Errorf("empty set should match %q", name)
		}
	}
}

func TestTarIncludeSet_ExactMatch(t *testing.T) {
	s := newIncludeSet([]string{"config/app.yml"})
	if !s.matches("config/app.yml") {
		t.Error("exact match must hit")
	}
	if s.matches("config/other.yml") {
		t.Error("non-listed sibling must miss")
	}
}

func TestTarIncludeSet_DescendantMatch(t *testing.T) {
	s := newIncludeSet([]string{"config"})
	if !s.matches("config") {
		t.Error("exact dir entry must hit")
	}
	if !s.matches("config/app.yml") {
		t.Error("descendant of selected dir must hit")
	}
	if !s.matches("config/sub/deeper.txt") {
		t.Error("nested descendant of selected dir must hit")
	}
	if s.matches("other/app.yml") {
		t.Error("unrelated path must miss")
	}
}

func TestTarIncludeSet_StripsLeadingTrailingSlashesAndBackslashes(t *testing.T) {
	s := newIncludeSet([]string{"/config/app.yml/", "data\\notes.txt"})
	if !s.matches("config/app.yml") {
		t.Error("leading/trailing slash normalization missed")
	}
	if !s.matches("data/notes.txt") {
		t.Error("backslash normalization missed")
	}
}

func TestTarIncludeSet_PrefixCollisionDoesNotOverMatch(t *testing.T) {
	// "config" should not match "configuration/foo".
	s := newIncludeSet([]string{"config"})
	if s.matches("configuration/foo") {
		t.Error("prefix similarity must not cross directory boundary")
	}
}

func TestIncludeSetIsAncestorOf(t *testing.T) {
	t.Parallel()
	set := newIncludeSet([]string{"3DS/Games/a.cia", "docs/"})
	for name, want := range map[string]bool{
		"3DS":             true,
		"3DS/":            true,
		"3DS/Games/":      true,
		"3DS/Games/a.cia": false, // the selection itself, not an ancestor
		"3DSX":            false,
		"docs":            false,
		"Other":           false,
		".":               false,
		"":                false,
	} {
		if got := set.isAncestorOf(name); got != want {
			t.Errorf("isAncestorOf(%q) = %v, want %v", name, got, want)
		}
	}
	if newIncludeSet(nil).isAncestorOf("3DS") {
		t.Error("empty set reported an ancestor")
	}
}
