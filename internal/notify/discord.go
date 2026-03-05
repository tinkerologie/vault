package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Discord embed color constants (decimal).
const (
	ColorSuccess = 5763719  // #57F287 green
	ColorWarning = 16776960 // #FFFF00 yellow
	ColorDanger  = 15548997 // #ED4245 red
	ColorInfo    = 5793266  // #5865F2 blurple
)

// DiscordField is one inline field in an embed.
type DiscordField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline"`
}

// DiscordEmbed is a single rich embed.
type DiscordEmbed struct {
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Color       int            `json:"color"`
	Fields      []DiscordField `json:"fields,omitempty"`
	Timestamp   string         `json:"timestamp,omitempty"`
	Footer      *DiscordFooter `json:"footer,omitempty"`
}

// DiscordFooter is the footer section of an embed.
type DiscordFooter struct {
	Text string `json:"text"`
}

// DiscordPayload is the top-level JSON sent to a Discord webhook.
type DiscordPayload struct {
	Embeds []DiscordEmbed `json:"embeds"`
}

// SendDiscord posts a rich embed to a Discord webhook URL.
// It uses a 10-second timeout and returns any error for the caller to log.
// Empty URL is a no-op.
func SendDiscord(webhookURL string, embed DiscordEmbed) error {
	if webhookURL == "" {
		return nil
	}

	if embed.Timestamp == "" {
		embed.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}
	if embed.Footer == nil {
		embed.Footer = &DiscordFooter{Text: "Vault Backup Manager"}
	}

	payload := DiscordPayload{Embeds: []DiscordEmbed{embed}}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal discord payload: %w", err)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(webhookURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("discord webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("discord webhook returned %d", resp.StatusCode)
	}
	return nil
}
