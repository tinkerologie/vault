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

func TestRoundTo(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		in       float64
		decimals int
		want     float64
	}{
		{"z-score from issue #134", -16.76413455138884, 2, -16.76},
		{"growth factor from issue #134", 0.5766537578335602, 2, 0.58},
		{"eta days one decimal", 12.3456, 1, 12.3},
		{"whole bytes", 1234.567, 0, 1235},
		{"already round", 3.5, 2, 3.5},
		{"negative half rounds away from zero", -2.345, 2, -2.35},
		{"zero", 0, 2, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := roundTo(c.in, c.decimals); got != c.want {
				t.Errorf("roundTo(%v, %d) = %v, want %v", c.in, c.decimals, got, c.want)
			}
		})
	}

	// Non-finite values pass through unchanged.
	if got := roundTo(math.NaN(), 2); !math.IsNaN(got) {
		t.Errorf("roundTo(NaN, 2) = %v, want NaN", got)
	}
	if got := roundTo(math.Inf(1), 2); !math.IsInf(got, 1) {
		t.Errorf("roundTo(+Inf, 2) = %v, want +Inf", got)
	}
}
