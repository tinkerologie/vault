package runner

import (
	"slices"
	"testing"
	"time"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/notify"
)

const backupNotificationFixtureWebhook = "https://discord.com/api/webhooks/123456789/test-fixture-token"

func recordBackupRecipients(t *testing.T, r *Runner, webhook string) *[]string {
	t.Helper()
	var recipients []string
	r.sendBackupUnraid = func(string, string, int, int, int64) error {
		recipients = append(recipients, "unraid")
		return nil
	}
	r.sendBackupDiscord = func(url string, _ notify.DiscordEmbed, _ notify.DiscordOptions) {
		if url != webhook {
			t.Error("Discord delivery did not use the saved webhook")
		}
		recipients = append(recipients, "discord")
	}
	return &recipients
}

func TestBackupNotificationRecipients(t *testing.T) {
	hook := backupNotificationFixtureWebhook
	for _, tc := range []struct {
		name, unraid, discord, webhook, job, status string
		want                                        []string
	}{
		{"both enabled", "true", "always", hook, "always", "completed", []string{"unraid", "discord"}},
		{"discord only", "false", "always", hook, "always", "completed", []string{"discord"}},
		{"unraid only", "true", "never", hook, "always", "completed", []string{"unraid"}},
		{"both disabled", "false", "never", hook, "always", "completed", nil},
		{"empty webhook", "true", "always", "", "always", "completed", []string{"unraid"}},
		{"discord ignores success", "false", "failure", hook, "always", "completed", nil},
		{"discord sends failure", "false", "failure", hook, "failure", "failed", []string{"discord"}},
		{"discord sends partial", "false", "failure", hook, "", "partial", []string{"discord"}},
		{"discord failure preference ignores cancellation", "false", "failure", hook, "always", "cancelled", nil},
		{"job never mutes both", "true", "always", hook, "never", "failed", nil},
		{"job failure preference allows Discord success", "true", "always", hook, "failure", "completed", []string{"discord"}},
		{"job default keeps Unraid failures", "true", "never", hook, "", "partial", []string{"unraid"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, database, _ := setupTestRunner(t)
			for key, value := range map[string]string{
				"notifications_enabled": tc.unraid,
				"discord_notify_on":     tc.discord,
				"discord_webhook_url":   tc.webhook,
			} {
				if err := database.SetSetting(key, value); err != nil {
					t.Fatal(err)
				}
			}
			recipients := recordBackupRecipients(t, r, tc.webhook)
			r.sendNotification(db.Job{Name: "fixture", NotifyOn: tc.job}, tc.status, 1, 0, 1024, time.Second, nil)
			if !slices.Equal(*recipients, tc.want) {
				t.Errorf("recipients = %v, want %v", *recipients, tc.want)
			}
		})
	}
}

func TestBackupNotificationUpgradeAndOptIn(t *testing.T) {
	r, database, storageDir := setupTestRunner(t)
	hook := backupNotificationFixtureWebhook
	for key, value := range map[string]string{
		"notifications_enabled": "false",
		"discord_notify_on":     "always",
		"discord_webhook_url":   hook,
	} {
		if err := database.SetSetting(key, value); err != nil {
			t.Fatal(err)
		}
	}
	// This fixture represents a database last opened by the old dispatcher.
	if _, err := database.Exec("DELETE FROM settings WHERE key = ?", db.DiscordBackupMuteMigrationKey); err != nil {
		t.Fatal(err)
	}
	if err := database.Reopen(); err != nil {
		t.Fatal(err)
	}
	jobID := d458Seed(t, database, storageDir, d458Job{
		job:   db.Job{Name: "notification fixture", NotifyOn: "always", Compression: "none", Encryption: "none"},
		items: []db.JobItem{d458FolderItem(t, "notification-source")},
	})
	recipients := recordBackupRecipients(t, r, hook)

	r.RunJob(jobID)
	if run := d458LatestRun(t, database, jobID); run.Status != "completed" {
		t.Fatalf("upgraded job finished with status %q", run.Status)
	}
	if len(*recipients) != 0 {
		t.Fatalf("upgrading a muted installation sent notifications to %v", *recipients)
	}

	if err := database.SetSetting("discord_notify_on", "always"); err != nil {
		t.Fatal(err)
	}
	if err := database.Reopen(); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct {
		name string
		run  func(int64)
	}{
		{"scheduled", r.RunJob},
		{"manual", r.RunJobManual},
	} {
		t.Run(entry.name, func(t *testing.T) {
			*recipients = nil
			entry.run(jobID)
			if run := d458LatestRun(t, database, jobID); run.Status != "completed" {
				t.Fatalf("job finished with status %q", run.Status)
			}
			if !slices.Equal(*recipients, []string{"discord"}) {
				t.Errorf("recipients after opt-in = %v, want only Discord", *recipients)
			}
		})
	}
}
