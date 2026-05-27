package db

import (
	"context"
	"fmt"
)

// ConfigurationSummary reports whether the database appears to hold real
// operator configuration vs. a fresh empty schema. Returned by
// ValidateHasConfiguration and surfaced via the /api/v1/health endpoint
// so support can tell at a glance whether a daemon started with the
// expected configuration after restoration (issue #108).
//
// HasConfiguration is true iff at least one row exists in either the
// jobs table or the storage_destinations table — those are the
// authoritative signals that the operator has configured Vault.
// Settings rows alone don't qualify, because default values are seeded
// at first DB open and would otherwise mask a true fresh-start.
type ConfigurationSummary struct {
	HasConfiguration bool   `json:"has_configuration"`
	Jobs             int    `json:"jobs"`
	StorageDests     int    `json:"storage_destinations"`
	Settings         int    `json:"settings"`
	Note             string `json:"note,omitempty"`
}

// ValidateHasConfiguration reports whether the database contains
// operator-supplied configuration (≥1 job OR ≥1 storage destination).
// Lightweight enough to call at startup — three COUNT(*) queries
// against indexed primary keys.
func (d *DB) ValidateHasConfiguration(ctx context.Context) (*ConfigurationSummary, error) {
	summary := &ConfigurationSummary{}

	count := func(table string) (int, error) {
		var n int
		row := d.DB.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s", table)) // #nosec G201 — table names are compile-time constants below
		if err := row.Scan(&n); err != nil {
			return 0, fmt.Errorf("counting %s: %w", table, err)
		}
		return n, nil
	}

	var err error
	if summary.Jobs, err = count("jobs"); err != nil {
		return nil, err
	}
	if summary.StorageDests, err = count("storage_destinations"); err != nil {
		return nil, err
	}
	if summary.Settings, err = count("settings"); err != nil {
		return nil, err
	}

	summary.HasConfiguration = summary.Jobs > 0 || summary.StorageDests > 0
	if !summary.HasConfiguration {
		summary.Note = "database contains no jobs or storage destinations — daemon may have started fresh after a failed restoration"
	}
	return summary, nil
}
