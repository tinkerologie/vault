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
	Username        string                  `json:"username,omitempty"`
	AvatarURL       string                  `json:"avatar_url,omitempty"`
	Content         string                  `json:"content,omitempty"`
	Embeds          []DiscordEmbed          `json:"embeds"`
	AllowedMentions *DiscordAllowedMentions `json:"allowed_mentions,omitempty"`
}

// DiscordAllowedMentions controls which mentions in a message are permitted to
// notify. Discord applies it to the message content (and message components) —
// the only place Vault ever emits a mention; mentions inside embeds never
// notify regardless. Vault sends an empty (but non-nil) Parse array on every
// message to disable all automatic mass pings (@everyone/@here and blanket role
// pings), and whitelists only the IDs explicitly listed in Roles/Users, so a
// stray mention can never ping an entire server.
type DiscordAllowedMentions struct {
	Parse []string `json:"parse"`
	Roles []string `json:"roles,omitempty"`
	Users []string `json:"users,omitempty"`
}

// DiscordOptions personalizes a webhook message. The zero value reproduces the
// original plain behaviour (default bot name/avatar, no mention).
type DiscordOptions struct {
	// Username overrides the webhook's default bot name when non-empty.
	Username string
	// AvatarURL overrides the webhook's default avatar when non-empty.
	AvatarURL string
	// MentionRoleID, when a valid Discord snowflake, prepends a role mention
	// (<@&id>) to the message content and is the only role permitted to ping.
	MentionRoleID string
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
// Empty URL is a no-op. An optional DiscordOptions personalizes the bot
// name/avatar and attaches a role mention; the zero value (or omitting it)
// preserves the original plain behaviour.
func SendDiscord(webhookURL string, embed DiscordEmbed, opts ...DiscordOptions) error {
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
	// Always lock down mention parsing so an automatic @everyone/@here or role
	// mention can never fire from any message we send; only an explicitly
	// configured role is whitelisted below. Emitting this unconditionally (even
	// when no mention is requested) keeps the guarantee independent of what ends
	// up in the content.
	allowed := &DiscordAllowedMentions{Parse: []string{}}
	if len(opts) > 0 {
		opt := opts[0]
		payload.Username = strings.TrimSpace(opt.Username)
		payload.AvatarURL = strings.TrimSpace(opt.AvatarURL)
		if roleID := sanitizeSnowflake(opt.MentionRoleID); roleID != "" {
			payload.Content = "<@&" + roleID + ">"
			allowed.Roles = []string{roleID}
		}
	}
	payload.AllowedMentions = allowed
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal discord payload: %w", err)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	// aikido-ignore-next-line AIK_go_G107 -- webhook URL is admin-configured and normalized to discord.com / discordapp.com via normalizeDiscordWebhookURL above; not user-controlled at request time.
	resp, err := client.Post(normalizedWebhookURL, "application/json", bytes.NewReader(body)) // #nosec G107
	if err != nil {
		return fmt.Errorf("discord webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("discord webhook returned %d", resp.StatusCode)
	}
	return nil
}

// sanitizeSnowflake returns the trimmed input only when it is a plausible
// Discord snowflake (a non-empty run of digits); otherwise "". This guards the
// mention/allowed_mentions fields against arbitrary text injection.
func sanitizeSnowflake(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return s
}
