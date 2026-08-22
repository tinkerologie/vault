package db

import (
	"fmt"
	"path/filepath"
	"testing"
)

func openActivityTestDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// TestCreateActivityLogEnforcesCap mirrors TestAppendRunLogEnforcesPerRunCap:
// with the table already at MaxActivityLogRows, one more insert through the
// public API must trim back to the cap — oldest entry evicted, newest kept.
func TestCreateActivityLogEnforcesCap(t *testing.T) {
	d := openActivityTestDB(t)

	// Seed exactly cap rows in one transaction via the internal Exec path
	// (LogActivity would pay the prune cost on every seed row).
	tx, err := d.Begin()
	if err != nil {
		t.Fatalf("begin seed tx: %v", err)
	}
	for i := 0; i < MaxActivityLogRows; i += 500 {
		// Batch the seed with a multi-row insert to keep the test fast.
		values := ""
		args := make([]any, 0, 500*4)
		for j := 0; j < 500 && i+j < MaxActivityLogRows; j++ {
			if values != "" {
				values += ","
			}
			values += "(?, 'backup', ?, '{}')"
			args = append(args, fmt.Sprintf("info"), fmt.Sprintf("seed-%d", i+j))
		}
		if _, err := tx.Exec("INSERT INTO activity_log (level, category, message, details) VALUES "+values, args...); err != nil {
			t.Fatalf("seed batch at %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	// One insert through the public API must trim the table back to the cap.
	if _, err := d.CreateActivityLog(ActivityLogEntry{Level: "info", Category: "backup", Message: "newest", Details: "{}"}); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Single-scenario integration test: at the cap, one more insert through
	// the public API must evict the oldest row and keep the newest.
	cases := []struct {
		name string
	}{
		{name: "insert at cap trims back to MaxActivityLogRows"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var n int
			if err := d.QueryRow("SELECT COUNT(*) FROM activity_log").Scan(&n); err != nil {
				t.Fatalf("count: %v", err)
			}
			if n != MaxActivityLogRows {
				t.Fatalf("rows = %d, want cap %d", n, MaxActivityLogRows)
			}

			// The oldest seed row was evicted; the newest entry survived.
			var oldest string
			if err := d.QueryRow("SELECT message FROM activity_log ORDER BY id ASC LIMIT 1").Scan(&oldest); err != nil {
				t.Fatalf("oldest: %v", err)
			}
			if oldest == "seed-0" {
				t.Fatalf("oldest row is still seed-0; cap prune did not run")
			}
			var newest string
			if err := d.QueryRow("SELECT message FROM activity_log ORDER BY id DESC LIMIT 1").Scan(&newest); err != nil {
				t.Fatalf("newest: %v", err)
			}
			if newest != "newest" {
				t.Fatalf("newest row = %q, want \"newest\"", newest)
			}
		})
	}
}
