package diagnostics

import (
	"encoding/json"
	"net/url"
	"strings"
)

// redactedPlaceholder is the replacement string for sensitive values.
const redactedPlaceholder = "[REDACTED]"

// sensitiveKeys are field names that must be fully redacted.
var sensitiveKeys = map[string]bool{
	"password":          true,
	"secret_key":        true,
	"secret_access_key": true,
	"passphrase":        true,
	"key_file":          true,
	"api_key":           true,
	"private_key":       true,
	"token":             true,
}

// sensitiveSubstrings are substrings in field names that trigger redaction.
var sensitiveSubstrings = []string{
	"password",
	"secret",
	"passphrase",
	"key",
	"token",
	"webhook",
}

// RedactJSON redacts sensitive fields in a JSON config string.
// Returns the original string if parsing fails.
func RedactJSON(configJSON string) string {
	if configJSON == "" {
		return configJSON
	}

	var raw any
	if err := json.Unmarshal([]byte(configJSON), &raw); err != nil {
		return configJSON
	}

	var redacted any
	switch v := raw.(type) {
	case map[string]any:
		redacted = redactMap(v)
	case []any:
		redacted = redactSlice(v)
	default:
		return configJSON
	}

	out, err := json.Marshal(redacted)
	if err != nil {
		return configJSON
	}
	return string(out)
}

// redactMap recursively redacts sensitive fields in a map.
func redactMap(m map[string]any) map[string]any {
	result := make(map[string]any, len(m))
	for k, v := range m {
		if isSensitiveKey(k) {
			if s, ok := v.(string); ok && s != "" {
				result[k] = redactedPlaceholder
				continue
			}
		}
		switch val := v.(type) {
		case map[string]any:
			result[k] = redactMap(val)
		case []any:
			result[k] = redactSlice(val)
		default:
			result[k] = val
		}
	}
	return result
}

// redactSlice recursively redacts sensitive fields in slice elements.
func redactSlice(s []any) []any {
	result := make([]any, len(s))
	for i, v := range s {
		switch val := v.(type) {
		case map[string]any:
			result[i] = redactMap(val)
		case []any:
			result[i] = redactSlice(val)
		default:
			result[i] = val
		}
	}
	return result
}

// isSensitiveKey checks if a key name indicates a sensitive value.
func isSensitiveKey(key string) bool {
	lower := strings.ToLower(key)
	if sensitiveKeys[lower] {
		return true
	}
	for _, sub := range sensitiveSubstrings {
		if strings.Contains(lower, sub) {
			return true
		}
	}
	return false
}

// RedactURL redacts inline credentials from a URL string.
// Preserves scheme, host, path, query, and fragment.
func RedactURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	if u.User == nil {
		return rawURL
	}
	u.User = url.User(redactedPlaceholder)
	return u.String()
}

// RedactDiscordWebhook replaces the webhook token in a Discord URL.
// Preserves any query string or fragment.
func RedactDiscordWebhook(rawURL string) string {
	if !strings.Contains(rawURL, "discord.com/api/webhooks/") {
		return rawURL
	}
	parts := strings.SplitN(rawURL, "discord.com/api/webhooks/", 2)
	if len(parts) != 2 {
		return rawURL
	}
	// Keep the webhook ID but redact the token.
	segments := strings.SplitN(parts[1], "/", 2)
	if len(segments) < 2 {
		return rawURL
	}
	// Preserve query string / fragment after the token.
	token := segments[1]
	suffix := ""
	if idx := strings.IndexAny(token, "?#"); idx >= 0 {
		suffix = token[idx:]
	}
	return parts[0] + "discord.com/api/webhooks/" + segments[0] + "/" + redactedPlaceholder + suffix
}
