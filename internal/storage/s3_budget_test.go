package storage

import (
	"testing"

	s3manager "github.com/aws/aws-sdk-go-v2/feature/s3/manager"
)

func TestS3UploadPartConcurrencyMatchesSDK(t *testing.T) {
	if S3UploadPartConcurrency != s3manager.DefaultUploadConcurrency { //nolint:staticcheck // SA1019: the uploader Vault pins
		t.Fatalf("S3UploadPartConcurrency = %d, SDK default = %d", S3UploadPartConcurrency, s3manager.DefaultUploadConcurrency) //nolint:staticcheck // SA1019
	}
}

func TestS3UploadBufferBytes(t *testing.T) {
	for _, tc := range []struct {
		cfg     string
		streams int
		want    int64
	}{
		{`{"bucket":"b"}`, 3, 3 * 5 * 64 << 20},
		{`{"part_size_mb":1024}`, 1, 5 << 30},
		{`{"part_size_mb":16}`, 0, 5 * 16 << 20},
	} {
		got, err := S3UploadBufferBytes(tc.cfg, tc.streams)
		if err != nil || got != tc.want {
			t.Errorf("S3UploadBufferBytes(%s, %d) = %d, %v; want %d", tc.cfg, tc.streams, got, err, tc.want)
		}
	}
	if _, err := S3UploadBufferBytes("{", 1); err == nil {
		t.Error("malformed config: want error")
	}
}
