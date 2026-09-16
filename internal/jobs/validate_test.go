package jobs

import (
	"strings"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
)

// The secondary schedules are validated and normalised like the primary one:
// a whitespace-only value becomes "" and an unparseable one is rejected, so a
// job can never look scheduled while its entry silently failed to register
// (issue #322).
func TestNormalizeSecondarySchedules(t *testing.T) {
	t.Parallel()

	t.Run("trims and accepts valid schedules", func(t *testing.T) {
		job := db.Job{Name: "j", BackupTypeChain: "incremental", VerifySchedule: "  0 4 * * 0  ", FullBackupSchedule: "  0 */3 */2 * *  "}
		if err := normalize(&job); err != nil {
			t.Fatalf("Validate: %v", err)
		}
		if job.VerifySchedule != "0 4 * * 0" {
			t.Errorf("verify_schedule = %q, want it trimmed", job.VerifySchedule)
		}
		if job.FullBackupSchedule != "0 */3 */2 * *" {
			t.Errorf("full_backup_schedule = %q, want it trimmed", job.FullBackupSchedule)
		}
	})

	t.Run("whitespace becomes disabled", func(t *testing.T) {
		job := db.Job{Name: "j", BackupTypeChain: "incremental", VerifySchedule: "   ", FullBackupSchedule: "   "}
		if err := normalize(&job); err != nil {
			t.Fatalf("Validate: %v", err)
		}
		if job.VerifySchedule != "" || job.FullBackupSchedule != "" {
			t.Errorf("schedules = %q/%q, want both empty", job.VerifySchedule, job.FullBackupSchedule)
		}
	})

	t.Run("an unparseable verify schedule is rejected", func(t *testing.T) {
		job := db.Job{Name: "j", VerifySchedule: "not-a-cron"}
		err := normalize(&job)
		if err == nil || !strings.Contains(err.Error(), "invalid verify schedule") {
			t.Fatalf("err = %v, want it to name the verify schedule", err)
		}
	})

	t.Run("an unparseable full backup schedule is rejected", func(t *testing.T) {
		job := db.Job{Name: "j", BackupTypeChain: "incremental", FullBackupSchedule: "99 99 * * *"}
		err := normalize(&job)
		if err == nil || !strings.Contains(err.Error(), "invalid full backup schedule") {
			t.Fatalf("err = %v, want it to name the full backup schedule", err)
		}
	})

	t.Run("a full chain drops the redundant full schedule", func(t *testing.T) {
		for _, chain := range []string{"", "full"} {
			job := db.Job{Name: "j", BackupTypeChain: chain, FullBackupSchedule: "0 4 * * 0"}
			if err := normalize(&job); err != nil {
				t.Fatalf("Validate(chain=%q): %v", chain, err)
			}
			if job.FullBackupSchedule != "" {
				t.Errorf("chain %q kept full_backup_schedule %q — every run is already a full", chain, job.FullBackupSchedule)
			}
		}
		// Even an unparseable full backup schedule is cleared when the chain is full.
		job := db.Job{Name: "j", BackupTypeChain: "full", FullBackupSchedule: "invalid-cron"}
		if err := normalize(&job); err != nil {
			t.Fatalf("Validate(chain=full, invalid schedule): %v", err)
		}
		if job.FullBackupSchedule != "" {
			t.Errorf("full chain kept full_backup_schedule %q", job.FullBackupSchedule)
		}
	})
}
