package engine

import (
	"math"
	"testing"
)

func TestHumanizeBytes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   float64
		want string
	}{
		{"zero", 0, "0 B"},
		{"whole bytes", 512, "512 B"},
		{"kilobytes", 1536, "1.5 KB"},
		{"megabytes", 1572864, "1.5 MB"},
		{"processed from issue #133", 6453198848, "6 GB"},
		{"total from issue #133", 34359738368, "32 GB"},
		{"terabytes", 1099511627776, "1 TB"},
		{"negative", -1536, "-1.5 KB"},
		{"NaN", math.NaN(), "—"},
		{"infinity", math.Inf(1), "—"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := humanizeBytes(c.in); got != c.want {
				t.Errorf("humanizeBytes(%v) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
