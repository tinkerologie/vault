package runner

import (
	"strings"
	"testing"

	"github.com/ruaan-deysel/vault/internal/crypto"
	"github.com/ruaan-deysel/vault/internal/notify"
)

func TestFmtDuration(t *testing.T) {
	t.Parallel()
	cases := []struct {
		seconds int
		want    string
	}{
		{0, "0s"},
		{1, "1s"},
		{59, "59s"},
		{60, "1m 0s"},
		{61, "1m 1s"},
		{3599, "59m 59s"},
		{3600, "1h 0m"},
		{3660, "1h 1m"},
		{7321, "2h 2m"},
		{86400, "24h 0m"},
	}
	for _, c := range cases {
		if got := fmtDuration(c.seconds); got != c.want {
			t.Errorf("fmtDuration(%d) = %q, want %q", c.seconds, got, c.want)
		}
	}
}

func TestFmtSize(t *testing.T) {
	t.Parallel()
	cases := []struct {
		bytes int64
		want  string
	}{
		{0, "0 B"},
		{1, "1 B"},
		{1023, "1023 B"},
		{1024, "1 KB"},
		{2048, "2 KB"},
		{1024 * 1024, "1.0 MB"},
		{int64(1024) * 1024 * 1024, "1.0 GB"},
		// Slightly off-binary values exercise the float formatting branch.
		{1536 * 1024, "1.5 MB"},
	}
	for _, c := range cases {
		if got := fmtSize(c.bytes); got != c.want {
			t.Errorf("fmtSize(%d) = %q, want %q", c.bytes, got, c.want)
		}
	}
}

// TestBuildDiscordEmbedCompleted covers the success/info/danger title+color
// branches and confirms Duration/Size/Items field shapes.
func TestBuildDiscordEmbedCompleted(t *testing.T) {
	t.Parallel()
	r, _, _ := setupTestRunner(t)
	embed := r.buildDiscordEmbed("nightly", "completed", 3, 0, 1024*1024, 60, nil)
	if !strings.Contains(embed.Title, "Backup Completed") {
		t.Errorf("Title = %q, want it to mention 'Backup Completed'", embed.Title)
	}
	if embed.Color != notify.ColorSuccess {
		t.Errorf("Color = %d, want ColorSuccess (%d)", embed.Color, notify.ColorSuccess)
	}
	if embed.Description != "nightly" {
		t.Errorf("Description = %q, want job name 'nightly'", embed.Description)
	}
	// Duration + Size + Speed + Items = 4 fields (no failed names).
	if len(embed.Fields) != 4 {
		t.Errorf("got %d fields, want 4: %+v", len(embed.Fields), embed.Fields)
	}
}

func TestBuildDiscordEmbedPartial(t *testing.T) {
	t.Parallel()
	r, _, _ := setupTestRunner(t)
	embed := r.buildDiscordEmbed("partial-job", "partial", 2, 1, 0, 0, []string{"failed-item"})
	if !strings.Contains(embed.Title, "Partially Completed") {
		t.Errorf("Title = %q, want 'Partially Completed'", embed.Title)
	}
	if embed.Color != notify.ColorWarning {
		t.Errorf("Color = %d, want ColorWarning (%d)", embed.Color, notify.ColorWarning)
	}
	// duration=0 → no Speed field. With Failed Items, that's Duration/Size/Items/FailedItems = 4.
	if len(embed.Fields) != 4 {
		t.Errorf("got %d fields, want 4 (no Speed, has FailedItems): %+v", len(embed.Fields), embed.Fields)
	}
	// Last field should be the failed-items list.
	last := embed.Fields[len(embed.Fields)-1]
	if last.Name != "Failed Items" {
		t.Errorf("last field name = %q, want 'Failed Items'", last.Name)
	}
	if !strings.Contains(last.Value, "failed-item") {
		t.Errorf("Failed Items value = %q, want it to mention failed-item", last.Value)
	}
}

func TestBuildDiscordEmbedFailedTruncatesLongNames(t *testing.T) {
	t.Parallel()
	r, _, _ := setupTestRunner(t)
	long := strings.Repeat("an-extremely-long-failed-item-name,", 20) // ~700 chars
	embed := r.buildDiscordEmbed("nightly", "failed", 0, 5, 0, 30, []string{long})
	if !strings.Contains(embed.Title, "Failed") {
		t.Errorf("Title = %q, want it to mention 'Failed'", embed.Title)
	}
	if embed.Color != notify.ColorDanger {
		t.Errorf("Color = %d, want ColorDanger (%d)", embed.Color, notify.ColorDanger)
	}
	// Find the Failed Items field and confirm it was truncated.
	var failedField *notify.DiscordField
	for i := range embed.Fields {
		if embed.Fields[i].Name == "Failed Items" {
			failedField = &embed.Fields[i]
			break
		}
	}
	if failedField == nil {
		t.Fatal("Failed Items field missing")
	}
	if len(failedField.Value) > 220 {
		t.Errorf("Failed Items value not truncated: len=%d", len(failedField.Value))
	}
	if !strings.HasSuffix(failedField.Value, "...") {
		t.Errorf("Failed Items value missing truncation suffix '...': %q", failedField.Value)
	}
}

// TestResolvePassphraseEmpty: a fresh DB has no encryption settings, so the
// resolver returns "" without panic.
func TestResolvePassphraseEmpty(t *testing.T) {
	t.Parallel()
	r, _, _ := setupTestRunner(t)
	if got := r.ResolvePassphrase(); got != "" {
		t.Errorf("ResolvePassphrase on fresh runner = %q, want \"\"", got)
	}
}

// TestResolvePassphraseLegacyPlaintext: the resolver falls back to the
// legacy plaintext setting when no sealed value is present.
func TestResolvePassphraseLegacyPlaintext(t *testing.T) {
	t.Parallel()
	r, database, _ := setupTestRunner(t)
	if err := database.SetSetting("encryption_passphrase", "hunter2"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if got := r.ResolvePassphrase(); got != "hunter2" {
		t.Errorf("ResolvePassphrase = %q, want hunter2", got)
	}
}

// TestResolvePassphraseSealed sets up a sealed passphrase + server key and
// confirms the resolver unseals successfully.
func TestResolvePassphraseSealed(t *testing.T) {
	t.Parallel()
	r, database, _ := setupTestRunner(t)
	// Build a deterministic 32-byte server key and seal a passphrase.
	key := make([]byte, crypto.ServerKeySize)
	for i := range key {
		key[i] = byte(i)
	}
	r.serverKey = key
	sealed, err := crypto.Seal(key, "vault-secret-2026")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if err := database.SetSetting("encryption_passphrase_sealed", sealed); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if got := r.ResolvePassphrase(); got != "vault-secret-2026" {
		t.Errorf("ResolvePassphrase = %q, want vault-secret-2026", got)
	}
}

// TestResolvePassphraseSealedFallbackOnBadKey: when the sealed setting is
// present but the server key cannot decrypt it, the resolver falls back
// to the legacy plaintext setting.
func TestResolvePassphraseSealedFallbackOnBadKey(t *testing.T) {
	t.Parallel()
	r, database, _ := setupTestRunner(t)
	// Seal with one key, then set a different one on the runner.
	goodKey := make([]byte, crypto.ServerKeySize)
	for i := range goodKey {
		goodKey[i] = byte(i + 1)
	}
	sealed, err := crypto.Seal(goodKey, "real-secret")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	wrongKey := make([]byte, crypto.ServerKeySize)
	r.serverKey = wrongKey // all zeros, different from goodKey
	if err := database.SetSetting("encryption_passphrase_sealed", sealed); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if err := database.SetSetting("encryption_passphrase", "legacy-fallback"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if got := r.ResolvePassphrase(); got != "legacy-fallback" {
		t.Errorf("ResolvePassphrase = %q, want legacy-fallback (unseal should fail and fall back)", got)
	}
}

// TestSendRestoreNotificationDisabled covers the early-return when global
// notifications are off.
func TestSendRestoreNotificationDisabled(t *testing.T) {
	t.Parallel()
	r, database, _ := setupTestRunner(t)
	if err := database.SetSetting("notifications_enabled", "false"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	// Both error and success branches must short-circuit.
	r.sendRestoreNotification("plex", "container", nil)
	r.sendRestoreNotification("plex", "container", errExampleRestore{})

	// No activity log entries should have been written.
	logs, err := database.ListActivityLogs(10, "")
	if err != nil {
		t.Fatalf("ListActivityLogs: %v", err)
	}
	if len(logs) != 0 {
		t.Errorf("expected no activity logs when notifications disabled, got %d", len(logs))
	}
}

// TestSendRestoreNotificationSuccess exercises the success branch (writes an
// info-level activity log). The notify.Send call no-ops on non-Linux platforms.
func TestSendRestoreNotificationSuccess(t *testing.T) {
	t.Parallel()
	r, database, _ := setupTestRunner(t)
	// Default is "true" — make it explicit.
	if err := database.SetSetting("notifications_enabled", "true"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	r.sendRestoreNotification("plex", "container", nil)

	logs, err := database.ListActivityLogs(10, "")
	if err != nil {
		t.Fatalf("ListActivityLogs: %v", err)
	}
	if len(logs) == 0 {
		t.Fatal("expected one activity log on successful restore notification")
	}
	if logs[0].Level != "info" || logs[0].Category != "restore" {
		t.Errorf("log level/category = %q/%q, want info/restore", logs[0].Level, logs[0].Category)
	}
	if !strings.Contains(logs[0].Message, "plex") {
		t.Errorf("log message = %q, should mention plex", logs[0].Message)
	}
}

// TestSendRestoreNotificationFailure exercises the error branch.
func TestSendRestoreNotificationFailure(t *testing.T) {
	t.Parallel()
	r, database, _ := setupTestRunner(t)
	if err := database.SetSetting("notifications_enabled", "true"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	r.sendRestoreNotification("plex", "container", errExampleRestore{})

	logs, err := database.ListActivityLogs(10, "")
	if err != nil {
		t.Fatalf("ListActivityLogs: %v", err)
	}
	if len(logs) == 0 {
		t.Fatal("expected one activity log on failed restore notification")
	}
	if logs[0].Level != "error" || logs[0].Category != "restore" {
		t.Errorf("log level/category = %q/%q, want error/restore", logs[0].Level, logs[0].Category)
	}
}

// errExampleRestore is a tiny error used by the restore-notification tests.
type errExampleRestore struct{}

func (errExampleRestore) Error() string { return "example restore failure" }
