package runner

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/net/webdav"

	"github.com/ruaan-deysel/vault/internal/db"
)

// TestRunJob_ClassicFolderBackup drives RunJob through the non-dedup
// classic-tar pipeline for a folder item. This exercises the bulk of
// runJobInternal: queue management, broadcast, item loop, classic
// stageItemLocally + uploadStagedFiles, restore point creation, and
// run finalisation.
func TestRunJob_ClassicFolderBackup(t *testing.T) {
	t.Parallel()
	r, d := newTestRunner(t)

	// Source folder with one file.
	sourceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDir, "a.txt"), []byte("classic content"), 0o644); err != nil {
		t.Fatalf("setup source: %v", err)
	}

	storageDir := filepath.Join(t.TempDir(), "store")
	if err := os.MkdirAll(storageDir, 0o755); err != nil {
		t.Fatalf("mkdir storage: %v", err)
	}
	destCfg, _ := json.Marshal(map[string]string{"path": storageDir})
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name:   "classic-" + nextUniqueRunner(t),
		Type:   "local",
		Config: string(destCfg),
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}

	jobID, err := d.CreateJob(db.Job{
		Name:            "classic-folder-" + nextUniqueRunner(t),
		StorageDestID:   destID,
		BackupTypeChain: "full",
		Enabled:         true,
		Compression:     "none",
		Encryption:      "none",
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	itemSettings, _ := json.Marshal(map[string]any{"path": sourceDir})
	if _, err := d.AddJobItem(db.JobItem{
		JobID:    jobID,
		ItemType: "folder",
		ItemName: "src-classic",
		Settings: string(itemSettings),
	}); err != nil {
		t.Fatalf("add job item: %v", err)
	}

	// RunJob is synchronous.
	r.RunJob(jobID)

	rps, err := d.ListRestorePoints(jobID)
	if err != nil {
		t.Fatalf("list restore points: %v", err)
	}
	if len(rps) != 1 {
		t.Fatalf("expected 1 restore point, got %d", len(rps))
	}
	if rps[0].StoragePath == "" {
		t.Errorf("restore point storage path is empty")
	}

	runs, err := d.GetJobRuns(jobID, 1)
	if err != nil || len(runs) == 0 {
		t.Fatalf("get job runs: %v", err)
	}
	var res []map[string]any
	if err := json.Unmarshal([]byte(runs[0].Log), &res); err != nil {
		t.Fatalf("unmarshal run log: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("expected 1 item result, got %d", len(res))
	}
	if res[0]["type"] != "folder" || res[0]["name"] != "src-classic" || res[0]["status"] != "ok" {
		t.Errorf("unexpected item result: %+v", res[0])
	}
}

// TestRunJob_ItemFailure verifies that failed items record their type,
// item_id, settings, and error in run.Log.
func TestRunJob_ItemFailure(t *testing.T) {
	t.Parallel()
	r, d := newTestRunner(t)

	destCfg, _ := json.Marshal(map[string]string{"path": t.TempDir()})
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name:   "fail-dest-" + nextUniqueRunner(t),
		Type:   "local",
		Config: string(destCfg),
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}

	jobID, err := d.CreateJob(db.Job{
		Name:            "fail-job-" + nextUniqueRunner(t),
		StorageDestID:   destID,
		BackupTypeChain: "full",
		Enabled:         true,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	itemSettings, _ := json.Marshal(map[string]any{"path": ""})
	if _, err := d.AddJobItem(db.JobItem{
		JobID:    jobID,
		ItemType: "folder",
		ItemName: "fail-item",
		ItemID:   "fail-item-id",
		Settings: string(itemSettings),
	}); err != nil {
		t.Fatalf("add job item: %v", err)
	}

	r.RunJob(jobID)

	runs, err := d.GetJobRuns(jobID, 1)
	if err != nil || len(runs) == 0 {
		t.Fatalf("get job runs: %v", err)
	}
	var res []map[string]any
	if err := json.Unmarshal([]byte(runs[0].Log), &res); err != nil {
		t.Fatalf("unmarshal run log: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("expected 1 item result, got %d", len(res))
	}
	if res[0]["type"] != "folder" || res[0]["status"] != "failed" || res[0]["error"] == nil ||
		res[0]["item_id"] != "fail-item-id" || res[0]["settings"] != string(itemSettings) {
		t.Errorf("unexpected failed item result: %+v", res[0])
	}
}

// TestRunJob_DeferredStagingAndUploadPaths verifies deferred upload staging failure,
// deferred upload success, and deferred upload failure branches record item metadata.
func TestRunJob_DeferredStagingAndUploadPaths(t *testing.T) {
	t.Parallel()
	r, d := newTestRunner(t)

	wdRoot := t.TempDir()
	server := httptest.NewServer(&webdav.Handler{
		FileSystem: webdav.Dir(wdRoot),
		LockSystem: webdav.NewMemLS(),
	})
	defer server.Close()

	wdCfg, _ := json.Marshal(map[string]any{
		"url": server.URL,
	})
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name:   "webdav-deferred-" + nextUniqueRunner(t),
		Type:   "webdav",
		Config: string(wdCfg),
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}

	// 1. Deferred staging failure (empty folder path)
	jobID, err := d.CreateJob(db.Job{
		Name:               "deferred-fail-job-" + nextUniqueRunner(t),
		StorageDestID:      destID,
		BackupTypeChain:    "full",
		DeferRemoteUpload:  true,
		MaxParallelUploads: 2,
		Enabled:            true,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	itemSettings, _ := json.Marshal(map[string]any{"path": ""})
	if _, err := d.AddJobItem(db.JobItem{
		JobID:    jobID,
		ItemType: "folder",
		ItemName: "staging-fail-item",
		ItemID:   "staging-fail-id",
		Settings: string(itemSettings),
	}); err != nil {
		t.Fatalf("add job item: %v", err)
	}

	r.RunJob(jobID)

	runs, err := d.GetJobRuns(jobID, 1)
	if err != nil || len(runs) == 0 {
		t.Fatalf("get job runs: %v", err)
	}
	var res []map[string]any
	if err := json.Unmarshal([]byte(runs[0].Log), &res); err != nil {
		t.Fatalf("unmarshal run log: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("expected 1 item result, got %d", len(res))
	}
	if res[0]["type"] != "folder" || res[0]["status"] != "failed" ||
		res[0]["item_id"] != "staging-fail-id" || res[0]["settings"] != string(itemSettings) {
		t.Errorf("unexpected staging fail result: %+v", res[0])
	}

	// 2. Deferred upload success (staging succeeds, concurrent upload to WebDAV succeeds)
	sourceDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(sourceDir, "file.txt"), []byte("data"), 0o644)

	jobID2, err := d.CreateJob(db.Job{
		Name:               "deferred-upload-ok-" + nextUniqueRunner(t),
		StorageDestID:      destID,
		BackupTypeChain:    "full",
		DeferRemoteUpload:  true,
		MaxParallelUploads: 2,
		Enabled:            true,
	})
	if err != nil {
		t.Fatalf("create job2: %v", err)
	}

	itemSettings2, _ := json.Marshal(map[string]any{"path": sourceDir})
	if _, err := d.AddJobItem(db.JobItem{
		JobID:    jobID2,
		ItemType: "folder",
		ItemName: "upload-ok-item",
		ItemID:   "upload-ok-id",
		Settings: string(itemSettings2),
	}); err != nil {
		t.Fatalf("add job item2: %v", err)
	}

	r.RunJob(jobID2)

	runs2, err := d.GetJobRuns(jobID2, 1)
	if err != nil || len(runs2) == 0 {
		t.Fatalf("get job runs 2: %v", err)
	}
	var res2 []map[string]any
	if err := json.Unmarshal([]byte(runs2[0].Log), &res2); err != nil {
		t.Fatalf("unmarshal run log 2: %v", err)
	}
	if len(res2) != 1 {
		t.Fatalf("expected 1 item result, got %d", len(res2))
	}
	if res2[0]["type"] != "folder" || res2[0]["status"] != "ok" ||
		res2[0]["item_id"] != "upload-ok-id" || res2[0]["settings"] != string(itemSettings2) {
		t.Errorf("unexpected upload ok result: %+v", res2[0])
	}

	// 3. Deferred upload failure (staging succeeds, server fails on PUT upload)
	wdRootFail := t.TempDir()
	wdHandler := &webdav.Handler{
		FileSystem: webdav.Dir(wdRootFail),
		LockSystem: webdav.NewMemLS(),
	}
	failServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" {
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}
		wdHandler.ServeHTTP(w, r)
	}))
	defer failServer.Close()

	failDestCfg, _ := json.Marshal(map[string]any{
		"url": failServer.URL,
	})
	failDestID, err := d.CreateStorageDestination(db.StorageDestination{
		Name:   "webdav-fail-" + nextUniqueRunner(t),
		Type:   "webdav",
		Config: string(failDestCfg),
	})
	if err != nil {
		t.Fatalf("create fail dest: %v", err)
	}

	jobID3, err := d.CreateJob(db.Job{
		Name:               "deferred-upload-fail-" + nextUniqueRunner(t),
		StorageDestID:      failDestID,
		BackupTypeChain:    "full",
		DeferRemoteUpload:  true,
		MaxParallelUploads: 2,
		Enabled:            true,
	})
	if err != nil {
		t.Fatalf("create job3: %v", err)
	}

	if _, err := d.AddJobItem(db.JobItem{
		JobID:    jobID3,
		ItemType: "folder",
		ItemName: "upload-fail-item",
		ItemID:   "upload-fail-id",
		Settings: string(itemSettings2),
	}); err != nil {
		t.Fatalf("add job item3: %v", err)
	}

	r.RunJob(jobID3)

	runs3, err := d.GetJobRuns(jobID3, 1)
	if err != nil || len(runs3) == 0 {
		t.Fatalf("get job runs 3: %v", err)
	}
	var res3 []map[string]any
	if err := json.Unmarshal([]byte(runs3[0].Log), &res3); err != nil {
		t.Fatalf("unmarshal run log 3: %v", err)
	}
	if len(res3) != 1 {
		t.Fatalf("expected 1 item result, got %d", len(res3))
	}
	if res3[0]["type"] != "folder" || res3[0]["status"] != "failed" ||
		res3[0]["item_id"] != "upload-fail-id" || res3[0]["settings"] != string(itemSettings2) {
		t.Errorf("unexpected upload fail result: %+v", res3[0])
	}
}

// TestRunJob_MissingJobIsNoOp drives the early-return branch when the
// job lookup fails (the row never existed in this test's DB).
func TestRunJob_MissingJobIsNoOp(t *testing.T) {
	t.Parallel()
	r, _ := newTestRunner(t)

	// 9999 doesn't exist. The function logs and returns without panicking.
	r.RunJob(9999)
}
