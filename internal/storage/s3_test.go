package storage

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
		{
			name:    "negative upload timeout",
			config:  S3Config{Bucket: "vault-bk", Region: "us-east-1", UploadTimeoutMinutes: -1},
			wantErr: true,
		},
		{
			name:    "explicit upload timeout",
			config:  S3Config{Bucket: "vault-bk", Region: "us-east-1", UploadTimeoutMinutes: 30},
			wantErr: false,
		},
		{
			name:    "part size below minimum",
			config:  S3Config{Bucket: "vault-bk", Region: "us-east-1", PartSizeMB: minS3PartSizeMB - 1},
			wantErr: true,
		},
		{
			name:    "part size above maximum",
			config:  S3Config{Bucket: "vault-bk", Region: "us-east-1", PartSizeMB: maxS3PartSizeMB + 1},
			wantErr: true,
		},
		{
			name:    "explicit part size at minimum",
			config:  S3Config{Bucket: "vault-bk", Region: "us-east-1", PartSizeMB: minS3PartSizeMB},
			wantErr: false,
		},
		{
			name:    "explicit part size at maximum",
			config:  S3Config{Bucket: "vault-bk", Region: "us-east-1", PartSizeMB: maxS3PartSizeMB},
			wantErr: false,
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

// TestS3AdapterPartSize locks in the part-size defaults and overrides that
// raise the per-object ceiling above transfermanager's 80 GB default. See
// issue #95: large Backblaze B2 uploads (e.g. ~281 GB Immich folder) failed
// with "exceeded total allowed S3 limit MaxUploadParts (10000)" because the
// SDK's auto-scale only triggers when objectSize is known up-front, and
// Vault's age-encrypted streams have no Size/ContentLength.
func TestS3AdapterPartSize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		configMB    int
		wantBytes   int64
		wantCeiling int64 // PartSize * 10 000, the multipart limit
	}{
		{name: "default when zero", configMB: 0, wantBytes: int64(defaultS3PartSizeMB) * 1024 * 1024, wantCeiling: int64(defaultS3PartSizeMB) * 1024 * 1024 * 10000},
		{name: "explicit 5 MiB minimum", configMB: 5, wantBytes: 5 * 1024 * 1024, wantCeiling: 5 * 1024 * 1024 * 10000},
		{name: "explicit 256 MiB", configMB: 256, wantBytes: 256 * 1024 * 1024, wantCeiling: 256 * 1024 * 1024 * 10000},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a, err := NewS3Adapter(S3Config{
				Bucket:     "b",
				Region:     "us-east-1",
				PartSizeMB: tt.configMB,
			})
			if err != nil {
				t.Fatalf("NewS3Adapter error: %v", err)
			}
			if a.partSizeBytes != tt.wantBytes {
				t.Errorf("partSizeBytes = %d, want %d", a.partSizeBytes, tt.wantBytes)
			}
			// The 10 000-part cap means the maximum-sized object we can
			// accept is partSizeBytes * 10000. Document that relationship
			// in the test so future tweaks don't silently lower the
			// ceiling below what users configured.
			if got := a.partSizeBytes * 10000; got != tt.wantCeiling {
				t.Errorf("multipart ceiling = %d, want %d", got, tt.wantCeiling)
			}
		})
	}
}

// TestS3AdapterChecksumDisabledForCustomEndpoints locks in the #88 follow-up
// fix for MEGA / Backblaze B2 / IDrive E2: when a custom Endpoint is set the
// adapter must dial back the SDK's default flexible-checksum trailer, which
// S3-compat gateways routinely reject with `403 SignatureDoesNotMatch` even
// though HeadBucket (Test Connection) succeeds. Real AWS (no endpoint) must
// preserve the SDK default so genuine S3 still validates checksums.
func TestS3AdapterChecksumDisabledForCustomEndpoints(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		endpoint     string
		wantRequest  aws.RequestChecksumCalculation
		wantResponse aws.ResponseChecksumValidation
	}{
		{
			name:         "custom endpoint dials back to WhenRequired",
			endpoint:     "https://s3.us-west-002.backblazeb2.com",
			wantRequest:  aws.RequestChecksumCalculationWhenRequired,
			wantResponse: aws.ResponseChecksumValidationWhenRequired,
		},
		{
			name:         "no endpoint keeps SDK default (WhenSupported)",
			endpoint:     "",
			wantRequest:  aws.RequestChecksumCalculationWhenSupported,
			wantResponse: aws.ResponseChecksumValidationWhenSupported,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a, err := NewS3Adapter(S3Config{
				Bucket:    "b",
				Region:    "us-east-1",
				AccessKey: "AK",
				SecretKey: "SK",
				Endpoint:  tt.endpoint,
			})
			if err != nil {
				t.Fatalf("NewS3Adapter error: %v", err)
			}
			if a.requestChecksum != tt.wantRequest {
				t.Errorf("requestChecksum = %v, want %v", a.requestChecksum, tt.wantRequest)
			}
			if a.responseChecksum != tt.wantResponse {
				t.Errorf("responseChecksum = %v, want %v", a.responseChecksum, tt.wantResponse)
			}
		})
	}
}

func TestS3AdapterUploadTimeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		configMins  int
		wantTimeout time.Duration
	}{
		{name: "default when zero", configMins: 0, wantTimeout: defaultS3UploadTimeout},
		{name: "explicit value", configMins: 30, wantTimeout: 30 * time.Minute},
		{name: "large explicit value", configMins: 720, wantTimeout: 12 * time.Hour},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a, err := NewS3Adapter(S3Config{
				Bucket:               "b",
				Region:               "us-east-1",
				UploadTimeoutMinutes: tt.configMins,
			})
			if err != nil {
				t.Fatalf("NewS3Adapter error: %v", err)
			}
			if a.uploadTimeout != tt.wantTimeout {
				t.Errorf("uploadTimeout = %v, want %v", a.uploadTimeout, tt.wantTimeout)
			}
			ctx, cancel := a.ctxUpload()
			defer cancel()
			dl, ok := ctx.Deadline()
			if !ok {
				t.Fatal("ctxUpload context has no deadline")
			}
			remaining := time.Until(dl)
			// Allow a small margin for clock skew between WithTimeout and Deadline read.
			if remaining < tt.wantTimeout-time.Second || remaining > tt.wantTimeout+time.Second {
				t.Errorf("ctxUpload deadline = %v from now, want ~%v", remaining, tt.wantTimeout)
			}
		})
	}
}

func TestS3GetCapacity(t *testing.T) {
	t.Parallel()
	page := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("list-type") != "2" {
			// Anything that isn't a List call — HeadBucket etc. — succeed minimally
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		page++
		if page == 1 {
			fmt.Fprintf(w, `<?xml version="1.0"?>
<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Name>b</Name><IsTruncated>true</IsTruncated><NextContinuationToken>tok</NextContinuationToken>
  <Contents><Key>a</Key><Size>%d</Size></Contents>
  <Contents><Key>b</Key><Size>%d</Size></Contents>
</ListBucketResult>`, 1<<30, 1<<30)
			return
		}
		fmt.Fprintf(w, `<?xml version="1.0"?>
<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Name>b</Name><IsTruncated>false</IsTruncated>
  <Contents><Key>c</Key><Size>%d</Size></Contents>
</ListBucketResult>`, 1<<30)
	}))
	defer server.Close()
	a, err := NewS3Adapter(S3Config{
		Bucket:         "b",
		Region:         "us-east-1",
		AccessKey:      "AK",
		SecretKey:      "SK",
		Endpoint:       server.URL,
		ForcePathStyle: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	cap, err := a.GetCapacity(context.Background())
	if err != nil {
		t.Fatalf("GetCapacity: %v", err)
	}
	if cap.Source != "s3-list-sum" {
		t.Errorf("source = %q", cap.Source)
	}
	if cap.TotalBytes != 0 {
		t.Errorf("expected TotalBytes=0, got %d", cap.TotalBytes)
	}
	if cap.FreeBytes != 0 {
		t.Errorf("expected FreeBytes=0, got %d", cap.FreeBytes)
	}
	if want := int64(3) << 30; cap.UsedBytes != want {
		t.Errorf("used = %d, want %d", cap.UsedBytes, want)
	}
}

func TestS3GetCapacityContextCancelled(t *testing.T) {
	t.Parallel()
	a, err := NewS3Adapter(S3Config{
		Bucket: "b", Region: "us-east-1", AccessKey: "AK", SecretKey: "SK",
		Endpoint: "http://127.0.0.1:1", ForcePathStyle: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cap, err := a.GetCapacity(ctx)
	if err == nil {
		t.Fatal("expected cancelled-context error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
	if cap.Source != "s3-list-sum" {
		t.Errorf("expected source set on partial result, got %q", cap.Source)
	}
}
