package storage

import (
	"strings"
	"testing"
)

func TestNewS3Adapter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  S3Config
		wantErr bool
	}{
		{
			name:    "valid aws",
			config:  S3Config{Bucket: "vault-bk", Region: "us-east-1", AccessKey: "AK", SecretKey: "SK"},
			wantErr: false,
		},
		{
			name:    "valid b2 with endpoint",
			config:  S3Config{Bucket: "vault-bk", Region: "us-west-002", AccessKey: "AK", SecretKey: "SK", Endpoint: "https://s3.us-west-002.backblazeb2.com"},
			wantErr: false,
		},
		{
			name:    "valid minio path style",
			config:  S3Config{Bucket: "vault", Region: "us-east-1", AccessKey: "AK", SecretKey: "SK", Endpoint: "http://minio.local:9000", ForcePathStyle: true},
			wantErr: false,
		},
		{
			name:    "valid no creds (default chain)",
			config:  S3Config{Bucket: "vault-bk", Region: "us-east-1"},
			wantErr: false,
		},
		{
			name:    "missing bucket",
			config:  S3Config{Region: "us-east-1", AccessKey: "AK", SecretKey: "SK"},
			wantErr: true,
		},
		{
			name:    "missing region",
			config:  S3Config{Bucket: "vault-bk", AccessKey: "AK", SecretKey: "SK"},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a, err := NewS3Adapter(tt.config)
			if (err != nil) != tt.wantErr {
				t.Fatalf("NewS3Adapter() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && a == nil {
				t.Fatal("adapter is nil")
			}
		})
	}
}

func TestS3KeyForRejectsTraversal(t *testing.T) {
	t.Parallel()
	a, err := NewS3Adapter(S3Config{Bucket: "b", Region: "us-east-1", BasePath: "vault"})
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"../escape", "ok/../bad", "/../etc/passwd"} {
		if _, err := a.keyFor(bad, false); err == nil {
			t.Errorf("expected traversal rejected for %q", bad)
		}
	}
	got, err := a.keyFor("backups/job-1.tar", false)
	if err != nil {
		t.Fatalf("legit path rejected: %v", err)
	}
	if !strings.HasPrefix(got, "vault/") {
		t.Errorf("expected base prefix in key, got %q", got)
	}
}
