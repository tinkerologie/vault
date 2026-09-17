package handlers

import (
	"testing"
)

func TestNormalizeRestoreDestination(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "valid mnt", input: "/mnt/user/appdata", want: "/mnt/user/appdata", wantErr: false},
		{name: "valid boot", input: "/boot/config", want: "/boot/config", wantErr: false},
		{name: "valid tmp", input: "/tmp/restore_test", want: "/tmp/restore_test", wantErr: false},
		{name: "traversal with slash", input: "/mnt/../etc/passwd", wantErr: true},
		{name: "traversal with backslash", input: "/mnt/..\\etc/passwd", wantErr: true},
		{name: "trailing double dot slash", input: "/mnt/user/..", wantErr: true},
		{name: "trailing double dot backslash", input: "/mnt/user\\..", wantErr: true},
		{name: "relative path denied", input: "relative/path", wantErr: true},
		{name: "unapproved root denied", input: "/root/secret", wantErr: true},
		{name: "empty path denied", input: "", wantErr: true},
	}

	for _, tt := range tests {
		tc := tt
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := normalizeRestoreDestination(tc.input)
			if (err != nil) != tc.wantErr {
				t.Fatalf("normalizeRestoreDestination(%q) err = %v, wantErr = %v", tc.input, err, tc.wantErr)
			}
			if err == nil && got != tc.want {
				t.Errorf("normalizeRestoreDestination(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
