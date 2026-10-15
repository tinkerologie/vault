package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tinkerologie/vault/internal/db"
)

func TestMountHandler458_List(t *testing.T) {
	h, d, _ := setupTestMountHandler(t)

	w := httptest.NewRecorder()
	h.List(w, httptest.NewRequest(http.MethodGet, "/mounts", nil))
	wantStatus458(t, w, http.StatusOK)
	if strings.TrimSpace(w.Body.String()) != "[]" {
		t.Errorf("body = %q, want []", w.Body.String())
	}

	breakTable458(t, d, "mount_sessions")
	w = httptest.NewRecorder()
	h.List(w, httptest.NewRequest(http.MethodGet, "/mounts?active=false", nil))
	wantStatus458(t, w, http.StatusInternalServerError)
}

func TestMountHandler458_GetQueryError(t *testing.T) {
	h, d, _ := setupTestMountHandler(t)
	breakTable458(t, d, "mount_sessions")
	w := httptest.NewRecorder()
	h.Get(w, withURLParam(httptest.NewRequest(http.MethodGet, "/x", nil), "id", "1"))
	wantStatus458(t, w, http.StatusInternalServerError)
}

func TestMountHandler458_MountRestorePoint(t *testing.T) {
	h, d, _ := setupTestMountHandler(t)

	cases := []struct {
		name   string
		id     string
		rpid   string
		status int
	}{
		{"bad job id", "x", "1", http.StatusBadRequest},
		{"bad rp id", "1", "x", http.StatusBadRequest},
		{"job not found", "999", "1", http.StatusNotFound},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		h.MountRestorePoint(w, withURLParams(httptest.NewRequest(http.MethodPost, "/x", nil), "id", c.id, "rpid", c.rpid))
		if w.Code != c.status {
			t.Errorf("%s: status = %d, want %d; body: %s", c.name, w.Code, c.status, w.Body.String())
		}
	}

	// A non-dedup destination is a server-side refusal, not a 404.
	destID, err := d.CreateStorageDestination(db.StorageDestination{Name: "plain", Type: "local", Config: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := d.CreateJob(db.Job{Name: "j458", StorageDestID: destID})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.MountRestorePoint(w, withURLParams(httptest.NewRequest(http.MethodPost, "/x", nil), "id", idStr(jobID), "rpid", "1"))
	wantStatus458(t, w, http.StatusInternalServerError)
}

func TestMountHandler458_Unmount(t *testing.T) {
	h, _, _ := setupTestMountHandler(t)

	w := httptest.NewRecorder()
	h.Unmount(w, withURLParam(httptest.NewRequest(http.MethodPost, "/x", nil), "id", "nope"))
	wantStatus458(t, w, http.StatusBadRequest)

	w = httptest.NewRecorder()
	h.Unmount(w, withURLParam(httptest.NewRequest(http.MethodPost, "/x", nil), "id", "4242"))
	wantStatus458(t, w, http.StatusNotFound)
}
