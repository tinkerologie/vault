package storage

import "testing"

// TestNewS3Adapter_PartialCredentials drives the "partial credentials"
// rejection branch in NewS3Adapter: access-key set without secret-key,
// and vice versa.
func TestNewS3Adapter_PartialCredentials(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		accessKey string
		secretKey string
	}{
		{"access only", "AK", ""},
		{"secret only", "", "SK"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewS3Adapter(S3Config{
				Bucket:    "b",
				Region:    "us-east-1",
				AccessKey: tt.accessKey,
				SecretKey: tt.secretKey,
			})
			if err == nil {
				t.Fatal("expected partial-credentials error")
			}
		})
	}
}
