package scheduler

import (
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// MaxPreviewRuns caps how many upcoming runs PreviewRuns returns.
const MaxPreviewRuns = 10

// maxLastDayCandidates bounds the daily triggers an "L" schedule may step
// through per preview, so a schedule that rarely matches can't spin.
const maxLastDayCandidates = 4000

// PreviewRuns returns up to n upcoming run times of spec after from, computed
// the way the scheduler fires it: the standard parser, in from's location, with
// the "L" (last day of month) token handled by a daily trigger filtered to the
// month's last day. An empty spec is a manual-only job and has no runs.
func PreviewRuns(spec string, from time.Time, n int) ([]time.Time, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" || n <= 0 {
		return nil, nil
	}
	if n > MaxPreviewRuns {
		n = MaxPreviewRuns
	}
	lastDay := false
	if normalized, ok := parseLastDaySchedule(spec); ok {
		spec = normalized
		lastDay = true
	}
	sched, err := cron.ParseStandard(spec)
	if err != nil {
		return nil, fmt.Errorf("parsing schedule: %w", err)
	}
	runs := make([]time.Time, 0, n)
	t := from
	for i := 0; len(runs) < n && i < maxLastDayCandidates; i++ {
		t = sched.Next(t)
		if t.IsZero() {
			break // the parser found no match within its search window
		}
		if lastDay && !isLastDayOfMonth(t) {
			continue
		}
		runs = append(runs, t)
	}
	return runs, nil
}
