package storage

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestDirRemoverLocalRemovesEmptyDir(t *testing.T) {
	root := t.TempDir()
	var a Adapter = NewLocalAdapter(root)
	if err := a.Write("job/run1/vol0.bin", bytes.NewReader([]byte("x"))); err != nil {
		t.Fatal(err)
	}
	if err := a.Delete("job/run1/vol0.bin"); err != nil {
		t.Fatal(err)
	}
	dr, ok := a.(dirRemover)
	if !ok {
		t.Fatal("LocalAdapter must implement dirRemover")
	}
	// relative path form, same as Delete takes:
	if err := dr.RemoveEmptyDir("job/run1"); err != nil {
		t.Fatalf("RemoveEmptyDir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "job/run1")); !os.IsNotExist(err) {
		t.Errorf("empty dir should be gone, stat err = %v", err)
	}
}

func TestDirRemoverLocalFailsOnNonEmpty(t *testing.T) {
	root := t.TempDir()
	var a Adapter = NewLocalAdapter(root)
	if err := a.Write("job/run1/vol0.bin", bytes.NewReader([]byte("x"))); err != nil {
		t.Fatal(err)
	}
	dr := a.(dirRemover)
	if err := dr.RemoveEmptyDir("job/run1"); err == nil {
		t.Error("RemoveEmptyDir on non-empty dir should error")
	}
}
