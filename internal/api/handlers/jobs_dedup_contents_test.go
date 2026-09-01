package handlers

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/dedup"
	"github.com/ruaan-deysel/vault/internal/engine"
	"github.com/ruaan-deysel/vault/internal/storage"
)

// seedDedupDest creates a dedup-enabled local destination backed by a temp
// directory and returns the destination row.
func seedDedupDest(t *testing.T, d *db.DB) db.StorageDestination {
	t.Helper()
	cfg, _ := json.Marshal(map[string]string{"path": t.TempDir()})
	id, err := d.CreateStorageDestination(db.StorageDestination{
		Name:         "dedup-dest-" + nextUnique(),
		Type:         "local",
		Config:       string(cfg),
		DedupEnabled: true,
	})
	if err != nil {
		t.Fatalf("CreateStorageDestination: %v", err)
	}
	dest, err := d.GetStorageDestination(id)
	if err != nil {
		t.Fatalf("GetStorageDestination: %v", err)
	}
	return dest
}

// writeContainerManifest initialises a dedup repo on dest and stores a
// container manifest shaped exactly as BackupChunked writes one: synthetic
// metadata keys, one backed-up volume pointing at a sub-manifest, and one
// skipped volume carrying the -1 sentinel. Returns the manifest ID.
func writeContainerManifest(t *testing.T, d *db.DB, dest db.StorageDestination) dedup.ID {
	t.Helper()
	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		t.Fatalf("NewAdapter: %v", err)
	}
	defer storage.CloseAdapter(adapter)

	// Same key newJobHandlerDB hands the runner, so the handler can unseal it.
	repo, err := dedup.InitRepo(d, adapter, dest.ID, bytes.Repeat([]byte{0xab}, 32))
	if err != nil {
		t.Fatalf("InitRepo: %v", err)
	}
	subID, err := repo.PutManifest("plex-vol", dedup.Manifest{
		Version: 1,
		Item:    "plex-vol",
		Files: map[string]dedup.ManifestEntry{
			"settings.yml":  {Size: 120, Mode: 0o644},
			"logs/plex.log": {Size: 4096, Mode: 0o644},
			"Library":       {Size: 0, Mode: 0o755, IsDir: true},
		},
	})
	if err != nil {
		t.Fatalf("PutManifest sub: %v", err)
	}
	mID, err := repo.PutManifest("plex", dedup.Manifest{
		Version: 1,
		Item:    "plex",
		Files: map[string]dedup.ManifestEntry{
			"__inspect":                 {Size: 8192, Mode: 0o644},
			"__image_meta":              {Size: 256, Mode: 0o644},
			engine.ContainerDBDumpKey:   {Size: 4096, Mode: 0o644},
			engine.ContainerDBReplayKey: {},
			// Backed up: a one-chunk pointer at the sub-manifest above.
			"__vol__/config": {Size: 1024, Mode: 0o755, Chunks: []dedup.ID{subID}},
			// Excluded by the job: the -1 sentinel, no chunks to dereference.
			"__vol__/transcode": {Size: -1, Mode: 0o755},
		},
	})
	if err != nil {
		t.Fatalf("PutManifest item: %v", err)
	}
	if err := repo.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	return mID
}

// seedDedupRestorePoint wires a job, a container item, a run, and a restore
// point whose metadata maps the item to manifestHex.
func seedDedupRestorePoint(t *testing.T, d *db.DB, dest db.StorageDestination, itemName, itemType, manifestHex string) (jobID, rpID int64) {
	t.Helper()
	jobID, err := d.CreateJob(db.Job{Name: "dedup-browse-" + nextUnique(), StorageDestID: dest.ID})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if _, err := d.AddJobItem(db.JobItem{JobID: jobID, ItemType: itemType, ItemName: itemName}); err != nil {
		t.Fatalf("AddJobItem: %v", err)
	}
	runID, err := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "success"})
	if err != nil {
		t.Fatalf("CreateJobRun: %v", err)
	}
	meta, _ := json.Marshal(map[string]any{"item_manifests": map[string]string{itemName: manifestHex}})
	rpID, err = d.CreateRestorePoint(db.RestorePoint{
		JobRunID:    runID,
		JobID:       jobID,
		BackupType:  "full",
		StoragePath: "dedup-rp-" + nextUnique(),
		Metadata:    string(meta),
	})
	if err != nil {
		t.Fatalf("CreateRestorePoint: %v", err)
	}
	return jobID, rpID
}

func browseContents(t *testing.T, h *JobHandler, jobID, rpID int64, item string) *httptest.ResponseRecorder {
	t.Helper()
	url := fmt.Sprintf("/api/v1/jobs/%d/restore-points/%d/contents?item=%s", jobID, rpID, item)
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req = withURLParams(req, "id", strconv.FormatInt(jobID, 10), "rpid", strconv.FormatInt(rpID, 10))
	w := httptest.NewRecorder()
	h.RestorePointContents(w, req)
	return w
}

// TestRestorePointContents_DedupContainer drives the whole dedup browse path
// through the HTTP handler against a real repo: manifest resolution, the
// single-session open, synthetic-key filtering, the skipped-volume sentinel,
// and volume expansion into container-internal absolute paths (issue #333).
func TestRestorePointContents_DedupContainer(t *testing.T) {
	t.Parallel()
	h, d := newJobHandlerDB(t)
	dest := seedDedupDest(t, d)
	mID := writeContainerManifest(t, d, dest)
	jobID, rpID := seedDedupRestorePoint(t, d, dest, "plex", "container", hex.EncodeToString(mID[:]))

	w := browseContents(t, h, jobID, rpID, "plex")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var idx engine.TarIndex
	if err := json.Unmarshal(w.Body.Bytes(), &idx); err != nil {
		t.Fatalf("decode body: %v (%s)", err, w.Body.String())
	}

	got := make(map[string]int64, len(idx.Files))
	for _, f := range idx.Files {
		got[f.Path] = f.Size
	}
	want := map[string]int64{
		"/config/settings.yml":  120,
		"/config/logs/plex.log": 4096,
		"/config/Library":       0,
	}
	if len(got) != len(want) {
		t.Fatalf("files = %v, want %v", got, want)
	}
	for p, size := range want {
		if got[p] != size {
			t.Errorf("%s = %d, want %d", p, got[p], size)
		}
	}
	// The excluded volume must never be dereferenced, and no entry may carry
	// the sentinel size or a synthetic key.
	for _, f := range idx.Files {
		if f.Size < 0 {
			t.Errorf("%s has sentinel size %d", f.Path, f.Size)
		}
		if engine.IsSyntheticContainerKey(f.Path) {
			t.Errorf("synthetic key %q leaked into the picker", f.Path)
		}
	}
}

// TestRestorePointContents_DedupUnreadableManifest covers the failure the
// handler reports when the manifest ID recorded on the restore point is not in
// the repo — the dedup fetch error branch.
func TestRestorePointContents_DedupUnreadableManifest(t *testing.T) {
	t.Parallel()
	h, d := newJobHandlerDB(t)
	dest := seedDedupDest(t, d)
	writeContainerManifest(t, d, dest)

	var bogus dedup.ID
	for i := range bogus {
		bogus[i] = 0x7e
	}
	jobID, rpID := seedDedupRestorePoint(t, d, dest, "plex", "container", hex.EncodeToString(bogus[:]))

	w := browseContents(t, h, jobID, rpID, "plex")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", w.Code, w.Body.String())
	}
}

// TestRestorePointContents_ChainStepUsesDedupManifest covers the dedup branch
// of itemIndexForPoint: a classic increment whose parent step was written to a
// dedup repo. The merged listing must combine the parent's expanded volume
// files with the increment's own sidecar entries.
func TestRestorePointContents_ChainStepUsesDedupManifest(t *testing.T) {
	t.Parallel()
	h, d := newJobHandlerDB(t)

	storageRoot := t.TempDir()
	cfg, _ := json.Marshal(map[string]string{"path": storageRoot})
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name:         "dedup-chain-" + nextUnique(),
		Type:         "local",
		Config:       string(cfg),
		DedupEnabled: true,
	})
	if err != nil {
		t.Fatalf("CreateStorageDestination: %v", err)
	}
	dest, err := d.GetStorageDestination(destID)
	if err != nil {
		t.Fatalf("GetStorageDestination: %v", err)
	}
	mID := writeContainerManifest(t, d, dest)

	jobID, err := d.CreateJob(db.Job{Name: "dedup-chain-job-" + nextUnique(), StorageDestID: destID})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if _, err := d.AddJobItem(db.JobItem{JobID: jobID, ItemType: "container", ItemName: "plex"}); err != nil {
		t.Fatalf("AddJobItem: %v", err)
	}
	runID, err := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "success"})
	if err != nil {
		t.Fatalf("CreateJobRun: %v", err)
	}
	meta, _ := json.Marshal(map[string]any{
		"item_manifests": map[string]string{"plex": hex.EncodeToString(mID[:])},
	})
	fullID, err := d.CreateRestorePoint(db.RestorePoint{
		JobRunID: runID, JobID: jobID, BackupType: "full",
		StoragePath: "dedup-chain-full", Metadata: string(meta),
	})
	if err != nil {
		t.Fatalf("create full rp: %v", err)
	}
	incID, err := d.CreateRestorePoint(db.RestorePoint{
		JobRunID: runID, JobID: jobID, BackupType: "incremental",
		StoragePath: "dedup-chain-inc", Metadata: "{}",
		ParentRestorePointID: fullID,
	})
	if err != nil {
		t.Fatalf("create inc rp: %v", err)
	}
	writeContentsSidecar(t, storageRoot, "dedup-chain-inc", "plex", []engine.TarIndexEntry{
		{Path: "/config/settings.yml", Size: 200, Mode: "0644"},
		{Path: "/config/new.log", Size: 7, Mode: "0644"},
	})

	w := browseContents(t, h, jobID, incID, "plex")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var idx engine.TarIndex
	if err := json.Unmarshal(w.Body.Bytes(), &idx); err != nil {
		t.Fatalf("decode body: %v (%s)", err, w.Body.String())
	}
	got := make(map[string]int64, len(idx.Files))
	for _, f := range idx.Files {
		got[f.Path] = f.Size
	}
	// Parent-only files survive; the increment wins where both carry a path.
	want := map[string]int64{
		"/config/settings.yml":  200,
		"/config/new.log":       7,
		"/config/logs/plex.log": 4096,
		"/config/Library":       0,
	}
	if len(got) != len(want) {
		t.Fatalf("files = %v, want %v", got, want)
	}
	for p, size := range want {
		if got[p] != size {
			t.Errorf("%s = %d, want %d", p, got[p], size)
		}
	}
}

// TestJobHandler_ItemTypeDBError covers the lookup-failure branch: the item
// type is unresolvable, so the caller falls back to treating the manifest as a
// non-container one rather than failing the request.
func TestJobHandler_ItemTypeDBError(t *testing.T) {
	t.Parallel()
	h, d := newJobHandlerDB(t)
	jobID, err := d.CreateJob(db.Job{Name: "item-type-err-" + nextUnique()})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := h.itemType(jobID, "plex"); got != "" {
		t.Errorf("itemType on a failed lookup = %q, want \"\"", got)
	}
}

// TestRestorePointContents_ChainStepDedupRepoUnavailable covers the branch
// where a chain step names a dedup manifest but the repo cannot be opened —
// the picker must fail closed rather than silently browse a partial chain.
func TestRestorePointContents_ChainStepDedupRepoUnavailable(t *testing.T) {
	t.Parallel()
	h, d := newJobHandlerDB(t)

	storageRoot := t.TempDir()
	cfg, _ := json.Marshal(map[string]string{"path": storageRoot})
	// Dedup-enabled, but no repo was ever initialised at this destination.
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name:         "dedup-noreop-" + nextUnique(),
		Type:         "local",
		Config:       string(cfg),
		DedupEnabled: true,
	})
	if err != nil {
		t.Fatalf("CreateStorageDestination: %v", err)
	}
	jobID, err := d.CreateJob(db.Job{Name: "dedup-norepo-job-" + nextUnique(), StorageDestID: destID})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	runID, err := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "success"})
	if err != nil {
		t.Fatalf("CreateJobRun: %v", err)
	}
	var mID dedup.ID
	for i := range mID {
		mID[i] = 0x11
	}
	meta, _ := json.Marshal(map[string]any{
		"item_manifests": map[string]string{"plex": hex.EncodeToString(mID[:])},
	})
	fullID, err := d.CreateRestorePoint(db.RestorePoint{
		JobRunID: runID, JobID: jobID, BackupType: "full",
		StoragePath: "dedup-norepo-full", Metadata: string(meta),
	})
	if err != nil {
		t.Fatalf("create full rp: %v", err)
	}
	incID, err := d.CreateRestorePoint(db.RestorePoint{
		JobRunID: runID, JobID: jobID, BackupType: "incremental",
		StoragePath: "dedup-norepo-inc", Metadata: "{}",
		ParentRestorePointID: fullID,
	})
	if err != nil {
		t.Fatalf("create inc rp: %v", err)
	}

	w := browseContents(t, h, jobID, incID, "plex")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}
