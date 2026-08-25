package anomaly

import (
	"math"
	"testing"
)

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

func TestHumanizePercent(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   float64
		want string
	}{
		{"half", 0.5, "50%"},
		{"rounds", 0.456, "46%"},
		{"tiny", 0.02, "2%"},
		{"exactly zero", 0, "0%"},
		{"non-zero sub-percent never reads zero", 0.004, "<1%"},
		{"infinity", math.Inf(1), "—"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := humanizePercent(c.in); got != c.want {
				t.Errorf("humanizePercent(%v) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestHumanizeDays(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   float64
		want string
	}{
		{"sub-day", 0.4, "less than a day"},
		{"one day", 1, "1 day"},
		{"rounds to one", 1.2, "1 day"},
		{"several", 5.6, "6 days"},
		{"negative", -3, "—"},
		{"infinity", math.Inf(1), "—"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := humanizeDays(c.in); got != c.want {
				t.Errorf("humanizeDays(%v) = %q, want %q", c.in, got, c.want)
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

func TestCommaGroup(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   int64
		want string
	}{
		{"zero", 0, "0"},
		{"under a thousand", 233, "233"},
		{"max no-comma", 999, "999"},
		{"exactly a thousand", 1000, "1,000"},
		{"four digits", 4000, "4,000"},
		{"five digits", 49900, "49,900"},
		{"six digits", 499900, "499,900"},
		{"seven digits", 1234567, "1,234,567"},
		{"negative", -4000, "-4,000"},
		{"max int64", math.MaxInt64, "9,223,372,036,854,775,807"},
		{"min int64", math.MinInt64, "-9,223,372,036,854,775,808"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := commaGroup(c.in); got != c.want {
				t.Errorf("commaGroup(%d) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestHumanizePercentChange(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   float64
		want string
	}{
		{"growth 1.3x", 1.3, "increased by 30%"},
		{"shrink 0.35x", 0.35, "decreased by 65%"},
		{"whole 5x", 5, "increased by 400%"},
		{"comma at thousands boundary", 11, "increased by 1,000%"},
		{"comma in large growth", 1000, "increased by 99,900%"},
		{"marginal shrink 0.964x", 0.964, "decreased by 4%"},
		{"tiny growth clamps to 1", 1.004, "increased by 1%"},
		{"tiny shrink clamps to 1", 0.999, "decreased by 1%"},
		{"infinity", math.Inf(1), "—"},
		{"NaN", math.NaN(), "—"},
		{"huge growth saturates", 1e18, "increased by 9,223,372,036,854,775,807%"},
		{"overflow to inf saturates", 1e308, "increased by 9,223,372,036,854,775,807%"},
		{"huge shrink saturates", -1e308, "decreased by 9,223,372,036,854,775,807%"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := humanizePercentChange(c.in); got != c.want {
				t.Errorf("humanizePercentChange(%v) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
