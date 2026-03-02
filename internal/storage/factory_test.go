package storage

import (
	"testing"
)

func TestNewAdapterLocal(t *testing.T) {
	dir := t.TempDir()
	adapter, err := NewAdapter("local", `{"path":"`+dir+`"}`)
	if err != nil {
		t.Fatalf("NewAdapter() error = %v", err)
	}
	if adapter == nil {
		t.Fatal("adapter is nil")
	}
	if err := adapter.TestConnection(); err != nil {
		t.Errorf("TestConnection() error = %v", err)
	}
}

func TestNewAdapterSFTP(t *testing.T) {
	adapter, err := NewAdapter("sftp", `{"host":"localhost","user":"test","password":"test","base_path":"/tmp"}`)
	if err != nil {
		t.Fatalf("NewAdapter() error = %v", err)
	}
	if adapter == nil {
		t.Fatal("adapter is nil")
	}
}

func TestNewAdapterSMB(t *testing.T) {
	adapter, err := NewAdapter("smb", `{"host":"localhost","user":"test","password":"test","share":"backup"}`)
	if err != nil {
		t.Fatalf("NewAdapter() error = %v", err)
	}
	if adapter == nil {
		t.Fatal("adapter is nil")
	}
}

func TestNewAdapterNFS(t *testing.T) {
	adapter, err := NewAdapter("nfs", `{"host":"nas.local","export":"/mnt/backups"}`)
	if err != nil {
		t.Fatalf("NewAdapter() error = %v", err)
	}
	if adapter == nil {
		t.Fatal("adapter is nil")
	}
}

func TestNewAdapterUnknown(t *testing.T) {
	_, err := NewAdapter("ftp", `{}`)
	if err == nil {
		t.Error("expected error for unknown type")
	}
}

func TestNewAdapterBadJSON(t *testing.T) {
	_, err := NewAdapter("sftp", `{bad json}`)
	if err == nil {
		t.Error("expected error for bad JSON")
	}
}
