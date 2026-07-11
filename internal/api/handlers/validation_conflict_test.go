package handlers

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/runner"
	"github.com/ruaan-deysel/vault/internal/ws"
)

// TestJobHandler_Create_Validation locks in the input validation that the
// Create handler previously lacked: an empty/whitespace name is a 400, an
// unparsable schedule is a 400, and a minimal valid (manual) job is created.
func TestJobHandler_Create_Validation(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{"empty body", `{}`, http.StatusBadRequest},
		{"empty name", `{"name":""}`, http.StatusBadRequest},
		{"whitespace name", `{"name":"   "}`, http.StatusBadRequest},
		{"invalid schedule", `{"name":"qa-sched","schedule":"not a cron"}`, http.StatusBadRequest},
		{"valid manual job", `{"name":"qa-ok"}`, http.StatusCreated},
		{"valid scheduled job", `{"name":"qa-ok2","schedule":"0 3 * * *"}`, http.StatusCreated},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newJobHandler(t)
			w := httptest.NewRecorder()
			h.Create(w, newReq("POST", "/jobs", []byte(tc.body)))
			if w.Code != tc.want {
				t.Fatalf("Create(%s): got %d, want %d (body=%s)", tc.body, w.Code, tc.want, w.Body.String())
			}
		})
	}
}

// TestJobHandler_Create_NormalizesWhitespaceSchedule ensures a whitespace-only
// schedule is stored as "" (manual-only). Otherwise it would pass validation
// but persist as non-empty, and the scheduler would try (and fail) to
// cron-parse it, leaving the job marked scheduled yet never running.
func TestJobHandler_Create_NormalizesWhitespaceSchedule(t *testing.T) {
	h := newJobHandler(t)
	w := httptest.NewRecorder()
	h.Create(w, newReq(http.MethodPost, "/jobs", []byte(`{"name":"qa-ws-sched","schedule":"   "}`)))
	if w.Code != http.StatusCreated {
		t.Fatalf("got %d, want 201 (%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"schedule":""`) {
		t.Fatalf("expected normalized empty schedule in response, got %s", w.Body.String())
	}
}

// TestHandlers_DuplicateName_Conflict confirms the shared duplicate-name → 409
// mapping is wired into the job, storage, and replication create handlers (not
// a raw 500 from the underlying UNIQUE constraint).
func TestHandlers_DuplicateName_Conflict(t *testing.T) {
	tests := []struct {
		name    string
		creates func(t *testing.T) (first, second *httptest.ResponseRecorder)
	}{
		{
			name: "job",
			creates: func(t *testing.T) (*httptest.ResponseRecorder, *httptest.ResponseRecorder) {
				h := newJobHandler(t)
				body := []byte(`{"name":"dup-job"}`)
				w1 := httptest.NewRecorder()
				h.Create(w1, newReq(http.MethodPost, "/jobs", body))
				w2 := httptest.NewRecorder()
				h.Create(w2, newReq(http.MethodPost, "/jobs", body))
				return w1, w2
			},
		},
		{
			name: "storage destination",
			creates: func(t *testing.T) (*httptest.ResponseRecorder, *httptest.ResponseRecorder) {
				h := newStorageHandlerForTest(t)
				body := []byte(`{"name":"dup-dest","type":"local","config":"{\"path\":\"/tmp\"}"}`)
				w1 := httptest.NewRecorder()
				h.Create(w1, newReq(http.MethodPost, "/storage", body))
				w2 := httptest.NewRecorder()
				h.Create(w2, newReq(http.MethodPost, "/storage", body))
				return w1, w2
			},
		},
		{
			name: "replication source",
			creates: func(t *testing.T) (*httptest.ResponseRecorder, *httptest.ResponseRecorder) {
				h, _ := setupReplicationTest(t)
				body := []byte(`{"name":"dup-repl","url":"http://192.168.1.1:24085","storage_dest_id":1,"schedule":"0 3 * * *","enabled":true}`)
				mk := func() *httptest.ResponseRecorder {
					w := httptest.NewRecorder()
					r := newReq(http.MethodPost, "/replication", body)
					r.Header.Set("Content-Type", "application/json")
					h.Create(w, r)
					return w
				}
				return mk(), mk()
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w1, w2 := tt.creates(t)
			if w1.Code != http.StatusCreated {
				t.Fatalf("first create: got %d, want 201 (%s)", w1.Code, w1.Body.String())
			}
			if w2.Code != http.StatusConflict {
				t.Fatalf("duplicate create: got %d, want 409 (%s)", w2.Code, w2.Body.String())
			}
			if !strings.Contains(w2.Body.String(), "already exists") {
				t.Fatalf("expected an actionable conflict message, got %s", w2.Body.String())
			}
		})
	}
}

// TestJobHandler_Update_Validation covers the Update path invariants: empty
// name → 400, renaming onto an existing name → 409, and an unknown id → 404
// (rather than a silent 200 no-op).
func TestJobHandler_Update_Validation(t *testing.T) {
	h, d := newJobHandlerDB(t)
	idA, err := d.CreateJob(db.Job{Name: "job-a"})
	if err != nil {
		t.Fatalf("seed job-a: %v", err)
	}
	if _, err := d.CreateJob(db.Job{Name: "job-b"}); err != nil {
		t.Fatalf("seed job-b: %v", err)
	}

	tests := []struct {
		name string
		id   int64
		body string
		want int
	}{
		{"empty name", idA, `{"name":""}`, http.StatusBadRequest},
		{"duplicate name", idA, `{"name":"job-b"}`, http.StatusConflict},
		{"unknown id", 99999, `{"name":"whatever"}`, http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idStr := strconv.FormatInt(tt.id, 10)
			w := httptest.NewRecorder()
			r := withURLParam(newReq(http.MethodPut, "/jobs/"+idStr, []byte(tt.body)), "id", idStr)
			h.Update(w, r)
			if w.Code != tt.want {
				t.Fatalf("got %d, want %d (%s)", w.Code, tt.want, w.Body.String())
			}
		})
	}
}

// newStorageHandlerForTest builds a StorageHandler backed by a temp DB and a
// real runner so broadcast hooks are safe to call.
func newStorageHandlerForTest(t *testing.T) *StorageHandler {
	t.Helper()
	d := newTestDB(t)
	hub := ws.NewHub()
	go hub.Run()
	r := runner.New(d, hub, bytes.Repeat([]byte{0xcd}, 32))
	return NewStorageHandler(d, r, bytes.Repeat([]byte{0xcd}, 32))
}
