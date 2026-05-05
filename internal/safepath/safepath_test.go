package safepath

import "testing"

func TestNormalizeRelative(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		allowRoot bool
		want      string
		wantErr   bool
	}{
		{name: "nested path", input: "jobs/run1/backup.tar", want: "jobs/run1/backup.tar"},
		{name: "empty root allowed", input: "", allowRoot: true, want: "."},
		{name: "empty root denied", input: "", wantErr: true},
		{name: "traversal denied", input: "../../etc/passwd", wantErr: true},
		{name: "absolute denied", input: "/tmp/data", wantErr: true},
		{name: "cleaned local path", input: "./jobs/../jobs/run1", want: "jobs/run1"},
	}

	for _, tt := range tests {
		tc := tt
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := NormalizeRelative(tc.input, tc.allowRoot)
			if (err != nil) != tc.wantErr {
				t.Fatalf("NormalizeRelative() error = %v, wantErr %v", err, tc.wantErr)
			}
			if err == nil && got != tc.want {
				t.Fatalf("NormalizeRelative() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNormalizeAbsoluteUnderRoots(t *testing.T) {
	t.Parallel()

	roots := []string{"/mnt", "/boot"}
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "mnt child", input: "/mnt/cache/vault.db", want: "/mnt/cache/vault.db"},
		{name: "boot child", input: "/boot/config/plugins", want: "/boot/config/plugins"},
		{name: "outside root", input: "/etc/passwd", wantErr: true},
		{name: "relative denied", input: "mnt/cache", wantErr: true},
	}

	for _, tt := range tests {
		tc := tt
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := NormalizeAbsoluteUnderRoots(tc.input, roots)
			if (err != nil) != tc.wantErr {
				t.Fatalf("NormalizeAbsoluteUnderRoots() error = %v, wantErr %v", err, tc.wantErr)
			}
			if err == nil && got != tc.want {
				t.Fatalf("NormalizeAbsoluteUnderRoots() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNormalizeComponent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "simple", input: "template.xml", want: "template.xml"},
		{name: "spaces trimmed", input: " plugin ", want: "plugin"},
		{name: "slash denied", input: "../../plugin", wantErr: true},
		{name: "backslash denied", input: `..\\plugin`, wantErr: true},
		{name: "empty denied", input: "", wantErr: true},
	}

	for _, tt := range tests {
		tc := tt
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := NormalizeComponent(tc.input)
			if (err != nil) != tc.wantErr {
				t.Fatalf("NormalizeComponent() error = %v, wantErr %v", err, tc.wantErr)
			}
			if err == nil && got != tc.want {
				t.Fatalf("NormalizeComponent() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestJoinUnderBase(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		base      string
		path      string
		allowRoot bool
		want      string
		wantErr   bool
	}{
		{name: "joins relative path", base: "/var/data", path: "sub/file", allowRoot: false, want: "/var/data/sub/file"},
		{name: "root with allowRoot returns base", base: "/var/data", path: "", allowRoot: true, want: "/var/data"},
		{name: "rejects empty when not allowing root", base: "/var/data", path: "", allowRoot: false, wantErr: true},
		{name: "rejects path traversal", base: "/var/data", path: "../escape", allowRoot: false, wantErr: true},
		{name: "rejects absolute", base: "/var/data", path: "/etc/passwd", allowRoot: false, wantErr: true},
		{name: "cleans base path", base: "/var/data/", path: "x", allowRoot: false, want: "/var/data/x"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, err := JoinUnderBase(c.base, c.path, c.allowRoot)
			if c.wantErr {
				if err == nil {
					t.Errorf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestNormalizeRelativeEdges(t *testing.T) {
	t.Parallel()
	if _, err := NormalizeRelative("   ", true); err != nil {
		t.Errorf("whitespace-only with allowRoot should normalize to '.', got %v", err)
	}
	if _, err := NormalizeRelative("../parent", false); err == nil {
		t.Error("expected error for path traversal")
	}
	if _, err := NormalizeRelative("/etc/passwd", false); err == nil {
		t.Error("expected error for absolute path")
	}
	if got, err := NormalizeRelative("./a/./b/", false); err != nil {
		t.Errorf("clean path failed: %v", err)
	} else if got != "a/b" {
		t.Errorf("got %q, want %q", got, "a/b")
	}
}

func TestNormalizeAbsoluteUnderRootsEdges(t *testing.T) {
	t.Parallel()
	roots := []string{"/mnt", "/boot"}

	if _, err := NormalizeAbsoluteUnderRoots("   ", roots); err == nil {
		t.Error("empty path should error")
	}
	if _, err := NormalizeAbsoluteUnderRoots("relative/path", roots); err == nil {
		t.Error("relative path should error")
	}
	// Path matching root exactly
	if got, err := NormalizeAbsoluteUnderRoots("/mnt", roots); err != nil {
		t.Errorf("root path itself: %v", err)
	} else if got != "/mnt" {
		t.Errorf("got %q, want /mnt", got)
	}
	// Path outside any root
	if _, err := NormalizeAbsoluteUnderRoots("/etc/passwd", roots); err == nil {
		t.Error("path outside roots should error")
	}
}

func TestNormalizeComponentEdges(t *testing.T) {
	t.Parallel()
	if _, err := NormalizeComponent("   "); err == nil {
		t.Error("whitespace-only should error")
	}
	if _, err := NormalizeComponent("a/b"); err == nil {
		t.Error("forward slash should error")
	}
	if _, err := NormalizeComponent(`a\\b`); err == nil {
		t.Error("backslash should error")
	}
	if _, err := NormalizeComponent(".."); err == nil {
		t.Error("'..' should error")
	}
	if got, err := NormalizeComponent("file.txt"); err != nil || got != "file.txt" {
		t.Errorf("file.txt: got %q err %v", got, err)
	}
}
