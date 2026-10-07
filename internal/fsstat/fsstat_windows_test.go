//go:build windows

package fsstat

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWithTrailingSeparator(t *testing.T) {
	for in, want := range map[string]string{
		`\\server\share`:  `\\server\share\`,
		`\\server\share\`: `\\server\share\`,
		`C:\`:             `C:\`,
		`C:\data`:         `C:\data\`,
		`C:/data/`:        `C:/data/`,
	} {
		if got := withTrailingSeparator(in); got != want {
			t.Errorf("withTrailingSeparator(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestVolumeDirUsesParentOfFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := volumeDir(file)
	if err != nil || got != dir+`\` {
		t.Fatalf("volumeDir(file) = %q, %v; want %q", got, err, dir+`\`)
	}
}
