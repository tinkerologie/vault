package db

import "testing"

func TestRestorePointItemSizes(t *testing.T) {
	tests := []struct {
		name     string
		metadata string
		want     map[string]int64
		wantOK   bool
	}{
		{
			name:     "classic backup records per-item sizes",
			metadata: `{"item_sizes":{"plex":1024,"sonarr":2048}}`,
			want:     map[string]int64{"plex": 1024, "sonarr": 2048},
			wantOK:   true,
		},
		{
			// Dedup points record item_sizes alongside item_manifests, from
			// the chunked path's logical byte total.
			name:     "dedup point records both sizes and manifests",
			metadata: `{"item_sizes":{"plex":4096},"item_manifests":{"plex":"ab12"}}`,
			want:     map[string]int64{"plex": 4096},
			wantOK:   true,
		},
		{
			// A point with membership but no sizes predates this metadata;
			// the caller must be able to tell "unknown" from "zero".
			name:     "manifests without sizes",
			metadata: `{"item_manifests":{"plex":"ab12"}}`,
			wantOK:   false,
		},
		{
			name:     "legacy point with no metadata",
			metadata: ``,
			wantOK:   false,
		},
		{
			name:     "empty item_sizes map",
			metadata: `{"item_sizes":{}}`,
			wantOK:   false,
		},
		{
			name:     "malformed metadata",
			metadata: `{not json`,
			wantOK:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rp := RestorePoint{Metadata: tc.metadata}
			got, ok := rp.ItemSizes()
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if len(got) != len(tc.want) {
				t.Fatalf("sizes = %v, want %v", got, tc.want)
			}
			for name, size := range tc.want {
				if got[name] != size {
					t.Errorf("%s = %d, want %d", name, got[name], size)
				}
			}
		})
	}
}
