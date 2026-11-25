package health

import (
	"testing"
	"time"

	"github.com/tinkerologie/vault/internal/db"
)

func TestJobIsStale_FollowsTheScheduledRuns(t *testing.T) {
	at := func(year int, month time.Month, day, hour int) time.Time {
		return time.Date(year, month, day, hour, 0, 0, 0, time.UTC)
	}
	cases := []struct {
		name   string
		spec   string
		newest time.Time
		now    time.Time
		want   bool
	}{
		// 2026-10-27 is a Tuesday. The next runs are Monday and Tuesday.
		{"Mon/Tue job in its long gap", "0 2 * * 1,2", at(2026, 10, 27, 2), at(2026, 11, 1, 12), false},
		{"Mon/Tue job one run missed", "0 2 * * 1,2", at(2026, 10, 27, 2), at(2026, 11, 2, 12), false},
		{"Mon/Tue job two runs missed, inside the day's grace", "0 2 * * 1,2", at(2026, 10, 27, 2), at(2026, 11, 4, 1), false},
		{"Mon/Tue job two runs missed", "0 2 * * 1,2", at(2026, 10, 27, 2), at(2026, 11, 4, 3), true},
		{"monthly job one run missed", "0 2 1 * *", at(2026, 9, 1, 2), at(2026, 10, 15, 0), false},
		{"monthly job two runs missed", "0 2 1 * *", at(2026, 9, 1, 2), at(2026, 11, 3, 0), true},
		{"last-day job one run missed", "0 2 L * *", at(2026, 8, 31, 2), at(2026, 10, 15, 0), false},
		{"last-day job two runs missed", "0 2 L * *", at(2026, 8, 31, 2), at(2026, 11, 2, 0), true},
		{"last-day yearly job within a year", "0 2 L 1 *", at(2026, 1, 31, 2), at(2026, 12, 1, 0), false},
		{"last-day Monday job between rare runs", "0 2 L * 1", at(2026, 8, 31, 2), at(2026, 11, 1, 0), false},
		{"last-day job in rare months on one weekday", "0 2 L 1,7 1", at(2025, 6, 1, 0), at(2026, 11, 1, 0), false},
		{"unparseable schedule falls back to age, recent", "not a schedule", at(2026, 10, 1, 0), at(2026, 10, 10, 0), false},
		{"unparseable schedule falls back to age, old", "not a schedule", at(2026, 8, 1, 0), at(2026, 10, 10, 0), true},
		// February's last day was a Monday in 2022 and next is in 2033.
		{"rare job before its next run, a decade away", "0 2 L 2 1", at(2022, 3, 1, 0), at(2030, 1, 1, 0), false},
		{"rare job after two missed runs", "0 2 L 2 1", at(2022, 3, 1, 0), at(2045, 1, 1, 0), true},
		{"never fires", "0 2 31 2 *", at(2026, 1, 15, 0), at(2030, 1, 1, 0), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			job := db.Job{Enabled: true, Schedule: tc.spec}
			if got := jobIsStale(job, tc.newest, tc.now); got != tc.want {
				t.Errorf("jobIsStale = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestScheduleStepper_LastDayMatchesTheSchedulersRuns(t *testing.T) {
	next, err := scheduleStepper("0 2 L * *")
	if err != nil {
		t.Fatal(err)
	}
	first, ok := next(time.Date(2026, time.January, 15, 12, 0, 0, 0, time.UTC))
	if !ok || !first.Equal(time.Date(2026, time.January, 31, 2, 0, 0, 0, time.UTC)) {
		t.Fatalf("first run = %v, %v", first, ok)
	}
	second, ok := next(first)
	if !ok || !second.Equal(time.Date(2026, time.February, 28, 2, 0, 0, 0, time.UTC)) {
		t.Fatalf("second run = %v, %v", second, ok)
	}
	if _, err := scheduleStepper("not a schedule"); err == nil {
		t.Error("an unparseable schedule should return an error")
	}
}

func TestScheduleStepper_RareRunsAndNeverFiring(t *testing.T) {
	next, err := scheduleStepper("0 2 L 2 1")
	if err != nil {
		t.Fatal(err)
	}
	first, ok := next(time.Date(2022, time.March, 1, 0, 0, 0, 0, time.UTC))
	if want := time.Date(2033, time.February, 28, 2, 0, 0, 0, time.UTC); !ok || !first.Equal(want) {
		t.Fatalf("first run = %v, %v, want %v", first, ok, want)
	}
	never, err := scheduleStepper("0 2 31 2 *")
	if err != nil {
		t.Fatal(err)
	}
	if at, ok := never(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)); ok || !at.IsZero() {
		t.Errorf("never-firing schedule = %v, %v", at, ok)
	}
}
