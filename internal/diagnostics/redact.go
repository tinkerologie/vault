package diagnostics

import (
	"encoding/json"
	"net/url"
	"regexp"
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

// logRedactPattern bundles a regex with its replacement template so
// some patterns can preserve more than just the leading capture group
// (e.g. URL credentials need the trailing `@host` kept).
type logRedactPattern struct {
	re   *regexp.Regexp
	repl string
}

// logRedactPatterns scrub credentials from captured log lines before
// embedding them in the diagnostics bundle. Order matters: the
// `Authorization:` header pattern fires before the more specific
// `Credential=` pattern would, which is fine because each pass
// preserves the header name and only the secret value is replaced.
//
// IMPORTANT: only add patterns here. Removing one widens what we leak
// to support tickets. All callers of RedactLogLines depend on this list
// being complete; a regression test in redact_test.go pins each pattern.
var logRedactPatterns = []logRedactPattern{
	// Generic auth headers — value runs to next whitespace/end-of-line.
	// Run BEFORE the SigV4 Credential= pattern because Authorization
	// header values often contain `Credential=AKIA...`; redacting the
	// whole header value here makes the secondary pattern a no-op for
	// the same line.
	{regexp.MustCompile(`(?i)(Authorization:\s*).*`), `${1}` + redactedPlaceholder},
	{regexp.MustCompile(`(?i)(X-API-Key:\s*)\S+`), `${1}` + redactedPlaceholder},
	{regexp.MustCompile(`(?i)(Proxy-Authorization:\s*)\S+`), `${1}` + redactedPlaceholder},
	{regexp.MustCompile(`(?i)(Cookie:\s*)[^\r\n]+`), `${1}` + redactedPlaceholder},
	{regexp.MustCompile(`(?i)(Set-Cookie:\s*)[^\r\n]+`), `${1}` + redactedPlaceholder},
	// AWS SigV4 credential outside Authorization header (e.g. in URL
	// query strings or debug-dumped canonical requests).
	{regexp.MustCompile(`(?i)(AWS4-HMAC-SHA256\s+Credential=)[A-Z0-9]+`), `${1}` + redactedPlaceholder},
	// JSON/query-string credential fields.
	{regexp.MustCompile(`(?i)("?password"?\s*[:=]\s*"?)[^"\s,&}]+`), `${1}` + redactedPlaceholder},
	{regexp.MustCompile(`(?i)("?secret[_-]?key"?\s*[:=]\s*"?)[^"\s,&}]+`), `${1}` + redactedPlaceholder},
	{regexp.MustCompile(`(?i)("?access[_-]?key"?\s*[:=]\s*"?)[^"\s,&}]+`), `${1}` + redactedPlaceholder},
	{regexp.MustCompile(`(?i)("?api[_-]?key"?\s*[:=]\s*"?)[^"\s,&}]+`), `${1}` + redactedPlaceholder},
	{regexp.MustCompile(`(?i)("?passphrase"?\s*[:=]\s*"?)[^"\s,&}]+`), `${1}` + redactedPlaceholder},
	{regexp.MustCompile(`(?i)("?token"?\s*[:=]\s*"?)[^"\s,&}]+`), `${1}` + redactedPlaceholder},
	// Inline URL credentials: `https://user:pass@host/`. Preserve scheme
	// and the `@host` suffix so the reader can still see which endpoint
	// was being contacted.
	{regexp.MustCompile(`(https?://)([^:/@\s]+):([^@\s]+)@`), `${1}` + redactedPlaceholder + `@`},
	// Discord webhook URLs are sensitive end-to-end (the token IS in
	// the path). Catch them anywhere in a log line, not just the
	// structured notification path. Anchored on https:// + optional
	// subdomain so the host isn't matched mid-string (CodeQL
	// go/regex/missing-regexp-anchor) — only legitimate Discord webhook
	// URLs trigger redaction, not arbitrary log text mentioning the
	// substring "discord.com/api/webhooks/...".
	{regexp.MustCompile(`(https://(?:[a-z0-9-]+\.)*discord(?:app)?\.com/api/webhooks/\d+/)\S+`), `${1}` + redactedPlaceholder},
}

// RedactLogLines scrubs credentials and secrets from captured log
// output before it's written into a diagnostics bundle. Operates on
// the entire buffer in one pass — efficient for the ~1 MiB ring-buffer
// snapshots the collector produces.
//
// Each pattern in logRedactPatterns has its own replacement template
// so patterns can preserve more than just the prefix (e.g. URL
// credentials keep the trailing `@host` so the reader can still see
// which endpoint was being contacted).
func RedactLogLines(input []byte) []byte {
	if len(input) == 0 {
		return input
	}
	out := input
	for _, p := range logRedactPatterns {
		out = p.re.ReplaceAll(out, []byte(p.repl))
	}
	return out
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
