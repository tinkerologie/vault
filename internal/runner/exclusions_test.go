package runner

import (
	"reflect"
	"testing"
)

// TestMergeExclusions covers the contract of the global list: it is a floor
// added to each item's own exclusions, never a replacement (issue #257).
func TestMergeExclusions(t *testing.T) {
	cases := []struct {
		name   string
		item   any
		global []string
		want   []string
	}{
		{
			name:   "global applies to an item with none of its own",
			item:   nil,
			global: []string{"/tmp", "*.sock"},
			want:   []string{"/tmp", "*.sock"},
		},
		{
			name:   "item keeps its own and gains the global",
			item:   []any{"/config/cache"},
			global: []string{"/tmp"},
			want:   []string{"/config/cache", "/tmp"},
		},
		{
			name:   "a path in both lists is not duplicated",
			item:   []any{"/tmp", "/data"},
			global: []string{"/tmp"},
			want:   []string{"/tmp", "/data"},
		},
		{
			name:   "no global leaves the item untouched",
			item:   []any{"/config"},
			global: nil,
			want:   []string{"/config"},
		},
		{
			name:   "already-normalised item settings are accepted",
			item:   []string{"/a"},
			global: []string{"/b"},
			want:   []string{"/a", "/b"},
		},
		{
			name:   "blank entries are dropped rather than matching everything",
			item:   []any{"", "/a"},
			global: []string{"", "/b"},
			want:   []string{"/a", "/b"},
		},
		{
			name:   "non-string entries in the JSON blob are ignored",
			item:   []any{"/a", 42, nil},
			global: []string{"/b"},
			want:   []string{"/a", "/b"},
		},
		{
			name:   "nothing anywhere yields nothing",
			item:   nil,
			global: nil,
			want:   nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeExclusions(tc.item, tc.global)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// TestMergeExclusionsPutsItemPathsFirst pins ordering: the more specific list
// leads, which is what an operator reading a log or the UI expects.
func TestMergeExclusionsPutsItemPathsFirst(t *testing.T) {
	got := mergeExclusions([]any{"/item"}, []string{"/global"})
	if len(got) != 2 || got[0] != "/item" {
		t.Fatalf("got %v, want the item's own path first", got)
	}
}
