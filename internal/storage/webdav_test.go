package storage

import (
	"strings"
	"testing"
)

func TestNewWebDAVAdapter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  WebDAVConfig
		wantErr bool
	}{
		{
			name:    "valid https with auth",
			config:  WebDAVConfig{URL: "https://webdav.example.com/", Username: "u", Password: "p", BasePath: "vault-backups"},
			wantErr: false,
		},
		{
			name:    "valid http no auth",
			config:  WebDAVConfig{URL: "http://nas.local:8080/dav"},
			wantErr: false,
		},
		{
			name:    "valid with insecure tls",
			config:  WebDAVConfig{URL: "https://self-signed.local/", InsecureSkipVerify: true},
			wantErr: false,
		},
		{
			name:    "missing url",
			config:  WebDAVConfig{Username: "u", Password: "p"},
			wantErr: true,
		},
		{
			name:    "url without scheme",
			config:  WebDAVConfig{URL: "webdav.example.com"},
			wantErr: true,
		},
		{
			name:    "url with ftp scheme",
			config:  WebDAVConfig{URL: "ftp://example.com/"},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a, err := NewWebDAVAdapter(tt.config)
			if (err != nil) != tt.wantErr {
				t.Fatalf("NewWebDAVAdapter() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr {
				if a == nil {
					t.Fatal("adapter is nil")
				}
				if strings.HasSuffix(a.config.URL, "/") {
					t.Errorf("trailing slash should be trimmed: %q", a.config.URL)
				}
			}
		})
	}
}

func TestWebDAVRejectsTraversal(t *testing.T) {
	t.Parallel()
	a, err := NewWebDAVAdapter(WebDAVConfig{URL: "https://x.test/"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.fullPath("../escape", false); err == nil {
		t.Fatal("expected traversal to be rejected")
	}
	if _, err := a.fullPath("ok/relative", false); err != nil {
		t.Fatalf("legit path rejected: %v", err)
	}
}
