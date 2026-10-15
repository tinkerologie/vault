package format

import (
	"math"
	"testing"
	"time"
)

// TestBytes pins the single project-wide rendering contract. The values here
// are the ones that used to differ between implementations: 1536 rendered as
// "2 KB" in the notification formatter but "1.5 KB" everywhere else, and a
// count just under a boundary rendered as "1024 KB" rather than promoting to
// the next unit.
func TestBytes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		bytes float64
		want  string
	}{
		{0, "0 B"},
		{1, "1 B"},
		{1023, "1023 B"},
		{1024, "1 KB"},
		{1536, "1.5 KB"}, // was "2 KB"
		{2048, "2 KB"},
		// Just under a boundary promotes rather than printing "1024 KB".
		{1024*1024 - 1, "1 MB"},
		{1024 * 1024, "1 MB"},
		{1536 * 1024, "1.5 MB"},
		{1 << 30, "1 GB"},
		{1536 * 1024 * 1024, "1.5 GB"},
		{1 << 40, "1 TB"},
		{2500 * (1 << 30), "2.4 TB"},
		{1<<50 - 1, "1 PB"}, // must not render as "1024 TB" (web parity)
		{1 << 50, "1 PB"},
		// Beyond the largest unit it keeps scaling PB rather than wrapping.
		{1024 * (1 << 50), "1024 PB"},
		{-1536, "-1.5 KB"},
		// Tie: Go's formatter would break this to even ("1.2 KB") while
		// JavaScript rounds it up, so both now round half away from zero.
		{1280, "1.3 KB"},
		{math.Copysign(0, -1), "0 B"}, // -0 must not render as "-0 B"
		// Sub-kilobyte ties. A transfer rate is a division, so fractional
		// byte counts do reach here; %.0f alone would break these to even.
		{0.5, "1 B"},
		{2.5, "3 B"},
	}
	for _, c := range cases {
		if got := Bytes(c.bytes); got != c.want {
			t.Errorf("Bytes(%v) = %q, want %q", c.bytes, got, c.want)
		}
	}
}

// TestBytesNonFinite covers inputs that can reach this from a computed rate
// (bytes divided by a duration), where a zero or missing duration can produce
// NaN or Inf. Rendering those as a number would be worse than saying nothing.
func TestBytesNonFinite(t *testing.T) {
	t.Parallel()
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if got := Bytes(v); got != "—" {
			t.Errorf("Bytes(%v) = %q, want %q", v, got, "—")
		}
	}
}

func TestDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{-time.Second, "—"},
		{0, "<1s"},
		{3 * time.Millisecond, "<1s"},
		{999 * time.Millisecond, "<1s"},
		{time.Second, "1.0s"},
		{1049 * time.Millisecond, "1.0s"},
		{1050 * time.Millisecond, "1.1s"},
		{2400 * time.Millisecond, "2.4s"},
		{59940 * time.Millisecond, "59.9s"},
		{59960 * time.Millisecond, "1m0s"},
		{time.Minute, "1m0s"},
		{65*time.Second + 700*time.Millisecond, "1m6s"},
		{11*time.Hour + 3*time.Minute, "11h3m0s"},
	}
	for _, tc := range cases {
		if got := Duration(tc.d); got != tc.want {
			t.Errorf("Duration(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestThroughput(t *testing.T) {
	cases := []struct {
		name   string
		bytes  int64
		d      time.Duration
		want   string
		wantOK bool
	}{
		{"sub-second restore is not rated", 1024, 3 * time.Millisecond, "", false},
		{"just below the minimum", 10 << 30, 999 * time.Millisecond, "", false},
		{"exactly the minimum", 10 << 20, time.Second, "10 MB/s", true},
		{"fractional seconds divide precisely", 3 << 20, 1500 * time.Millisecond, "2 MB/s", true},
		{"zero bytes", 0, time.Minute, "", false},
		{"negative bytes", -5, time.Minute, "", false},
		{"long transfer", 225 << 30, 11 * time.Hour, "5.8 MB/s", true},
		{"very low rate", 10, 10 * time.Second, "1 B/s", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Throughput(tc.bytes, tc.d)
			if got != tc.want || ok != tc.wantOK {
				t.Errorf("Throughput(%d, %v) = (%q, %v), want (%q, %v)", tc.bytes, tc.d, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}
