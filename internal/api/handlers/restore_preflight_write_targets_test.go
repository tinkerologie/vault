package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/restorerequest"
)

// TestRestorePointPreflightWriteTargets checks that a scoped pre-flight lists
// the exact host paths each selected container restore writes, from the
// saved configuration in the Restore Point, and leaves other types out.
func TestRestorePointPreflightWriteTargets(t *testing.T) {
	h, d := newJobHandlerDB(t)
	root := t.TempDir()
	host, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	appdata := filepath.Join(host, "appdata", "app")
	itemDir := filepath.Join(root, "points", "run1", "app")
	if err := os.MkdirAll(itemDir, 0o755); err != nil {
		t.Fatal(err)
	}
	inspect, err := json.Marshal(map[string]any{
		"Name": "/app", "Config": map[string]any{"Image": "fixture/app:backup"}, "State": map[string]any{"Running": false},
		"Mounts": []map[string]string{{"Type": "bind", "Source": appdata, "Destination": "/config"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	volumes, err := json.Marshal([]map[string]any{{"index": 0, "source": appdata, "backed_up": true, "archive": "volume_0.tar"}})
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"config.json": string(inspect), "volumes.json": string(volumes), "image.tar": "image", "volume_0.tar": "data",
	} {
		if err := os.WriteFile(filepath.Join(itemDir, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := json.Marshal(map[string]string{"path": root})
	if err != nil {
		t.Fatal(err)
	}
	destID, err := d.CreateStorageDestination(db.StorageDestination{Name: "wt-" + nextUnique(), Type: "local", Config: string(cfg)})
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := d.CreateJob(db.Job{Name: "wtjob-" + nextUnique(), Schedule: "0 * * * *", Compression: "none", Encryption: "none", StorageDestID: destID})
	if err != nil {
		t.Fatal(err)
	}
	folderPath := filepath.Join(host, "share")
	for _, item := range []db.JobItem{
		{JobID: jobID, ItemName: "app", ItemType: "container"},
		{JobID: jobID, ItemName: "share", ItemType: "folder", Settings: `{"path":"` + folderPath + `"}`},
	} {
		if _, err := d.AddJobItem(item); err != nil {
			t.Fatal(err)
		}
	}
	runID, err := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "completed"})
	if err != nil {
		t.Fatal(err)
	}
	rpID, err := d.CreateRestorePoint(db.RestorePoint{
		JobRunID: runID, JobID: jobID, BackupType: "full", StoragePath: "points/run1", SizeBytes: 4,
		Metadata: `{"item_sizes":{"app":3,"share":1},"item_types":{"app":"container","share":"folder"}}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	custom := filepath.Join(host, "restored")

	type response struct {
		OK    bool                              `json:"ok"`
		Items []restorerequest.ItemWriteTargets `json:"items"`
	}
	cases := []struct {
		name string
		body string
		want []restorerequest.WriteTarget
	}{
		{"original location", `{"items":["app","share"]}`,
			[]restorerequest.WriteTarget{{Kind: "volume", Path: appdata, Mount: "/config", Clearable: true}}},
		{"custom destination", `{"items":["app","share"],"destination":"` + custom + `"}`,
			[]restorerequest.WriteTarget{{Kind: "volume", Path: filepath.Join(custom, "app"), Mount: "/config", Clearable: true}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := withURLParams(newReq(http.MethodPost, "/preflight", []byte(tc.body)),
				"id", strconv.FormatInt(jobID, 10), "rpid", strconv.FormatInt(rpID, 10))
			w := httptest.NewRecorder()
			h.RestorePointPreflight(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", w.Code, w.Body.String())
			}
			var res response
			if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
				t.Fatal(err)
			}
			if len(res.Items) != 1 {
				t.Fatalf("items = %+v, want only the container", res.Items)
			}
			item := res.Items[0]
			if item.Name != "app" || item.Type != "container" || item.Status != "resolved" {
				t.Fatalf("item = %+v", item)
			}
			if len(item.WriteTargets) != len(tc.want) || item.WriteTargets[0] != tc.want[0] {
				t.Fatalf("write targets = %+v, want %+v", item.WriteTargets, tc.want)
			}
		})
	}

	// An unscoped pre-flight keeps its original shape.
	req := withURLParams(newReq(http.MethodPost, "/preflight", []byte(`{}`)),
		"id", strconv.FormatInt(jobID, 10), "rpid", strconv.FormatInt(rpID, 10))
	w := httptest.NewRecorder()
	h.RestorePointPreflight(w, req)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), `"items"`) {
		t.Fatalf("unscoped pre-flight = %d %s", w.Code, w.Body.String())
	}

	// Items past the per-request bound are reported, not reviewed.
	previous := maxPreflightWriteTargetItems
	maxPreflightWriteTargetItems = 0
	t.Cleanup(func() { maxPreflightWriteTargetItems = previous })
	req = withURLParams(newReq(http.MethodPost, "/preflight", []byte(`{"items":["app"]}`)),
		"id", strconv.FormatInt(jobID, 10), "rpid", strconv.FormatInt(rpID, 10))
	w = httptest.NewRecorder()
	h.RestorePointPreflight(w, req)
	if !strings.Contains(w.Body.String(), `"reason":"too_many_items"`) {
		t.Fatalf("bounded pre-flight = %s", w.Body.String())
	}
	maxPreflightWriteTargetItems = previous

	// Unreachable storage skips the path review rather than waiting on it.
	if err := d.UpdateStorageDestination(db.StorageDestination{ID: destID, Name: "wt-gone", Type: "local", Config: `{"path":"` + filepath.Join(root, "missing") + `"}`}); err != nil {
		t.Fatal(err)
	}
	req = withURLParams(newReq(http.MethodPost, "/preflight", []byte(`{"items":["app"]}`)),
		"id", strconv.FormatInt(jobID, 10), "rpid", strconv.FormatInt(rpID, 10))
	w = httptest.NewRecorder()
	h.RestorePointPreflight(w, req)
	var res response
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 || res.Items[0].Status != "unavailable" || res.Items[0].Reason != "storage_unavailable" {
		t.Fatalf("unreachable storage items = %+v (%s)", res.Items, w.Body.String())
	}
}
