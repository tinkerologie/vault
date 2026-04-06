package diagnostics

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"testing"
	"time"
)

func TestPackageAsZip(t *testing.T) {
	t.Parallel()

	bundle := &DiagnosticBundle{
		GeneratedAt:   time.Now().UTC(),
		CorrelationID: "test-correlation-id",
		System: SystemInfo{
			Version:   "2026.3.0",
			GoVersion: "go1.26",
			OS:        "linux",
			Arch:      "amd64",
			Hostname:  "test-host",
		},
		Storage: []StorageInfo{
			{ID: 1, Name: "local", Type: "local", Config: `{"path":"/mnt/user/backups"}`},
		},
	}

	reader, err := PackageAsZip(bundle)
	if err != nil {
		t.Fatalf("PackageAsZip() error = %v", err)
	}

	// Read the full ZIP content.
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("reading zip: %v", err)
	}

	if len(data) == 0 {
		t.Fatal("zip output is empty")
	}

	// Parse the ZIP to verify structure.
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("parsing zip: %v", err)
	}

	if len(zr.File) != 1 {
		t.Fatalf("expected 1 file in zip, got %d", len(zr.File))
	}

	if zr.File[0].Name != "diagnostics.json" {
		t.Errorf("expected file name 'diagnostics.json', got %q", zr.File[0].Name)
	}

	// Read and verify the JSON content.
	f, err := zr.File[0].Open()
	if err != nil {
		t.Fatalf("opening zip entry: %v", err)
	}
	defer f.Close()

	var result DiagnosticBundle
	if err := json.NewDecoder(f).Decode(&result); err != nil {
		t.Fatalf("decoding json: %v", err)
	}

	if result.CorrelationID != "test-correlation-id" {
		t.Errorf("correlation_id = %q, want %q", result.CorrelationID, "test-correlation-id")
	}
	if result.System.Version != "2026.3.0" {
		t.Errorf("system.vault_version = %q, want %q", result.System.Version, "2026.3.0")
	}
	if len(result.Storage) != 1 {
		t.Errorf("storage count = %d, want 1", len(result.Storage))
	}
}
