package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadCfg_NonexistentFile(t *testing.T) {
	t.Parallel()
	cfg, err := ReadCfg("/nonexistent/path/vault.cfg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg) != 0 {
		t.Errorf("expected empty map, got %v", cfg)
	}
}

func TestReadCfg_BasicKeyValues(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "vault.cfg")
	content := `PORT=24085
BIND_ADDRESS=127.0.0.1
SNAPSHOT_PATH=/mnt/cache/.vault/vault.db
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := ReadCfg(cfgPath)
	if err != nil {
		t.Fatalf("ReadCfg: %v", err)
	}
	if cfg["PORT"] != "24085" {
		t.Errorf("PORT = %q, want %q", cfg["PORT"], "24085")
	}
	if cfg["BIND_ADDRESS"] != "127.0.0.1" {
		t.Errorf("BIND_ADDRESS = %q, want %q", cfg["BIND_ADDRESS"], "127.0.0.1")
	}
	if cfg["SNAPSHOT_PATH"] != "/mnt/cache/.vault/vault.db" {
		t.Errorf("SNAPSHOT_PATH = %q, want %q", cfg["SNAPSHOT_PATH"], "/mnt/cache/.vault/vault.db")
	}
}

func TestReadCfg_QuotedValues(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "vault.cfg")
	content := `PORT="24085"
BIND_ADDRESS='0.0.0.0'
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := ReadCfg(cfgPath)
	if err != nil {
		t.Fatalf("ReadCfg: %v", err)
	}
	if cfg["PORT"] != "24085" {
		t.Errorf("PORT = %q, want %q", cfg["PORT"], "24085")
	}
	if cfg["BIND_ADDRESS"] != "0.0.0.0" {
		t.Errorf("BIND_ADDRESS = %q, want %q", cfg["BIND_ADDRESS"], "0.0.0.0")
	}
}

func TestReadCfg_CommentsAndBlankLines(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "vault.cfg")
	content := `# Vault configuration
PORT=9999

# Network settings
BIND_ADDRESS=0.0.0.0
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := ReadCfg(cfgPath)
	if err != nil {
		t.Fatalf("ReadCfg: %v", err)
	}
	if len(cfg) != 2 {
		t.Errorf("expected 2 keys, got %d: %v", len(cfg), cfg)
	}
}

func TestReadCfgValue_DefaultOnMissing(t *testing.T) {
	t.Parallel()
	val := ReadCfgValue("/nonexistent/path/vault.cfg", "PORT", "24085")
	if val != "24085" {
		t.Errorf("got %q, want %q", val, "24085")
	}
}

func TestReadCfgValue_ExistingKey(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "vault.cfg")
	if err := os.WriteFile(cfgPath, []byte("PORT=9999\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	val := ReadCfgValue(cfgPath, "PORT", "24085")
	if val != "9999" {
		t.Errorf("got %q, want %q", val, "9999")
	}
}

func TestWriteCfgValue_NewFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "newdir", "vault.cfg")

	if err := WriteCfgValue(cfgPath, "PORT", "24085"); err != nil {
		t.Fatalf("WriteCfgValue: %v", err)
	}

	cfg, err := ReadCfg(cfgPath)
	if err != nil {
		t.Fatalf("ReadCfg: %v", err)
	}
	if cfg["PORT"] != "24085" {
		t.Errorf("PORT = %q, want %q", cfg["PORT"], "24085")
	}
}

func TestWriteCfgValue_UpdateExisting(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "vault.cfg")
	initial := "PORT=24085\nBIND_ADDRESS=127.0.0.1\n"
	if err := os.WriteFile(cfgPath, []byte(initial), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := WriteCfgValue(cfgPath, "PORT", "9999"); err != nil {
		t.Fatalf("WriteCfgValue: %v", err)
	}

	cfg, err := ReadCfg(cfgPath)
	if err != nil {
		t.Fatalf("ReadCfg: %v", err)
	}
	if cfg["PORT"] != "9999" {
		t.Errorf("PORT = %q, want %q", cfg["PORT"], "9999")
	}
	if cfg["BIND_ADDRESS"] != "127.0.0.1" {
		t.Errorf("BIND_ADDRESS = %q, want %q", cfg["BIND_ADDRESS"], "127.0.0.1")
	}
}

func TestWriteCfgValue_AppendNew(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "vault.cfg")
	initial := "PORT=24085\n"
	if err := os.WriteFile(cfgPath, []byte(initial), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := WriteCfgValue(cfgPath, "SNAPSHOT_PATH", "/mnt/cache/.vault/vault.db"); err != nil {
		t.Fatalf("WriteCfgValue: %v", err)
	}

	cfg, err := ReadCfg(cfgPath)
	if err != nil {
		t.Fatalf("ReadCfg: %v", err)
	}
	if cfg["PORT"] != "24085" {
		t.Errorf("PORT = %q, want %q", cfg["PORT"], "24085")
	}
	if cfg["SNAPSHOT_PATH"] != "/mnt/cache/.vault/vault.db" {
		t.Errorf("SNAPSHOT_PATH = %q, want %q", cfg["SNAPSHOT_PATH"], "/mnt/cache/.vault/vault.db")
	}
}

func TestWriteCfgValue_PreservesComments(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "vault.cfg")
	initial := "# Vault config\nPORT=24085\n# Network\nBIND_ADDRESS=127.0.0.1\n"
	if err := os.WriteFile(cfgPath, []byte(initial), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := WriteCfgValue(cfgPath, "SNAPSHOT_PATH", "/mnt/user/backups/vault.db"); err != nil {
		t.Fatalf("WriteCfgValue: %v", err)
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	content := string(data)
	if !contains(content, "# Vault config") {
		t.Error("comment was lost")
	}
	if !contains(content, "# Network") {
		t.Error("second comment was lost")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
