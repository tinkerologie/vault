package handlers

import (
	"sort"
	"time"

	"github.com/tinkerologie/vault/internal/db"
)

// protectionClassifier sorts every Job Item into one protection state, once, so
// the health summary (Dashboard, MCP, Home Assistant) and the recovery plan
// (Disaster Recovery) agree on which items are protected (#708, #694):
//
//   - protected: a retained Restore Point contains the item, and the newest one
//     is recent enough for the job's schedule;
//   - out of date: a Restore Point contains it, but the newest is older than the
//     schedule allows (the same rule as stale_jobs, see jobIsStale);
//   - pending: in a job, but in no Restore Point yet.
//
// Items are keyed "type:name" and counted once however many jobs cover them. An
// item is protected if any job covering it has a recent enough Restore Point.
type protectionClassifier struct {
	now     time.Time
	all     map[string]struct{}
	backed  map[string]struct{}
	current map[string]struct{}
	newest  map[string]time.Time
}

func newProtectionClassifier(now time.Time) *protectionClassifier {
	return &protectionClassifier{
		now:     now,
		all:     map[string]struct{}{},
		backed:  map[string]struct{}{},
		current: map[string]struct{}{},
		newest:  map[string]time.Time{},
	}
}

// addJob records one job's items and its Restore Points, newest first as
// ListRestorePoints returns them.
func (c *protectionClassifier) addJob(job db.Job, items []db.JobItem, rps []db.RestorePoint) {
	newestByName := newestRestorePointPerItem(items, rps)
	for _, item := range items {
		key := item.ItemType + ":" + item.ItemName
		c.all[key] = struct{}{}
		at, ok := newestByName[item.ItemName]
		if !ok {
			continue
		}
		c.backed[key] = struct{}{}
		if prev, seen := c.newest[key]; !seen || at.After(prev) {
			c.newest[key] = at
		}
		// A replicated job runs on its source, so its age here says nothing.
		if job.SourceID != 0 || !jobIsStale(job, at, c.now) {
			c.current[key] = struct{}{}
		}
	}
}

// newestRestorePointPerItem maps each item name to the creation time of the
// newest Restore Point that contains it. A legacy Restore Point, recorded before
// per-item membership was, counts for every item in the job.
func newestRestorePointPerItem(items []db.JobItem, rps []db.RestorePoint) map[string]time.Time {
	newest := map[string]time.Time{}
	note := func(name string, at time.Time) {
		if prev, ok := newest[name]; !ok || at.After(prev) {
			newest[name] = at
		}
	}
	for _, rp := range rps {
		members, known := rp.BackedUpItems()
		if !known {
			for _, item := range items {
				note(item.ItemName, rp.CreatedAt)
			}
			continue
		}
		for name := range members {
			note(name, rp.CreatedAt)
		}
	}
	return newest
}

// isBacked reports whether any Restore Point contains the item.
func (c *protectionClassifier) isBacked(key string) bool {
	_, ok := c.backed[key]
	return ok
}

// isOutOfDate reports whether the item has Restore Points, all too old.
func (c *protectionClassifier) isOutOfDate(key string) bool {
	_, current := c.current[key]
	return c.isBacked(key) && !current
}

// newestBackup is the newest Restore Point holding the item, if any.
func (c *protectionClassifier) newestBackup(key string) (time.Time, bool) {
	at, ok := c.newest[key]
	return at, ok
}

// newestBackups maps each backed-up item to its newest Restore Point, so a
// page can say "Last backup 9 days ago" for an out-of-date item.
func (c *protectionClassifier) newestBackups() map[string]time.Time {
	out := make(map[string]time.Time, len(c.newest))
	for key, at := range c.newest {
		out[key] = at.UTC()
	}
	return out
}

// keys returns the sorted keys of every item in the given state.
func (c *protectionClassifier) keys(include func(key string) bool) []string {
	out := []string{}
	for key := range c.all {
		if include(key) {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

func (c *protectionClassifier) backedKeys() []string { return c.keys(c.isBacked) }

func (c *protectionClassifier) outOfDateKeys() []string { return c.keys(c.isOutOfDate) }

func (c *protectionClassifier) pendingKeys() []string {
	return c.keys(func(key string) bool { return !c.isBacked(key) })
}
