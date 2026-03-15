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
