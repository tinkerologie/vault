package diagnostics

import (
	"testing"
)

func TestRedactJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "invalid json",
			input: "not json",
			want:  "not json",
		},
		{
			name:  "no sensitive fields",
			input: `{"host":"example.com","port":22}`,
			want:  `{"host":"example.com","port":22}`,
		},
		{
			name:  "redacts password",
			input: `{"host":"server","password":"s3cret"}`,
			want:  `{"host":"server","password":"[REDACTED]"}`,
		},
		{
			name:  "redacts multiple sensitive fields",
			input: `{"host":"x","password":"p","secret_key":"k","api_key":"a"}`,
			want:  `{"api_key":"[REDACTED]","host":"x","password":"[REDACTED]","secret_key":"[REDACTED]"}`,
		},
		{
			name:  "skips empty password",
			input: `{"host":"x","password":""}`,
			want:  `{"host":"x","password":""}`,
		},
		{
			name:  "redacts by substring match",
			input: `{"db_password":"secret","some_token":"abc"}`,
			want:  `{"db_password":"[REDACTED]","some_token":"[REDACTED]"}`,
		},
		{
			name:  "root level array with objects",
			input: `[{"password":"secret"},{"host":"ok"}]`,
			want:  `[{"password":"[REDACTED]"},{"host":"ok"}]`,
		},
		{
			name:  "nested array of objects",
			input: `{"items":[{"token":"abc"},{"name":"safe"}]}`,
			want:  `{"items":[{"token":"[REDACTED]"},{"name":"safe"}]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := RedactJSON(tt.input)
			if got != tt.want {
				t.Errorf("RedactJSON() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRedactURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "no credentials",
			input: "https://example.com/path",
			want:  "https://example.com/path",
		},
		{
			name:  "redacts inline credentials",
			input: "sftp://user:pass123@host.com:22/data",
			want:  "sftp://%5BREDACTED%5D@host.com:22/data",
		},
		{
			name:  "preserves query and fragment",
			input: "https://user:secret@host.com/path?q=1#frag",
			want:  "https://%5BREDACTED%5D@host.com/path?q=1#frag",
		},
		{
			name:  "no scheme",
			input: "host.com/path",
			want:  "host.com/path",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := RedactURL(tt.input)
			if got != tt.want {
				t.Errorf("RedactURL() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRedactDiscordWebhook(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "not a discord url",
			input: "https://example.com/hook",
			want:  "https://example.com/hook",
		},
		{
			name:  "redacts webhook token",
			input: "https://discord.com/api/webhooks/123456/abcdef-token-here",
			want:  "https://discord.com/api/webhooks/123456/[REDACTED]",
		},
		{
			name:  "webhook id only",
			input: "https://discord.com/api/webhooks/123456",
			want:  "https://discord.com/api/webhooks/123456",
		},
		{
			name:  "preserves query string",
			input: "https://discord.com/api/webhooks/123456/secret-token?wait=true",
			want:  "https://discord.com/api/webhooks/123456/[REDACTED]?wait=true",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := RedactDiscordWebhook(tt.input)
			if got != tt.want {
				t.Errorf("RedactDiscordWebhook() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsSensitiveKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		key  string
		want bool
	}{
		{"password", true},
		{"PASSWORD", true},
		{"secret_key", true},
		{"api_key", true},
		{"db_password", true},
		{"auth_token", true},
		{"host", false},
		{"port", false},
		{"name", false},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			t.Parallel()
			if got := isSensitiveKey(tt.key); got != tt.want {
				t.Errorf("isSensitiveKey(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

func TestRedactLogLines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		mustNot []string // substrings that MUST NOT appear in the output
		must    []string // substrings that MUST appear (preserved prefix etc.)
	}{
		// Fixture values are deliberately synthetic (lowercase, dashes)
		// so the repo's pre-commit secret scanner doesn't flag them as
		// real credentials. Pattern matching is case-insensitive so
		// these still exercise the production path.
		{
			// Authorization header's whole value gets blanked (preferred —
			// the value is one big secret), so any access-key buried in
			// `Credential=` is also gone. The reader still sees the
			// `Authorization:` prefix and the redacted marker.
			name:    "Authorization header with sigv4",
			input:   `Authorization: aws4-hmac-sha256 Credential=fake-access-key-1/20260521/us-east-1/s3/aws4_request, SignedHeaders=host`,
			mustNot: []string{"fake-access-key-1", "aws4-hmac-sha256", "SignedHeaders=host"},
			must:    []string{"Authorization:", "[REDACTED]"},
		},
		{
			// Standalone Credential= outside a header (e.g. in a debug
			// dump of the canonical request) is caught by the secondary
			// pattern.
			name:    "standalone sigv4 credential",
			input:   `canonical request: GET\n/?X-Amz-Algorithm=aws4-hmac-sha256 Credential=fake-access-key-1&X-Amz-Date=...`,
			mustNot: []string{"fake-access-key-1"},
			must:    []string{"aws4-hmac-sha256 Credential=", "[REDACTED]"},
		},
		{
			name:    "X-API-Key header",
			input:   `2026/05/21 10:00:00 X-API-Key: fake-vault-token-1`,
			mustNot: []string{"fake-vault-token-1"},
			must:    []string{"X-API-Key:", "[REDACTED]"},
		},
		{
			name:    "JSON password",
			input:   `{"username":"vault","password":"fake-pass-1","url":"http://x"}`,
			mustNot: []string{"fake-pass-1"},
			must:    []string{`"password"`, `"username":"vault"`, "[REDACTED]"},
		},
		{
			name:    "JSON secret_key",
			input:   `"access_key":"fake-ak","secret_key":"fake-sk-1"`,
			mustNot: []string{"fake-sk-1"},
			must:    []string{`"secret_key"`, "[REDACTED]"},
		},
		{
			name:    "inline URL credentials",
			input:   `dial tcp https://admin:fake-pass-1@nas.local:5000/webdav`,
			mustNot: []string{"fake-pass-1", "admin:fake-pass-1"},
			must:    []string{"https://", "@nas.local", "[REDACTED]"},
		},
		{
			name:    "discord webhook in log",
			input:   `notify: posting to https://discord.com/api/webhooks/123456/fake-discord-token-1`,
			mustNot: []string{"fake-discord-token-1"},
			must:    []string{"discord.com/api/webhooks/123456/", "[REDACTED]"},
		},
		{
			// Regex must NOT match arbitrary text mentioning the substring —
			// only legitimate https Discord URLs. This is the regression
			// test for CodeQL go/regex/missing-regexp-anchor.
			name:    "discord mention without https scheme is not redacted",
			input:   `user said: "see discord.com/api/webhooks/123456/fake-but-mentioned in chat"`,
			mustNot: []string{"[REDACTED]"},
			must:    []string{"discord.com/api/webhooks/123456/fake-but-mentioned"},
		},
		{
			name:    "cookie header",
			input:   `Cookie: session=fake-session-1; other=xyz`,
			mustNot: []string{"session=fake-session-1"},
			must:    []string{"Cookie:", "[REDACTED]"},
		},
		{
			name:    "passphrase",
			input:   `{"passphrase":"fake passphrase value"}`,
			mustNot: []string{"fake passphrase value"},
			must:    []string{`"passphrase"`, "[REDACTED]"},
		},
		{
			name:    "no-op when no secrets",
			input:   `runner: job 24 run 31 finished: completed (done=14, failed=0, size=5462181524)`,
			mustNot: []string{"[REDACTED]"}, // nothing should be redacted
			must:    []string{"job 24", "done=14"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := string(RedactLogLines([]byte(tt.input)))
			for _, s := range tt.mustNot {
				if contains(got, s) {
					t.Errorf("output still contains %q\ninput:  %s\noutput: %s", s, tt.input, got)
				}
			}
			for _, s := range tt.must {
				if !contains(got, s) {
					t.Errorf("output missing required %q\ninput:  %s\noutput: %s", s, tt.input, got)
				}
			}
		})
	}
}

func TestRedactLogLinesEmpty(t *testing.T) {
	t.Parallel()
	if got := RedactLogLines(nil); got != nil {
		t.Fatalf("nil in → got %v, want nil", got)
	}
	if got := RedactLogLines([]byte{}); len(got) != 0 {
		t.Fatalf("empty in → got %v, want empty", got)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
