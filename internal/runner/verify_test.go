package runner

import "testing"

func TestParseRestorePointChecksums_HappyPath(t *testing.T) {
	// Matches the actual restore-point metadata shape: items is an int
	// (count), item_sizes is per-item totals, checksums is the per-file
	// SHA-256 map.
	meta := `{
	  "backup_type": "full",
	  "checksums": {
	    "Flash Drive": {"data.tar.zst": "abc123", "metadata.json": "def456"},
	    "OtherItem":   {"data.tar.gz": "deadbeef"}
	  },
	  "item_sizes": {"Flash Drive": 100, "OtherItem": 50},
	  "items": 2,
	  "items_failed": 0,
	  "verified": true
	}`
	got, err := parseRestorePointChecksums(meta)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if got["Flash Drive/data.tar.zst"].SHA256 != "abc123" {
		t.Errorf("missing or wrong SHA for Flash Drive/data.tar.zst: %+v", got["Flash Drive/data.tar.zst"])
	}
	if got["OtherItem/data.tar.gz"].SHA256 != "deadbeef" {
		t.Errorf("missing other-item SHA: %+v", got["OtherItem/data.tar.gz"])
	}
}

func TestParseRestorePointChecksums_EmptyMetadata(t *testing.T) {
	got, err := parseRestorePointChecksums("")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil map, got %v", got)
	}
}

func TestParseRestorePointChecksums_NoChecksumsKey(t *testing.T) {
	meta := `{"items": 1, "item_sizes": {"a": 0}}`
	got, err := parseRestorePointChecksums(meta)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty result without checksums key, got %d", len(got))
	}
}

func TestParseRestorePointChecksums_SizeAlwaysZero(t *testing.T) {
	// Per-file sizes are not persisted in restore-point metadata today,
	// so every recordedChecksum has Size = 0 (the verifier's
	// size-mismatch check is skipped when Size is 0).
	meta := `{"checksums": {"X": {"f.tar": "ffee"}}, "items": 1, "item_sizes": {"X": 42}}`
	got, err := parseRestorePointChecksums(meta)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got["X/f.tar"].SHA256 != "ffee" {
		t.Errorf("missing SHA: %+v", got)
	}
	if got["X/f.tar"].Size != 0 {
		t.Errorf("size should default to 0, got %d", got["X/f.tar"].Size)
	}
}

func TestParseRestorePointChecksums_MalformedJSON(t *testing.T) {
	_, err := parseRestorePointChecksums("not json")
	if err == nil {
		t.Error("expected error on malformed JSON")
	}
}

func TestVerifyMode_IsValid(t *testing.T) {
	cases := map[VerifyMode]bool{
		VerifyModeQuick: true,
		VerifyModeDeep:  true,
		"":              false,
		"slow":          false,
		"QUICK":         false, // case sensitive — handler lowercases before checking
	}
	for m, want := range cases {
		if got := m.IsValid(); got != want {
			t.Errorf("IsValid(%q) = %v, want %v", m, got, want)
		}
	}
}
