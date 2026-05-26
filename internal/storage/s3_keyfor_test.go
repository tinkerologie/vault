package storage

import (
	"strings"
	"testing"
)

// TestS3KeyFor_AllowRootEmpty covers the allowRoot=true + empty input
// branch where keyFor returns the trimmed BasePath.
func TestS3KeyFor_AllowRootEmpty(t *testing.T) {
	t.Parallel()
	a, err := NewS3Adapter(S3Config{
		Bucket: "b", Region: "us-east-1", BasePath: "vault/",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := a.keyFor("", true)
	if err != nil {
		t.Fatalf("keyFor(\"\", true): %v", err)
	}
	if got != "vault" {
		t.Errorf("keyFor empty allowRoot = %q, want %q", got, "vault")
	}
}

// TestS3KeyFor_AllowRootEmptyNoBase covers allowRoot=true + no BasePath.
func TestS3KeyFor_AllowRootEmptyNoBase(t *testing.T) {
	t.Parallel()
	a, err := NewS3Adapter(S3Config{Bucket: "b", Region: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := a.keyFor("", true)
	if err != nil {
		t.Fatalf("keyFor: %v", err)
	}
	if got != "" {
		t.Errorf("keyFor empty allowRoot no base = %q, want \"\"", got)
	}
}

// TestS3KeyFor_EmptyDisallowed covers allowRoot=false + empty input.
func TestS3KeyFor_EmptyDisallowed(t *testing.T) {
	t.Parallel()
	a, err := NewS3Adapter(S3Config{Bucket: "b", Region: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.keyFor("", false); err == nil {
		t.Error("expected error for empty path without allowRoot")
	}
	// Trailing slashes that collapse to empty after Trim should also be
	// rejected when allowRoot=false.
	if _, err := a.keyFor("////", false); err == nil {
		t.Error("expected error for slash-only path without allowRoot")
	}
}

// TestS3KeyFor_BackslashToSlash exercises the "\\" → "/" normalisation
// branch (Windows-style paths supplied by an operator on a Linux daemon).
func TestS3KeyFor_BackslashToSlash(t *testing.T) {
	t.Parallel()
	a, err := NewS3Adapter(S3Config{Bucket: "b", Region: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := a.keyFor("dir\\sub\\file.bin", false)
	if err != nil {
		t.Fatalf("keyFor backslash: %v", err)
	}
	if got != "dir/sub/file.bin" {
		t.Errorf("keyFor = %q, want %q", got, "dir/sub/file.bin")
	}
}

// TestS3KeyFor_DotSegment exercises the explicit "."-segment rejection.
func TestS3KeyFor_DotSegment(t *testing.T) {
	t.Parallel()
	a, err := NewS3Adapter(S3Config{Bucket: "b", Region: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.keyFor("ok/./nope", false); err == nil {
		t.Error("expected '.' segment to be rejected")
	}
}

// TestSanitizeS3KeySegment covers the character-by-character allow/deny
// branches: alpha, digit, dot, dash, underscore, tilde pass through;
// everything else (incl. unicode) becomes "_".
func TestSanitizeS3KeySegment(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
	}{
		{"plain", "plain"},
		{"with space", "with_space"},
		{"a.b-c_d~e", "a.b-c_d~e"},
		{"abc123", "abc123"},
		// Unicode characters fall through to the default branch.
		{"café", "caf_"},
		// Multiple consecutive non-allowed chars each become one "_".
		{"a$%^b", "a___b"},
		{"", ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			if got := sanitizeS3KeySegment(tc.in); got != tc.want {
				t.Errorf("sanitizeS3KeySegment(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestS3KeyFor_SegmentsSanitised covers the in-loop sanitizeS3KeySegment
// branch by passing a path with whitespace.
func TestS3KeyFor_SegmentsSanitised(t *testing.T) {
	t.Parallel()
	a, err := NewS3Adapter(S3Config{Bucket: "b", Region: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := a.keyFor("QA S3 verify/data file.tar", false)
	if err != nil {
		t.Fatalf("keyFor: %v", err)
	}
	if strings.Contains(got, " ") {
		t.Errorf("keyFor did not sanitise spaces: %q", got)
	}
}
