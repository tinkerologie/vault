package storage

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalWrite(t *testing.T) {
	dir := t.TempDir()
	adapter := NewLocalAdapter(dir)

	data := []byte("hello vault")
	err := adapter.Write("test/backup.tar", bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	r, err := adapter.Read("test/backup.tar")
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	defer r.Close()

	got, _ := io.ReadAll(r)
	if !bytes.Equal(got, data) {
		t.Errorf("Read() = %q, want %q", got, data)
	}
}

func TestLocalList(t *testing.T) {
	dir := t.TempDir()
	adapter := NewLocalAdapter(dir)

	adapter.Write("backups/a.tar", bytes.NewReader([]byte("a")))
	adapter.Write("backups/b.tar", bytes.NewReader([]byte("b")))
	adapter.Write("other/c.tar", bytes.NewReader([]byte("c")))

	files, err := adapter.List("backups")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(files) != 2 {
		t.Errorf("List() returned %d files, want 2", len(files))
	}
}

func TestLocalDelete(t *testing.T) {
	dir := t.TempDir()
	adapter := NewLocalAdapter(dir)

	adapter.Write("test.tar", bytes.NewReader([]byte("data")))
	if err := adapter.Delete("test.tar"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	_, err := adapter.Read("test.tar")
	if err == nil {
		t.Error("Read() after Delete() should fail")
	}
}

func TestLocalTestConnection(t *testing.T) {
	dir := t.TempDir()
	adapter := NewLocalAdapter(dir)
	if err := adapter.TestConnection(); err != nil {
		t.Fatalf("TestConnection() error = %v", err)
	}

	bad := NewLocalAdapter(filepath.Join(dir, "nonexistent"))
	if err := bad.TestConnection(); err == nil {
		t.Error("TestConnection() on bad path should fail")
	}
}

func TestLocalStat(t *testing.T) {
	dir := t.TempDir()
	adapter := NewLocalAdapter(dir)

	adapter.Write("test.tar", bytes.NewReader([]byte("hello")))
	info, err := adapter.Stat("test.tar")
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if info.Size != 5 {
		t.Errorf("Size = %d, want 5", info.Size)
	}
}

func TestLocalRejectsTraversal(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	adapter := NewLocalAdapter(dir)
	outside := filepath.Join(dir, "..", "outside.txt")

	if err := adapter.Write("../../outside.txt", bytes.NewReader([]byte("blocked"))); err == nil {
		t.Fatal("Write() should reject traversal path")
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("expected no file outside base path, got err=%v", err)
	}
	if _, err := adapter.Read("../../outside.txt"); err == nil {
		t.Fatal("Read() should reject traversal path")
	}
	if _, err := adapter.List("../../"); err == nil {
		t.Fatal("List() should reject traversal path")
	}
	if _, err := adapter.Stat("../../outside.txt"); err == nil {
		t.Fatal("Stat() should reject traversal path")
	}
	if err := adapter.Delete("../../outside.txt"); err == nil {
		t.Fatal("Delete() should reject traversal path")
	}
}
