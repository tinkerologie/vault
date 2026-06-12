package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
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
			// Backblaze B2's web console displays endpoints as bare hostnames;
			// pasting that verbatim must be accepted (auto-prefixed to https://)
			// rather than failing with `unsupported protocol scheme ""`.
			name:    "valid b2 bare hostname endpoint",
			config:  S3Config{Bucket: "vault-bk", Region: "us-east-005", AccessKey: "AK", SecretKey: "SK", Endpoint: "s3.us-east-005.backblazeb2.com"},
			wantErr: false,
		},
		{
			name:    "valid minio path style",
			config:  S3Config{Bucket: "vault", Region: "us-east-1", AccessKey: "AK", SecretKey: "SK", Endpoint: "http://minio.local:9000", ForcePathStyle: true},
			wantErr: false,
		},
		{
			name:    "invalid endpoint scheme",
			config:  S3Config{Bucket: "vault-bk", Region: "us-east-1", AccessKey: "AK", SecretKey: "SK", Endpoint: "ftp://s3.example.com"},
			wantErr: true,
		},
		{
			name:    "endpoint with no host",
			config:  S3Config{Bucket: "vault-bk", Region: "us-east-1", AccessKey: "AK", SecretKey: "SK", Endpoint: "https://"},
			wantErr: true,
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

// TestNormalizeS3Endpoint locks in the regression fix for Backblaze B2 users
// pasting the bare-hostname endpoint shown in B2's web console. Without
// normalisation the SDK's url.Parse leaves Host empty and the virtual-host
// bucket prefix produces "//bucket./<original>: unsupported protocol scheme".
func TestNormalizeS3Endpoint(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"empty stays empty", "", "", false},
		{"bare hostname gets https", "s3.us-east-005.backblazeb2.com", "https://s3.us-east-005.backblazeb2.com", false},
		{"bare hostname with port", "minio.local:9000", "https://minio.local:9000", false},
		{"https unchanged", "https://s3.example.com", "https://s3.example.com", false},
		{"http unchanged", "http://minio.local:9000", "http://minio.local:9000", false},
		{"https with path unchanged", "https://r2.example.com/bucket-prefix", "https://r2.example.com/bucket-prefix", false},
		{"ftp scheme rejected", "ftp://s3.example.com", "", true},
		{"file scheme rejected", "file:///tmp/x", "", true},
		{"empty host rejected", "https://", "", true},
		{"empty host with path rejected", "https:///bucket", "", true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := normalizeS3Endpoint(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("normalizeS3Endpoint(%q) err=%v wantErr=%v", tt.in, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("normalizeS3Endpoint(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestS3AdapterTrimsCredentials verifies the constructor trims stray
// whitespace from access/secret keys. Backblaze B2's console renders the
// access key with surrounding padding that selects on double-click; without
// trimming the SDK signs with the leading space and the server returns 403.
func TestS3AdapterTrimsCredentials(t *testing.T) {
	t.Parallel()
	a, err := NewS3Adapter(S3Config{
		Bucket:    "vault-bk",
		Region:    "us-east-005",
		AccessKey: "  0053c196b92ee8c0000000001  ",
		SecretKey: "\tsecret-value\n",
		Endpoint:  "s3.us-east-005.backblazeb2.com",
	})
	if err != nil {
		t.Fatalf("NewS3Adapter: %v", err)
	}
	if a.config.AccessKey != "0053c196b92ee8c0000000001" {
		t.Errorf("AccessKey not trimmed: got %q", a.config.AccessKey)
	}
	if a.config.SecretKey != "secret-value" {
		t.Errorf("SecretKey not trimmed: got %q", a.config.SecretKey)
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

// TestS3KeyForLogsSanitizedSegmentsOnce — when a key segment is rewritten
// for gateway-signing compatibility, the original→stored mapping must be
// logged exactly once per adapter instance (previously the substitution was
// completely silent), and unchanged segments must not be logged.
//
// Deliberately not t.Parallel(): it captures the global log writer.
func TestS3KeyForLogsSanitizedSegmentsOnce(t *testing.T) {
	a, err := NewS3Adapter(S3Config{Bucket: "b", Region: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	for range 3 {
		key, err := a.keyFor("QA S3 sanitize-log/data.tar", false)
		if err != nil {
			t.Fatal(err)
		}
		if key != "QA_S3_sanitize-log/data.tar" {
			t.Fatalf("keyFor = %q, want sanitized key", key)
		}
	}

	out := buf.String()
	if got := strings.Count(out, `"QA S3 sanitize-log" is stored as "QA_S3_sanitize-log"`); got != 1 {
		t.Fatalf("sanitization logged %d times, want exactly once\nlog output: %s", got, out)
	}
	if strings.Contains(out, `"data.tar"`) {
		t.Fatalf("unchanged segment was logged:\n%s", out)
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
