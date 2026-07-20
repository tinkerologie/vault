package handlers

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
)

// postJob issues POST /api/v1/jobs with the given raw JSON body and returns the
// recorder so tests can assert on status and message.
func postJob(t *testing.T, h *JobHandler, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r := newReq(http.MethodPost, "/api/v1/jobs", []byte(body))
	h.Create(w, r)
	return w
}

// TestCreateJob_UnknownStorageDestIsBadRequest is the regression test for the
// QA finding that a nonexistent storage_dest_id produced a 500: the bad foreign
// key fell through validation to SQLite and surfaced as an opaque
// "internal server error", so a caller could not distinguish a typo from a
// server fault.
func TestCreateJob_UnknownStorageDestIsBadRequest(t *testing.T) {
	h := newJobHandler(t)

	w := postJob(t, h, `{"name":"bad-dest","schedule":"0 3 * * *","storage_dest_id":999999}`)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "storage destination") {
		t.Errorf("body %q should name the offending field", w.Body.String())
	}
}

// TestUpdateJob_UnknownStorageDestIsBadRequest covers the same guard on the
// update path, which shares validateJobInput but is a separate entry point.
func TestUpdateJob_UnknownStorageDestIsBadRequest(t *testing.T) {
	h, d := newJobHandlerDB(t)
	destID := seedStorageDest(t, d)
	jobID, err := d.CreateJob(db.Job{
		Name: "good-job", Enabled: true, StorageDestID: destID,
		BackupTypeChain: "full", Schedule: "0 3 * * *",
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	w := httptest.NewRecorder()
	r := newReq(http.MethodPut, "/api/v1/jobs/1",
		[]byte(`{"name":"good-job","schedule":"0 3 * * *","storage_dest_id":424242}`))
	r = withURLParam(r, "id", strconv.FormatInt(jobID, 10))
	h.Update(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// TestCreateJob_StorageDestZeroAllowed guards the fix from over-reaching: a job
// with no destination yet (0) is a legitimate intermediate state and must not
// be rejected.
func TestCreateJob_StorageDestZeroAllowed(t *testing.T) {
	h := newJobHandler(t)

	w := postJob(t, h, `{"name":"no-dest-yet","schedule":"0 3 * * *","storage_dest_id":0}`)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", w.Code, w.Body.String())
	}
}

func TestCreateJob_RejectsFolderOutsideBrowseRoots(t *testing.T) {
	h, d := newJobHandlerDB(t)
	h.SetPathValidator(NewBrowseHandler().ValidatePath)
	destID := seedStorageDest(t, d)
	body := `{"name":"outside-root","storage_dest_id":` + strconv.FormatInt(destID, 10) +
		`,"items":[{"item_type":"folder","item_name":"private","item_id":"/tmp/private","settings":"{\"path\":\"/tmp/private\"}"}]}`

	w := postJob(t, h, body)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}

	malformed := `{"name":"malformed-folder","storage_dest_id":` + strconv.FormatInt(destID, 10) +
		`,"items":[{"item_type":"folder","item_name":"private","item_id":"/tmp/private","settings":"{not-json"}]}`
	w = postJob(t, h, malformed)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("malformed settings status = %d, want 400; body: %s", w.Code, w.Body.String())
	}

	legacyPath := `{"name":"legacy-folder","storage_dest_id":` + strconv.FormatInt(destID, 10) +
		`,"items":[{"item_type":"folder","item_name":"private","item_id":"/tmp/private","settings":""}]}`
	w = postJob(t, h, legacyPath)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("item_id path status = %d, want 400; body: %s", w.Code, w.Body.String())
	}

	jobID, err := d.CreateJob(db.Job{
		Name: "existing-job", Enabled: true, StorageDestID: destID,
		BackupTypeChain: "full", Schedule: "0 3 * * *",
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := d.AddJobItem(db.JobItem{
		JobID: jobID, ItemType: "container", ItemName: "keep-me", ItemID: "keep-me",
	}); err != nil {
		t.Fatalf("add existing item: %v", err)
	}

	w = httptest.NewRecorder()
	r := newReq(http.MethodPut, "/api/v1/jobs/1", []byte(body))
	r = withURLParam(r, "id", strconv.FormatInt(jobID, 10))
	h.Update(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("update status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
	items, err := d.GetJobItems(jobID)
	if err != nil {
		t.Fatalf("get existing items: %v", err)
	}
	if len(items) != 1 || items[0].ItemName != "keep-me" {
		t.Fatalf("items changed after rejected update: %#v", items)
	}
}

// TestCreateJob_RejectsInvalidEnums covers the QA finding that free-string
// fields accepted arbitrary junk that only failed later inside the engine.
func TestCreateJob_RejectsInvalidEnums(t *testing.T) {
	h, d := newJobHandlerDB(t)
	destID := seedStorageDest(t, d)

	cases := []struct {
		name  string
		field string
	}{
		{"backup type", `"backup_type_chain":"bogus"`},
		{"compression", `"compression":"rar-ultra"`},
		{"encryption", `"encryption":"rot13"`},
		{"container mode", `"container_mode":"sideways"`},
		{"vm mode", `"vm_mode":"lukewarm"`},
		{"verify mode", `"verify_mode":"vigorous"`},
		{"notify on", `"notify_on":"maybe"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"name":"enum-` + tc.name + `","schedule":"0 3 * * *","storage_dest_id":` +
				strconv.FormatInt(destID, 10) + `,` + tc.field + `}`
			w := postJob(t, h, body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 for %s; body: %s", w.Code, tc.field, w.Body.String())
			}
		})
	}
}

// TestCreateJob_AcceptsValidEnums ensures the new enum guard does not reject
// the values the wizard actually sends, including empty (field omitted).
func TestCreateJob_AcceptsValidEnums(t *testing.T) {
	h, d := newJobHandlerDB(t)
	destID := seedStorageDest(t, d)

	body := `{"name":"valid-enums","schedule":"0 3 * * *","storage_dest_id":` + strconv.FormatInt(destID, 10) +
		`,"backup_type_chain":"incremental","compression":"zstd","encryption":"age",` +
		`"container_mode":"one_by_one","vm_mode":"snapshot","verify_mode":"deep","notify_on":"always"}`
	w := postJob(t, h, body)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", w.Code, w.Body.String())
	}
}

// TestCreateJob_RejectsOutOfRangeNumbers covers negative retention and
// out-of-band retry values that previously persisted verbatim.
//
// max_parallel_uploads is intentionally excluded: it is clamped by design
// (see TestJobCreate_ClampsParallelUploads and Job.EffectiveUploadConcurrency),
// and rejecting it here would contradict that contract.
func TestCreateJob_RejectsOutOfRangeNumbers(t *testing.T) {
	h, d := newJobHandlerDB(t)
	destID := seedStorageDest(t, d)

	cases := []struct {
		name  string
		field string
	}{
		{"negative retention count", `"retention_count":-5`},
		{"negative retention days", `"retention_days":-1`},
		{"retry override too high", `"retry_max_override":9999`},
		{"retry override negative", `"retry_max_override":-3`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"name":"range-` + tc.name + `","schedule":"0 3 * * *","storage_dest_id":` +
				strconv.FormatInt(destID, 10) + `,` + tc.field + `}`
			w := postJob(t, h, body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 for %s; body: %s", w.Code, tc.field, w.Body.String())
			}
		})
	}
}

// TestCreateJob_RejectsOverlongName bounds the name so a pathological value
// cannot bloat the row or break list rendering.
func TestCreateJob_RejectsOverlongName(t *testing.T) {
	h, d := newJobHandlerDB(t)
	destID := seedStorageDest(t, d)

	body := `{"name":"` + strings.Repeat("a", maxJobNameLen+1) +
		`","schedule":"0 3 * * *","storage_dest_id":` + strconv.FormatInt(destID, 10) + `}`
	w := postJob(t, h, body)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}

	// Exactly at the limit must still be accepted.
	body = `{"name":"` + strings.Repeat("b", maxJobNameLen) +
		`","schedule":"0 3 * * *","storage_dest_id":` + strconv.FormatInt(destID, 10) + `}`
	if w := postJob(t, h, body); w.Code != http.StatusCreated {
		t.Fatalf("name at limit: status = %d, want 201; body: %s", w.Code, w.Body.String())
	}
}

// TestProbeTimeoutLeavesRoomToRespond is the regression test for the QA finding
// that outbound connectivity probes timed out at exactly the server's
// WriteTimeout, so the 502/504 the handler wrote could never be flushed and
// callers saw an empty reply (HTTP 000) instead of an error.
func TestProbeTimeoutLeavesRoomToRespond(t *testing.T) {
	if probeTimeout() >= ServerWriteTimeout {
		t.Fatalf("probeTimeout() = %v must be strictly less than ServerWriteTimeout = %v",
			probeTimeout(), ServerWriteTimeout)
	}
	if testConnectionTimeout >= ServerWriteTimeout {
		t.Fatalf("testConnectionTimeout = %v must be strictly less than ServerWriteTimeout = %v",
			testConnectionTimeout, ServerWriteTimeout)
	}
	if got := ServerWriteTimeout - probeTimeout(); got < probeTimeoutHeadroom {
		t.Errorf("headroom = %v, want at least %v to serialise and flush the error", got, probeTimeoutHeadroom)
	}
}
