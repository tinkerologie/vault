package recovery

import (
	"archive/tar"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/dedup"
	"github.com/ruaan-deysel/vault/internal/storage"
)

// writeLockedRun stores a run whose manifest is an age envelope, which reads
// as locked without the passphrase.
func writeLockedRun(t *testing.T, root, job, run string) {
	t.Helper()
	dir := filepath.Join(root, job, run)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	env := `{"vault_manifest_enc":1,"key":"age","algo":"age","payload":"AAAA"}`
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(env), 0o644); err != nil {
		t.Fatal(err)
	}
}

func openPlain(t *testing.T, root string, key []byte) *Session {
	t.Helper()
	cfg, _ := json.Marshal(map[string]string{"path": root})
	s, err := Open(Options{StorageType: "local", StorageConfig: string(cfg), ServerKey: key})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

// TestOpenRejectsUnknownStorage checks a bad connection fails up front.
func TestOpenRejectsUnknownStorage(t *testing.T) {
	if _, err := Open(Options{StorageType: "carrier-pigeon", StorageConfig: "{}"}); err == nil {
		t.Fatal("unknown storage type opened")
	}
}

// TestDedupRepoNeedsTheRightKey checks a dedup destination without a key, or
// with another server's key, fails with an explanation.
func TestDedupRepoNeedsTheRightKey(t *testing.T) {
	root := t.TempDir()
	d, err := db.Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	adapter, _ := storage.NewAdapter("local", `{"path":`+jsonString(root)+`}`)
	id, _ := d.CreateStorageDestination(db.StorageDestination{Name: "t", Type: "local", Config: "{}", DedupEnabled: true})
	if _, err := dedup.InitRepo(d, adapter, id, fixtureServerKey()); err != nil {
		t.Fatal(err)
	}
	_ = d.Close()

	s := openPlain(t, root, nil)
	if !s.IsDedup() {
		t.Fatal("IsDedup = false for a dedup destination")
	}
	if _, err := s.dedupRepo(); err == nil || !strings.Contains(err.Error(), "--key") {
		t.Fatalf("no key: %v", err)
	}
	wrong := make([]byte, len(fixtureServerKey()))
	s = openPlain(t, root, wrong)
	if _, err := s.dedupRepo(); err == nil || !strings.Contains(err.Error(), "vault.key from the server") {
		t.Fatalf("wrong key: %v", err)
	}
	if openPlain(t, t.TempDir(), nil).IsDedup() {
		t.Fatal("IsDedup = true for an empty folder")
	}
}

// TestPointsAndChains covers ordering across jobs, explicit and missing
// references, a locked explicit reference, differential chains and a chain
// with no full base.
func TestPointsAndChains(t *testing.T) {
	root := t.TempDir()
	vm := Item{Name: "vm", Type: "vm"}
	writeClassicRun(t, root, "B", "2026-10-01_020000", "full", "2026-10-01T02:00:00Z", vm, map[string]string{"d.img": "1"})
	writeClassicRun(t, root, "B", "2026-10-02_020000", "incremental", "2026-10-02T02:00:00Z", vm, map[string]string{"d.img": "2"})
	writeClassicRun(t, root, "B", "2026-10-03_020000", "differential", "2026-10-03T02:00:00Z", vm, map[string]string{"d.img": "3"})
	writeClassicRun(t, root, "A", "2026-10-01_030000", "incremental", "2026-10-01T03:00:00Z", vm, map[string]string{"d.img": "a"})
	writeLockedRun(t, root, "A", "2026-10-02_030000")

	s := openPlain(t, root, nil)
	points, err := s.Points()
	if err != nil || len(points) != 5 {
		t.Fatalf("points = %d, %v", len(points), err)
	}
	if points[0].Job != "A" || points[2].Job != "B" {
		t.Fatalf("points not grouped by job: %v, %v", points[0].StoragePath, points[2].StoragePath)
	}

	diff, err := s.FindPoint("B/2026-10-03_020000")
	if err != nil {
		t.Fatal(err)
	}
	chain, err := s.chain(diff)
	if err != nil || len(chain) != 2 || chain[0].BackupType != "full" {
		t.Fatalf("differential chain = %+v, %v; want full + differential", chain, err)
	}
	baseless, _ := s.FindPoint("A/2026-10-01_030000")
	if _, err := s.chain(baseless); err == nil || !strings.Contains(err.Error(), "no earlier full backup") {
		t.Fatalf("baseless chain: %v", err)
	}
	if _, err := s.FindPoint("A/2026-10-02_030000"); err == nil || !strings.Contains(err.Error(), "passphrase") {
		t.Fatalf("explicit locked point: %v", err)
	}
	if _, err := s.FindPoint("B/1999-01-01_000000"); err == nil || !strings.Contains(err.Error(), "no backup at") {
		t.Fatalf("missing point: %v", err)
	}
	if err := (Point{StoragePath: "x", Locked: true}).lockedError(); err == nil || !strings.Contains(err.Error(), "could not be opened") {
		t.Fatalf("generic locked error: %v", err)
	}
	if _, err := s.chain(Point{Job: "B", StoragePath: "B/missing", BackupType: "incremental"}); err == nil {
		t.Fatal("chain for a point that is not on storage succeeded")
	}
}

// TestContentsClassicVariants covers a plugin tree, a single-file container
// volume, a classic item with no index, and an unknown item.
func TestContentsClassicVariants(t *testing.T) {
	root := t.TempDir()
	plugin := Item{Name: "plug", Type: "plugin"}
	writeClassicRun(t, root, "P", "2026-10-01_020000", "full", "2026-10-01T02:00:00Z", plugin, map[string]string{
		"config.tar":            buildTar(t, []tarEntry{{name: "settings.cfg", body: "x", typ: tar.TypeReg}}),
		"config.tar.index.json": `{"version":1,"archive":"config.tar","files":[{"path":"settings.cfg","size":1,"mode":"0644"}]}`,
	})
	c := Item{Name: "app", Type: "container"}
	writeClassicRun(t, root, "C", "2026-10-01_020000", "full", "2026-10-01T02:00:00Z", c, map[string]string{
		"volumes.json":            `[{"destination":"/etc/app.conf","backed_up":true,"archive":"volume_1.tar","is_file":true}]`,
		"volume_1.tar":            buildTar(t, []tarEntry{{name: "app.conf", body: "x", typ: tar.TypeReg}}),
		"volume_1.tar.index.json": `{"version":1,"archive":"volume_1.tar","files":[{"path":"app.conf","size":1,"mode":"0644"}]}`,
	})
	folder := Item{Name: "f", Type: "folder"}
	writeClassicRun(t, root, "F", "2026-10-01_020000", "full", "2026-10-01T02:00:00Z", folder, map[string]string{
		"data.tar": buildTar(t, []tarEntry{{name: "a", body: "x", typ: tar.TypeReg}}),
	})

	s := openPlain(t, root, nil)
	for ref, want := range map[string]string{"P/latest": "settings.cfg", "C/latest": "etc/app.conf"} {
		p, _ := s.FindPoint(ref)
		entries, err := s.Contents(p, p.Items[0].Name)
		if err != nil {
			t.Fatalf("%s: %v", ref, err)
		}
		found := false
		for _, e := range entries {
			found = found || e.Path == want
		}
		if !found {
			t.Errorf("%s: contents %+v missing %s", ref, entries, want)
		}
	}
	p, _ := s.FindPoint("F/latest")
	if _, err := s.Contents(p, "f"); err == nil || !strings.Contains(err.Error(), "no file index") {
		t.Fatalf("no index: %v", err)
	}
	if _, err := s.Contents(p, "nope"); err == nil {
		t.Fatal("unknown item listed")
	}
}

// TestNamerEdgeCases covers the root path, invalid UTF-8 and numbering a
// dot-file twin.
func TestNamerEdgeCases(t *testing.T) {
	n := newNamer(true, true)
	if got, err := n.local("./"); err != nil || got != "" {
		t.Fatalf("root = %q, %v", got, err)
	}
	if _, err := n.local("bad\xff"); err == nil {
		t.Fatal("invalid UTF-8 accepted")
	}
	if got := numberedName(".bashrc", 2); got != ".bashrc (2)" {
		t.Fatalf("numberedName(.bashrc) = %q", got)
	}
	if got := rawName("vdisk1.img.gz.age"); got != "vdisk1.img" {
		t.Fatalf("rawName = %q", got)
	}
	if parseMode("not-octal") != 0 {
		t.Fatal("parseMode accepted garbage")
	}
}

// TestReadOnlyAdapterPassesThroughProbes checks connection and capacity
// probes still reach the storage.
func TestReadOnlyAdapterPassesThroughProbes(t *testing.T) {
	inner, _ := storage.NewAdapter("local", `{"path":`+jsonString(t.TempDir())+`}`)
	a := readOnlyAdapter{inner: inner}
	if err := a.TestConnection(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if c, err := a.GetCapacity(ctx); err != nil || c.TotalBytes == 0 {
		t.Fatalf("GetCapacity = %+v, %v", c, err)
	}
}

// TestDedupRepoEmptyIndex checks a dedup repository that has never stored a
// pack (no index folder yet) opens with the right key.
func TestDedupRepoEmptyIndex(t *testing.T) {
	root := t.TempDir()
	d, _ := db.Open(filepath.Join(t.TempDir(), "vault.db"))
	adapter, _ := storage.NewAdapter("local", `{"path":`+jsonString(root)+`}`)
	id, _ := d.CreateStorageDestination(db.StorageDestination{Name: "t", Type: "local", Config: "{}", DedupEnabled: true})
	if _, err := dedup.InitRepo(d, adapter, id, fixtureServerKey()); err != nil {
		t.Fatal(err)
	}
	_ = d.Close()
	if _, err := openPlain(t, root, fixtureServerKey()).dedupRepo(); err != nil {
		t.Fatalf("empty repository: %v", err)
	}
}
