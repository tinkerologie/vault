package engine

import (
	"reflect"
	"testing"
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
