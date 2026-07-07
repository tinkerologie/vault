package unraid

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRecycleBinInstalled(t *testing.T) {
	orig := recycleBinPlgPath
	t.Cleanup(func() { recycleBinPlgPath = orig })

	tests := []struct {
		name   string
		create bool
		want   bool
	}{
		{name: "absent installer", create: false, want: false},
		{name: "present installer", create: true, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			recycleBinPlgPath = filepath.Join(t.TempDir(), "recycle.bin.plg")
			if tc.create {
				if err := os.WriteFile(recycleBinPlgPath, []byte("<PLUGIN/>"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if got := RecycleBinInstalled(); got != tc.want {
				t.Errorf("RecycleBinInstalled() = %v, want %v", got, tc.want)
			}
		})
	}
}
