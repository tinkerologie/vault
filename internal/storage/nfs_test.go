package storage

import "testing"

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
