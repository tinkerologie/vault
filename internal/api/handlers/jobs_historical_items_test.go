package handlers

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/engine"
)

// An item removed from its job stays restorable from the Restore Points that
// hold it (#669): its type comes from the point's metadata, not the job.

func seedHistoricalPoint(t *testing.T, d *db.DB, jobID int64, metadata string) int64 {
	t.Helper()
	runID, err := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "success", BackupType: "full"})
	if err != nil {
		t.Fatalf("CreateJobRun: %v", err)
	}
	rpID, err := d.CreateRestorePoint(db.RestorePoint{
		JobRunID: runID, JobID: jobID, BackupType: "full",
		StoragePath: "historical-" + nextUnique(), Metadata: metadata,
	})
	if err != nil {
		t.Fatalf("CreateRestorePoint: %v", err)
	}
	return rpID
}

func postJSON(t *testing.T, handler http.HandlerFunc, path string, body any, params ...string) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	handler(w, withURLParams(newReq(http.MethodPost, path, raw), params...))
	return w
}

func TestRestorePointContents_RemovedContainerExpandsVolumes(t *testing.T) {
	t.Parallel()
	h, d := newJobHandlerDB(t)
	dest := seedDedupDest(t, d)
	mID := writeContainerManifest(t, d, dest)
	jobID, err := d.CreateJob(db.Job{Name: "removed-" + nextUnique(), StorageDestID: dest.ID})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	// The container is no longer in the job; only the point records it.
	meta, _ := json.Marshal(map[string]any{
		"item_manifests": map[string]string{"plex": hex.EncodeToString(mID[:])},
		"item_types":     map[string]string{"plex": "container"},
	})
	rpID := seedHistoricalPoint(t, d, jobID, string(meta))

	w := browseContents(t, h, jobID, rpID, "plex")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body: %s", w.Code, w.Body.String())
	}
	var idx engine.TarIndex
	if err := json.Unmarshal(w.Body.Bytes(), &idx); err != nil {
		t.Fatalf("decode: %v", err)
	}
	paths := map[string]bool{}
	for _, f := range idx.Files {
		paths[f.Path] = true
		if engine.IsSyntheticContainerKey(f.Path) {
			t.Errorf("raw volume entry %q leaked into the listing", f.Path)
		}
	}
	if !paths["/config/settings.yml"] {
		t.Fatalf("volume was not expanded: %v", paths)
	}
}

func TestRestore_RemovedItemResolvesFromRestorePoint(t *testing.T) {
	t.Parallel()
	h, d := newJobHandlerDB(t)
	jobID := seedJob(t, d)
	rpID := seedHistoricalPoint(t, d, jobID, `{"item_sizes":{"plex":10},"item_types":{"plex":"container"}}`)
	path := "/api/v1/jobs/" + strconv.FormatInt(jobID, 10) + "/restore"
	params := []string{"id", strconv.FormatInt(jobID, 10)}

	if w := postJSON(t, h.Restore, path, map[string]any{"restore_point_id": rpID, "items": []string{"ghost"}}, params...); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown item: status = %d; body: %s", w.Code, w.Body.String())
	}
	if w := postJSON(t, h.Restore, path, map[string]any{
		"restore_point_id": rpID, "items": []string{"plex"}, "destination": "/mnt/user/../etc",
	}, params...); w.Code != http.StatusBadRequest {
		t.Fatalf("unsafe destination: status = %d; body: %s", w.Code, w.Body.String())
	}
	if w := postJSON(t, h.Restore, path, map[string]any{"restore_point_id": rpID, "items": []string{"plex"}}, params...); w.Code != http.StatusAccepted {
		t.Fatalf("removed item: status = %d; body: %s", w.Code, w.Body.String())
	}
	h.runner.WaitForBackgroundWork()
}

func TestRestorePointPreflight_RemovedItemResolvesFromRestorePoint(t *testing.T) {
	t.Parallel()
	h, d := newJobHandlerDB(t)
	jobID := seedJob(t, d)
	rpID := seedHistoricalPoint(t, d, jobID, `{"item_sizes":{"plex":10},"item_types":{"plex":"container"}}`)
	path := "/api/v1/jobs/" + strconv.FormatInt(jobID, 10) + "/restore-points/" + strconv.FormatInt(rpID, 10) + "/preflight"
	params := []string{"id", strconv.FormatInt(jobID, 10), "rpid", strconv.FormatInt(rpID, 10)}

	if w := postJSON(t, h.RestorePointPreflight, path, map[string]any{"items": []string{"ghost"}}, params...); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown item: status = %d; body: %s", w.Code, w.Body.String())
	}
	if w := postJSON(t, h.RestorePointPreflight, path, map[string]any{"items": []string{"plex"}}, params...); w.Code != http.StatusOK {
		t.Fatalf("removed item: status = %d; body: %s", w.Code, w.Body.String())
	}
}

func TestJobList_HistoryListsItemsNoLongerInTheJob(t *testing.T) {
	t.Parallel()
	h, d := newJobHandlerDB(t)
	jobID := seedJob(t, d)
	seedJobItem(t, d, jobID, "container", "sonarr")
	seedHistoricalPoint(t, d, jobID, `{"item_sizes":{"plex":10,"sonarr":5},"item_types":{"plex":"container","sonarr":"container"}}`)
	seedHistoricalPoint(t, d, jobID, `{"item_sizes":{"plex":12,"legacy":1,"win":3},"vm_checkpoints":{"win":"cp"},"item_types":{"plex":"container"}}`)

	type listed struct {
		ID              int64 `json:"id"`
		HistoricalItems []struct {
			ItemName      string `json:"item_name"`
			ItemType      string `json:"item_type"`
			RestorePoints int    `json:"restore_points"`
		} `json:"historical_items"`
	}
	read := func(url string) []listed {
		t.Helper()
		w := httptest.NewRecorder()
		h.List(w, newReq(http.MethodGet, url, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d; body: %s", w.Code, w.Body.String())
		}
		var jobs []listed
		if err := json.Unmarshal(w.Body.Bytes(), &jobs); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return jobs
	}

	if jobs := read("/api/v1/jobs?details=true"); len(jobs) != 1 || jobs[0].HistoricalItems != nil {
		t.Fatalf("history listed without being asked: %+v", jobs)
	}
	jobs := read("/api/v1/jobs?details=true&history=true")
	if len(jobs) != 1 {
		t.Fatalf("jobs = %+v", jobs)
	}
	got := map[string]string{}
	counts := map[string]int{}
	for _, item := range jobs[0].HistoricalItems {
		got[item.ItemName] = item.ItemType
		counts[item.ItemName] = item.RestorePoints
	}
	// sonarr is still in the job; legacy has no recorded type, so it cannot be restored safely.
	want := map[string]string{"plex": "container", "win": "vm"}
	if len(got) != len(want) || got["plex"] != "container" || got["win"] != "vm" {
		t.Fatalf("historical items = %v, want %v", got, want)
	}
	if counts["plex"] != 2 {
		t.Fatalf("plex restore points = %d, want 2", counts["plex"])
	}
}

// When the job's item and the Restore Point disagree on an item's type, the
// restore is refused: browsing shows the point's recorded type, so restoring
// with the job's type would write data in the wrong layout.
func TestRestore_ItemTypeChangedSinceBackupIsRefused(t *testing.T) {
	t.Parallel()
	h, d := newJobHandlerDB(t)
	jobID := seedJob(t, d)
	seedJobItem(t, d, jobID, "folder", "app")
	rpID := seedHistoricalPoint(t, d, jobID, `{"item_sizes":{"app":10},"item_types":{"app":"container"}}`)
	idStr := strconv.FormatInt(jobID, 10)

	restore := postJSON(t, h.Restore, "/api/v1/jobs/"+idStr+"/restore",
		map[string]any{"restore_point_id": rpID, "items": []string{"app"}}, "id", idStr)
	if restore.Code != http.StatusBadRequest || !strings.Contains(restore.Body.String(), "type changed") {
		t.Fatalf("restore: status = %d; body: %s", restore.Code, restore.Body.String())
	}
	preflight := postJSON(t, h.RestorePointPreflight,
		"/api/v1/jobs/"+idStr+"/restore-points/"+strconv.FormatInt(rpID, 10)+"/preflight",
		map[string]any{"items": []string{"app"}}, "id", idStr, "rpid", strconv.FormatInt(rpID, 10))
	if preflight.Code != http.StatusBadRequest || !strings.Contains(preflight.Body.String(), "type changed") {
		t.Fatalf("preflight: status = %d; body: %s", preflight.Code, preflight.Body.String())
	}
}

func TestResolveNamedRestoreTargets(t *testing.T) {
	t.Parallel()
	point := db.RestorePoint{Metadata: `{"item_types":{"app":"container","plex":"container"}}`}
	jobItems := []db.JobItem{{ItemName: "plex", ItemType: "container"}, {ItemName: "share", ItemType: "folder"}}

	got, err := resolveNamedRestoreTargets(jobItems, point, []string{"plex", "share"})
	if err != nil || len(got) != 2 || got[0].Type != "container" || got[1].Type != "folder" {
		t.Fatalf("matching types: %+v %v", got, err)
	}
	if got, err := resolveNamedRestoreTargets(nil, point, []string{"app"}); err != nil || got[0].Type != "container" {
		t.Fatalf("historical item: %+v %v", got, err)
	}
	if _, err := resolveNamedRestoreTargets([]db.JobItem{{ItemName: "app", ItemType: "folder"}}, point, []string{"app"}); err == nil || !strings.Contains(err.Error(), "type changed") {
		t.Fatalf("type changed: %v", err)
	}
}

// The legacy single-item form (item_name + item_type) gets the same check: a
// type that differs from the one the Restore Point recorded is refused.
func TestRestore_LegacyItemTypeMismatchIsRefused(t *testing.T) {
	t.Parallel()
	h, d := newJobHandlerDB(t)
	jobID := seedJob(t, d)
	rpID := seedHistoricalPoint(t, d, jobID, `{"item_sizes":{"app":10},"item_types":{"app":"folder"}}`)
	idStr := strconv.FormatInt(jobID, 10)
	w := postJSON(t, h.Restore, "/api/v1/jobs/"+idStr+"/restore",
		map[string]any{"restore_point_id": rpID, "item_name": "app", "item_type": "container"}, "id", idStr)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "type changed") {
		t.Fatalf("status = %d; body: %s", w.Code, w.Body.String())
	}
}

// A whole-job restore gets the same recorded-type check as named items.
func TestRestore_WholeJobTypeMismatchIsRefused(t *testing.T) {
	t.Parallel()
	h, d := newJobHandlerDB(t)
	jobID := seedJob(t, d)
	seedJobItem(t, d, jobID, "folder", "app")
	rpID := seedHistoricalPoint(t, d, jobID, `{"item_sizes":{"app":10},"item_types":{"app":"container"}}`)
	idStr := strconv.FormatInt(jobID, 10)
	w := postJSON(t, h.Restore, "/api/v1/jobs/"+idStr+"/restore", map[string]any{"restore_point_id": rpID}, "id", idStr)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "type changed") {
		t.Fatalf("status = %d; body: %s", w.Code, w.Body.String())
	}
}

// A folder or ZFS item that left its job has no recorded original location,
// so its restore needs an explicit destination instead of failing mid-run.
func TestRestore_RemovedFolderNeedsADestination(t *testing.T) {
	t.Parallel()
	h, d := newJobHandlerDB(t)
	jobID := seedJob(t, d)
	rpID := seedHistoricalPoint(t, d, jobID, `{"item_sizes":{"share":10},"item_types":{"share":"folder"}}`)
	idStr := strconv.FormatInt(jobID, 10)
	path := "/api/v1/jobs/" + idStr + "/restore"
	w := postJSON(t, h.Restore, path, map[string]any{"restore_point_id": rpID, "items": []string{"share"}}, "id", idStr)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "choose a restore destination") {
		t.Fatalf("no destination: status = %d; body: %s", w.Code, w.Body.String())
	}
	w = postJSON(t, h.Restore, path, map[string]any{"restore_point_id": rpID, "items": []string{"share"}, "destination": "/mnt/user/restored"}, "id", idStr)
	if w.Code != http.StatusAccepted {
		t.Fatalf("with destination: status = %d; body: %s", w.Code, w.Body.String())
	}
	h.runner.WaitForBackgroundWork()
}
