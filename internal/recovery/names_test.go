package recovery

import (
	"path/filepath"
	"testing"
)

func TestWindowsSafeName(t *testing.T) {
	cases := map[string]string{
		"plain.txt":            "plain.txt",
		"2026-10-07T02:03.log": "2026-10-07T02_03.log",
		`a<b>c"d|e?f*g\h`:      "a_b_c_d_e_f_g_h",
		"trailing.":            "trailing_",
		"spaces  ":             "spaces__",
		"CON":                  "_CON",
		"con.txt":              "_con.txt",
		"Com1.log":             "_Com1.log",
		"LPT¹":                 "_LPT¹",
		"CONSOLE":              "CONSOLE",
		"nul .txt":             "_nul .txt",
		"tab\there":            "tab_here",
		"ümlaut":               "ümlaut",
	}
	for in, want := range cases {
		if got := windowsSafeName(in); got != want {
			t.Errorf("windowsSafeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNamerRejectsEscapes(t *testing.T) {
	n := newNamer(true, true)
	for _, p := range []string{"../etc/passwd", "a/../../b", ".."} {
		if _, err := n.local(p); err == nil {
			t.Errorf("local(%q) succeeded, want an escape error", p)
		}
	}
	for p, want := range map[string]string{
		"/config/app.yml": "config/app.yml",
		"./data/x":        "data/x",
		"a/./b/../c":      "a/c",
		"":                "",
	} {
		got, err := n.local(p)
		if err != nil || got != filepath.FromSlash(want) {
			t.Errorf("local(%q) = %q, %v; want %q", p, got, err, want)
		}
	}
}

// TestNamerKeepsCaseCollisionsApart checks names that differ only by case get
// distinct local names, and files under a renamed directory follow it.
func TestNamerKeepsCaseCollisionsApart(t *testing.T) {
	n := newNamer(true, true)
	mustLocal := func(p string) string {
		t.Helper()
		got, err := n.local(p)
		if err != nil {
			t.Fatalf("local(%q): %v", p, err)
		}
		return filepath.ToSlash(got)
	}
	if got := mustLocal("Docs/readme.md"); got != "Docs/readme.md" {
		t.Fatalf("first = %q", got)
	}
	if got := mustLocal("docs/README.md"); got != "docs (2)/README.md" {
		t.Fatalf("case twin dir = %q, want %q", got, "docs (2)/README.md")
	}
	if got := mustLocal("docs/other.md"); got != "docs (2)/other.md" {
		t.Fatalf("sibling under renamed dir = %q", got)
	}
	if got := mustLocal("Docs/README.MD"); got != "Docs/README (2).MD" {
		t.Fatalf("case twin file = %q", got)
	}
	if got := mustLocal("Docs/readme.md"); got != "Docs/readme.md" {
		t.Fatalf("repeat lookup changed: %q", got)
	}
	if len(n.renames) != 2 {
		t.Fatalf("renames = %+v, want 2 entries", n.renames)
	}
}

// TestNamerSanitisedCollision checks two different names that sanitise to the
// same Windows name ("a:b" and "a?b" both become "a_b") stay distinct.
func TestNamerSanitisedCollision(t *testing.T) {
	n := newNamer(true, true)
	a, _ := n.local("a:b")
	b, _ := n.local("a?b")
	if a == b {
		t.Fatalf("a:b and a?b both mapped to %q", a)
	}
}

// TestNamerUnsafeModeKeepsNames checks Linux-style extraction leaves legal
// POSIX names alone and is case-sensitive.
func TestNamerUnsafeModeKeepsNames(t *testing.T) {
	n := newNamer(false, false)
	for _, p := range []string{"a:b", "Docs/x", "docs/x", "CON"} {
		got, err := n.local(p)
		if err != nil || filepath.ToSlash(got) != p {
			t.Errorf("local(%q) = %q, %v; want unchanged", p, got, err)
		}
	}
	if len(n.renames) != 0 {
		t.Fatalf("renames = %+v, want none", n.renames)
	}
}
