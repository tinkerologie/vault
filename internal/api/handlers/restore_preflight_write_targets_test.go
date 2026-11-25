package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/restorerequest"
	"github.com/tinkerologie/vault/internal/runner"
)

// writeTargetsFixture is a local Restore Point holding two containers and a
// folder, with each container's saved configuration.
type writeTargetsFixture struct {
	h       *JobHandler
	d       *db.DB
	root    string
	host    string
	appdata string
	destID  int64
	jobID   int64
	rpID    int64
}

func newWriteTargetsFixture(t *testing.T) writeTargetsFixture {
	t.Helper()
	h, d := newJobHandlerDB(t)
	root := t.TempDir()
	host, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	appdata := filepath.Join(host, "appdata", "app")
	for _, name := range []string{"app", "app2"} {
		itemDir := filepath.Join(root, "points", "run1", name)
		if err := os.MkdirAll(itemDir, 0o755); err != nil {
			t.Fatal(err)
		}
		source := filepath.Join(host, "appdata", name)
		inspect, err := json.Marshal(map[string]any{
			"Name": "/" + name, "Config": map[string]any{"Image": "fixture/" + name + ":backup"}, "State": map[string]any{"Running": false},
			"Mounts": []map[string]string{{"Type": "bind", "Source": source, "Destination": "/config"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		volumes, err := json.Marshal([]map[string]any{{"index": 0, "source": source, "backed_up": true, "archive": "volume_0.tar"}})
		if err != nil {
			t.Fatal(err)
		}
		for file, data := range map[string]string{
			"config.json": string(inspect), "volumes.json": string(volumes), "image.tar": "image", "volume_0.tar": "data",
		} {
			if err := os.WriteFile(filepath.Join(itemDir, file), []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
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
		{JobID: jobID, ItemName: "app2", ItemType: "container"},
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
		Metadata: `{"item_sizes":{"app":3,"app2":3,"share":1},"item_types":{"app":"container","app2":"container","share":"folder"}}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	return writeTargetsFixture{h: h, d: d, root: root, host: host, appdata: appdata, destID: destID, jobID: jobID, rpID: rpID}
}

func (f writeTargetsFixture) call(handler http.HandlerFunc, path, body string) *httptest.ResponseRecorder {
	req := withURLParams(newReq(http.MethodPost, path, []byte(body)),
		"id", strconv.FormatInt(f.jobID, 10), "rpid", strconv.FormatInt(f.rpID, 10))
	w := httptest.NewRecorder()
	handler(w, req)
	return w
}


func decodeWriteTargets(t *testing.T, w *httptest.ResponseRecorder) []restorerequest.ItemWriteTargets {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var res writeTargetsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	return res.Items
}

// stubWriteTargetReview replaces the bounded-metadata review for one test.
func stubWriteTargetReview(t *testing.T, review func(ctx context.Context, in restorerequest.Input) restorerequest.ItemWriteTargets) {
	t.Helper()
	previous := previewWriteTarget
	previewWriteTarget = func(ctx context.Context, _ *db.DB, _ *runner.Runner, in restorerequest.Input, _ string) restorerequest.ItemWriteTargets {
		return review(ctx, in)
	}
	t.Cleanup(func() { previewWriteTarget = previous })
}

// TestRestorePointWriteTargets checks that the write-targets endpoint lists
// the exact host paths each selected container restore writes, from the
// saved configuration in the Restore Point, and leaves other types out (#716).
func TestRestorePointWriteTargets(t *testing.T) {
	f := newWriteTargetsFixture(t)
	custom := filepath.Join(f.host, "restored")
	cases := []struct {
		name string
		body string
		want restorerequest.WriteTarget
	}{
		{"original location", `{"items":["app","share"]}`,
			restorerequest.WriteTarget{Kind: "volume", Path: f.appdata, Mount: "/config", Clearable: true}},
		{"custom destination", `{"items":["app","share"],"destination":"` + custom + `"}`,
			restorerequest.WriteTarget{Kind: "volume", Path: filepath.Join(custom, "app"), Mount: "/config", Clearable: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			items := decodeWriteTargets(t, f.call(f.h.RestorePointWriteTargets, "/write-targets", tc.body))
			if len(items) != 1 {
				t.Fatalf("items = %+v, want only the container", items)
			}
			item := items[0]
			if item.Name != "app" || item.Type != "container" || item.Status != "resolved" {
				t.Fatalf("item = %+v", item)
			}
			if len(item.WriteTargets) != 1 || item.WriteTargets[0] != tc.want {
				t.Fatalf("write targets = %+v, want %+v", item.WriteTargets, tc.want)
			}
		})
	}

	t.Run("no container or VM selected", func(t *testing.T) {
		w := f.call(f.h.RestorePointWriteTargets, "/write-targets", `{"items":["share"]}`)
		if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"items":[]}` {
			t.Fatalf("folder-only = %d %s", w.Code, w.Body.String())
		}
	})

	t.Run("items past the bound are reported, not reviewed", func(t *testing.T) {
		previous := maxWriteTargetItems
		maxWriteTargetItems = 1
		t.Cleanup(func() { maxWriteTargetItems = previous })
		items := decodeWriteTargets(t, f.call(f.h.RestorePointWriteTargets, "/write-targets", `{"items":["app","app2"]}`))
		if len(items) != 2 || items[0].Status != "resolved" || items[1].Reason != "too_many_items" {
			t.Fatalf("bounded items = %+v", items)
		}
	})

	t.Run("invalid requests are refused", func(t *testing.T) {
		for _, body := range []string{`{`, `{"items":["missing"]}`, `{"items":["app"],"destination":"relative"}`} {
			if w := f.call(f.h.RestorePointWriteTargets, "/write-targets", body); w.Code != http.StatusBadRequest {
				t.Errorf("%s: status = %d, want 400", body, w.Code)
			}
		}
	})

	t.Run("unreachable storage reports each item unavailable", func(t *testing.T) {
		if err := f.d.UpdateStorageDestination(db.StorageDestination{ID: f.destID, Name: "wt-gone", Type: "local", Config: `{"path":"` + filepath.Join(f.root, "missing") + `"}`}); err != nil {
			t.Fatal(err)
		}
		items := decodeWriteTargets(t, f.call(f.h.RestorePointWriteTargets, "/write-targets", `{"items":["app"]}`))
		if len(items) != 1 || items[0].Status != "unavailable" || items[0].Reason == "" || len(items[0].WriteTargets) != 0 {
			t.Fatalf("unreachable storage items = %+v", items)
		}
	})
}

// TestRestorePointWriteTargetsBudget checks that the endpoint answers within
// its budget even when a review ignores cancellation: finished items keep
// their paths and the rest report cancelled.
func TestRestorePointWriteTargetsBudget(t *testing.T) {
	f := newWriteTargetsFixture(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	stubWriteTargetReview(t, func(_ context.Context, in restorerequest.Input) restorerequest.ItemWriteTargets {
		if in.ItemName == "app2" {
			<-release // a review stuck on slow storage
		}
		return restorerequest.ItemWriteTargets{Name: in.ItemName, Type: in.ItemType, Status: "resolved",
			WriteTargets: []restorerequest.WriteTarget{{Kind: "template", Path: "/t/" + in.ItemName}}}
	})
	previous := writeTargetsBudget
	writeTargetsBudget = 200 * time.Millisecond
	t.Cleanup(func() { writeTargetsBudget = previous })

	start := time.Now()
	items := decodeWriteTargets(t, f.call(f.h.RestorePointWriteTargets, "/write-targets", `{"items":["app","app2"]}`))
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("write targets took %v, want the %v budget", elapsed, writeTargetsBudget)
	}
	if len(items) != 2 || items[0].Status != "resolved" ||
		items[1].Name != "app2" || items[1].Status != "unavailable" || items[1].Reason != "cancelled" || items[1].WriteTargets == nil {
		t.Fatalf("items = %+v, want app resolved and app2 cancelled", items)
	}
}

// TestWriteTargetsBudgetFitsEveryRequestLimit keeps the budget inside the
// Unraid proxy's 10 s default (VAULT_HTTP_TIMEOUT_DEFAULT), the browser's
// 15 s request timeout and the daemon's write timeout, with room to reply.
func TestWriteTargetsBudgetFitsEveryRequestLimit(t *testing.T) {
	const proxyTimeout = 10 * time.Second
	if writeTargetsBudget > proxyTimeout-2*time.Second || writeTargetsBudget >= ServerWriteTimeout {
		t.Fatalf("writeTargetsBudget = %v, want at most %v", writeTargetsBudget, proxyTimeout-2*time.Second)
	}
}

// TestRestorePointPreflightLeavesWriteTargetsOut checks that the pre-flight
// returns only its checks and never waits on the write-target review, so a
// slow review cannot turn passing checks into a timeout.
func TestRestorePointPreflightLeavesWriteTargetsOut(t *testing.T) {
	f := newWriteTargetsFixture(t)
	reviewed := make(chan struct{}, 4)
	stubWriteTargetReview(t, func(ctx context.Context, in restorerequest.Input) restorerequest.ItemWriteTargets {
		reviewed <- struct{}{}
		select {
		case <-ctx.Done():
		case <-time.After(30 * time.Second):
		}
		return restorerequest.ItemWriteTargets{Name: in.ItemName, Type: in.ItemType, Status: "unavailable", Reason: "cancelled"}
	})
	start := time.Now()
	w := f.call(f.h.RestorePointPreflight, "/preflight", `{"items":["app","app2"]}`)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("pre-flight took %v while the review was slow", elapsed)
	}
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), `"items"`) || !strings.Contains(w.Body.String(), `"checks"`) {
		t.Fatalf("pre-flight = %d %s, want checks only", w.Code, w.Body.String())
	}
	if len(reviewed) != 0 {
		t.Fatal("pre-flight ran the write-target review")
	}
}
