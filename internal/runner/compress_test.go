package runner

import (
	"testing"
)

func TestFileCompressionExt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		want string
	}{
		{"backup.tar.gz", ".gz"},
		{"backup.tar.zst", ".zst"},
		{"backup.tar.gz.age", ".gz"},
		{"backup.tar.zst.age", ".zst"},
		{"backup.tar", ""},
		{"backup.tar.age", ""},
		{"simple.age", ""},
		{"file.gz", ".gz"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := fileCompressionExt(tt.name)
			if got != tt.want {
				t.Errorf("fileCompressionExt(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}
