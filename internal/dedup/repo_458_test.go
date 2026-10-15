package dedup

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"strings"
	"testing"

	"github.com/tinkerologie/vault/internal/storage"
)

type listErrAdapter struct {
	*FakeAdapter
	err error
}

func (a *listErrAdapter) List(string) ([]storage.FileInfo, error) { return nil, a.err }

func TestRepo458_HeaderBackups(t *testing.T) {
	t.Parallel()
	a := NewFakeAdapter()
	_ = a.Write(RepoRoot+"/repo.json", strings.NewReader("{}"))
	_ = a.Write(RepoRoot+"/repo.json.20260101T000000.bak", strings.NewReader("{}"))
	_ = a.Write(RepoRoot+"/vault.db.123", strings.NewReader("x"))
	got, err := HeaderBackups(a)
	if err != nil || len(got) != 1 || got[0] != RepoRoot+"/repo.json.20260101T000000.bak" {
		t.Fatalf("HeaderBackups = %v, %v", got, err)
	}
	if got, err := HeaderBackups(&listErrAdapter{FakeAdapter: a, err: fs.ErrNotExist}); got != nil || err != nil {
		t.Errorf("missing root = %v, %v", got, err)
	}
	boom := errors.New("list failed")
	if _, err := HeaderBackups(&listErrAdapter{FakeAdapter: a, err: boom}); !errors.Is(err, boom) {
		t.Errorf("list error = %v", err)
	}
}

func TestRepo458_ManifestEntryOwner(t *testing.T) {
	t.Parallel()
	if u, g := (ManifestEntry{}).Owner(); u != -1 || g != -1 {
		t.Errorf("unset owner = %d:%d", u, g)
	}
	uid, gid := 0, 100
	if u, g := (ManifestEntry{UID: &uid, GID: &gid}).Owner(); u != 0 || g != 100 {
		t.Errorf("owner = %d:%d", u, g)
	}
}

type writeErrAdapter struct{ *FakeAdapter }

func (writeErrAdapter) Write(string, io.Reader) error { return errors.New("read-only") }

func TestRepo458_InitRepoFailures(t *testing.T) {
	t.Parallel()
	r, key, cleanup := newTestRepo(t)
	defer cleanup()
	if _, err := InitRepo(r.db, r.adapter, r.storageID, key); err == nil || !strings.Contains(err.Error(), "already initialised") {
		t.Errorf("re-init: %v", err)
	}
	if _, err := InitRepo(r.db, writeErrAdapter{NewFakeAdapter()}, r.storageID, key); err == nil || !strings.Contains(err.Error(), "write repo.json") {
		t.Errorf("write failure: %v", err)
	}
	if _, err := InitRepo(r.db, NewFakeAdapter(), r.storageID, []byte("short")); err == nil {
		t.Error("bad server key: want error")
	}
}

func TestRepo458_PutGetFailures(t *testing.T) {
	t.Parallel()
	r, _, cleanup := newTestRepo(t)
	defer cleanup()
	if _, err := r.Put(make([]byte, maxChunkPlaintext+1)); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Errorf("oversized Put: %v", err)
	}
	var unknown ID
	unknown[1] = 9
	if _, err := r.Get(unknown); err == nil || !strings.Contains(err.Error(), "locate chunk") {
		t.Errorf("Get unknown: %v", err)
	}
	id, err := r.Put([]byte("payload"))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	packPath, _, _, err := r.LocateForVerify(id)
	if err != nil {
		t.Fatal(err)
	}
	_ = r.adapter.Delete(packPath)
	if _, err := r.Get(id); err == nil || !strings.Contains(err.Error(), "read chunk") {
		t.Errorf("Get missing pack: %v", err)
	}
	if _, err := r.ReadAndVerify(id); err == nil {
		t.Error("ReadAndVerify missing pack: want error")
	}
}

func TestRepo458_GetManifestDecodeFailures(t *testing.T) {
	t.Parallel()
	r, _, cleanup := newTestRepo(t)
	defer cleanup()
	put := func(b []byte) ID {
		t.Helper()
		id, err := r.Put(b)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	notJSON := put([]byte("definitely not json"))
	badEnv := put([]byte(`{"type":"segmented","segments":"nope"}`))
	var missing ID
	missing[2] = 7
	missingEnvBody, _ := json.Marshal(SegmentedManifest{Type: segmentedManifestType, Segments: []ID{missing}})
	missingEnv := put(missingEnvBody)
	garbageEnvBody, _ := json.Marshal(SegmentedManifest{Type: segmentedManifestType, Segments: []ID{notJSON}})
	garbageEnv := put(garbageEnvBody)
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}

	for name, tc := range map[string]struct {
		id   ID
		want string
	}{
		"plain":       {notJSON, "decode manifest"},
		"envelope":    {badEnv, "decode manifest envelope"},
		"segment":     {missingEnv, "get manifest segment 1/1"},
		"reassembled": {garbageEnv, "decode reassembled manifest"},
	} {
		if _, err := r.GetManifest(tc.id); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
	var unknown ID
	unknown[3] = 1
	if _, err := r.GetManifest(unknown); err == nil {
		t.Error("unknown manifest: want error")
	}
}

func TestRepo458_ManifestBlobRoundTrip(t *testing.T) {
	t.Parallel()
	r, _, cleanup := newTestRepo(t)
	defer cleanup()
	ct, err := r.EncryptManifest([]byte("secret manifest"))
	if err != nil {
		t.Fatal(err)
	}
	pt, err := r.DecryptManifest(ct)
	if err != nil || !bytes.Equal(pt, []byte("secret manifest")) {
		t.Fatalf("round trip = %q, %v", pt, err)
	}
	if _, err := r.DecryptManifest([]byte("short")); err == nil {
		t.Error("truncated ciphertext: want error")
	}
}

func TestRepo458_FakeAdapterWriteFromOpenError(t *testing.T) {
	t.Parallel()
	boom := errors.New("open failed")
	if err := NewFakeAdapter().WriteFrom("p", func() (io.ReadCloser, error) { return nil, boom }); !errors.Is(err, boom) {
		t.Errorf("WriteFrom = %v", err)
	}
	a := NewFakeAdapter()
	if err := a.WriteFrom("p", func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("x")), nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Stat("p"); err != nil {
		t.Errorf("Stat after WriteFrom: %v", err)
	}
}
