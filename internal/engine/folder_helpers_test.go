package engine

import (
	"reflect"
	"testing"
)

// extractRestoreFilePaths normalises a job's restore_file_paths setting,
// accepting both Go-typed []string (used by direct callers) and the
// JSON-decoded []any (the shape the API decodes settings into). Empty
// strings inside []any are dropped; nil/missing yields nil so callers
// can treat the result as "no filter applied".
func TestExtractRestoreFilePaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		settings map[string]any
		want     []string
	}{
		{
			name:     "nil settings returns nil",
			settings: nil,
			want:     nil,
		},
		{
			name:     "absent key returns nil",
			settings: map[string]any{},
			want:     nil,
		},
		{
			name:     "explicit nil value returns nil",
			settings: map[string]any{"restore_file_paths": nil},
			want:     nil,
		},
		{
			name:     "typed string slice passes through",
			settings: map[string]any{"restore_file_paths": []string{"a/b", "c"}},
			want:     []string{"a/b", "c"},
		},
		{
			name:     "json-decoded any slice converts and drops empties",
			settings: map[string]any{"restore_file_paths": []any{"a", "", "b", 42, "c"}},
			want:     []string{"a", "b", "c"},
		},
		{
			name:     "wrong type returns nil",
			settings: map[string]any{"restore_file_paths": "single string"},
			want:     nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := extractRestoreFilePaths(tc.settings)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("extractRestoreFilePaths(%v) = %v, want %v", tc.settings, got, tc.want)
			}
		})
	}
}

// extractExcludePaths mirrors extractRestoreFilePaths for the exclude_paths
// setting. Same shape rules — accept both []string and []any, drop empties,
// return nil when missing.
func TestExtractExcludePaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		settings map[string]any
		want     []string
	}{
		{
			name:     "nil settings returns nil",
			settings: nil,
			want:     nil,
		},
		{
			name:     "absent key returns nil",
			settings: map[string]any{},
			want:     nil,
		},
		{
			name:     "explicit nil value returns nil",
			settings: map[string]any{"exclude_paths": nil},
			want:     nil,
		},
		{
			name:     "typed string slice passes through",
			settings: map[string]any{"exclude_paths": []string{"*.log", "node_modules"}},
			want:     []string{"*.log", "node_modules"},
		},
		{
			name:     "json-decoded any slice converts and drops empties",
			settings: map[string]any{"exclude_paths": []any{"*.tmp", "", "cache", true}},
			want:     []string{"*.tmp", "cache"},
		},
		{
			name:     "wrong scalar type returns nil",
			settings: map[string]any{"exclude_paths": 1234},
			want:     nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := extractExcludePaths(tc.settings)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("extractExcludePaths(%v) = %v, want %v", tc.settings, got, tc.want)
			}
		})
	}
}
