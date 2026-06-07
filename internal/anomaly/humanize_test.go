package anomaly

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
		{"1 KB", 1024, "1 KB"},
		{"1.5 KB", 1536, "1.5 KB"},
		{"1 MB", 1048576, "1 MB"},
		{"4 GB observed", 4259532913, "4 GB"}, // observed in issue screenshot
		{"5 GB expected", 5397551730, "5 GB"}, // expected in issue screenshot
		{"1 TB", 1024 * 1024 * 1024 * 1024, "1 TB"},
		{"negative", -2048, "-2 KB"},
		{"infinity", math.Inf(1), "—"},
		{"NaN", math.NaN(), "—"},
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

func TestHumanizeDuration(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   float64
		want string
	}{
		{"zero", 0, "0s"},
		{"45 seconds", 45, "45s"},
		{"59 seconds", 59, "59s"},
		{"1 minute", 60, "1m 0s"},
		{"5m 15s observed", 315, "5m 15s"}, // observed in issue screenshot
		{"5m 26s anomaly", 326, "5m 26s"},  // duration anomaly in issue screenshot
		{"6m 49s expected", 409, "6m 49s"}, // expected in issue screenshot
		{"1 hour", 3600, "1h 0m"},
		{"2h 13m", 8000, "2h 13m"},
		{"negative", -5, "—"},
		{"infinity", math.Inf(1), "—"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := humanizeDuration(c.in); got != c.want {
				t.Errorf("humanizeDuration(%v) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
