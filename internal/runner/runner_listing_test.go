package runner

import (
	"bytes"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/ruaan-deysel/vault/internal/crypto"
	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/storage"
	"github.com/ruaan-deysel/vault/internal/ws"
)

// TestLoadParentVolumeListingPaths verifies the runner reads the parent
// restore point's volumes.json manifest and per-volume effective-listing
// sidecars into a mount-source -> path-list map for classic container
// differentials (issue #320).
func TestLoadParentVolumeListingPaths(t *testing.T) {
	t.Parallel()

	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	const (
		sourcePath = "/mnt/cache/appdata/foo"
		itemPrefix = "chain-test/1_full/my-item"
	)

	cases := []struct {
		name  string
		files map[string]string
		want  map[string][]string
	}{
		{
			name: "manifest and listing resolve to per-volume paths",
			files: map[string]string{
				itemPrefix + "/volumes.json":              `[{"index":0,"source":"` + sourcePath + `","destination":"/data","backed_up":true,"archive":"volume_0.tar"}]`,
				itemPrefix + "/volume_0.tar.listing.json": `{"version":1,"archive":"volume_0.tar","files":[{"path":"old.txt"},{"path":"sub/nested.txt"}]}`,
			},
			want: map[string][]string{
				sourcePath: {"old.txt", "sub/nested.txt"},
			},
		},
		{
			name: "compressed archive name resolves via manifest",
			files: map[string]string{
				itemPrefix + "/volumes.json":                 `[{"index":0,"source":"` + sourcePath + `","destination":"/data","backed_up":true,"archive":"volume_0.tar.gz"}]`,
				itemPrefix + "/volume_0.tar.gz.listing.json": `{"version":1,"archive":"volume_0.tar.gz","files":[{"path":"old.txt"},{"path":"sub/nested.txt"}]}`,
			},
			want: map[string][]string{
				sourcePath: {"old.txt", "sub/nested.txt"},
			},
		},
		{
			name:  "missing manifest returns nil",
			files: map[string]string{},
			want:  nil,
		},
		{
			name: "manifest without a listing returns nil",
			files: map[string]string{
				itemPrefix + "/volumes.json": `[{"index":0,"source":"` + sourcePath + `","destination":"/data","backed_up":true,"archive":"volume_0.tar"}]`,
			},
			want: nil,
		},
		{
			// All-or-nothing (issue #320 review follow-up): one backed-up
			// volume's listing resolving while another's is missing must
			// return nil — a partial map would keep changed_since set and
			// leave the unresolved volume on mtime-only filtering, silently
			// dropping a NEW stale-mtime file. The caller degrades to a full
			// archive instead.
			name: "partial listing resolution returns nil",
			files: map[string]string{
				itemPrefix + "/volumes.json": `[
					{"index":0,"source":"` + sourcePath + `","destination":"/data","backed_up":true,"archive":"volume_0.tar"},
					{"index":1,"source":"/mnt/cache/appdata/bar","destination":"/config","backed_up":true,"archive":"volume_1.tar"}
				]`,
				itemPrefix + "/volume_0.tar.listing.json": `{"version":1,"archive":"volume_0.tar","files":[{"path":"old.txt"}]}`,
			},
			want: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			storageRoot := t.TempDir()
			storageConfig := fmt.Sprintf(`{"path":%q}`, storageRoot)
			adapter, err := storage.NewAdapter("local", storageConfig)
			if err != nil {
				t.Fatalf("NewAdapter: %v", err)
			}
			t.Cleanup(func() { storage.CloseAdapter(adapter) })

			writeStorageFiles(t, adapter, tc.files)

			dest := db.StorageDestination{Type: "local", Config: storageConfig}
			parentRP := db.RestorePoint{StoragePath: "chain-test/1_full"}

			r := New(database, ws.NewHub(), nil)
			got := r.loadParentVolumeListingPaths(&parentRP, dest, "my-item", "")
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("loadParentVolumeListingPaths() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

// writeEncryptedStorageFile writes content to adapter at path as an
// age-encrypted blob — the on-disk form the encrypted upload path produces
// (the storage name carries a .age suffix, which the caller encodes in path).
func writeEncryptedStorageFile(t *testing.T, adapter storage.Adapter, path, content, passphrase string) {
	t.Helper()
	enc, err := crypto.EncryptReader(passphrase, strings.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	encBytes, err := io.ReadAll(enc)
	_ = enc.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Write(path, bytes.NewReader(encBytes)); err != nil {
		t.Fatalf("adapter.Write(%s): %v", path, err)
	}
}

// TestLoadParentVolumeListingPaths_Encrypted verifies that a classic container
// differential's per-volume effective-listing sidecars are encrypted at rest
// (stored as .age) and correctly decrypted back on read. The listing carries
// the full file set used to detect stale-mtime new files (issue #320), so it
// must not leak plaintext paths when the job has encryption enabled.
func TestLoadParentVolumeListingPaths_Encrypted(t *testing.T) {
	t.Parallel()

	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	const (
		sourcePath = "/mnt/cache/appdata/foo"
		itemPrefix = "chain-test/1_full/my-item"
		passphrase = "hunter2"
	)

	cases := []struct {
		name       string
		passphrase string
		want       map[string][]string
	}{
		{
			name:       "correct passphrase decrypts the encrypted listing",
			passphrase: passphrase,
			want:       map[string][]string{sourcePath: {"old.txt", "sub/nested.txt"}},
		},
		{
			name:       "wrong passphrase returns nil",
			passphrase: "wrong-passphrase",
			want:       nil,
		},
		{
			name:       "no passphrase returns nil",
			passphrase: "",
			want:       nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			storageRoot := t.TempDir()
			storageConfig := fmt.Sprintf(`{"path":%q}`, storageRoot)
			adapter, err := storage.NewAdapter("local", storageConfig)
			if err != nil {
				t.Fatalf("NewAdapter: %v", err)
			}
			t.Cleanup(func() { storage.CloseAdapter(adapter) })

			// Write both the volumes.json manifest and the volume listing
			// sidecar encrypted with `passphrase`, under their .age names —
			// the exact on-disk form the encrypted upload path produces.
			writeEncryptedStorageFile(t, adapter, itemPrefix+"/volumes.json.age",
				`[{"index":0,"source":"`+sourcePath+`","destination":"/data","backed_up":true,"archive":"volume_0.tar"}]`, passphrase)
			writeEncryptedStorageFile(t, adapter, itemPrefix+"/volume_0.tar.listing.json.age",
				`{"version":1,"archive":"volume_0.tar","files":[{"path":"old.txt"},{"path":"sub/nested.txt"}]}`, passphrase)

			dest := db.StorageDestination{Type: "local", Config: storageConfig}
			parentRP := db.RestorePoint{StoragePath: "chain-test/1_full"}

			r := New(database, ws.NewHub(), nil)
			got := r.loadParentVolumeListingPaths(&parentRP, dest, "my-item", tc.passphrase)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("loadParentVolumeListingPaths() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

// TestApplyClassicDiffListing is the regression test for the classic-path
// degradation of issue #320. When the parent's effective listing is
// unavailable for a classic differential/incremental folder or container item,
// changed_since must be CLEARED so the engine degrades to a full archive
// instead of silently mtime-only filtering (which drops NEW stale-mtime files).
func TestApplyClassicDiffListing(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name               string
		itemType           string
		listingPaths       []string
		volumeListingPaths map[string][]string
		wantChangedSince   bool
		wantPrevKey        string
		wantPrevValue      any
	}{
		{
			name:             "folder with listing keeps changed_since and attaches prev_listing_paths",
			itemType:         "folder",
			listingPaths:     []string{"old.txt", "sub/new.txt"},
			wantChangedSince: true,
			wantPrevKey:      "prev_listing_paths",
			wantPrevValue:    []string{"old.txt", "sub/new.txt"},
		},
		{
			name:             "folder with nil listing clears changed_since (full archive)",
			itemType:         "folder",
			listingPaths:     nil,
			wantChangedSince: false,
		},
		{
			name:               "container with listing keeps changed_since and attaches prev_volume_listing_paths",
			itemType:           "container",
			volumeListingPaths: map[string][]string{"/mnt/appdata": {"old.txt"}},
			wantChangedSince:   true,
			wantPrevKey:        "prev_volume_listing_paths",
			wantPrevValue:      map[string][]string{"/mnt/appdata": {"old.txt"}},
		},
		{
			name:               "container with nil listing clears changed_since (full archive)",
			itemType:           "container",
			volumeListingPaths: nil,
			wantChangedSince:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			settings := map[string]any{"changed_since": "2026-08-24T00:00:00Z"}
			applyClassicDiffListing(settings, tc.itemType, tc.listingPaths, tc.volumeListingPaths)

			_, hasChangedSince := settings["changed_since"]
			if hasChangedSince != tc.wantChangedSince {
				t.Fatalf("changed_since present = %v, want %v (settings = %#v)", hasChangedSince, tc.wantChangedSince, settings)
			}

			if tc.wantPrevKey != "" {
				got, ok := settings[tc.wantPrevKey]
				if !ok {
					t.Fatalf("settings missing %q: %#v", tc.wantPrevKey, settings)
				}
				if !reflect.DeepEqual(got, tc.wantPrevValue) {
					t.Fatalf("%s = %#v, want %#v", tc.wantPrevKey, got, tc.wantPrevValue)
				}
			} else {
				if _, ok := settings["prev_listing_paths"]; ok {
					t.Fatalf("unexpected prev_listing_paths set: %#v", settings)
				}
				if _, ok := settings["prev_volume_listing_paths"]; ok {
					t.Fatalf("unexpected prev_volume_listing_paths set: %#v", settings)
				}
			}
		})
	}
}

// TestIsSidecarBase verifies the tightened sidecar-name match (issue #320
// review feedback): only a real <archive>.{listing,index}.json name — one whose
// prefix carries ".tar" — is accepted, while unrelated files that merely end in
// the suffix are rejected.
func TestIsSidecarBase(t *testing.T) {
	t.Parallel()

	const (
		listing = ".listing.json"
		index   = ".index.json"
	)

	cases := []struct {
		name   string
		base   string
		suffix string
		want   bool
	}{
		{name: "plain listing", base: "data.tar.listing.json", suffix: listing, want: true},
		{name: "gzip listing", base: "data.tar.gz.listing.json", suffix: listing, want: true},
		{name: "zstd listing", base: "data.tar.zst.listing.json", suffix: listing, want: true},
		{name: "encrypted listing", base: "data.tar.zst.listing.json.age", suffix: listing, want: true},
		{name: "volume listing", base: "volume_0.tar.listing.json", suffix: listing, want: true},
		{name: "compressed volume listing", base: "volume_0.tar.gz.listing.json", suffix: listing, want: true},
		{name: "index sidecar", base: "data.tar.zst.index.json", suffix: index, want: true},
		{name: "unrelated file ending in suffix is rejected", base: "unrelated.listing.json", suffix: listing, want: false},
		{name: "unrelated encrypted file ending in suffix is rejected", base: "unrelated.listing.json.age", suffix: listing, want: false},
		{name: "bare suffix is rejected", base: ".listing.json", suffix: listing, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isSidecarBase(tc.base, tc.suffix); got != tc.want {
				t.Errorf("isSidecarBase(%q, %q) = %v, want %v", tc.base, tc.suffix, got, tc.want)
			}
		})
	}
}
