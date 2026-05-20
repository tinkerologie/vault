package runner

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/ws"
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

// TestRunnerDedupBackupRoundTrip exercises the runner's dedup branch end to
// end: backup a folder to a dedup-enabled local destination, assert the
// resulting restore_point has manifest_id persisted, restore to a fresh
// directory, and verify file contents match. This is the contract Task 11
// is responsible for — backup + restore both routed through the chunked
// path when dedup is on, with the classic tar path untouched.
func TestRunnerDedupBackupRoundTrip(t *testing.T) {
	storageDir := t.TempDir()
	sourceDir := t.TempDir()
	const fileContent = "hello dedup, this is content that will be chunked"
	if err := os.WriteFile(filepath.Join(sourceDir, "x.txt"), []byte(fileContent), 0o644); err != nil {
		t.Fatalf("write source file: %v", err)
	}

	dbPath := filepath.Join(t.TempDir(), "vault.db")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	defer d.Close()

	serverKey := bytes.Repeat([]byte{0xee}, 32)
	hub := ws.NewHub()
	go hub.Run()
	r := New(d, hub, serverKey)

	destCfg, _ := json.Marshal(map[string]string{"path": storageDir})
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name:         "dedup-test",
		Type:         "local",
		Config:       string(destCfg),
		DedupEnabled: true,
	})
	if err != nil {
		t.Fatalf("create storage destination: %v", err)
	}

	jobID, err := d.CreateJob(db.Job{
		Name:            "dedup-folder-job",
		StorageDestID:   destID,
		BackupTypeChain: "full",
		Enabled:         true,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	itemSettings, _ := json.Marshal(map[string]any{"path": sourceDir})
	if _, err := d.AddJobItem(db.JobItem{
		JobID:    jobID,
		ItemType: "folder",
		ItemName: "src",
		Settings: string(itemSettings),
	}); err != nil {
		t.Fatalf("add job item: %v", err)
	}

	// RunJob is synchronous (holds r.mu and runs the full pipeline inline).
	r.RunJob(jobID)

	rps, err := d.ListRestorePoints(jobID)
	if err != nil {
		t.Fatalf("list restore points: %v", err)
	}
	if len(rps) != 1 {
		t.Fatalf("expected 1 restore point, got %d (job runs may have failed; check test logs)", len(rps))
	}
	rp := rps[0]
	if len(rp.ManifestID) != 32 {
		t.Fatalf("manifest_id not persisted on restore point %d: got %d bytes (%v)", rp.ID, len(rp.ManifestID), rp.ManifestID)
	}

	// Verify the metadata also carries item_manifests so multi-item jobs
	// can resolve manifests per item.
	var meta map[string]any
	if err := json.Unmarshal([]byte(rp.Metadata), &meta); err != nil {
		t.Fatalf("decode rp metadata: %v", err)
	}
	itemManifests, ok := meta["item_manifests"].(map[string]any)
	if !ok {
		t.Fatalf("rp.metadata.item_manifests missing or wrong type: %v", meta["item_manifests"])
	}
	if hexID, ok := itemManifests["src"].(string); !ok || hexID == "" {
		t.Fatalf("item_manifests[\"src\"] missing or empty: %v", itemManifests["src"])
	}

	// Restore to a fresh directory and verify file contents match the source.
	restoreDir := t.TempDir()
	if err := r.RestoreItem(rp, "src", "folder", restoreDir, ""); err != nil {
		t.Fatalf("restore item: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(restoreDir, "x.txt"))
	if err != nil {
		t.Fatalf("read restored file: %v", err)
	}
	if string(got) != fileContent {
		t.Fatalf("restored content mismatch: got %q, want %q", got, fileContent)
	}

	// Sanity: the dedup repo header must exist on the destination so
	// subsequent backups Open (rather than Init) the same repo.
	if _, err := os.Stat(filepath.Join(storageDir, "_vault", "repo.json")); err != nil {
		t.Fatalf("expected _vault/repo.json on dest: %v", err)
	}
}

// TestRunnerOrphanScanRejectsDedupDestination ensures the orphan GC paths
// return a helpful error for dedup destinations, directing callers to the
// chunk-store GC endpoint instead. Silently skipping would hide the fact
// that orphan deletion is the wrong tool for dedup destinations.
func TestRunnerOrphanScanRejectsDedupDestination(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "vault.db")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	defer d.Close()

	r := New(d, nil, nil)
	dest := db.StorageDestination{ID: 7, Name: "d", Type: "local", DedupEnabled: true}

	if _, _, err := r.ScanStorageOrphans(dest); err == nil {
		t.Fatalf("ScanStorageOrphans on dedup dest: expected error, got nil")
	} else if !strings.Contains(err.Error(), "chunk-store GC") {
		t.Fatalf("ScanStorageOrphans on dedup dest: error %q missing 'chunk-store GC' guidance", err)
	}

	if _, errs := r.DeleteStorageOrphans(dest, []string{"x"}); len(errs) == 0 {
		t.Fatal("DeleteStorageOrphans on dedup dest: expected at least one error message")
	} else if !strings.Contains(errs[0], "chunk-store GC") {
		t.Fatalf("DeleteStorageOrphans on dedup dest: message %q missing 'chunk-store GC' guidance", errs[0])
	}
}
