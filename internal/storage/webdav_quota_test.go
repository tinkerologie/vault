package storage

import "testing"

// TestExtractWebDAVPropInt covers the local-name match and the
// missing-element fallback branches without spinning up a full
// httptest server.
func TestExtractWebDAVPropInt(t *testing.T) {
	t.Parallel()
	body := []byte(`<?xml version="1.0"?>
<d:multistatus xmlns:d="DAV:">
  <d:response>
    <d:propstat>
      <d:prop>
        <d:quota-used-bytes>123</d:quota-used-bytes>
        <d:quota-available-bytes>-1</d:quota-available-bytes>
      </d:prop>
    </d:propstat>
  </d:response>
</d:multistatus>`)

	used, ok := extractWebDAVPropInt(body, "quota-used-bytes")
	if !ok || used != 123 {
		t.Errorf("quota-used-bytes: got (%d, %t), want (123, true)", used, ok)
	}
	avail, ok := extractWebDAVPropInt(body, "quota-available-bytes")
	if !ok || avail != -1 {
		t.Errorf("quota-available-bytes: got (%d, %t), want (-1, true)", avail, ok)
	}
	if v, ok := extractWebDAVPropInt(body, "quota-missing"); ok {
		t.Errorf("missing prop should return ok=false, got (%d, true)", v)
	}
}

// TestParseWebDAVQuotaProps covers both successful and partial cases
// (used present, available missing) which exercise the &&-short-circuit
// branch of parseWebDAVQuotaProps.
func TestParseWebDAVQuotaProps(t *testing.T) {
	t.Parallel()
	full := []byte(`<a><quota-used-bytes>10</quota-used-bytes><quota-available-bytes>20</quota-available-bytes></a>`)
	used, avail, ok := parseWebDAVQuotaProps(full)
	if !ok || used != 10 || avail != 20 {
		t.Errorf("full body: got (%d, %d, %t)", used, avail, ok)
	}

	usedOnly := []byte(`<a><quota-used-bytes>10</quota-used-bytes></a>`)
	if _, _, ok := parseWebDAVQuotaProps(usedOnly); ok {
		t.Error("partial body should report ok=false")
	}

	empty := []byte(`<a></a>`)
	if _, _, ok := parseWebDAVQuotaProps(empty); ok {
		t.Error("empty body should report ok=false")
	}
}
