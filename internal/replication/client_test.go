package replication

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTestConnection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/health" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("X-API-Key"); got != "test-key" {
			t.Errorf("X-API-Key = %q, want %q", got, "test-key")
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(HealthResponse{Status: "ok", Version: "2026.1.0"})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-key")
	health, err := c.TestConnection()
	if err != nil {
		t.Fatalf("TestConnection() error = %v", err)
	}
	if health.Status != "ok" {
		t.Errorf("Status = %q, want %q", health.Status, "ok")
	}
	if health.Version != "2026.1.0" {
		t.Errorf("Version = %q, want %q", health.Version, "2026.1.0")
	}
}

func TestListJobs(t *testing.T) {
	jobs := []RemoteJob{
		{ID: 1, Name: "Web Server", Compression: "zstd"},
		{ID: 2, Name: "Database", Compression: "gzip"},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(jobs)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	got, err := c.ListJobs()
	if err != nil {
		t.Fatalf("ListJobs() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(jobs) = %d, want 2", len(got))
	}
	if got[0].Name != "Web Server" {
		t.Errorf("jobs[0].Name = %q, want %q", got[0].Name, "Web Server")
	}
}

func TestListRestorePoints(t *testing.T) {
	rps := []RemoteRestorePoint{
		{ID: 1, JobID: 1, BackupType: "full", StoragePath: "job-1/2025-01-01"},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/jobs/1/restore-points" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(rps)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	got, err := c.ListRestorePoints(1)
	if err != nil {
		t.Fatalf("ListRestorePoints() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(rps) = %d, want 1", len(got))
	}
	if got[0].StoragePath != "job-1/2025-01-01" {
		t.Errorf("StoragePath = %q, want %q", got[0].StoragePath, "job-1/2025-01-01")
	}
}

func TestDownloadFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/storage/1/files" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("path"); got != "job-1/backup.tar.zst" {
			t.Errorf("path query = %q, want %q", got, "job-1/backup.tar.zst")
		}
		w.Write([]byte("fake-backup-data"))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	rc, err := c.DownloadFile(1, "job-1/backup.tar.zst")
	if err != nil {
		t.Fatalf("DownloadFile() error = %v", err)
	}
	defer rc.Close()

	buf := make([]byte, 256)
	n, _ := rc.Read(buf)
	if string(buf[:n]) != "fake-backup-data" {
		t.Errorf("body = %q, want %q", string(buf[:n]), "fake-backup-data")
	}
}

func TestConnectionError(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", "key")
	_, err := c.TestConnection()
	if err == nil {
		t.Error("expected error for unreachable server")
	}
}

func TestNon200Response(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"invalid api key"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "bad-key")
	_, err := c.ListJobs()
	if err == nil {
		t.Error("expected error for 401 response")
	}
}
