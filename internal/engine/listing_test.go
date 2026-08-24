package engine

import "testing"

// TestPrevListingSet verifies the "prev_listing_paths" setting is parsed into a
// lookup set for the classic differential/incremental folder path (issue #320).
// Absent or nil settings produce nil so tarDirectoryFilteredWithPrev falls back
// to its mtime-only behaviour; an empty listing yields an empty (non-nil) set
// so a file added to a previously-empty source is still treated as new.
func TestPrevListingSet(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		settings map[string]any
		wantNil  bool
		wantLen  int
		wantKeys []string
	}{
		{
			name:     "absent setting returns nil",
			settings: map[string]any{},
			wantNil:  true,
		},
		{
			name:     "nil value returns nil",
			settings: map[string]any{"prev_listing_paths": nil},
			wantNil:  true,
		},
		{
			name:     "empty slice yields an empty (non-nil) set",
			settings: map[string]any{"prev_listing_paths": []string{}},
			wantLen:  0,
		},
		{
			name:     "string slice is parsed",
			settings: map[string]any{"prev_listing_paths": []string{"a.txt", "sub/b.txt"}},
			wantLen:  2,
			wantKeys: []string{"a.txt", "sub/b.txt"},
		},
		{
			name:     "any slice is parsed (JSON-decoded form)",
			settings: map[string]any{"prev_listing_paths": []any{"a.txt", "sub/b.txt"}},
			wantLen:  2,
			wantKeys: []string{"a.txt", "sub/b.txt"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := prevListingSet(tt.settings)
			if tt.wantNil {
				if got != nil {
					t.Fatalf("expected nil set, got %v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("expected non-nil set")
			}
			if len(got) != tt.wantLen {
				t.Errorf("set length = %d, want %d", len(got), tt.wantLen)
			}
			for _, k := range tt.wantKeys {
				if _, ok := got[k]; !ok {
					t.Errorf("expected key %q in set", k)
				}
			}
		})
	}
}

// TestPrevVolumeListingSet verifies the "prev_volume_listing_paths" setting is
// parsed into a per-volume lookup (mount source host path -> volume-relative
// path set) for the classic container differential/incremental path
// (issue #320). Absent/nil settings produce nil; both the typed map and the
// JSON-decoded map forms are accepted.
func TestPrevVolumeListingSet(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		settings map[string]any
		wantNil  bool
		wantSrc  string
		wantKeys []string
	}{
		{
			name:     "absent setting returns nil",
			settings: map[string]any{},
			wantNil:  true,
		},
		{
			name:     "nil value returns nil",
			settings: map[string]any{"prev_volume_listing_paths": nil},
			wantNil:  true,
		},
		{
			name: "typed string map is parsed",
			settings: map[string]any{
				"prev_volume_listing_paths": map[string][]string{
					"/mnt/cache/appdata/foo": {"a.txt", "sub/b.txt"},
				},
			},
			wantSrc:  "/mnt/cache/appdata/foo",
			wantKeys: []string{"a.txt", "sub/b.txt"},
		},
		{
			name: "json-decoded any map with string slices is parsed",
			settings: map[string]any{
				"prev_volume_listing_paths": map[string]any{
					"/mnt/cache/appdata/foo": []string{"a.txt"},
				},
			},
			wantSrc:  "/mnt/cache/appdata/foo",
			wantKeys: []string{"a.txt"},
		},
		{
			name: "json-decoded any map with any slices is parsed",
			settings: map[string]any{
				"prev_volume_listing_paths": map[string]any{
					"/mnt/cache/appdata/foo": []any{"a.txt", "sub/b.txt"},
				},
			},
			wantSrc:  "/mnt/cache/appdata/foo",
			wantKeys: []string{"a.txt", "sub/b.txt"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := prevVolumeListingSet(tt.settings)
			if tt.wantNil {
				if got != nil {
					t.Fatalf("expected nil map, got %v", got)
				}
				return
			}
			set, ok := got[tt.wantSrc]
			if !ok {
				t.Fatalf("expected key %q in map, got %v", tt.wantSrc, got)
			}
			if len(set) != len(tt.wantKeys) {
				t.Errorf("set length = %d, want %d", len(set), len(tt.wantKeys))
			}
			for _, k := range tt.wantKeys {
				if _, ok := set[k]; !ok {
					t.Errorf("expected key %q in set", k)
				}
			}
		})
	}
}
