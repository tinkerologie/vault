package handlers

import (
	"encoding/json"
	"testing"
)

func TestRedactConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  string
		want   map[string]string
		intact []string
	}{
		{
			name:   "local config unchanged",
			input:  `{"base_path":"/mnt/backups"}`,
			intact: []string{"base_path"},
		},
		{
			name:   "sftp password redacted",
			input:  `{"host":"192.168.1.1","user":"admin","password":"s3cret","base_path":"/backups"}`,
			want:   map[string]string{"password": "••••••••"},
			intact: []string{"host", "user", "base_path"},
		},
		{
			name:   "s3 secret key redacted",
			input:  `{"bucket":"my-bucket","access_key":"AKIA","secret_key":"wJalr","region":"us-east-1"}`,
			want:   map[string]string{"secret_key": "••••••••"},
			intact: []string{"bucket", "access_key", "region"},
		},
		{
			name:   "empty password not redacted",
			input:  `{"host":"192.168.1.1","password":""}`,
			intact: []string{"host", "password"},
		},
		{
			name:   "invalid json returns original",
			input:  `not-json`,
			intact: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := redactConfig(tt.input)

			if tt.intact == nil {
				if got != tt.input {
					t.Errorf("expected original string, got %q", got)
				}
				return
			}

			var result map[string]any
			if err := json.Unmarshal([]byte(got), &result); err != nil {
				t.Fatalf("failed to parse result: %v", err)
			}

			for key, expected := range tt.want {
				val, ok := result[key].(string)
				if !ok {
					t.Errorf("key %q missing", key)
					continue
				}
				if val != expected {
					t.Errorf("key %q = %q, want %q", key, val, expected)
				}
			}

			for _, key := range tt.intact {
				if _, ok := tt.want[key]; ok {
					continue
				}
				var orig map[string]any
				if err := json.Unmarshal([]byte(tt.input), &orig); err != nil {
					continue
				}
				if result[key] != orig[key] {
					t.Errorf("key %q modified: got %v, want %v", key, result[key], orig[key])
				}
			}
		})
	}
}
