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
