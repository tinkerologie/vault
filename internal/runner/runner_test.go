package runner

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/ws"
)

// fakeEnqueuer records every RunID enqueued for anomaly evaluation.
// Satisfies the unexported anomalyEnqueuer interface. The runner calls
// EnqueueRun synchronously, so EnqueueRun returns immediately and the
// recorded ids are observable as soon as RunJob returns.
type fakeEnqueuer struct {
	mu  sync.Mutex
	ids []int64
}

func (f *fakeEnqueuer) EnqueueRun(runID int64) {
	f.mu.Lock()
	f.ids = append(f.ids, runID)
	f.mu.Unlock()
}

func (f *fakeEnqueuer) enqueuedIDs() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]int64, len(f.ids))
	copy(cp, f.ids)
	return cp
}

// boundedEnqueuer mimics the real *anomaly.Evaluator.EnqueueRun contract: a
// buffered channel with drop-oldest semantics on full, so EnqueueRun never
// blocks the caller. The runner relies on exactly this contract when it calls
// EnqueueRun directly (synchronously) on its own goroutine.
type boundedEnqueuer struct {
	ch chan int64
}

func newBoundedEnqueuer() *boundedEnqueuer {
	return &boundedEnqueuer{ch: make(chan int64, 64)}
}

func (b *boundedEnqueuer) EnqueueRun(runID int64) {
	select {
	case b.ch <- runID:
		// Fast path: room in the buffer.
	default:
		// Full — drop the oldest, then enqueue the new id. Never blocks.
		select {
		case <-b.ch:
		default:
		}
		select {
		case b.ch <- runID:
		default:
		}
	}
}

func (b *boundedEnqueuer) drain() []int64 {
	var out []int64
	for {
		select {
		case id := <-b.ch:
			out = append(out, id)
		default:
			return out
		}
	}
}

// newSuccessJob creates a Job + one folder item pointing at a real directory.
// RunJob on this job should always complete successfully.
func newSuccessJob(t *testing.T, d *db.DB, storageDir, sourceDir string) int64 {
	t.Helper()
	destCfg, _ := json.Marshal(map[string]string{"path": storageDir})
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name: "test-local", Type: "local", Config: string(destCfg),
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}
	jobID, err := d.CreateJob(db.Job{
		Name: "test-job", StorageDestID: destID,
		BackupTypeChain: "full", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	itemSettings, _ := json.Marshal(map[string]any{"path": sourceDir})
	if _, err := d.AddJobItem(db.JobItem{
		JobID: jobID, ItemType: "folder", ItemName: "src",
		Settings: string(itemSettings),
	}); err != nil {
		t.Fatalf("add item: %v", err)
	}
	return jobID
}

// newFailJob creates a Job + one folder item whose "path" setting is empty so
// the engine rejects it ("folder path not specified in settings"). An empty
// path returns StatusUnknown from stale detection, so the item is NOT skipped
// by the stale filter and reaches the engine, producing status="failed".
func newFailJob(t *testing.T, d *db.DB, storageDir string) int64 {
	t.Helper()
	destCfg, _ := json.Marshal(map[string]string{"path": storageDir})
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name: "test-local-fail", Type: "local", Config: string(destCfg),
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}
	jobID, err := d.CreateJob(db.Job{
		Name: "test-fail-job", StorageDestID: destID,
		BackupTypeChain: "full", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	// Empty path → engine fails with "folder path not specified" (item-level
	// failure, not a stale skip, because StatusUnknown is never skipped).
	itemSettings, _ := json.Marshal(map[string]any{"path": ""})
	if _, err := d.AddJobItem(db.JobItem{
		JobID: jobID, ItemType: "folder", ItemName: "no-path",
		Settings: string(itemSettings),
	}); err != nil {
		t.Fatalf("add item: %v", err)
	}
	return jobID
}

// TestEvaluatorEnqueuedOnSuccess verifies that SetEvaluator + RunJob calls
// EnqueueRun exactly once with the persisted run ID on the success path.
func TestEvaluatorEnqueuedOnSuccess(t *testing.T) {
	storageDir := t.TempDir()
	sourceDir := t.TempDir()
	// Write a real file so the folder backup has content to archive.
	if err := os.WriteFile(filepath.Join(sourceDir, "data.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	r, d := newTestRunner(t)
	fake := &fakeEnqueuer{}
	r.SetEvaluator(fake)

	jobID := newSuccessJob(t, d, storageDir, sourceDir)
	r.RunJob(jobID) // synchronous; EnqueueRun is called inline before it returns

	ids := fake.enqueuedIDs()
	if len(ids) != 1 {
		t.Fatalf("expected 1 enqueued run, got %d (%v)", len(ids), ids)
	}

	// Confirm the enqueued ID matches the persisted run record.
	runs, err := d.GetJobRuns(jobID, 5)
	if err != nil || len(runs) == 0 {
		t.Fatalf("no job runs found: err=%v", err)
	}
	if ids[0] != runs[0].ID {
		t.Errorf("enqueued run ID %d != persisted run ID %d", ids[0], runs[0].ID)
	}
}

// TestEvaluatorEnqueuedOnFailure verifies that EnqueueRun is called even when
// a job fails (all items fail, status="failed"). The reliability detector
// feeds off failure runs, so we must not skip them.
func TestEvaluatorEnqueuedOnFailure(t *testing.T) {
	storageDir := t.TempDir()

	r, d := newTestRunner(t)
	fake := &fakeEnqueuer{}
	r.SetEvaluator(fake)

	jobID := newFailJob(t, d, storageDir)
	r.RunJob(jobID)

	ids := fake.enqueuedIDs()
	if len(ids) != 1 {
		t.Fatalf("expected 1 enqueued run on failure, got %d (%v)", len(ids), ids)
	}

	runs, err := d.GetJobRuns(jobID, 5)
	if err != nil || len(runs) == 0 {
		t.Fatalf("no job runs found: err=%v", err)
	}
	if ids[0] != runs[0].ID {
		t.Errorf("enqueued run ID %d != persisted run ID %d", ids[0], runs[0].ID)
	}
	if runs[0].Status != "failed" {
		t.Errorf("expected status=failed, got %q", runs[0].Status)
	}
}

// TestEvaluatorEnqueueNonBlocking injects a boundedEnqueuer that mimics the
// real *anomaly.Evaluator.EnqueueRun contract (buffered channel, drop-oldest
// on full, never blocks). The runner calls EnqueueRun directly (synchronously)
// and relies on that contract. This test asserts the run completes well within
// a deadline AND that the enqueue actually happened — i.e. the contract the
// runner depends on holds.
func TestEvaluatorEnqueueNonBlocking(t *testing.T) {
	storageDir := t.TempDir()
	sourceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDir, "data.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	r, d := newTestRunner(t)
	bounded := newBoundedEnqueuer()
	r.SetEvaluator(bounded)

	jobID := newSuccessJob(t, d, storageDir, sourceDir)

	done := make(chan struct{})
	go func() {
		r.RunJob(jobID)
		close(done)
	}()

	select {
	case <-done:
		// Runner returned promptly — the non-blocking enqueue contract holds.
	case <-time.After(5 * time.Second):
		t.Fatal("RunJob did not return within 5s — EnqueueRun appears to be blocking the runner")
	}

	// The run's id must have been enqueued via the non-blocking path.
	ids := bounded.drain()
	if len(ids) != 1 {
		t.Fatalf("expected 1 enqueued run, got %d (%v)", len(ids), ids)
	}
	runs, err := d.GetJobRuns(jobID, 5)
	if err != nil || len(runs) == 0 {
		t.Fatalf("no job runs found: err=%v", err)
	}
	if ids[0] != runs[0].ID {
		t.Errorf("enqueued run ID %d != persisted run ID %d", ids[0], runs[0].ID)
	}
}

// TestEvaluatorNilSafe confirms that runners without a configured evaluator
// (evaluator == nil) behave identically to pre-feature runners. No panic,
// no behavioral change.
func TestEvaluatorNilSafe(t *testing.T) {
	storageDir := t.TempDir()
	sourceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDir, "data.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	r, d := newTestRunner(t)
	// Intentionally do NOT call SetEvaluator — evaluator stays nil.

	jobID := newSuccessJob(t, d, storageDir, sourceDir)
	r.RunJob(jobID) // must not panic

	runs, err := d.GetJobRuns(jobID, 5)
	if err != nil || len(runs) == 0 {
		t.Fatalf("no job runs found: err=%v", err)
	}
	if runs[0].Status != "completed" {
		t.Errorf("expected status=completed, got %q", runs[0].Status)
	}
}

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
