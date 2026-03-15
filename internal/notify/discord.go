package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Discord embed color constants (decimal).
const (
	ColorSuccess = 5763719  // #57F287 green
	ColorWarning = 16776960 // #FFFF00 yellow
	ColorDanger  = 15548997 // #ED4245 red
	ColorInfo    = 5793266  // #5865F2 blurple
)

const defaultDiscordAPIBaseURL = "https://discord.com"

var discordAPIBaseURL = defaultDiscordAPIBaseURL

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

func normalizeDiscordWebhookURL(webhookURL string) (string, error) {
	trimmed := strings.TrimSpace(webhookURL)
	if trimmed == "" {
		return "", nil
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("parse webhook url: %w", err)
	}
	if parsed.Scheme != "https" {
		return "", fmt.Errorf("webhook url must use https")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("webhook url must not include credentials, query strings, or fragments")
	}

	if discordAPIBaseURL == defaultDiscordAPIBaseURL {
		host := strings.ToLower(parsed.Hostname())
		switch host {
		case "discord.com", "discordapp.com", "ptb.discord.com", "canary.discord.com":
		default:
			return "", fmt.Errorf("webhook url must point to a Discord host")
		}
	}

	segments := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	var webhookID, webhookToken string
	switch {
	case len(segments) == 4 && segments[0] == "api" && segments[1] == "webhooks":
		webhookID, webhookToken = segments[2], segments[3]
	case len(segments) == 5 && segments[0] == "api" && strings.HasPrefix(segments[1], "v") && segments[2] == "webhooks":
		webhookID, webhookToken = segments[3], segments[4]
	default:
		return "", fmt.Errorf("webhook url must match Discord webhook format")
	}
	if webhookID == "" || webhookToken == "" {
		return "", fmt.Errorf("webhook url must include a webhook id and token")
	}

	base, err := url.Parse(discordAPIBaseURL)
	if err != nil {
		return "", fmt.Errorf("parse discord api base url: %w", err)
	}
	base.Path = "/api/webhooks/" + url.PathEscape(webhookID) + "/" + url.PathEscape(webhookToken)
	base.RawPath = ""
	base.RawQuery = ""
	base.Fragment = ""
	return base.String(), nil
}

// SendDiscord posts a rich embed to a Discord webhook URL.
// It uses a 10-second timeout and returns any error for the caller to log.
// Empty URL is a no-op.
func SendDiscord(webhookURL string, embed DiscordEmbed) error {
	if webhookURL == "" {
		return nil
	}

	normalizedWebhookURL, err := normalizeDiscordWebhookURL(webhookURL)
	if err != nil {
		return fmt.Errorf("normalize discord webhook: %w", err)
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
	resp, err := client.Post(normalizedWebhookURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("discord webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("discord webhook returned %d", resp.StatusCode)
	}
	return nil
}
