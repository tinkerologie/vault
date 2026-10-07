package fsstat

import (
	"os"
	"testing"
)

// TestStatReportsCapacityForTempDir checks the platform call returns a
// non-empty, self-consistent capacity for a directory that always exists.
func TestStatReportsCapacityForTempDir(t *testing.T) {
	u, err := Stat(t.TempDir())
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if u.Total == 0 || u.Free > u.Total {
		t.Fatalf("Stat = %+v, want Total > 0 and Free <= Total", u)
	}
}

// TestStatMissingPathFails checks a missing path is an error, not zeros.
func TestStatMissingPathFails(t *testing.T) {
	if _, err := Stat(t.TempDir() + "/does-not-exist"); err == nil {
		t.Fatal("Stat on a missing path returned no error")
	}
}

// TestStatAcceptsFilePath checks a path naming a file reports its
// filesystem, as statfs does on Unix, on every platform.
func TestStatAcceptsFilePath(t *testing.T) {
	file := t.TempDir() + "/f.txt"
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if u, err := Stat(file); err != nil || u.Total == 0 {
		t.Fatalf("Stat(file) = %+v, %v", u, err)
	}
}
