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
		{name: "valid plugin config", input: "/boot/config/plugins/foo", want: "/boot/config/plugins/foo", wantErr: false},
		{name: "rest of flash denied", input: "/boot/config", wantErr: true},
		{name: "system tree denied", input: "/etc/cron.d", wantErr: true},
		{name: "var denied", input: "/var/lib/foo", wantErr: true},
		{name: "usr local denied", input: "/usr/local/emhttp", wantErr: true},
		{name: "opt denied", input: "/opt/foo", wantErr: true},
		{name: "home denied", input: "/home/foo", wantErr: true},
		{name: "newline denied", input: "/mnt/user/x\ny", wantErr: true},
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
