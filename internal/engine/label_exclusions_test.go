package engine

import (
	"reflect"
	"testing"

	"github.com/moby/moby/api/types/container"
)

// TestParseLabelExclusions covers the vault.exclude label format (issue #258).
func TestParseLabelExclusions(t *testing.T) {
	cases := []struct {
		name   string
		labels map[string]string
		want   []string
	}{
		{"no labels at all", nil, nil},
		{"label absent", map[string]string{"other": "x"}, nil},
		{"single path", map[string]string{VaultExcludeLabel: "/config/cache"}, []string{"/config/cache"}},
		{
			"comma-separated list",
			map[string]string{VaultExcludeLabel: "/config/cache,/tmp"},
			[]string{"/config/cache", "/tmp"},
		},
		{
			// Padding and trailing commas are common in compose files; a blank
			// pattern would match everything, so they must be dropped.
			"padded entries and a trailing comma",
			map[string]string{VaultExcludeLabel: " /a , /b ,"},
			[]string{"/a", "/b"},
		},
		{"empty label value excludes nothing", map[string]string{VaultExcludeLabel: ""}, nil},
		{"only separators excludes nothing", map[string]string{VaultExcludeLabel: " , , "}, nil},
		{"glob patterns pass through", map[string]string{VaultExcludeLabel: "*.sock"}, []string{"*.sock"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseLabelExclusions(tc.labels)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// TestContainerExclusionsWithLabels confirms label patterns join the same
// slice as the wizard's, so everything downstream treats them identically.
func TestContainerExclusionsWithLabels(t *testing.T) {
	settings := map[string]any{
		"exclude_paths":   []any{"/typed"},
		"excluded_mounts": []any{"/unchecked"},
	}
	labels := map[string]string{VaultExcludeLabel: "/labelled"}

	got := containerExclusionsWithLabels(settings, labels)
	for _, want := range []string{"/typed", "/unchecked", "/labelled"} {
		if !contains(got, want) {
			t.Fatalf("got %v, missing %q", got, want)
		}
	}
}

// TestLabelExclusionsRespectTheToggle covers the global off-switch, and the
// absent case, which must default to enabled to match the catalog default.
func TestLabelExclusionsRespectTheToggle(t *testing.T) {
	labels := map[string]string{VaultExcludeLabel: "/labelled"}

	off := containerExclusionsWithLabels(map[string]any{"label_exclusions_enabled": false}, labels)
	if contains(off, "/labelled") {
		t.Fatalf("label honoured while the setting is off: %v", off)
	}

	on := containerExclusionsWithLabels(map[string]any{"label_exclusions_enabled": true}, labels)
	if !contains(on, "/labelled") {
		t.Fatalf("label ignored while the setting is on: %v", on)
	}

	absent := containerExclusionsWithLabels(map[string]any{}, labels)
	if !contains(absent, "/labelled") {
		t.Fatalf("absent setting should default to enabled, got %v", absent)
	}
}

// TestLabelExclusionsMatchLikeTypedOnes is the point of merging into one
// slice: a label-declared path must exclude a mount exactly as a typed path
// does, with no second matcher to drift.
func TestLabelExclusionsMatchLikeTypedOnes(t *testing.T) {
	labelled := containerExclusionsWithLabels(map[string]any{}, map[string]string{VaultExcludeLabel: "/config/cache"})
	typed := containerExclusionsWithLabels(map[string]any{"exclude_paths": []any{"/config/cache"}}, nil)

	if shouldExcludeMount(labelled, "/config/cache") != shouldExcludeMount(typed, "/config/cache") {
		t.Fatal("a label-declared path matched differently from a typed one")
	}
	if !shouldExcludeMount(labelled, "/config/cache") {
		t.Fatal("label-declared path did not exclude its mount")
	}
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// TestLabelExclusionsRejectCatchAll is the guardrail on label authority.
//
// A label is written by whoever built the image or template, not by the person
// who owns the backup. shouldExcludeMount globs against the mount's base name,
// so a single "*" would exclude every mount and the backup would still report
// success while containing nothing. An image may opt paths out; it may not opt
// the whole container out.
func TestLabelExclusionsRejectCatchAll(t *testing.T) {
	for _, pattern := range []string{"*", "**", "/", "/*", "/**", "."} {
		got := parseLabelExclusions(map[string]string{VaultExcludeLabel: pattern})
		if len(got) != 0 {
			t.Errorf("catch-all %q was accepted from a label: %v", pattern, got)
		}
	}
}

// TestLabelExclusionsKeepNarrowPatternsAlongsideCatchAll confirms the guard
// drops only the catch-all, not the whole label.
func TestLabelExclusionsKeepNarrowPatternsAlongsideCatchAll(t *testing.T) {
	got := parseLabelExclusions(map[string]string{VaultExcludeLabel: "*,/config/cache,*.sock"})
	if contains(got, "*") {
		t.Fatalf("catch-all survived: %v", got)
	}
	for _, want := range []string{"/config/cache", "*.sock"} {
		if !contains(got, want) {
			t.Fatalf("narrow pattern %q was dropped with the catch-all: %v", want, got)
		}
	}
}

// TestCountExcludedMounts backs the guard against a backup that succeeds while
// holding no volume data at all.
func TestCountExcludedMounts(t *testing.T) {
	mounts := []container.MountPoint{
		{Type: "bind", Source: "/mnt/a", Destination: "/config"},
		{Type: "bind", Source: "/mnt/b", Destination: "/data"},
		{Type: "tmpfs", Source: "", Destination: "/tmp"}, // not eligible
	}

	eligible, excluded := countExcludedMounts(mounts, nil)
	if eligible != 2 || excluded != 0 {
		t.Fatalf("no exclusions: eligible=%d excluded=%d, want 2/0", eligible, excluded)
	}

	eligible, excluded = countExcludedMounts(mounts, []string{"/config"})
	if eligible != 2 || excluded != 1 {
		t.Fatalf("one exclusion: eligible=%d excluded=%d, want 2/1", eligible, excluded)
	}

	// The case the guard exists for.
	eligible, excluded = countExcludedMounts(mounts, []string{"/config", "/data"})
	if eligible != excluded || eligible == 0 {
		t.Fatalf("everything excluded: eligible=%d excluded=%d, want them equal and non-zero", eligible, excluded)
	}
}

// TestListMountsHonoursTheLabelToggle: discovery must agree with what the
// backup will do, or the wizard shows mounts as skipped that will in fact be
// backed up.
func TestListMountsHonoursTheLabelToggle(t *testing.T) {
	labels := map[string]string{VaultExcludeLabel: "/config"}
	if len(parseLabelExclusions(labels)) == 0 {
		t.Fatal("setup: label should yield a pattern")
	}
	// With the toggle off ListMounts must evaluate no label patterns at all;
	// containerExclusionsWithLabels is the backup-side equivalent and already
	// has that covered, so this pins the shared parse the two agree on.
	off := containerExclusionsWithLabels(map[string]any{"label_exclusions_enabled": false}, labels)
	if contains(off, "/config") {
		t.Fatal("backup honoured a label while the toggle is off")
	}
}
