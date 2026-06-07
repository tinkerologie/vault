package dedup

import (
	"encoding/json"
	"testing"
	"time"
)

func TestManifestRoundTrip(t *testing.T) {
	m := Manifest{
		Version: ManifestVersion,
		Item:    "Plex",
		Files: map[string]ManifestEntry{
			"config/Preferences.xml": {Mode: 0o644, ModTime: time.Now().UTC().Format(time.RFC3339), Size: 1234, Chunks: []ID{{0x01}, {0x02}}},
			"logs/Plex.log":          {Mode: 0o644, ModTime: time.Now().UTC().Format(time.RFC3339), Size: 5678, Chunks: []ID{{0x03}}},
		},
	}
	b, err := m.EncodeJSON()
	if err != nil {
		t.Fatal(err)
	}
	out, err := DecodeManifest(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Files) != len(m.Files) {
		t.Fatalf("files count mismatch: got %d want %d", len(out.Files), len(m.Files))
	}
	for k, v := range m.Files {
		got, ok := out.Files[k]
		if !ok {
			t.Fatalf("missing file %q", k)
		}
		if got.Size != v.Size || len(got.Chunks) != len(v.Chunks) {
			t.Fatalf("file %q mismatch", k)
		}
	}
}

func TestIsSegmentedManifest(t *testing.T) {
	env, err := json.Marshal(SegmentedManifest{Type: segmentedManifestType, Segments: []ID{{0x01}}})
	if err != nil {
		t.Fatal(err)
	}
	v1, err := Manifest{Version: ManifestVersion, Item: "x", Files: map[string]ManifestEntry{}}.EncodeJSON()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		in   []byte
		want bool
	}{
		{"envelope", env, true},
		{"v1 manifest", v1, false},
		{"malformed", []byte("{not json"), false},
		{"empty", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isSegmentedManifest(c.in); got != c.want {
				t.Fatalf("isSegmentedManifest(%s) = %v, want %v", c.name, got, c.want)
			}
		})
	}
}
