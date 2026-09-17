package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/mount"
)

func setupTestMountHandler(t *testing.T) (*MountHandler, *db.DB, *mount.Manager) {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })

	mgr := mount.NewManager(d, nil, []byte("01234567890123456789012345678901"))
	h := NewMountHandler(mgr, d)
	return h, d, mgr
}

func TestMountHandler_List(t *testing.T) {
	h, d, _ := setupTestMountHandler(t)

	destID, _ := d.CreateStorageDestination(db.StorageDestination{Name: "dest", Type: "local", Config: "{}"})
	jobID, _ := d.CreateJob(db.Job{Name: "job", StorageDestID: destID})
	runID, _ := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "completed", BackupType: "full"})
	rpID, _ := d.CreateRestorePoint(db.RestorePoint{JobRunID: runID, JobID: jobID, BackupType: "full", StoragePath: "p"})

	_, err := d.CreateMountSession(db.MountSession{
		JobID:          jobID,
		RestorePointID: &rpID,
		StorageDestID:  destID,
		MountPath:      "/mnt/vault-fuse/test-1",
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/mounts", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	var sessions []db.MountSession
	if err := json.NewDecoder(rr.Body).Decode(&sessions); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions count = %d, want 1", len(sessions))
	}
	if sessions[0].MountPath != "/mnt/vault-fuse/test-1" {
		t.Errorf("mount_path = %q", sessions[0].MountPath)
	}
}

func TestMountHandler_Get(t *testing.T) {
	h, d, _ := setupTestMountHandler(t)

	destID, _ := d.CreateStorageDestination(db.StorageDestination{Name: "dest", Type: "local", Config: "{}"})
	jobID, _ := d.CreateJob(db.Job{Name: "job", StorageDestID: destID})
	runID, _ := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "completed", BackupType: "full"})
	rpID, _ := d.CreateRestorePoint(db.RestorePoint{JobRunID: runID, JobID: jobID, BackupType: "full", StoragePath: "p"})

	sessID, _ := d.CreateMountSession(db.MountSession{
		JobID:          jobID,
		RestorePointID: &rpID,
		StorageDestID:  destID,
		MountPath:      "/mnt/vault-fuse/test-2",
	})

	// 1. Success
	req := httptest.NewRequest(http.MethodGet, "/mounts/1", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()
	h.Get(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	// 2. Not found
	req = httptest.NewRequest(http.MethodGet, "/mounts/999", nil)
	rctx = chi.NewRouteContext()
	rctx.URLParams.Add("id", "999")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr = httptest.NewRecorder()
	h.Get(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}

	// 3. Bad ID
	req = httptest.NewRequest(http.MethodGet, "/mounts/abc", nil)
	rctx = chi.NewRouteContext()
	rctx.URLParams.Add("id", "abc")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr = httptest.NewRecorder()
	h.Get(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	_ = sessID
}

func TestMountHandler_Unmount(t *testing.T) {
	h, d, _ := setupTestMountHandler(t)

	destID, _ := d.CreateStorageDestination(db.StorageDestination{Name: "dest", Type: "local", Config: "{}"})
	jobID, _ := d.CreateJob(db.Job{Name: "job", StorageDestID: destID})
	runID, _ := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "completed", BackupType: "full"})
	rpID, _ := d.CreateRestorePoint(db.RestorePoint{JobRunID: runID, JobID: jobID, BackupType: "full", StoragePath: "p"})

	sessID, _ := d.CreateMountSession(db.MountSession{
		JobID:          jobID,
		RestorePointID: &rpID,
		StorageDestID:  destID,
		MountPath:      "/mnt/vault-fuse/test-3",
	})

	req := httptest.NewRequest(http.MethodPost, "/mounts/1/unmount", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()
	h.Unmount(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	s, err := d.GetMountSession(sessID)
	if err != nil {
		t.Fatal(err)
	}
	if s.Status != "stopped" {
		t.Errorf("status = %q, want stopped", s.Status)
	}
}
