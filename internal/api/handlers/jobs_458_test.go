package handlers

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/tinkerologie/vault/internal/crypto"
	"github.com/tinkerologie/vault/internal/db"
)

// breakTable458 renames a table so every later query against its original
// name fails, letting a test reach a DB error branch that sits behind earlier
// successful reads on other tables.
func breakTable458(t *testing.T, d *db.DB, table string) {
	t.Helper()
	if _, err := d.Exec("ALTER TABLE " + table + " RENAME TO " + table + "_broken458"); err != nil {
		t.Fatalf("rename %s: %v", table, err)
	}
}

// failWrites458 installs a trigger that aborts the given statement kind
// (INSERT, UPDATE or DELETE) on a table while reads keep working.
func failWrites458(t *testing.T, d *db.DB, table, op string) {
	t.Helper()
	name := "fail458_" + table + "_" + strings.ToLower(op)
	stmt := "CREATE TRIGGER " + name + " BEFORE " + op + " ON " + table +
		" BEGIN SELECT RAISE(ABORT, 'forced failure 458'); END"
	if _, err := d.Exec(stmt); err != nil {
		t.Fatalf("create trigger %s: %v", name, err)
	}
}

func idStr(id int64) string { return strconv.FormatInt(id, 10) }

// seedRP458 creates a job run and a restore point for jobID.
func seedRP458(t *testing.T, d *db.DB, jobID int64, backupType, storagePath, metadata string) int64 {
	t.Helper()
	runID, err := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "success"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	rpID, err := d.CreateRestorePoint(db.RestorePoint{
		JobRunID: runID, JobID: jobID, BackupType: backupType,
		StoragePath: storagePath, Metadata: metadata,
	})
	if err != nil {
		t.Fatalf("create rp: %v", err)
	}
	return rpID
}

func wantStatus458(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, want, w.Body.String())
	}
}

func TestJobList458_DetailsItemsError(t *testing.T) {
	t.Parallel()
	h, d := newJobHandlerDB(t)
	seedJob(t, d)
	breakTable458(t, d, "job_items")
	w := httptest.NewRecorder()
	h.List(w, newReq(http.MethodGet, "/api/v1/jobs?details=true", nil))
	wantStatus458(t, w, http.StatusInternalServerError)
}

func TestJobList458_DetailsBaselinesError(t *testing.T) {
	t.Parallel()
	h, d := newJobHandlerDB(t)
	seedJob(t, d)
	breakTable458(t, d, "job_baselines")
	w := httptest.NewRecorder()
	h.List(w, newReq(http.MethodGet, "/api/v1/jobs?details=true", nil))
	wantStatus458(t, w, http.StatusInternalServerError)
}

// TestJob458_InvalidIDs drives the parseID failure branch of every JobHandler
// endpoint that takes an {id}.
func TestJob458_InvalidIDs(t *testing.T) {
	t.Parallel()
	h := newJobHandler(t)
	cases := map[string]http.HandlerFunc{
		"GetHistory":       h.GetHistory,
		"RetentionPreview": h.RetentionPreview,
		"RunNow":           h.RunNow,
		"NextRun":          h.NextRun,
		"GetStaleItems":    h.GetStaleItems,
		"RemoveStaleItems": h.RemoveStaleItems,
		"Cancel":           h.Cancel,
		"GetRestorePoints": h.GetRestorePoints,
	}
	for name, fn := range cases {
		w := httptest.NewRecorder()
		fn(w, withURLParam(newReq(http.MethodGet, "/x", nil), "id", "nope"))
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, w.Code)
		}
	}
	// Second-param failures (valid id, bad rpid / itemId).
	second := map[string]struct {
		fn    http.HandlerFunc
		param string
	}{
		"Preflight":      {h.RestorePointPreflight, "rpid"},
		"DeleteJobItem":  {h.DeleteJobItem, "itemId"},
		"Verify":         {h.VerifyRestorePoint, "rpid"},
		"ListVerifyRuns": {h.ListRestorePointVerifyRuns, "rpid"},
		"GetVerifyRun":   {h.GetVerifyRun, "vrid"},
		"DeleteRP":       {h.DeleteRestorePoint, "rpid"},
	}
	for name, c := range second {
		w := httptest.NewRecorder()
		c.fn(w, withURLParams(newReq(http.MethodPost, "/x", nil), "id", "1", c.param, "bad"))
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, w.Code)
		}
	}
}

func TestJobGetHistory458(t *testing.T) {
	t.Parallel()
	h, d := newJobHandlerDB(t)
	jobID := seedJob(t, d)

	// Limit above the cap is clamped, not rejected.
	w := httptest.NewRecorder()
	h.GetHistory(w, withURLParam(newReq(http.MethodGet, "/x?limit=5000", nil), "id", idStr(jobID)))
	wantStatus458(t, w, http.StatusOK)
	if strings.TrimSpace(w.Body.String()) != "[]" {
		t.Errorf("body = %q, want []", w.Body.String())
	}

	breakTable458(t, d, "job_runs")
	w = httptest.NewRecorder()
	h.GetHistory(w, withURLParam(newReq(http.MethodGet, "/x", nil), "id", idStr(jobID)))
	wantStatus458(t, w, http.StatusInternalServerError)
}

func TestJobGetHistory458_JobQueryError(t *testing.T) {
	t.Parallel()
	h, d := newJobHandlerDB(t)
	_ = d.Close()
	w := httptest.NewRecorder()
	h.GetHistory(w, withURLParam(newReq(http.MethodGet, "/x", nil), "id", "1"))
	wantStatus458(t, w, http.StatusInternalServerError)
}

func TestJobGetRestorePoints458(t *testing.T) {
	t.Parallel()
	h, d := newJobHandlerDB(t)
	jobID := seedJob(t, d)

	w := httptest.NewRecorder()
	h.GetRestorePoints(w, withURLParam(newReq(http.MethodGet, "/x", nil), "id", idStr(jobID)))
	wantStatus458(t, w, http.StatusOK)
	if strings.TrimSpace(w.Body.String()) != "[]" {
		t.Errorf("body = %q, want []", w.Body.String())
	}

	breakTable458(t, d, "restore_points")
	w = httptest.NewRecorder()
	h.GetRestorePoints(w, withURLParam(newReq(http.MethodGet, "/x", nil), "id", idStr(jobID)))
	wantStatus458(t, w, http.StatusInternalServerError)
}

func TestJobRestorePointContents458_Branches(t *testing.T) {
	t.Parallel()

	t.Run("rp query error", func(t *testing.T) {
		t.Parallel()
		h, d := newJobHandlerDB(t)
		breakTable458(t, d, "restore_points")
		w := httptest.NewRecorder()
		h.RestorePointContents(w, withURLParams(newReq(http.MethodGet, "/x?item=a", nil), "id", "1", "rpid", "1"))
		wantStatus458(t, w, http.StatusInternalServerError)
	})

	t.Run("item not in restore point", func(t *testing.T) {
		t.Parallel()
		h, d := newJobHandlerDB(t)
		jobID := seedJob(t, d)
		rpID := seedRP458(t, d, jobID, "full", "rp", `{"item_sizes":{"other":1}}`)
		w := httptest.NewRecorder()
		h.RestorePointContents(w, withURLParams(newReq(http.MethodGet, "/x?item=mine", nil), "id", idStr(jobID), "rpid", idStr(rpID)))
		wantStatus458(t, w, http.StatusNotFound)
	})

	t.Run("job query error", func(t *testing.T) {
		t.Parallel()
		h, d := newJobHandlerDB(t)
		jobID := seedJob(t, d)
		rpID := seedRP458(t, d, jobID, "full", "rp", "{}")
		breakTable458(t, d, "jobs")
		w := httptest.NewRecorder()
		h.RestorePointContents(w, withURLParams(newReq(http.MethodGet, "/x?item=a", nil), "id", idStr(jobID), "rpid", idStr(rpID)))
		wantStatus458(t, w, http.StatusInternalServerError)
	})

	t.Run("destination query error", func(t *testing.T) {
		t.Parallel()
		h, d := newJobHandlerDB(t)
		jobID := seedJob(t, d)
		rpID := seedRP458(t, d, jobID, "full", "rp", "{}")
		breakTable458(t, d, "storage_destinations")
		w := httptest.NewRecorder()
		h.RestorePointContents(w, withURLParams(newReq(http.MethodGet, "/x?item=a", nil), "id", idStr(jobID), "rpid", idStr(rpID)))
		wantStatus458(t, w, http.StatusInternalServerError)
	})

	t.Run("incomplete incremental chain", func(t *testing.T) {
		t.Parallel()
		h, d := newJobHandlerDB(t)
		jobID := seedJob(t, d)
		rpID := seedRP458(t, d, jobID, "incremental", "rp", "{}")
		w := httptest.NewRecorder()
		h.RestorePointContents(w, withURLParams(newReq(http.MethodGet, "/x?item=a", nil), "id", idStr(jobID), "rpid", idStr(rpID)))
		wantStatus458(t, w, http.StatusNotFound)
		if !strings.Contains(w.Body.String(), "chain is incomplete") {
			t.Errorf("body = %s", w.Body.String())
		}
	})
}

func TestJobRetentionPreview458(t *testing.T) {
	t.Parallel()
	t.Run("job query error", func(t *testing.T) {
		t.Parallel()
		h, d := newJobHandlerDB(t)
		_ = d.Close()
		w := httptest.NewRecorder()
		h.RetentionPreview(w, withURLParam(newReq(http.MethodGet, "/x", nil), "id", "1"))
		wantStatus458(t, w, http.StatusInternalServerError)
	})
	t.Run("restore points error", func(t *testing.T) {
		t.Parallel()
		h, d := newJobHandlerDB(t)
		jobID := seedJob(t, d)
		breakTable458(t, d, "restore_points")
		w := httptest.NewRecorder()
		h.RetentionPreview(w, withURLParam(newReq(http.MethodGet, "/x?keep_latest=1", nil), "id", idStr(jobID)))
		wantStatus458(t, w, http.StatusInternalServerError)
	})
}

func TestJobVerifyEndpoints458_DBErrors(t *testing.T) {
	t.Parallel()

	t.Run("verify rp query error", func(t *testing.T) {
		t.Parallel()
		h, d := newJobHandlerDB(t)
		breakTable458(t, d, "restore_points")
		w := httptest.NewRecorder()
		h.VerifyRestorePoint(w, withURLParams(newReq(http.MethodPost, "/x", nil), "id", "1", "rpid", "1"))
		wantStatus458(t, w, http.StatusInternalServerError)
	})

	t.Run("verify runner error", func(t *testing.T) {
		t.Parallel()
		h, d := newJobHandlerDB(t)
		jobID := seedJob(t, d)
		rpID := seedRP458(t, d, jobID, "full", "rp", "{}")
		breakTable458(t, d, "verify_runs")
		w := httptest.NewRecorder()
		h.VerifyRestorePoint(w, withURLParams(newReq(http.MethodPost, "/x", []byte(`{"mode":"quick"}`)), "id", idStr(jobID), "rpid", idStr(rpID)))
		wantStatus458(t, w, http.StatusInternalServerError)
	})

	t.Run("get verify run error", func(t *testing.T) {
		t.Parallel()
		h, d := newJobHandlerDB(t)
		breakTable458(t, d, "verify_runs")
		w := httptest.NewRecorder()
		h.GetVerifyRun(w, withURLParams(newReq(http.MethodGet, "/x", nil), "id", "1", "vrid", "1"))
		wantStatus458(t, w, http.StatusInternalServerError)
	})

	t.Run("list verify runs rp error", func(t *testing.T) {
		t.Parallel()
		h, d := newJobHandlerDB(t)
		breakTable458(t, d, "restore_points")
		w := httptest.NewRecorder()
		h.ListRestorePointVerifyRuns(w, withURLParams(newReq(http.MethodGet, "/x", nil), "id", "1", "rpid", "1"))
		wantStatus458(t, w, http.StatusInternalServerError)
	})

	t.Run("list verify runs query error", func(t *testing.T) {
		t.Parallel()
		h, d := newJobHandlerDB(t)
		jobID := seedJob(t, d)
		rpID := seedRP458(t, d, jobID, "full", "rp", "{}")
		breakTable458(t, d, "verify_runs")
		w := httptest.NewRecorder()
		h.ListRestorePointVerifyRuns(w, withURLParams(newReq(http.MethodGet, "/x?limit=3", nil), "id", idStr(jobID), "rpid", idStr(rpID)))
		wantStatus458(t, w, http.StatusInternalServerError)
	})
}

func TestJobDeleteRestorePoint458_Branches(t *testing.T) {
	t.Parallel()

	t.Run("rp query error", func(t *testing.T) {
		t.Parallel()
		h, d := newJobHandlerDB(t)
		breakTable458(t, d, "restore_points")
		w := httptest.NewRecorder()
		h.DeleteRestorePoint(w, withURLParams(newReq(http.MethodDelete, "/x", nil), "id", "1", "rpid", "1"))
		wantStatus458(t, w, http.StatusInternalServerError)
	})

	t.Run("destination query error", func(t *testing.T) {
		t.Parallel()
		h, d := newJobHandlerDB(t)
		jobID := seedJob(t, d)
		rpID := seedRP458(t, d, jobID, "full", "rp", "{}")
		breakTable458(t, d, "storage_destinations")
		w := httptest.NewRecorder()
		h.DeleteRestorePoint(w, withURLParams(newReq(http.MethodDelete, "/x", nil), "id", idStr(jobID), "rpid", idStr(rpID)))
		wantStatus458(t, w, http.StatusInternalServerError)
	})

	t.Run("job query error", func(t *testing.T) {
		t.Parallel()
		h, d := newJobHandlerDB(t)
		jobID := seedJob(t, d)
		rpID := seedRP458(t, d, jobID, "full", "rp", "{}")
		breakTable458(t, d, "jobs")
		w := httptest.NewRecorder()
		h.DeleteRestorePoint(w, withURLParams(newReq(http.MethodDelete, "/x", nil), "id", idStr(jobID), "rpid", idStr(rpID)))
		wantStatus458(t, w, http.StatusInternalServerError)
	})

	t.Run("delete error", func(t *testing.T) {
		t.Parallel()
		h, d := newJobHandlerDB(t)
		jobID := seedJob(t, d)
		rpID := seedRP458(t, d, jobID, "full", "", "{}")
		failWrites458(t, d, "restore_points", "DELETE")
		w := httptest.NewRecorder()
		h.DeleteRestorePoint(w, withURLParams(newReq(http.MethodDelete, "/x", nil), "id", idStr(jobID), "rpid", idStr(rpID)))
		wantStatus458(t, w, http.StatusInternalServerError)
	})
}

func TestJobRestorePointPreflight458_Branches(t *testing.T) {
	t.Parallel()

	t.Run("job not found", func(t *testing.T) {
		t.Parallel()
		h := newJobHandler(t)
		w := httptest.NewRecorder()
		h.RestorePointPreflight(w, withURLParams(newReq(http.MethodPost, "/x", nil), "id", "999", "rpid", "1"))
		wantStatus458(t, w, http.StatusNotFound)
	})

	t.Run("job query error", func(t *testing.T) {
		t.Parallel()
		h, d := newJobHandlerDB(t)
		_ = d.Close()
		w := httptest.NewRecorder()
		h.RestorePointPreflight(w, withURLParams(newReq(http.MethodPost, "/x", nil), "id", "1", "rpid", "1"))
		wantStatus458(t, w, http.StatusInternalServerError)
	})

	t.Run("rp query error", func(t *testing.T) {
		t.Parallel()
		h, d := newJobHandlerDB(t)
		jobID := seedJob(t, d)
		breakTable458(t, d, "restore_points")
		w := httptest.NewRecorder()
		h.RestorePointPreflight(w, withURLParams(newReq(http.MethodPost, "/x", nil), "id", idStr(jobID), "rpid", "1"))
		wantStatus458(t, w, http.StatusInternalServerError)
	})

	t.Run("rp belongs to other job", func(t *testing.T) {
		t.Parallel()
		h, d := newJobHandlerDB(t)
		jobA := seedJob(t, d)
		jobB := seedJob(t, d)
		rpID := seedRP458(t, d, jobB, "full", "rp", "{}")
		w := httptest.NewRecorder()
		h.RestorePointPreflight(w, withURLParams(newReq(http.MethodPost, "/x", nil), "id", idStr(jobA), "rpid", idStr(rpID)))
		wantStatus458(t, w, http.StatusNotFound)
	})
}

func TestJobRestore458_DBErrors(t *testing.T) {
	t.Parallel()

	t.Run("bad id", func(t *testing.T) {
		t.Parallel()
		h := newJobHandler(t)
		w := httptest.NewRecorder()
		h.Restore(w, withURLParam(newReq(http.MethodPost, "/x", []byte(`{"restore_point_id":1}`)), "id", "bad"))
		wantStatus458(t, w, http.StatusBadRequest)
	})

	t.Run("list restore points error", func(t *testing.T) {
		t.Parallel()
		h, d := newJobHandlerDB(t)
		breakTable458(t, d, "restore_points")
		w := httptest.NewRecorder()
		h.Restore(w, withURLParam(newReq(http.MethodPost, "/x", []byte(`{"restore_point_id":1}`)), "id", "1"))
		wantStatus458(t, w, http.StatusInternalServerError)
	})

	t.Run("job items error", func(t *testing.T) {
		t.Parallel()
		h, d := newJobHandlerDB(t)
		jobID := seedJob(t, d)
		rpID := seedRP458(t, d, jobID, "full", "rp", "{}")
		breakTable458(t, d, "job_items")
		body, _ := json.Marshal(map[string]any{"restore_point_id": rpID, "items": []string{"a"}})
		w := httptest.NewRecorder()
		h.Restore(w, withURLParam(newReq(http.MethodPost, "/x", body), "id", idStr(jobID)))
		wantStatus458(t, w, http.StatusInternalServerError)
	})
}

func TestJobStaleItems458(t *testing.T) {
	t.Parallel()

	t.Run("get items error", func(t *testing.T) {
		t.Parallel()
		h, d := newJobHandlerDB(t)
		jobID := seedJob(t, d)
		breakTable458(t, d, "job_items")
		for name, fn := range map[string]http.HandlerFunc{
			"GetStaleItems":    h.GetStaleItems,
			"RemoveStaleItems": h.RemoveStaleItems,
			"DeleteJobItem":    h.DeleteJobItem,
		} {
			w := httptest.NewRecorder()
			fn(w, withURLParams(newReq(http.MethodPost, "/x", nil), "id", idStr(jobID), "itemId", "1"))
			if w.Code != http.StatusInternalServerError {
				t.Errorf("%s: status = %d, want 500; body: %s", name, w.Code, w.Body.String())
			}
		}
	})

	t.Run("job not found", func(t *testing.T) {
		t.Parallel()
		h := newJobHandler(t)
		for name, fn := range map[string]http.HandlerFunc{
			"GetStaleItems":    h.GetStaleItems,
			"RemoveStaleItems": h.RemoveStaleItems,
		} {
			w := httptest.NewRecorder()
			fn(w, withURLParam(newReq(http.MethodGet, "/x", nil), "id", "424242"))
			if w.Code != http.StatusNotFound {
				t.Errorf("%s: status = %d, want 404", name, w.Code)
			}
		}
	})

	t.Run("mark and clear missing, malformed settings", func(t *testing.T) {
		t.Parallel()
		h, d := newJobHandlerDB(t)
		jobID := seedJob(t, d)
		present := t.TempDir()
		gone := filepath.Join(t.TempDir(), "gone")

		presentID, err := d.AddJobItem(db.JobItem{JobID: jobID, ItemType: "folder", ItemName: "present", Settings: `{"path":"` + present + `"}`})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.AddJobItem(db.JobItem{JobID: jobID, ItemType: "folder", ItemName: "gone", Settings: `{"path":"` + gone + `"}`}); err != nil {
			t.Fatal(err)
		}
		badID, err := d.AddJobItem(db.JobItem{JobID: jobID, ItemType: "folder", ItemName: "bad", Settings: "{}"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.Exec(`UPDATE job_items SET settings = '{not json' WHERE id = ?`, badID); err != nil {
			t.Fatal(err)
		}
		// Present item previously marked missing must be cleared.
		if err := d.MarkJobItemsMissing([]int64{presentID}, "2020-01-01T00:00:00Z"); err != nil {
			t.Fatal(err)
		}

		w := httptest.NewRecorder()
		h.GetStaleItems(w, withURLParam(newReq(http.MethodGet, "/x", nil), "id", idStr(jobID)))
		wantStatus458(t, w, http.StatusOK)
		var resp struct {
			Count int `json:"count"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if resp.Count != 1 {
			t.Fatalf("count = %d, want 1; body %s", resp.Count, w.Body.String())
		}
		items, err := d.GetJobItems(jobID)
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range items {
			switch it.ItemName {
			case "present":
				if it.MissingSince != nil {
					t.Errorf("present item still marked missing")
				}
			case "gone":
				if it.MissingSince == nil {
					t.Errorf("gone item not marked missing")
				}
			}
		}
	})

	t.Run("mark and clear failures are logged only", func(t *testing.T) {
		t.Parallel()
		h, d := newJobHandlerDB(t)
		jobID := seedJob(t, d)
		present := t.TempDir()
		gone := filepath.Join(t.TempDir(), "gone")
		presentID, _ := d.AddJobItem(db.JobItem{JobID: jobID, ItemType: "folder", ItemName: "present", Settings: `{"path":"` + present + `"}`})
		_, _ = d.AddJobItem(db.JobItem{JobID: jobID, ItemType: "folder", ItemName: "gone", Settings: `{"path":"` + gone + `"}`})
		if err := d.MarkJobItemsMissing([]int64{presentID}, "2020-01-01T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
		failWrites458(t, d, "job_items", "UPDATE")

		w := httptest.NewRecorder()
		h.GetStaleItems(w, withURLParam(newReq(http.MethodGet, "/x", nil), "id", idStr(jobID)))
		wantStatus458(t, w, http.StatusOK)
	})

	t.Run("remove stale delete error", func(t *testing.T) {
		t.Parallel()
		h, d := newJobHandlerDB(t)
		jobID := seedJob(t, d)
		gone := filepath.Join(t.TempDir(), "gone")
		_, _ = d.AddJobItem(db.JobItem{JobID: jobID, ItemType: "folder", ItemName: "gone", Settings: `{"path":"` + gone + `"}`})
		failWrites458(t, d, "job_items", "DELETE")
		w := httptest.NewRecorder()
		h.RemoveStaleItems(w, withURLParam(newReq(http.MethodPost, "/x", nil), "id", idStr(jobID)))
		wantStatus458(t, w, http.StatusInternalServerError)
	})

	t.Run("delete job item error", func(t *testing.T) {
		t.Parallel()
		h, d := newJobHandlerDB(t)
		jobID := seedJob(t, d)
		itemID := seedJobItem(t, d, jobID, "folder", "x")
		failWrites458(t, d, "job_items", "DELETE")
		w := httptest.NewRecorder()
		h.DeleteJobItem(w, withURLParams(newReq(http.MethodDelete, "/x", nil), "id", idStr(jobID), "itemId", idStr(itemID)))
		wantStatus458(t, w, http.StatusInternalServerError)
	})
}

// writeAge458 encrypts data with pass and writes it to path.
func writeAge458(t *testing.T, path, pass string, data []byte) {
	t.Helper()
	enc, err := crypto.EncryptReader(pass, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	out, err := io.ReadAll(enc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestJobRestorePointContents458_EncryptedSidecars covers the .age sidecar
// paths: decrypted successfully, undecryptable, and a plaintext sidecar that
// is not valid index JSON.
func TestJobRestorePointContents458_EncryptedSidecars(t *testing.T) {
	t.Parallel()
	const pass = "sidecar-passphrase"

	t.Run("decrypts with configured passphrase", func(t *testing.T) {
		t.Parallel()
		h, itemDir, url, jobID, rpID := classicContainerPoint(t, "app")
		if err := h.db.SetSetting("encryption_passphrase", pass); err != nil {
			t.Fatal(err)
		}
		idx := []byte(`{"version":1,"archive":"app.tar","files":[{"path":"etc/conf","size":3}]}`)
		writeAge458(t, filepath.Join(itemDir, "app.tar.index.json.age"), pass, idx)
		w := httptest.NewRecorder()
		h.RestorePointContents(w, withURLParams(newReq(http.MethodGet, url, nil), "id", idStr(jobID), "rpid", idStr(rpID)))
		wantStatus458(t, w, http.StatusOK)
		if !strings.Contains(w.Body.String(), "etc/conf") {
			t.Errorf("body = %s", w.Body.String())
		}
	})

	t.Run("undecryptable sidecar is skipped", func(t *testing.T) {
		t.Parallel()
		h, itemDir, url, jobID, rpID := classicContainerPoint(t, "app")
		if err := h.db.SetSetting("encryption_passphrase", pass); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(itemDir, "app.tar.index.json.age"), []byte("not age data"), 0o600); err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		h.RestorePointContents(w, withURLParams(newReq(http.MethodGet, url, nil), "id", idStr(jobID), "rpid", idStr(rpID)))
		wantStatus458(t, w, http.StatusNotFound)
	})

	t.Run("corrupt plaintext index", func(t *testing.T) {
		t.Parallel()
		h, itemDir, url, jobID, rpID := classicContainerPoint(t, "app")
		if err := os.WriteFile(filepath.Join(itemDir, "app.tar.index.json"), []byte("{corrupt"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(itemDir, "subdir"), 0o755); err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		h.RestorePointContents(w, withURLParams(newReq(http.MethodGet, url, nil), "id", idStr(jobID), "rpid", idStr(rpID)))
		wantStatus458(t, w, http.StatusInternalServerError)
	})
}
