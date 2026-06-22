package notify

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSendDiscord_Success(t *testing.T) {
	previousBaseURL := discordAPIBaseURL
	discordAPIBaseURL = ""
	t.Cleanup(func() { discordAPIBaseURL = previousBaseURL })

	var received DiscordPayload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Fatalf("expected application/json, got %s", ct)
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &received); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	discordAPIBaseURL = srv.URL

	embed := DiscordEmbed{
		Title:       "✅ Backup Completed",
		Description: "My Job",
		Color:       ColorSuccess,
		Fields: []DiscordField{
			{Name: "Duration", Value: "5m 30s", Inline: true},
			{Name: "Size", Value: "1.2 GB", Inline: true},
		},
	}

	if err := SendDiscord("https://discord.example/api/webhooks/123/token", embed); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(received.Embeds) != 1 {
		t.Fatalf("expected 1 embed, got %d", len(received.Embeds))
	}
	if received.Embeds[0].Title != "✅ Backup Completed" {
		t.Errorf("unexpected title: %s", received.Embeds[0].Title)
	}
	if received.Embeds[0].Footer == nil || received.Embeds[0].Footer.Text != "Vault Backup Manager" {
		t.Error("expected footer to be set automatically")
	}
	if received.Embeds[0].Timestamp == "" {
		t.Error("expected timestamp to be set automatically")
	}
	// Even with no options, mention parsing is locked down so nothing can ping.
	if received.AllowedMentions == nil || len(received.AllowedMentions.Parse) != 0 {
		t.Errorf("expected allowed_mentions with empty parse, got %+v", received.AllowedMentions)
	}
	if received.Content != "" {
		t.Errorf("content = %q, want empty when no mention configured", received.Content)
	}
}

func TestSendDiscord_ErrorStatus(t *testing.T) {
	previousBaseURL := discordAPIBaseURL
	discordAPIBaseURL = ""
	t.Cleanup(func() { discordAPIBaseURL = previousBaseURL })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()
	discordAPIBaseURL = srv.URL

	err := SendDiscord("https://discord.example/api/webhooks/123/token", DiscordEmbed{Title: "test"})
	if err == nil {
		t.Fatal("expected error for 400 status")
	}
}

func TestSendDiscord_EmptyURL(t *testing.T) {
	if err := SendDiscord("", DiscordEmbed{Title: "test"}); err != nil {
		t.Fatalf("empty URL should be a no-op, got: %v", err)
	}
}

func TestSendDiscord_RejectsNonDiscordHost(t *testing.T) {
	t.Parallel()

	err := SendDiscord("https://example.com/api/webhooks/123/token", DiscordEmbed{Title: "test"})
	if err == nil {
		t.Fatal("expected error for non-Discord host")
	}
}

func TestSendDiscord_OptionsPersonalizeAndMention(t *testing.T) {
	previousBaseURL := discordAPIBaseURL
	discordAPIBaseURL = ""
	t.Cleanup(func() { discordAPIBaseURL = previousBaseURL })

	var received DiscordPayload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &received); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	discordAPIBaseURL = srv.URL

	opts := DiscordOptions{Username: "Vault Backups", AvatarURL: "https://cdn.example/a.png", MentionRoleID: "123456789012345678"}
	if err := SendDiscord("https://discord.example/api/webhooks/123/token", DiscordEmbed{Title: "x"}, opts); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if received.Username != "Vault Backups" {
		t.Errorf("username = %q, want %q", received.Username, "Vault Backups")
	}
	if received.AvatarURL != "https://cdn.example/a.png" {
		t.Errorf("avatar_url = %q, want the configured URL", received.AvatarURL)
	}
	if received.Content != "<@&123456789012345678>" {
		t.Errorf("content = %q, want a role mention", received.Content)
	}
	if received.AllowedMentions == nil {
		t.Fatal("expected allowed_mentions to be set")
	}
	if len(received.AllowedMentions.Parse) != 0 {
		t.Errorf("allowed_mentions.parse = %v, want empty (blocks @everyone)", received.AllowedMentions.Parse)
	}
	if len(received.AllowedMentions.Roles) != 1 || received.AllowedMentions.Roles[0] != "123456789012345678" {
		t.Errorf("allowed_mentions.roles = %v, want only the configured role", received.AllowedMentions.Roles)
	}
}

func TestSendDiscord_InvalidRoleIDOmitsMention(t *testing.T) {
	previousBaseURL := discordAPIBaseURL
	discordAPIBaseURL = ""
	t.Cleanup(func() { discordAPIBaseURL = previousBaseURL })

	var received DiscordPayload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &received)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	discordAPIBaseURL = srv.URL

	// A non-numeric "role ID" (e.g. an injected @everyone) must not produce a
	// mention. allowed_mentions is still emitted (locked down) so nothing can
	// ping, but with no content mention and no whitelisted role.
	opts := DiscordOptions{MentionRoleID: "@everyone"}
	if err := SendDiscord("https://discord.example/api/webhooks/123/token", DiscordEmbed{Title: "x"}, opts); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if received.Content != "" {
		t.Errorf("content = %q, want empty for invalid role ID", received.Content)
	}
	if received.AllowedMentions == nil {
		t.Fatal("expected allowed_mentions to be emitted (locked down) even without a valid role")
	}
	if len(received.AllowedMentions.Parse) != 0 {
		t.Errorf("allowed_mentions.parse = %v, want empty (blocks @everyone)", received.AllowedMentions.Parse)
	}
	if len(received.AllowedMentions.Roles) != 0 {
		t.Errorf("allowed_mentions.roles = %v, want empty for invalid role ID", received.AllowedMentions.Roles)
	}
}

func TestSanitizeSnowflake(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"valid snowflake", "123456789012345678", "123456789012345678"},
		{"trims surrounding whitespace", "  123  ", "123"},
		{"empty", "", ""},
		{"everyone literal", "@everyone", ""},
		{"trailing letters", "123abc", ""},
		{"role mention syntax", "<@&123>", ""},
		{"embedded space", "123456789012345678 0", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := sanitizeSnowflake(tc.in); got != tc.want {
				t.Errorf("sanitizeSnowflake(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
