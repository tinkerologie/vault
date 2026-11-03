package storage

import "testing"

func TestPathsOverlap(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		a, b string
		want bool
	}{
		{"job/run", "job/run", true},
		{"job/run/", "job/run", true},
		{"job/run/sub", "job/run", true},
		{"job/run", "job/run/sub", true},
		{"job/run", "job/run2", false},
		{"job/run", "other/run", false},
		{"My Job/run", "My_Job/run", true},
		{`victim\run`, "victim/run", true},
		{"victim/run/sub", `victim\run`, true},
	} {
		if got := PathsOverlap(tt.a, tt.b); got != tt.want {
			t.Errorf("PathsOverlap(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}
