package db

import (
	"path/filepath"
	"testing"
)

func TestAnomalyMigrationsAreIdempotent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "vault.db")

	// First open: applies schema + alter migrations + seeds settings.
	d, err := Open(dbPath)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}

	// Verify new tables exist and are empty.
	tables := []string{"anomalies", "job_baselines", "destination_capacity_samples"}
	for _, tbl := range tables {
		var count int
		if err := d.QueryRow("SELECT COUNT(*) FROM " + tbl).Scan(&count); err != nil {
			t.Errorf("table %s missing or unreadable: %v", tbl, err)
			continue
		}
		if count != 0 {
			t.Errorf("table %s: expected 0 rows, got %d", tbl, count)
		}
	}

	// Verify new columns on jobs and storage_destinations.
	colQueries := map[string]string{
		"jobs.anomaly_sensitivity":                 `SELECT anomaly_sensitivity FROM jobs LIMIT 1`,
		"storage_destinations.anomaly_sensitivity": `SELECT anomaly_sensitivity FROM storage_destinations LIMIT 1`,
	}
	for label, q := range colQueries {
		rows, err := d.Query(q)
		if err != nil {
			t.Errorf("column %s missing: %v", label, err)
			continue
		}
		rows.Close()
	}

	// Verify default settings were seeded.
	for key, want := range map[string]string{
		"anomaly_detection_enabled":   "true",
		"anomaly_sensitivity_default": "balanced",
		"anomaly_notify_min_severity": "critical",
	} {
		got, err := d.GetSetting(key, "")
		if err != nil {
			t.Errorf("setting %s: %v", key, err)
			continue
		}
		if got != want {
			t.Errorf("setting %s: got %q, want %q", key, got, want)
		}
	}

	if err := d.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Second open: no duplicate-column or duplicate-table errors.
	d2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("second open (idempotency check): %v", err)
	}

	// COUNT still 0 after re-open.
	var count int
	if err := d2.QueryRow(`SELECT COUNT(*) FROM anomalies`).Scan(&count); err != nil {
		t.Fatalf("SELECT COUNT(*) FROM anomalies on second open: %v", err)
	}
	if count != 0 {
		t.Errorf("anomalies: expected 0 rows after re-open, got %d", count)
	}

	if err := d2.Close(); err != nil {
		t.Fatalf("close second: %v", err)
	}
}

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
