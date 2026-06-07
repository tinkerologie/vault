package anomaly

import (
	"math"
	"testing"
)

func TestHumanizeBytes(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1 KB"},
		{1536, "1.5 KB"},
		{1048576, "1 MB"},
		{4259532913, "4 GB"}, // observed in issue screenshot
		{5397551730, "5 GB"}, // expected in issue screenshot
		{1024 * 1024 * 1024 * 1024, "1 TB"},
		{-2048, "-2 KB"},
		{math.Inf(1), "—"},
		{math.NaN(), "—"},
	}
	for _, c := range cases {
		if got := humanizeBytes(c.in); got != c.want {
			t.Errorf("humanizeBytes(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestHumanizeDuration(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "0s"},
		{45, "45s"},
		{59, "59s"},
		{60, "1m 0s"},
		{315, "5m 15s"}, // observed in issue screenshot
		{326, "5m 26s"}, // duration anomaly in issue screenshot
		{409, "6m 49s"}, // expected in issue screenshot
		{3600, "1h 0m"},
		{8000, "2h 13m"},
		{-5, "—"},
		{math.Inf(1), "—"},
	}
	for _, c := range cases {
		if got := humanizeDuration(c.in); got != c.want {
			t.Errorf("humanizeDuration(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
