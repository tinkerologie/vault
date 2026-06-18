package db

import "testing"

func TestRestorePointBackedUpItems(t *testing.T) {
	tests := []struct {
		name      string
		metadata  string
		wantItems []string
		wantKnown bool
	}{
		{
			name:      "item_sizes lists membership",
			metadata:  `{"item_sizes":{"plex":100,"sonarr":200}}`,
			wantItems: []string{"plex", "sonarr"},
			wantKnown: true,
		},
		{
			name:      "item_manifests lists membership for dedup",
			metadata:  `{"item_manifests":{"radarr":"ab12","seerr":"cd34"}}`,
			wantItems: []string{"radarr", "seerr"},
			wantKnown: true,
		},
		{
			name:      "union of item_sizes and item_manifests",
			metadata:  `{"item_sizes":{"plex":1},"item_manifests":{"radarr":"ab"}}`,
			wantItems: []string{"plex", "radarr"},
			wantKnown: true,
		},
		{
			name:      "item in both fields is counted once",
			metadata:  `{"item_sizes":{"plex":1},"item_manifests":{"plex":"ab","radarr":"cd"}}`,
			wantItems: []string{"plex", "radarr"},
			wantKnown: true,
		},
		{
			name:      "legacy restore point with no item metadata is unknown",
			metadata:  `{"size_bytes":1234}`,
			wantItems: nil,
			wantKnown: false,
		},
		{
			name:      "empty metadata is unknown",
			metadata:  ``,
			wantItems: nil,
			wantKnown: false,
		},
		{
			name:      "malformed metadata is unknown, not a panic",
			metadata:  `{not json`,
			wantItems: nil,
			wantKnown: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rp := RestorePoint{Metadata: tt.metadata}
			got, known := rp.BackedUpItems()
			if known != tt.wantKnown {
				t.Fatalf("known = %v, want %v", known, tt.wantKnown)
			}
			if len(got) != len(tt.wantItems) {
				t.Fatalf("got %d items %v, want %d %v", len(got), keys(got), len(tt.wantItems), tt.wantItems)
			}
			for _, name := range tt.wantItems {
				if _, ok := got[name]; !ok {
					t.Errorf("missing item %q in %v", name, keys(got))
				}
			}
		})
	}
}

func keys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
