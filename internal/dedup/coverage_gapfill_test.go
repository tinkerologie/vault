// Coverage gap-fill for previously-uncovered functions:
// repo.go: SplitterSecret, Has, SessionLogicalBytes
// testhelpers.go: FakeAdapter.TestConnection, GetCapacity, NewTestRepoForEngine
// chunker.go: NewChunker invalid-secret-length branch
// crypto.go: SealMaster / UnsealMaster bad-key-length branches
// repo.go: Put error path (intra-pack duplicate skip), Flush no-op
package dedup

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
)

// --- repo.go: SplitterSecret -----------------------------------------------

func TestSplitterSecretReturnsDefensiveCopy(t *testing.T) {
	t.Parallel()
	r, _, cleanup := newTestRepo(t)
	defer cleanup()

	s := r.SplitterSecret()
	if len(s) != SecretSize {
		t.Fatalf("SplitterSecret length = %d, want %d", len(s), SecretSize)
	}
	// Mutating the returned slice must not affect future calls — function
	// returns a defensive copy.
	original := append([]byte(nil), s...)
	for i := range s {
		s[i] ^= 0xff
	}
	again := r.SplitterSecret()
	if !bytes.Equal(again, original) {
		t.Fatal("SplitterSecret returned a non-copy: caller mutation leaked")
	}
}

// --- repo.go: Has ----------------------------------------------------------

func TestRepoHasReflectsPutFlush(t *testing.T) {
	t.Parallel()
	r, _, cleanup := newTestRepo(t)
	defer cleanup()

	plain := []byte("payload")
	id := r.ChunkID(plain)

	// Before Put, Has must be false.
	if r.Has(id) {
		t.Fatal("Has() = true before Put")
	}

	if _, err := r.Put(plain); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := r.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	if !r.Has(id) {
		t.Fatal("Has() = false after Put+Flush")
	}

	// Unrelated ID stays false.
	var unrelated ID
	if r.Has(unrelated) {
		t.Fatal("Has(unrelated) = true")
	}
}

// --- repo.go: SessionLogicalBytes ------------------------------------------

func TestRepoSessionLogicalBytesCountsEveryPut(t *testing.T) {
	t.Parallel()
	r, _, cleanup := newTestRepo(t)
	defer cleanup()

	if got := r.SessionLogicalBytes(); got != 0 {
		t.Fatalf("initial SessionLogicalBytes = %d, want 0", got)
	}

	// First Put adds len(plaintext) bytes.
	plain := bytes.Repeat([]byte{0xab}, 1000)
	if _, err := r.Put(plain); err != nil {
		t.Fatal(err)
	}
	if got := r.SessionLogicalBytes(); got != 1000 {
		t.Fatalf("after first Put: SessionLogicalBytes = %d, want 1000", got)
	}

	// Duplicate Put also adds bytes — counter reflects "what the user wrote",
	// not "what was newly stored".
	if _, err := r.Put(plain); err != nil {
		t.Fatal(err)
	}
	if got := r.SessionLogicalBytes(); got != 2000 {
		t.Fatalf("after duplicate Put: SessionLogicalBytes = %d, want 2000 (counts duplicates)", got)
	}

	// Distinct content adds further bytes.
	other := bytes.Repeat([]byte{0xcd}, 250)
	if _, err := r.Put(other); err != nil {
		t.Fatal(err)
	}
	if got := r.SessionLogicalBytes(); got != 2250 {
		t.Fatalf("after distinct Put: SessionLogicalBytes = %d, want 2250", got)
	}
}

// --- testhelpers.go: FakeAdapter.TestConnection / GetCapacity --------------

func TestFakeAdapterTestConnection(t *testing.T) {
	t.Parallel()
	a := NewFakeAdapter()
	if err := a.TestConnection(); err != nil {
		t.Fatalf("TestConnection: %v", err)
	}
}

func TestFakeAdapterGetCapacity(t *testing.T) {
	t.Parallel()
	a := NewFakeAdapter()
	cap, err := a.GetCapacity(context.Background())
	if err != nil {
		t.Fatalf("GetCapacity: %v", err)
	}
	if cap.Source != "fake" {
		t.Fatalf("Source = %q, want fake", cap.Source)
	}
}

// --- testhelpers.go: NewTestRepoForEngine ----------------------------------

func TestNewTestRepoForEngineSpinsUp(t *testing.T) {
	t.Parallel()
	r, a, cleanup := NewTestRepoForEngine(t)
	defer cleanup()

	if r == nil || a == nil {
		t.Fatal("NewTestRepoForEngine returned nil components")
	}

	// Repo must be usable — round-trip a chunk to prove it.
	plain := []byte("engine test payload")
	id, err := r.Put(plain)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := r.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	out, err := r.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(out, plain) {
		t.Fatal("round-trip mismatch")
	}

	// FakeAdapter must already have the repo's config file written.
	if _, err := a.Stat("_vault/repo.json"); err != nil {
		t.Fatalf("repo.json missing on fake adapter: %v", err)
	}
}

// --- chunker.go: NewChunker invalid-secret-length branch --------------------

func TestNewChunkerRejectsWrongSecretSize(t *testing.T) {
	t.Parallel()
	cases := [][]byte{
		nil,
		{},
		bytes.Repeat([]byte{0x01}, SecretSize-1),
		bytes.Repeat([]byte{0x01}, SecretSize+1),
	}
	for i, secret := range cases {
		if _, err := NewChunker(secret); err == nil {
			t.Fatalf("case %d (len=%d): NewChunker accepted invalid secret", i, len(secret))
		}
	}
}

// --- crypto.go: Seal/Unseal bad-key-length branches ------------------------

func TestSealMasterRejectsWrongKeyLengths(t *testing.T) {
	t.Parallel()
	good := bytes.Repeat([]byte{0x77}, SecretSize)

	// serverKey wrong length.
	if _, err := SealMaster(bytes.Repeat([]byte{0x01}, 16), good); err == nil {
		t.Fatal("SealMaster accepted 16-byte serverKey")
	}
	// repoMaster wrong length.
	if _, err := SealMaster(good, bytes.Repeat([]byte{0x01}, 16)); err == nil {
		t.Fatal("SealMaster accepted 16-byte repoMaster")
	}
}

func TestUnsealMasterRejectsWrongKeyLength(t *testing.T) {
	t.Parallel()
	if _, err := UnsealMaster(bytes.Repeat([]byte{0x01}, 16), []byte("doesntmatter")); err == nil {
		t.Fatal("UnsealMaster accepted 16-byte serverKey")
	}
}

func TestUnsealMasterRejectsShortEnvelope(t *testing.T) {
	t.Parallel()
	sk := bytes.Repeat([]byte{0x99}, SecretSize)
	if _, err := UnsealMaster(sk, []byte{0x01, 0x02}); err == nil {
		t.Fatal("UnsealMaster accepted envelope shorter than nonce size")
	}
}

// --- repo.go: Put pending-set dedup path -----------------------------------

// When two Puts with the same content happen BEFORE a Flush, the first goes
// into the packer's buffer and is added to r.pending. The second Put sees
// the chunk in r.pending and returns the same ID without re-adding — drives
// the `if _, ok := r.pending[id]; ok` short-circuit branch in Put().
func TestRepoPutPendingShortCircuit(t *testing.T) {
	t.Parallel()
	r, _, cleanup := newTestRepo(t)
	defer cleanup()

	plain := bytes.Repeat([]byte{0xde, 0xad}, 100)

	id1, err := r.Put(plain)
	if err != nil {
		t.Fatal(err)
	}
	// Same content, no Flush in between — should hit r.pending and return same ID.
	id2, err := r.Put(plain)
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Fatal("pending-skip returned a different ID")
	}

	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	// Stats must reflect exactly one chunk on storage despite two Puts.
	if got := r.Stats().TotalChunks; got != 1 {
		t.Fatalf("TotalChunks = %d, want 1 (intra-pack duplicate must not double-count)", got)
	}
}

// --- repo.go: Flush no-op branch -------------------------------------------

func TestRepoFlushOnEmptyRepo(t *testing.T) {
	t.Parallel()
	r, _, cleanup := newTestRepo(t)
	defer cleanup()
	// Flush on a fresh repo (no pending pack) is a no-op — drives the
	// packer.Flush short-circuit and exposes Repo.Flush's success path.
	if err := r.Flush(); err != nil {
		t.Fatalf("Flush on empty repo: %v", err)
	}
	// Calling it twice in a row is fine.
	if err := r.Flush(); err != nil {
		t.Fatalf("second Flush: %v", err)
	}
}

// --- chunker.go: Split early-error branch in callback ----------------------

// The Split callback's non-nil error is returned through Split; covers the
// callback-error branch.
func TestChunkerSplitCallbackErrorPropagates(t *testing.T) {
	t.Parallel()
	secret := bytes.Repeat([]byte{0xab}, SecretSize)
	chunker, err := NewChunker(secret)
	if err != nil {
		t.Fatal(err)
	}
	// Force at least one chunk by feeding enough data.
	data := make([]byte, ChunkMin*2)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	sentinel := errFromCallback{}
	err = chunker.Split(bytes.NewReader(data), func(chunk []byte) error {
		return sentinel
	})
	if err == nil {
		t.Fatal("Split should propagate callback error, got nil")
	}
}

type errFromCallback struct{}

func (errFromCallback) Error() string { return "callback says no" }

// --- testhelpers.go: FakeAdapter error / range branches -------------------

// failingReader is an io.Reader that always errors. Used to drive the
// io.ReadAll failure branch inside FakeAdapter.Write.
type failingReader struct{}

func (failingReader) Read(_ []byte) (int, error) { return 0, errFromCallback{} }

func TestFakeAdapterWriteReadAllError(t *testing.T) {
	t.Parallel()
	a := NewFakeAdapter()
	if err := a.Write("any", failingReader{}); err == nil {
		t.Fatal("expected Write to surface reader error")
	}
}

func TestFakeAdapterReadMissing(t *testing.T) {
	t.Parallel()
	a := NewFakeAdapter()
	if _, err := a.Read("no/such/path"); err == nil {
		t.Fatal("Read on missing path should error")
	}
}

func TestFakeAdapterReadRangeBranches(t *testing.T) {
	t.Parallel()
	a := NewFakeAdapter()
	// Seed a small file.
	if err := a.Write("p", bytes.NewReader([]byte("0123456789"))); err != nil {
		t.Fatal(err)
	}
	// Missing path branch.
	if _, err := a.ReadRange("missing", 0, 1); err == nil {
		t.Fatal("ReadRange missing path should error")
	}
	// offset >= len(b) branch.
	if _, err := a.ReadRange("p", 100, 1); err == nil {
		t.Fatal("ReadRange beyond end should error")
	}
	// length runs past end — truncates rather than erroring.
	rc, err := a.ReadRange("p", 8, 100)
	if err != nil {
		t.Fatalf("ReadRange tail-truncate: %v", err)
	}
	defer rc.Close()
	buf := make([]byte, 100)
	n, _ := rc.Read(buf)
	if string(buf[:n]) != "89" {
		t.Fatalf("got %q, want '89'", buf[:n])
	}
}

// --- index.go: AppendStorageIndex / AppendTombstone happy paths ------------

// These hit the success paths and exercise nextIndexSeq when the directory
// is empty (returns 1).
func TestIndexAppendStorageIndexThenTombstone(t *testing.T) {
	t.Parallel()
	r, _, cleanup := newTestRepo(t)
	defer cleanup()

	info := PackInfo{
		ID: "pack-test", Path: "p/test", SizeBytes: 100, ChunkCount: 1,
		Entries: []PackEntry{{ID: ID{0x01}, Offset: 0, Length: 50, Flags: 0}},
	}
	if err := r.idx.AppendStorageIndex(info); err != nil {
		t.Fatalf("AppendStorageIndex: %v", err)
	}
	if err := r.idx.AppendTombstone("pack-test"); err != nil {
		t.Fatalf("AppendTombstone: %v", err)
	}
	// Two files exist under _vault/index.
	entries, err := r.adapter.List("_vault/index")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 index files, got %d", len(entries))
	}
}

// --- index.go: RebuildFromStorage end-to-end -------------------------------

func TestIndexRebuildFromStorageReplaysAddAndTombstone(t *testing.T) {
	t.Parallel()
	r, _, cleanup := newTestRepo(t)
	defer cleanup()

	// Write a real chunk so a pack exists in SQLite + storage, then flush.
	plain := []byte("rebuild-me")
	id, err := r.Put(plain)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}

	// Rebuild — wipes SQLite tables then replays from _vault/index.
	if err := r.idx.RebuildFromStorage(); err != nil {
		t.Fatalf("RebuildFromStorage: %v", err)
	}

	// The chunk must still be locatable after rebuild.
	if !r.Has(id) {
		t.Fatal("chunk missing after RebuildFromStorage")
	}
	out, err := r.Get(id)
	if err != nil {
		t.Fatalf("Get after rebuild: %v", err)
	}
	if !bytes.Equal(out, plain) {
		t.Fatal("rebuilt chunk plaintext mismatch")
	}
}

// TestIndexRebuildFromStorageCorruptEntry: a corrupt JSONL line in an index
// blob causes RebuildFromStorage to fail with a parse error, driving the
// `json.Unmarshal err != nil` branch (index.go:174-177).
func TestIndexRebuildFromStorageCorruptEntry(t *testing.T) {
	t.Parallel()
	r, _, cleanup := newTestRepo(t)
	defer cleanup()
	// Write a junk file under _vault/index so the scanner reads it as a line.
	if err := r.adapter.Write("_vault/index/0000000001.idx", bytes.NewReader([]byte("not json\n"))); err != nil {
		t.Fatal(err)
	}
	if err := r.idx.RebuildFromStorage(); err == nil {
		t.Fatal("RebuildFromStorage with corrupt entry should fail")
	}
}

// --- repo.go: OpenRepo failure paths ---------------------------------------

func TestOpenRepoMissingConfig(t *testing.T) {
	t.Parallel()
	r, _, cleanup := newTestRepo(t)
	defer cleanup()
	// Delete repo.json on the adapter to force the Read error path in OpenRepo.
	if err := r.adapter.Delete("_vault/repo.json"); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenRepo(r.db, r.adapter, r.storageID, bytes.Repeat([]byte{0xee}, SecretSize)); err == nil {
		t.Fatal("OpenRepo with missing repo.json should error")
	}
}

func TestOpenRepoWrongVersion(t *testing.T) {
	t.Parallel()
	r, _, cleanup := newTestRepo(t)
	defer cleanup()
	// Overwrite repo.json with a config carrying an unsupported version.
	cfg := []byte(`{"version":99,"uuid":"abc","sealed_master":"00"}`)
	if err := r.adapter.Write("_vault/repo.json", bytes.NewReader(cfg)); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenRepo(r.db, r.adapter, r.storageID, bytes.Repeat([]byte{0xee}, SecretSize)); err == nil {
		t.Fatal("OpenRepo with unsupported version should error")
	}
}

func TestOpenRepoCorruptConfig(t *testing.T) {
	t.Parallel()
	r, _, cleanup := newTestRepo(t)
	defer cleanup()
	// Overwrite repo.json with junk → json.Unmarshal fails.
	if err := r.adapter.Write("_vault/repo.json", bytes.NewReader([]byte("not json"))); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenRepo(r.db, r.adapter, r.storageID, bytes.Repeat([]byte{0xee}, SecretSize)); err == nil {
		t.Fatal("OpenRepo on corrupt config should error")
	}
}

// --- packer.go: ReadPackFooter on a real pack ------------------------------

func TestReadPackFooterRoundTrip(t *testing.T) {
	t.Parallel()
	r, _, cleanup := newTestRepo(t)
	defer cleanup()

	// Put → Flush so a pack lands on storage.
	if _, err := r.Put([]byte("hello footer")); err != nil {
		t.Fatal(err)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	// Find the freshly-written pack path.
	packs, err := r.adapter.List("_vault/packs")
	if err != nil {
		t.Fatal(err)
	}
	var packPath string
	for _, e := range packs {
		if !e.IsDir {
			packPath = e.Path
			break
		}
	}
	if packPath == "" {
		t.Fatal("no pack file found after Flush")
	}

	entries, err := ReadPackFooter(r.adapter, packPath)
	if err != nil {
		t.Fatalf("ReadPackFooter: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("ReadPackFooter returned zero entries")
	}
}

func TestReadPackFooterOnMissingFile(t *testing.T) {
	t.Parallel()
	a := NewFakeAdapter()
	if _, err := ReadPackFooter(a, "does/not/exist.pack"); err == nil {
		t.Fatal("ReadPackFooter on missing file should error")
	}
}

func TestReadPackFooterRejectsTooSmall(t *testing.T) {
	t.Parallel()
	a := NewFakeAdapter()
	// Write a file shorter than 4 bytes — should fail the size sanity check.
	if err := a.Write("tiny.pack", bytes.NewReader([]byte{0x00, 0x01})); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPackFooter(a, "tiny.pack"); err == nil {
		t.Fatal("ReadPackFooter on tiny file should error")
	}
}

// --- packer.go: AddRaw too-short rejection (Put error path proxy) ----------

func TestPackerAddRawRejectsTooShort(t *testing.T) {
	t.Parallel()
	a := NewFakeAdapter()
	master := bytes.Repeat([]byte{0x55}, SecretSize)
	p := NewPacker(a, master, "_vault/packs", nil)
	// raw must be >= 17 bytes (flags + min ciphertext + AEAD tag).
	if _, err := p.AddRaw(ID{}, []byte{0x00, 0x01}); err == nil {
		t.Fatal("AddRaw with too-short raw should error")
	}
}

// --- manifest.go: DecodeManifest unmarshal error ---------------------------

func TestDecodeManifestRejectsGarbage(t *testing.T) {
	t.Parallel()
	if _, err := DecodeManifest([]byte("not json at all")); err == nil {
		t.Fatal("DecodeManifest accepted garbage")
	}
}

// --- repo.go: PutManifest / GetManifest happy + Get error path -------------

func TestPutManifestGetManifestRoundTrip(t *testing.T) {
	t.Parallel()
	r, _, cleanup := newTestRepo(t)
	defer cleanup()

	m := Manifest{Version: ManifestVersion, Item: "round", Files: map[string]ManifestEntry{
		"a.txt": {Size: 12, Chunks: []ID{{0x01}, {0x02}}},
	}}
	id, err := r.PutManifest("round", m)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	got, err := r.GetManifest(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Item != "round" || len(got.Files) != 1 {
		t.Fatalf("got = %+v", got)
	}
}

// TestRepoPutAfterFlushHitsHasShortcut: a second Put of the same content
// AFTER Flush hits the `if r.idx.Has(id) { return id, nil }` early-return
// branch in Put (before the lock acquisition), covering line 218.
func TestRepoPutAfterFlushHitsHasShortcut(t *testing.T) {
	t.Parallel()
	r, _, cleanup := newTestRepo(t)
	defer cleanup()

	plain := []byte("flushed once, then re-put")
	id1, err := r.Put(plain)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	id2, err := r.Put(plain)
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Fatal("re-Put after Flush should return identical ID")
	}
	if got := r.Stats().TotalChunks; got != 1 {
		t.Fatalf("TotalChunks = %d, want 1 (re-Put must not duplicate)", got)
	}
}

func TestRepoGetUnknownChunkErrors(t *testing.T) {
	t.Parallel()
	r, _, cleanup := newTestRepo(t)
	defer cleanup()

	// Locate must fail because the chunk has never been written → drives the
	// `idx.Locate err != nil` branch in Repo.Get.
	if _, err := r.Get(ID{0xff, 0xff, 0xff}); err == nil {
		t.Fatal("Get on unknown chunk should error")
	}
}

// TestRepoGetMissingPackBlob: chunk is locatable in the index but the pack
// blob has been wiped from storage → adapter.ReadRange fails, driving the
// `Repo.Get` ReadRange error branch (line 256).
func TestRepoGetMissingPackBlob(t *testing.T) {
	t.Parallel()
	r, _, cleanup := newTestRepo(t)
	defer cleanup()

	id, err := r.Put([]byte("vanish"))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	// Wipe every pack blob — index still references them.
	packs, _ := r.adapter.List("_vault/packs")
	for _, e := range packs {
		if !e.IsDir {
			_ = r.adapter.Delete(e.Path)
		}
	}
	if _, err := r.Get(id); err == nil {
		t.Fatal("Get with missing pack blob should fail")
	}
}

// TestRepoGetTooShortChunk: directly craft a pack blob whose chunk body is
// < 1 byte (no flags byte) and point the index at it. Drives the
// `len(raw) < 1` branch in Repo.Get (line 264).
func TestRepoGetTooShortChunk(t *testing.T) {
	t.Parallel()
	r, _, cleanup := newTestRepo(t)
	defer cleanup()

	// Write a 1-byte file (just the flags byte) and register a chunk that
	// claims length=0 — ReadRange returns an empty body and Get's
	// `len(raw) < 1` branch fires.
	packPath := "_vault/packs/aa/short"
	if err := r.adapter.Write(packPath, bytes.NewReader([]byte{0x00})); err != nil {
		t.Fatal(err)
	}
	chunkID := ID{0xaa, 0xbb, 0xcc}
	if err := r.idx.Register(PackInfo{
		ID: "short-pack", Path: packPath, SizeBytes: 1, ChunkCount: 1,
		Entries: []PackEntry{{ID: chunkID, Offset: 0, Length: 0}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get(chunkID); err == nil {
		t.Fatal("Get on too-short chunk should fail")
	}
}

// --- Failing adapter wrapper to drive callback error paths ----------------

// failingOnPrefix wraps a FakeAdapter and forces Write to fail when path
// starts with failPrefix. All other operations delegate unchanged.
type failingOnPrefix struct {
	*FakeAdapter
	failPrefix string
}

func (f *failingOnPrefix) Write(p string, r io.Reader) error {
	if f.failPrefix != "" && strings.HasPrefix(p, f.failPrefix) {
		return errFromCallback{}
	}
	return f.FakeAdapter.Write(p, r)
}

// failingOnDelete wraps a FakeAdapter so Delete always returns an error
// (Write/Read still delegate normally).
type failingOnDelete struct{ *FakeAdapter }

func (f *failingOnDelete) Delete(_ string) error { return errFromCallback{} }

// TestRepoFlushSurfacesAppendStorageIndexFailure: the packer onFlush callback
// in buildRepo records r.lastFlushErr when idx.AppendStorageIndex fails.
// Repo.Flush surfaces that error on the NEXT explicit Flush. Drives the
// "AppendStorageIndex returns err" branch (repo.go:175-178) and the
// `lastFlushErr` clearing path inside Repo.Flush (line 286-290).
func TestRepoFlushSurfacesAppendStorageIndexFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })

	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name: "fi", Type: "local", Config: "{}", DedupEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Build the repo against a FakeAdapter wrapped to fail on _vault/index writes.
	wrapped := &failingOnPrefix{FakeAdapter: NewFakeAdapter()}
	r, err := InitRepo(d, wrapped, destID, bytes.Repeat([]byte{0xee}, SecretSize))
	if err != nil {
		t.Fatal(err)
	}

	// Now switch the wrapper to fail on index writes BEFORE we trigger a flush.
	wrapped.failPrefix = "_vault/index"

	if _, err := r.Put([]byte("payload that must flush")); err != nil {
		t.Fatal(err)
	}
	// Explicit Flush should now surface the AppendStorageIndex failure.
	if err := r.Flush(); err == nil {
		t.Fatal("Flush should surface AppendStorageIndex failure recorded in lastFlushErr")
	}
}

// TestGCSweepTombstoneFailure: a fully-dead pack triggers the deletion arm
// of RunGC. With index writes failing, AppendTombstone errors and the sweep
// records res.Errors. Covers gc.go:112-115.
func TestGCSweepTombstoneFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name: "st", Type: "local", Config: "{}", DedupEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	wrapped := &failingOnPrefix{FakeAdapter: NewFakeAdapter()}
	r, err := InitRepo(d, wrapped, destID, bytes.Repeat([]byte{0xee}, SecretSize))
	if err != nil {
		t.Fatal(err)
	}
	// Populate one pack of dead chunks (no manifests referencing them).
	for i := 0; i < 3; i++ {
		if _, err := r.Put([]byte{byte(i), 'x'}); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}

	// Now make AppendTombstone fail — sweep should record res.Errors.
	wrapped.failPrefix = "_vault/index"
	res, err := RunGC(r, nil, GCOptions{})
	if err != nil {
		t.Fatalf("RunGC: %v", err)
	}
	if len(res.Errors) == 0 {
		t.Fatal("expected res.Errors when tombstone write fails")
	}
}

// TestGCSweepStorageDeleteFailure: tombstone succeeds but the storage Delete
// for the dead pack fails. Drives gc.go:121-124.
func TestGCSweepStorageDeleteFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name: "sd", Type: "local", Config: "{}", DedupEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	wrapped := &failingOnDelete{FakeAdapter: NewFakeAdapter()}
	r, err := InitRepo(d, wrapped, destID, bytes.Repeat([]byte{0xee}, SecretSize))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := r.Put([]byte{byte(i), 'y'}); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}

	// adapter.Delete fails → sweep records storage-delete error.
	res, err := RunGC(r, nil, GCOptions{})
	if err != nil {
		t.Fatalf("RunGC: %v", err)
	}
	if len(res.Errors) == 0 {
		t.Fatal("expected res.Errors when adapter.Delete fails on sweep")
	}
}

// TestCompactionOldPackDeleteFailure: with a mixed pack + compaction enabled,
// the compactor writes a new pack and tries to Delete the old one. With
// failingOnDelete, that Delete fails → res.Errors records the storage delete
// failure. Drives gc.go:351-355.
func TestCompactionOldPackDeleteFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name: "od", Type: "local", Config: "{}", DedupEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	wrapped := &failingOnDelete{FakeAdapter: NewFakeAdapter()}
	r, err := InitRepo(d, wrapped, destID, bytes.Repeat([]byte{0xee}, SecretSize))
	if err != nil {
		t.Fatal(err)
	}
	liveM, _ := buildOneMixedPack(t, r, 4, 4)
	res, err := RunGC(r, liveM, GCOptions{CompactMinDeadRatio: 0.4})
	if err != nil {
		t.Fatalf("RunGC: %v", err)
	}
	if len(res.Errors) == 0 {
		t.Fatal("expected res.Errors when compactor Delete fails")
	}
}

// TestCompactionRegisterFailureRecordsError: drives compactMixedPacks'
// callback-error path. The packer.Flush() returns nil on the actual error
// (it's recorded in res.Errors), but the new pack's Register/
// AppendStorageIndex/Repoint calls inside the callback are skipped. The end
// result is the live chunks are still in the old pack (the old pack is
// preserved) and res.Errors is set.
func TestCompactionRegisterFailureRecordsError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name: "cf", Type: "local", Config: "{}", DedupEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	wrapped := &failingOnPrefix{FakeAdapter: NewFakeAdapter()}
	r, err := InitRepo(d, wrapped, destID, bytes.Repeat([]byte{0xee}, SecretSize))
	if err != nil {
		t.Fatal(err)
	}

	// Build a mixed pack: 4 live + 4 dead chunks.
	liveM, _ := buildOneMixedPack(t, r, 4, 4)

	// Now make the index writes fail so the compaction callback's
	// AppendStorageIndex returns an error → recorded in res.Errors.
	wrapped.failPrefix = "_vault/index"

	res, err := RunGC(r, liveM, GCOptions{CompactMinDeadRatio: 0.4})
	if err != nil {
		t.Fatalf("RunGC: %v", err)
	}
	if len(res.Errors) == 0 {
		t.Fatal("expected RunGC res.Errors to be populated when index writes fail mid-compaction")
	}
}

// TestRepoFlushPackerFailure: when the packer's adapter.Write fails the
// packer.Flush() inside Repo.Flush() returns an error immediately (before
// lastFlushErr can be considered). Drives the `packer.Flush err != nil`
// branch in Repo.Flush (line 283).
func TestRepoFlushPackerFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name: "fp", Type: "local", Config: "{}", DedupEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	wrapped := &failingOnPrefix{FakeAdapter: NewFakeAdapter()}
	r, err := InitRepo(d, wrapped, destID, bytes.Repeat([]byte{0xee}, SecretSize))
	if err != nil {
		t.Fatal(err)
	}
	// Now flip the wrapper to fail on pack writes only.
	wrapped.failPrefix = "_vault/packs"

	// Put a small chunk so the packer has bytes to flush.
	if _, err := r.Put([]byte("payload")); err != nil {
		t.Fatal(err)
	}
	if err := r.Flush(); err == nil {
		t.Fatal("Flush should fail when adapter.Write on pack path fails")
	}
}

// TestInitRepoBadServerKey: a bad-length serverKey causes SealMaster to fail
// inside InitRepo, covering its error-return branch (line 101-103).
func TestInitRepoBadServerKey(t *testing.T) {
	t.Parallel()
	r, _, cleanup := newTestRepo(t)
	defer cleanup()
	// Wipe repo.json so InitRepo doesn't refuse for "already initialised".
	if err := r.adapter.Delete("_vault/repo.json"); err != nil {
		t.Fatal(err)
	}
	// 16-byte serverKey violates SealMaster's SecretSize check.
	short := bytes.Repeat([]byte{0x11}, 16)
	if _, err := InitRepo(r.db, r.adapter, r.storageID, short); err == nil {
		t.Fatal("InitRepo with short serverKey should fail")
	}
}
