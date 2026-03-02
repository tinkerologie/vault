package runner

import (
	"encoding/json"
	"testing"

	"github.com/ruaandeysel/vault/internal/db"
)

func TestStructuredDetails(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input any
		check func(t *testing.T, result string)
	}{
		{
			name:  "simple map",
			input: map[string]any{"job_id": 1, "run_id": 2},
			check: func(t *testing.T, result string) {
				t.Helper()
				var m map[string]any
				if err := json.Unmarshal([]byte(result), &m); err != nil {
					t.Fatalf("not valid JSON: %v", err)
				}
				if m["job_id"] != float64(1) {
					t.Errorf("job_id = %v, want 1", m["job_id"])
				}
				if m["run_id"] != float64(2) {
					t.Errorf("run_id = %v, want 2", m["run_id"])
				}
			},
		},
		{
			name:  "slice of maps",
			input: []map[string]any{{"name": "nginx", "status": "ok"}},
			check: func(t *testing.T, result string) {
				t.Helper()
				var items []map[string]any
				if err := json.Unmarshal([]byte(result), &items); err != nil {
					t.Fatalf("not valid JSON: %v", err)
				}
				if len(items) != 1 {
					t.Fatalf("got %d items, want 1", len(items))
				}
				if items[0]["name"] != "nginx" {
					t.Errorf("name = %v, want nginx", items[0]["name"])
				}
			},
		},
		{
			name:  "string input",
			input: "plain text",
			check: func(t *testing.T, result string) {
				t.Helper()
				if result != `"plain text"` {
					t.Errorf("got %q, want %q", result, `"plain text"`)
				}
			},
		},
		{
			name:  "nil input",
			input: nil,
			check: func(t *testing.T, result string) {
				t.Helper()
				if result != "null" {
					t.Errorf("got %q, want %q", result, "null")
				}
			},
		},
		{
			name: "nested with failed items",
			input: map[string]any{
				"run_id":           5,
				"done":             3,
				"failed":           1,
				"size_bytes":       int64(191813253),
				"duration_seconds": 45,
				"failed_items":     []string{"nginx"},
			},
			check: func(t *testing.T, result string) {
				t.Helper()
				var m map[string]any
				if err := json.Unmarshal([]byte(result), &m); err != nil {
					t.Fatalf("not valid JSON: %v", err)
				}
				if m["failed"] != float64(1) {
					t.Errorf("failed = %v, want 1", m["failed"])
				}
				items, ok := m["failed_items"].([]any)
				if !ok || len(items) != 1 {
					t.Errorf("failed_items = %v, want [nginx]", m["failed_items"])
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := structuredDetails(tt.input)
			tt.check(t, result)
		})
	}
}

func TestJobItemNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		items []db.JobItem
		want  []string
	}{
		{
			name:  "nil items",
			items: nil,
			want:  []string{},
		},
		{
			name:  "empty slice",
			items: []db.JobItem{},
			want:  []string{},
		},
		{
			name: "single item",
			items: []db.JobItem{
				{ItemName: "nginx"},
			},
			want: []string{"nginx"},
		},
		{
			name: "multiple items preserves order",
			items: []db.JobItem{
				{ItemName: "nginx"},
				{ItemName: "postgres"},
				{ItemName: "redis"},
			},
			want: []string{"nginx", "postgres", "redis"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := jobItemNames(tt.items)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d names, want %d", len(got), len(tt.want))
			}
			for i, name := range got {
				if name != tt.want[i] {
					t.Errorf("name[%d] = %q, want %q", i, name, tt.want[i])
				}
			}
		})
	}
}
