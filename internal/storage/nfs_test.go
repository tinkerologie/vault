package storage

import (
	"context"
	"testing"
)

func TestNFSGetCapacity(t *testing.T) {
	t.Parallel()
	// NFSAdapter delegates GetCapacity to its wrapped *LocalAdapter. We can't
	// invoke a real NFS mount in CI/macOS, so we wire a LocalAdapter backed by
	// a temp dir directly into the struct (white-box, same package).
	dir := t.TempDir()
	a := &NFSAdapter{
		local:   NewLocalAdapter(dir),
		mounted: true,
	}
	cap, err := a.GetCapacity(context.Background())
	if err != nil {
		t.Fatalf("GetCapacity: %v", err)
	}
	if cap.Source != "statfs" {
		t.Errorf("source = %q, want statfs", cap.Source)
	}
	if cap.TotalBytes <= 0 {
		t.Errorf("TotalBytes = %d, want > 0", cap.TotalBytes)
	}
}

func TestNewNFSAdapter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  NFSConfig
		wantErr bool
	}{
		{
			name:    "valid config",
			config:  NFSConfig{Host: "192.168.1.100", Export: "/mnt/backups"},
			wantErr: false,
		},
		{
			name:    "with version and options",
			config:  NFSConfig{Host: "nas.local", Export: "/share", Version: "3", Options: "nolock,soft"},
			wantErr: false,
		},
		{
			name:    "missing host",
			config:  NFSConfig{Export: "/mnt/backups"},
			wantErr: true,
		},
		{
			name:    "missing export",
			config:  NFSConfig{Host: "192.168.1.100"},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			adapter, err := NewNFSAdapter(tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewNFSAdapter() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && adapter == nil {
				t.Error("NewNFSAdapter() returned nil adapter without error")
			}
		})
	}
}

func TestNFSDefaultVersion(t *testing.T) {
	t.Parallel()

	adapter, err := NewNFSAdapter(NFSConfig{Host: "nas", Export: "/data"})
	if err != nil {
		t.Fatalf("NewNFSAdapter() error = %v", err)
	}
	if adapter.config.Version != "4" {
		t.Errorf("default version = %q, want %q", adapter.config.Version, "4")
	}
}
