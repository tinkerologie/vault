package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/tinkerologie/vault/internal/docsmeta"
)

func TestDiscordBackupMuteMigration(t *testing.T) {
	const webhook = "https://discord.com/api/webhooks/123456789/test-fixture-token"
	for _, tc := range []struct {
		name, unraid, discord, hook, want string
	}{
		{"muted all backups", "false", "always", webhook, "never"},
		{"muted failures", "false", "failure", webhook, "never"},
		{"muted default", "false", "", webhook, "never"},
		{"already disabled", "false", "never", webhook, "never"},
		{"native enabled", "true", "failure", webhook, "failure"},
		{"native default", "", "", webhook, "always"},
		{"no webhook", "false", "failure", "", "failure"},
		{"fresh database", "", "", "", "always"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "vault.db")
			legacy, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer legacy.Close()
			if _, err := legacy.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL DEFAULT '')`); err != nil {
				t.Fatal(err)
			}
			for key, value := range map[string]string{
				"notifications_enabled": tc.unraid,
				"discord_notify_on":     tc.discord,
				"discord_webhook_url":   tc.hook,
			} {
				if value != "" {
					if _, err := legacy.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)`, key, value); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := legacy.Close(); err != nil {
				t.Fatal(err)
			}

			d, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = d.Close() })
			check := func(key, want string) {
				t.Helper()
				got, err := d.GetSetting(key, docsmeta.DefaultFor(key))
				if err != nil || got != want {
					// The webhook is a credential in production. Avoid printing its value.
					t.Errorf("migrated setting %q did not match the expected value: %v", key, err)
				}
			}
			wantUnraid := tc.unraid
			if wantUnraid == "" {
				wantUnraid = "true"
			}
			check("notifications_enabled", wantUnraid)
			check("discord_notify_on", tc.want)
			check("discord_webhook_url", tc.hook)

			// A later operator choice survives reopening with the same webhook.
			if tc.unraid == "false" && tc.hook != "" {
				if err := d.SetSetting("discord_notify_on", "always"); err != nil {
					t.Fatal(err)
				}
				if err := d.Reopen(); err != nil {
					t.Fatal(err)
				}
				check("discord_notify_on", "always")
				check("notifications_enabled", wantUnraid)
				check("discord_webhook_url", tc.hook)
			}
		})
	}
}

func TestDiscordBackupMuteMigrationRollsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	if _, err := legacy.Exec(`
		CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL DEFAULT '');
		INSERT INTO settings (key, value) VALUES
			('notifications_enabled', 'false'),
			('discord_notify_on', 'always'),
			('discord_webhook_url', 'fixture-webhook');
		CREATE TRIGGER fail_discord_migration BEFORE INSERT ON settings
		WHEN NEW.key = 'discord_notify_on' AND NEW.value = 'never'
		BEGIN SELECT RAISE(ABORT, 'fixture write failure'); END;
	`); err != nil {
		t.Fatal(err)
	}
	if d, err := Open(path); err == nil {
		_ = d.Close()
		t.Fatal("opening a database with a failed Discord migration succeeded")
	}
	var marked int
	if err := legacy.QueryRow("SELECT count(*) FROM settings WHERE key = ?", DiscordBackupMuteMigrationKey).Scan(&marked); err != nil || marked != 0 {
		t.Fatalf("marker remained after failed migration: count = %d, error = %v", marked, err)
	}
	if _, err := legacy.Exec("DROP TRIGGER fail_discord_migration"); err != nil {
		t.Fatal(err)
	}
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if got, err := d.GetSetting("discord_notify_on", ""); err != nil || got != "never" {
		t.Fatalf("migration retry did not preserve the mute: %v", err)
	}
}
