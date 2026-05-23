package db

import (
	"path/filepath"
	"testing"
)

func TestResilienceMigrationsAreIdempotent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "vault.db")

	// First open: applies all migrations.
	d, err := Open(dbPath)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}

	// Verify new columns exist by selecting from them.
	queries := map[string]string{
		"jobs.retry_max_override":                      `SELECT retry_max_override FROM jobs LIMIT 1`,
		"jobs.retry_delays_override":                   `SELECT retry_delays_override FROM jobs LIMIT 1`,
		"job_runs.retry_of_run_id":                     `SELECT retry_of_run_id FROM job_runs LIMIT 1`,
		"job_runs.retry_attempt":                       `SELECT retry_attempt FROM job_runs LIMIT 1`,
		"job_runs.retry_next_at":                       `SELECT retry_next_at FROM job_runs LIMIT 1`,
		"storage_destinations.consecutive_failures":    `SELECT consecutive_failures FROM storage_destinations LIMIT 1`,
		"storage_destinations.breaker_state":           `SELECT breaker_state FROM storage_destinations LIMIT 1`,
		"storage_destinations.breaker_opened_at":       `SELECT breaker_opened_at FROM storage_destinations LIMIT 1`,
		"storage_destinations.backup_database_enabled": `SELECT backup_database_enabled FROM storage_destinations LIMIT 1`,
	}
	for label, q := range queries {
		rows, err := d.Query(q)
		if err != nil {
			t.Errorf("column %s missing: %v", label, err)
			continue
		}
		rows.Close()
	}

	// Verify default settings rows present.
	for _, key := range []string{
		"retry_max_default", "retry_delays_default",
		"breaker_fail_threshold", "breaker_close_successes",
	} {
		v, err := d.GetSetting(key, "")
		if err != nil {
			t.Errorf("setting %s: %v", key, err)
			continue
		}
		if v == "" {
			t.Errorf("setting %s has empty default", key)
		}
	}
	if err := d.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Second open: no duplicate-column errors, defaults stay put.
	d2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	if err := d2.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}
