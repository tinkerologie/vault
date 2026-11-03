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

// unparseableConfig replaces JSON that cannot be read, since it may hold anything.
const unparseableConfig = `"` + redactedPlaceholder + ` unparseable"`

// RedactJSON redacts sensitive fields in a JSON config string, and credentials
// embedded in URL values. Input that does not parse is replaced entirely.
func RedactJSON(configJSON string) string {
	if configJSON == "" {
		return configJSON
	}

	var raw any
	if err := json.Unmarshal([]byte(configJSON), &raw); err != nil {
		return unparseableConfig
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
		return unparseableConfig
	}
	return string(out)
}

// redactMap recursively redacts sensitive fields in a map.
func redactMap(m map[string]any) map[string]any {
	result := make(map[string]any, len(m))
	for k, v := range m {
		if isSensitiveKey(k) {
			switch val := v.(type) {
			case string:
				if val != "" {
					result[k] = redactedPlaceholder
					continue
				}
			case map[string]any, []any:
				result[k] = redactedPlaceholder
				continue
			}
		}
		result[k] = redactValue(v)
	}
	return result
}

// redactValue redacts one JSON value. Strings are free text as far as this code
// knows, so they are scrubbed too, and a string holding JSON is redacted as JSON:
// per-item settings sit in run logs in that form.
func redactValue(v any) any {
	switch val := v.(type) {
	case map[string]any:
		return redactMap(val)
	case []any:
		return redactSlice(val)
	case string:
		trimmed := strings.TrimSpace(val)
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			var nested any
			if json.Unmarshal([]byte(trimmed), &nested) == nil {
				if out, err := json.Marshal(redactValue(nested)); err == nil {
					return string(out)
				}
			}
		}
		return RedactText(val)
	default:
		return v
	}
}

// redactSlice recursively redacts sensitive fields in slice elements.
func redactSlice(s []any) []any {
	result := make([]any, len(s))
	for i, v := range s {
		result[i] = redactValue(v)
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
		return RedactText(rawURL)
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
	// URL query parameters used as bearer or signature credentials.
	{regexp.MustCompile(`(?i)([?&](?:sig|signature|x-amz-signature|x-amz-credential|client_secret)=)[^&#\s]+`), `${1}` + redactedPlaceholder},
	// The same for any scheme and for a bare token with no colon.
	{regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^/@\s]+@`), `${1}` + redactedPlaceholder + `@`},
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

// RedactText scrubs credentials from a single free-text value.
func RedactText(s string) string {
	return string(RedactLogLines([]byte(s)))
}

// RedactRunLog redacts a persisted run log. Item logs are JSON arrays holding
// settings and error text; other runs store plain text.
func RedactRunLog(runLog string) string {
	var raw any
	if err := json.Unmarshal([]byte(runLog), &raw); err != nil {
		return RedactText(runLog)
	}
	out, err := json.Marshal(redactValue(raw))
	if err != nil {
		return RedactText(runLog)
	}
	return string(out)
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
