package engine

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPathChangedSinceFileAndDirectory(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	unchangedDir := filepath.Join(root, "unchanged")
	changedDir := filepath.Join(root, "changed")
	if err := os.MkdirAll(unchangedDir, 0755); err != nil {
		t.Fatalf("MkdirAll unchangedDir: %v", err)
	}
	if err := os.MkdirAll(changedDir, 0755); err != nil {
		t.Fatalf("MkdirAll changedDir: %v", err)
	}

	oldFile := filepath.Join(unchangedDir, "old.txt")
	newFile := filepath.Join(changedDir, "new.txt")
	if err := os.WriteFile(oldFile, []byte("old"), 0644); err != nil {
		t.Fatalf("WriteFile old: %v", err)
	}
	if err := os.WriteFile(newFile, []byte("new"), 0644); err != nil {
		t.Fatalf("WriteFile new: %v", err)
	}

	reference := time.Now()
	oldTime := reference.Add(-2 * time.Hour)
	newTime := reference.Add(2 * time.Hour)
	if err := os.Chtimes(oldFile, oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes old: %v", err)
	}
	if err := os.Chtimes(newFile, newTime, newTime); err != nil {
		t.Fatalf("Chtimes new: %v", err)
	}

	changed, err := pathChangedSince(oldFile, reference)
	if err != nil {
		t.Fatalf("pathChangedSince(oldFile) error = %v", err)
	}
	if changed {
		t.Fatal("expected unchanged file to be skipped")
	}

	changed, err = pathChangedSince(newFile, reference)
	if err != nil {
		t.Fatalf("pathChangedSince(newFile) error = %v", err)
	}
	if !changed {
		t.Fatal("expected changed file to be detected")
	}

	changed, err = pathChangedSince(unchangedDir, reference)
	if err != nil {
		t.Fatalf("pathChangedSince(unchangedDir) error = %v", err)
	}
	if changed {
		t.Fatal("expected unchanged directory to be skipped")
	}

	changed, err = pathChangedSince(changedDir, reference)
	if err != nil {
		t.Fatalf("pathChangedSince(changedDir) error = %v", err)
	}
	if !changed {
		t.Fatal("expected changed directory to be detected")
	}
}

func TestFilterChangedDomainDisksPreservesIndexes(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	firstDisk := filepath.Join(root, "disk0.qcow2")
	secondDisk := filepath.Join(root, "disk1.qcow2")
	if err := os.WriteFile(firstDisk, []byte("first"), 0644); err != nil {
		t.Fatalf("WriteFile firstDisk: %v", err)
	}
	if err := os.WriteFile(secondDisk, []byte("second"), 0644); err != nil {
		t.Fatalf("WriteFile secondDisk: %v", err)
	}

	reference := time.Now()
	oldTime := reference.Add(-2 * time.Hour)
	newTime := reference.Add(2 * time.Hour)
	if err := os.Chtimes(firstDisk, oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes firstDisk: %v", err)
	}
	if err := os.Chtimes(secondDisk, newTime, newTime); err != nil {
		t.Fatalf("Chtimes secondDisk: %v", err)
	}

	disks := []domainDisk{{Index: 0, Path: firstDisk, Target: "vda"}, {Index: 1, Path: secondDisk, Target: "vdb"}}
	changed, err := filterChangedDomainDisks(disks, reference)
	if err != nil {
		t.Fatalf("filterChangedDomainDisks() error = %v", err)
	}
	if len(changed) != 1 {
		t.Fatalf("expected 1 changed disk, got %d", len(changed))
	}
	if changed[0].Index != 1 || changed[0].Target != "vdb" {
		t.Fatalf("unexpected changed disk: %+v", changed[0])
	}
}
