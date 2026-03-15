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
