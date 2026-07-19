package storage

import "testing"

// TestNewAdapter_BadJSON_AllTypes exercises the parse-error branch in each
// case of newRawAdapter. The happy paths are already covered by
// TestNewAdapterX; here we feed each storage type an unparsable JSON
// blob to drive its bad-JSON return.
func TestNewAdapter_BadJSON_AllTypes(t *testing.T) {
	t.Parallel()

	tests := []struct{ kind, blob string }{
		{"local", `{not json`},
		{"sftp", `{not json`},
		{"smb", `{not json`},
		{"nfs", `{not json`},
		{"webdav", `{not json`},
		{"s3", `{not json`},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.kind, func(t *testing.T) {
			t.Parallel()
			if _, err := NewAdapter(tt.kind, tt.blob); err == nil {
				t.Fatalf("expected error for %s with bad JSON", tt.kind)
			}
		})
	}
}
