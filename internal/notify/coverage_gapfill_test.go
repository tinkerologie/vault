package notify

import (
	"testing"
)

// TestJobPartial exercises JobPartial, mirroring TestJobSuccess/JobFailed.
// On non-Linux platforms Send no-ops, so the call returns nil.
func TestJobPartial(t *testing.T) {
	t.Parallel()
	if err := JobPartial("test-job", 3, 2); err != nil {
		t.Errorf("JobPartial() error = %v", err)
	}
}

// TestNormalizeDiscordWebhookURL_Errors targets the remaining error branches
// of normalizeDiscordWebhookURL that the existing tests don't reach.
func TestNormalizeDiscordWebhookURL_Errors(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "empty returns empty", input: "", wantErr: false},
		{name: "whitespace returns empty", input: "   ", wantErr: false},
		{name: "invalid url with control bytes", input: "https://discord.com/\x7f", wantErr: true},
		{name: "http scheme rejected", input: "http://discord.com/api/webhooks/1/t", wantErr: true},
		{name: "userinfo rejected", input: "https://user:pass@discord.com/api/webhooks/1/t", wantErr: true},
		{name: "query string rejected", input: "https://discord.com/api/webhooks/1/t?x=1", wantErr: true},
		{name: "fragment rejected", input: "https://discord.com/api/webhooks/1/t#frag", wantErr: true},
		{name: "non-discord host rejected", input: "https://evil.example/api/webhooks/1/t", wantErr: true},
		{name: "wrong path shape rejected", input: "https://discord.com/foo/bar", wantErr: true},
		{name: "extra path segments rejected", input: "https://discord.com/api/webhooks/1/t/extra", wantErr: true},
		{name: "missing token rejected", input: "https://discord.com/api/webhooks//", wantErr: true},
		{name: "valid plain webhook accepted", input: "https://discord.com/api/webhooks/1/abc", wantErr: false},
		{name: "valid versioned webhook accepted", input: "https://discord.com/api/v10/webhooks/1/abc", wantErr: false},
		{name: "valid discordapp.com host accepted", input: "https://discordapp.com/api/webhooks/1/abc", wantErr: false},
		{name: "valid ptb.discord.com host accepted", input: "https://ptb.discord.com/api/webhooks/1/abc", wantErr: false},
		{name: "valid canary.discord.com host accepted", input: "https://canary.discord.com/api/webhooks/1/abc", wantErr: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := normalizeDiscordWebhookURL(tt.input)
			if tt.wantErr && err == nil {
				t.Fatalf("normalizeDiscordWebhookURL(%q): expected error, got nil", tt.input)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("normalizeDiscordWebhookURL(%q): unexpected error: %v", tt.input, err)
			}
		})
	}
}
