package health

import (
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/tinkerologie/vault/internal/db"
)

// defaultStaleAfter is how long a job with no cadence to judge it by (disabled,
// manual-only, or a schedule cron can't parse such as "L") may go without a
// successful backup before the dashboard flags it.
const defaultStaleAfter = 35 * 24 * time.Hour

// maxScheduleGap bounds how far ahead a schedule is searched for its next run. The
// rarest valid schedules, such as a last day that must also be a given weekday in
// one month, repeat within about 12 years, so only a schedule that never fires
// reaches it.
const maxScheduleGap = 40 * 365 * 24 * time.Hour

// jobIsStale reports whether a job's newest backup is old enough to raise an
// alarm: two scheduled runs have since come and gone, plus a day, so one late or
// retried run doesn't count. Walking the runs themselves keeps irregular schedules
// such as "Mondays and Tuesdays" quiet through their long gap. A disabled schedule
// never fires, so only age says anything about it.
func jobIsStale(job db.Job, newest, now time.Time) bool {
	if !job.Enabled || job.Schedule == "" {
		return now.Sub(newest) > defaultStaleAfter
	}
	next, err := scheduleStepper(job.Schedule)
	if err != nil {
		return now.Sub(newest) > defaultStaleAfter
	}
	first, _ := next(newest)
	second, ok := next(first)
	return ok && now.After(second.Add(24*time.Hour))
}

// scheduleStepper returns a function giving a schedule's next run after t. It
// reports false when the schedule doesn't fire within maxScheduleGap.
func scheduleStepper(spec string) (func(time.Time) (time.Time, bool), error) {
	fields := strings.Fields(spec)
	lastDayOnly := len(fields) == 5 && fields[2] == "L"
	if lastDayOnly {
		// The scheduler runs "L" as a daily trigger that fires only on the month's
		// last day, so step through the same days.
		fields[2] = "*"
		spec = strings.Join(fields, " ")
	}
	schedule, err := cron.ParseStandard(spec)
	if err != nil {
		return nil, err
	}
	return func(t time.Time) (time.Time, bool) {
		limit := t.Add(maxScheduleGap)
		for at := schedule.Next(t); !at.IsZero() && at.Before(limit); at = schedule.Next(at) {
			if !lastDayOnly || at.AddDate(0, 0, 1).Month() != at.Month() {
				return at, true
			}
		}
		return time.Time{}, false
	}, nil
}
