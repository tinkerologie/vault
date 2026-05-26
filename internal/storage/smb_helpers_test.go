package storage

import (
	"path/filepath"
	"strings"
	"testing"
)

// ---- fullPath ---------------------------------------------------------

func TestSMBFullPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		base      string
		input     string
		allowRoot bool
		want      string
		wantErr   bool
	}{
		{name: "empty base, simple", base: "", input: "a/b.txt", want: filepath.Join("", "a/b.txt")},
		{name: "with base", base: "backups", input: "j/i.tar", want: filepath.Join("backups", "j/i.tar")},
		{name: "traversal rejected", base: "backups", input: "../escape", wantErr: true},
		{name: "empty requires allowRoot", base: "backups", input: "", wantErr: true},
		{name: "empty allowed at root", base: "backups", input: "", allowRoot: true, want: filepath.Clean("backups")},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a, err := NewSMBAdapter(SMBConfig{Host: "x", Share: "s", BasePath: tt.base})
			if err != nil {
				t.Fatalf("NewSMBAdapter: %v", err)
			}
			got, err := a.fullPath(tt.input, tt.allowRoot)
			if (err != nil) != tt.wantErr {
				t.Fatalf("fullPath(%q, %t) err=%v wantErr=%v", tt.input, tt.allowRoot, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got != tt.want {
				t.Errorf("fullPath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ---- basePathOrShareRoot ----------------------------------------------

func TestSMBBasePathOrShareRoot(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		base string
		want string
	}{
		{"empty -> .", "", "."},
		{"plain stays", "data", "data"},
		{"leading slash trimmed", "/data", "data"},
		{"trailing slash trimmed", "data/", "data"},
		{"backslash trimmed", "\\data\\", "data"},
		{"mixed slashes -> .", "//\\\\", "."},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := &SMBAdapter{config: SMBConfig{BasePath: tc.base}}
			if got := a.basePathOrShareRoot(); got != tc.want {
				t.Errorf("basePathOrShareRoot(%q) = %q, want %q", tc.base, got, tc.want)
			}
		})
	}
}

// ---- connect failure paths -------------------------------------------

// Helper: SMB adapter pointing at port 1, where dial refuses fast.
func unreachableSMB(t *testing.T) *SMBAdapter {
	t.Helper()
	a, err := NewSMBAdapter(SMBConfig{
		Host: "127.0.0.1", Port: 1, User: "u", Password: "p", Share: "s",
	})
	if err != nil {
		t.Fatalf("NewSMBAdapter: %v", err)
	}
	return a
}

func TestSMBConnect_BadHostErrors(t *testing.T) {
	t.Parallel()
	a := unreachableSMB(t)
	_, _, err := a.connect()
	if err == nil {
		t.Fatal("expected dial error")
	}
	if !strings.Contains(err.Error(), "smb dial") {
		t.Errorf("expected wrapped 'smb dial' error, got %v", err)
	}
}

func TestSMBOperations_ConnectFailurePropagates(t *testing.T) {
	t.Parallel()
	a := unreachableSMB(t)

	if err := a.Write("x", strings.NewReader("data")); err == nil {
		t.Error("Write: expected connect error")
	}
	if _, err := a.Read("x"); err == nil {
		t.Error("Read: expected connect error")
	}
	if _, err := a.ReadRange("x", 0, 1); err == nil {
		t.Error("ReadRange: expected connect error")
	}
	if err := a.Delete("x"); err == nil {
		t.Error("Delete: expected connect error")
	}
	if _, err := a.List("/"); err == nil {
		t.Error("List: expected connect error")
	}
	if _, err := a.Stat("x"); err == nil {
		t.Error("Stat: expected connect error")
	}
	if err := a.TestConnection(); err == nil {
		t.Error("TestConnection: expected connect error")
	}
}

func TestSMBReadRange_RejectsNegativeArgs(t *testing.T) {
	t.Parallel()
	a := unreachableSMB(t)
	if _, err := a.ReadRange("x", -1, 10); err == nil {
		t.Error("expected error on negative offset")
	}
	if _, err := a.ReadRange("x", 0, -1); err == nil {
		t.Error("expected error on negative length")
	}
}
