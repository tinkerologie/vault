package storage

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
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

func TestStaticS3EndpointResolver(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		endpoint     string
		usePathStyle bool
		bucket       string
		wantHost     string
		wantPath     string
	}{
		{
			name:         "path style prepends bucket to path",
			endpoint:     "http://127.0.0.1:9100",
			usePathStyle: true,
			bucket:       "vault-test",
			wantHost:     "127.0.0.1:9100",
			wantPath:     "/vault-test",
		},
		{
			name:         "path style preserves existing endpoint path",
			endpoint:     "http://example.com/api",
			usePathStyle: true,
			bucket:       "mybucket",
			wantHost:     "example.com",
			wantPath:     "/api/mybucket",
		},
		{
			name:         "virtual host style prepends bucket subdomain",
			endpoint:     "https://s3.example.com",
			usePathStyle: false,
			bucket:       "mybucket",
			wantHost:     "mybucket.s3.example.com",
			wantPath:     "",
		},
		{
			name:         "no bucket leaves endpoint untouched",
			endpoint:     "http://127.0.0.1:9100",
			usePathStyle: true,
			bucket:       "",
			wantHost:     "127.0.0.1:9100",
			wantPath:     "",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := &staticS3EndpointResolver{endpoint: tt.endpoint, usePathStyle: tt.usePathStyle}
			params := s3.EndpointParameters{}
			if tt.bucket != "" {
				params.Bucket = aws.String(tt.bucket)
			}
			got, err := r.ResolveEndpoint(context.Background(), params)
			if err != nil {
				t.Fatalf("ResolveEndpoint err: %v", err)
			}
			if got.URI.Host != tt.wantHost {
				t.Errorf("host = %q, want %q", got.URI.Host, tt.wantHost)
			}
			if got.URI.Path != tt.wantPath {
				t.Errorf("path = %q, want %q", got.URI.Path, tt.wantPath)
			}
		})
	}
}
