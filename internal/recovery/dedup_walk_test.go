package recovery

import (
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/dedup"
	"github.com/ruaan-deysel/vault/internal/storage"
)

// TestWalkDedupContainer checks a container manifest's synthetic keys and
// volume pointers expand to the paths a user recognises: volume files under
// the volume's container path, single-file mounts at their path, metadata
// under _vault-metadata, and skipped volumes and markers dropped.
func TestWalkDedupContainer(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	adapter, err := storage.NewAdapter("local", `{"path":`+jsonString(t.TempDir())+`}`)
	if err != nil {
		t.Fatal(err)
	}
	destID, err := d.CreateStorageDestination(db.StorageDestination{Name: "t", Type: "local", Config: "{}", DedupEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	repo, err := dedup.InitRepo(d, adapter, destID, fixtureServerKey())
	if err != nil {
		t.Fatal(err)
	}
	chunk := func(b string) []dedup.ID {
		id, err := repo.Put([]byte(b))
		if err != nil {
			t.Fatal(err)
		}
		return []dedup.ID{id}
	}
	subID, err := repo.PutManifest("vol", dedup.Manifest{Version: 1, Files: map[string]dedup.ManifestEntry{
		"settings.yml":  {Size: 3, Chunks: chunk("abc")},
		"cache":         {IsDir: true, Mode: 0o755},
		"cache/blob.db": {Size: 4, Chunks: chunk("blob")},
	}})
	if err != nil {
		t.Fatal(err)
	}
	m := dedup.Manifest{Version: 1, Files: map[string]dedup.ManifestEntry{
		"__inspect":                {Size: 2, Chunks: chunk("{}")},
		"__template":               {Size: 6, Chunks: chunk("<xml/>")},
		"__dbdump_replay__":        {},
		"__vol__/config":           {IsDir: true, Chunks: []dedup.ID{subID}},
		"__vol__/excluded":         {Size: -1},
		"__volfile__/etc/app.conf": {Size: 2, Chunks: chunk("ok")},
	}}
	if err := repo.Flush(); err != nil {
		t.Fatal(err)
	}

	var got []string
	if err := walkDedup(repo, "container", m, func(p string, _ dedup.ManifestEntry) error {
		got = append(got, p)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	want := []string{
		"_vault-metadata/inspect.json",
		"_vault-metadata/template.xml",
		"config",
		"config/cache",
		"config/cache/blob.db",
		"config/settings.yml",
		"etc/app.conf",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("walkDedup paths =\n%v\nwant\n%v", got, want)
	}

	// The same keys in a folder manifest are ordinary file names.
	var folder []string
	_ = walkDedup(repo, "folder", dedup.Manifest{Files: map[string]dedup.ManifestEntry{"__inspect": {}}}, func(p string, _ dedup.ManifestEntry) error {
		folder = append(folder, p)
		return nil
	})
	if !reflect.DeepEqual(folder, []string{"__inspect"}) {
		t.Fatalf("folder walk = %v, want the key passed through", folder)
	}
}
