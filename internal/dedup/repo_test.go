package dedup

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
)

// newTestRepo spins up a fresh DB + fakeAdapter + InitRepo'd Repo. Returns
// the Repo, the serverKey used, and a cleanup callback.
func newTestRepo(t *testing.T) (*Repo, []byte, func()) {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	destID, err := d.CreateStorageDestination(db.StorageDestination{Name: "t", Type: "local", Config: "{}", DedupEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	serverKey := bytes.Repeat([]byte{0xee}, SecretSize)
	a := NewFakeAdapter()
	r, err := InitRepo(d, a, destID, serverKey)
	if err != nil {
		t.Fatal(err)
	}
	return r, serverKey, func() { d.Close(); os.RemoveAll(dir) }
}

func TestRepoInitWritesConfig(t *testing.T) {
	r, _, cleanup := newTestRepo(t)
	defer cleanup()
	// _vault/repo.json must exist on the underlying adapter.
	if _, err := r.adapter.Stat("_vault/repo.json"); err != nil {
		t.Fatalf("repo.json missing after Init: %v", err)
	}
}

func TestRepoInitRefusesDoubleInit(t *testing.T) {
	r, sk, cleanup := newTestRepo(t)
	defer cleanup()
	if _, err := InitRepo(r.db, r.adapter, r.storageID, sk); err == nil {
		t.Fatal("second InitRepo on same destination should fail")
	}
}

func TestRepoOpenWrongKeyFails(t *testing.T) {
	r, _, cleanup := newTestRepo(t)
	defer cleanup()
	wrong := bytes.Repeat([]byte{0x00}, SecretSize)
	if _, err := OpenRepo(r.db, r.adapter, r.storageID, wrong); err == nil {
		t.Fatal("OpenRepo accepted wrong serverKey")
	}
}

func TestRepoPutGetRoundTrip(t *testing.T) {
	r, _, cleanup := newTestRepo(t)
	defer cleanup()
	plain := make([]byte, 4096)
	if _, err := rand.Read(plain); err != nil {
		t.Fatal(err)
	}
	id, err := r.Put(plain)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	out, err := r.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, plain) {
		t.Fatal("round-trip mismatch")
	}
}

func TestRepoPutSkipsDuplicate(t *testing.T) {
	r, _, cleanup := newTestRepo(t)
	defer cleanup()
	plain := []byte("identical content")
	id1, err := r.Put(plain)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := r.Put(plain)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Fatal("identical input produced different ID")
	}
	if got := r.Stats().TotalChunks; got != 1 {
		t.Fatalf("expected TotalChunks=1, got %d", got)
	}
}

func TestRepoManifestRoundTrip(t *testing.T) {
	r, _, cleanup := newTestRepo(t)
	defer cleanup()
	m := Manifest{Version: ManifestVersion, Item: "test", Files: map[string]ManifestEntry{}}
	for i := 0; i < 1000; i++ {
		m.Files[fmt.Sprintf("file_%d", i)] = ManifestEntry{Size: int64(i), Chunks: []ID{{byte(i % 256)}}}
	}
	id, err := r.PutManifest("test", m)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	out, err := r.GetManifest(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Files) != 1000 {
		t.Fatalf("got %d files in restored manifest", len(out.Files))
	}
}

func TestRepoOpenAfterInit(t *testing.T) {
	r, sk, cleanup := newTestRepo(t)
	defer cleanup()
	plain := []byte("persisted data")
	id, err := r.Put(plain)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	// Open a second Repo instance against the same destination + storage.
	r2, err := OpenRepo(r.db, r.adapter, r.storageID, sk)
	if err != nil {
		t.Fatal(err)
	}
	out, err := r2.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, plain) {
		t.Fatal("data not readable after re-Open")
	}
}
