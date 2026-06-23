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

// TestJobHandler_Create_DuplicateName_Conflict ensures a duplicate job name
// returns 409 Conflict with an actionable message instead of a raw 500.
func TestJobHandler_Create_DuplicateName_Conflict(t *testing.T) {
	h := newJobHandler(t)
	body := `{"name":"dup-job"}`

	w1 := httptest.NewRecorder()
	h.Create(w1, newReq("POST", "/jobs", []byte(body)))
	if w1.Code != http.StatusCreated {
		t.Fatalf("first create: got %d, want 201 (%s)", w1.Code, w1.Body.String())
	}

	w2 := httptest.NewRecorder()
	h.Create(w2, newReq("POST", "/jobs", []byte(body)))
	if w2.Code != http.StatusConflict {
		t.Fatalf("duplicate create: got %d, want 409 (%s)", w2.Code, w2.Body.String())
	}
	if !strings.Contains(w2.Body.String(), "already exists") {
		t.Fatalf("expected an actionable conflict message, got %s", w2.Body.String())
	}
}

// TestJobHandler_Update_Validation_And_Conflict covers the same invariants on
// the Update path: empty name → 400, and renaming onto an existing name → 409.
func TestJobHandler_Update_Validation_And_Conflict(t *testing.T) {
	h, d := newJobHandlerDB(t)
	idA, err := d.CreateJob(db.Job{Name: "job-a"})
	if err != nil {
		t.Fatalf("seed job-a: %v", err)
	}
	if _, err := d.CreateJob(db.Job{Name: "job-b"}); err != nil {
		t.Fatalf("seed job-b: %v", err)
	}
	idStr := strconv.FormatInt(idA, 10)

	// Empty name on update → 400.
	wEmpty := httptest.NewRecorder()
	rEmpty := withURLParam(newReq("PUT", "/jobs/"+idStr, []byte(`{"name":""}`)), "id", idStr)
	h.Update(wEmpty, rEmpty)
	if wEmpty.Code != http.StatusBadRequest {
		t.Fatalf("update empty name: got %d, want 400 (%s)", wEmpty.Code, wEmpty.Body.String())
	}

	// Rename job-a onto job-b's name → 409.
	wDup := httptest.NewRecorder()
	rDup := withURLParam(newReq("PUT", "/jobs/"+idStr, []byte(`{"name":"job-b"}`)), "id", idStr)
	h.Update(wDup, rDup)
	if wDup.Code != http.StatusConflict {
		t.Fatalf("update to duplicate name: got %d, want 409 (%s)", wDup.Code, wDup.Body.String())
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
	return NewStorageHandler(d, r)
}

// TestStorageHandler_Create_DuplicateName_Conflict confirms the shared
// duplicate-name → 409 mapping is wired into the storage handler too.
func TestStorageHandler_Create_DuplicateName_Conflict(t *testing.T) {
	h := newStorageHandlerForTest(t)
	body := `{"name":"dup-dest","type":"local","config":"{\"path\":\"/tmp\"}"}`

	w1 := httptest.NewRecorder()
	h.Create(w1, newReq("POST", "/storage", []byte(body)))
	if w1.Code != http.StatusCreated {
		t.Fatalf("first create: got %d, want 201 (%s)", w1.Code, w1.Body.String())
	}

	w2 := httptest.NewRecorder()
	h.Create(w2, newReq("POST", "/storage", []byte(body)))
	if w2.Code != http.StatusConflict {
		t.Fatalf("duplicate create: got %d, want 409 (%s)", w2.Code, w2.Body.String())
	}
}

// TestReplicationHandler_Create_DuplicateName_Conflict confirms the same for
// replication sources.
func TestReplicationHandler_Create_DuplicateName_Conflict(t *testing.T) {
	h, _ := setupReplicationTest(t)
	body := `{"name":"dup-repl","url":"http://192.168.1.1:24085","storage_dest_id":1,"schedule":"0 3 * * *","enabled":true}`

	w1 := httptest.NewRecorder()
	r1 := newReq("POST", "/replication", []byte(body))
	r1.Header.Set("Content-Type", "application/json")
	h.Create(w1, r1)
	if w1.Code != http.StatusCreated {
		t.Fatalf("first create: got %d, want 201 (%s)", w1.Code, w1.Body.String())
	}

	w2 := httptest.NewRecorder()
	r2 := newReq("POST", "/replication", []byte(body))
	r2.Header.Set("Content-Type", "application/json")
	h.Create(w2, r2)
	if w2.Code != http.StatusConflict {
		t.Fatalf("duplicate create: got %d, want 409 (%s)", w2.Code, w2.Body.String())
	}
}
