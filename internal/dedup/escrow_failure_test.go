package dedup

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tinkerologie/vault/internal/crypto"
	"github.com/tinkerologie/vault/internal/storage"
)

// faultyAdapter injects failures into a FakeAdapter by path.
type faultyAdapter struct {
	*FakeAdapter
	failWrite map[string]bool // path prefix -> fail Write
	failRead  map[string]bool // exact path -> fail Read (not "not found")
	// corruptAfterWrite makes a repo.json write store garbage, so the
	// read-back verification fails.
	corruptAfterWrite bool
}

var errInjected = errors.New("injected storage failure")

func (f *faultyAdapter) Write(p string, r io.Reader) error {
	for prefix := range f.failWrite {
		if strings.HasPrefix(p, prefix) {
			return errInjected
		}
	}
	if f.corruptAfterWrite && p == repoConfigPath {
		_, _ = io.Copy(io.Discard, r)
		return f.FakeAdapter.Write(p, strings.NewReader(`{"version":1,"uuid":"x","sealed_master":"AAAA"}`))
	}
	return f.FakeAdapter.Write(p, r)
}

func (f *faultyAdapter) Read(p string) (io.ReadCloser, error) {
	if f.failRead[p] {
		return nil, errInjected
	}
	return f.FakeAdapter.Read(p)
}

var _ storage.Adapter = (*faultyAdapter)(nil)

// newFaultyRepo is newTestRepo on a faultyAdapter, with an escrow under "pw".
func newFaultyRepo(t *testing.T) (*Repo, *faultyAdapter, func()) {
	t.Helper()
	r, _, cleanup := newTestRepo(t)
	fa := &faultyAdapter{FakeAdapter: r.adapter.(*FakeAdapter), failWrite: map[string]bool{}, failRead: map[string]bool{}}
	r.adapter = fa
	if _, err := r.EnsurePassphraseEscrow("pw"); err != nil {
		t.Fatal(err)
	}
	return r, fa, cleanup
}

func TestEscrowStorageFailures(t *testing.T) {
	r, fa, cleanup := newFaultyRepo(t)
	defer cleanup()

	fa.failRead[escrowPath] = true
	if _, err := OpenRepoFromEscrow(r.db, fa, r.storageID, "pw"); !errors.Is(err, errInjected) {
		t.Fatalf("read failure: %v", err)
	}
	fa.failRead = map[string]bool{}

	fa.failWrite[escrowPath] = true
	if _, err := r.EnsurePassphraseEscrow("new"); !errors.Is(err, errInjected) {
		t.Fatalf("write failure: %v", err)
	}
	fa.failWrite = map[string]bool{}

	// Not an age file at all.
	_ = fa.FakeAdapter.Write(escrowPath, strings.NewReader("garbage"))
	if _, err := OpenRepoFromEscrow(r.db, fa, r.storageID, "pw"); err == nil || errors.Is(err, ErrPassphraseMismatch) {
		t.Fatalf("garbage escrow: %v, want an open error", err)
	}
	// Valid age, but not an escrow payload.
	enc, _ := crypto.EncryptReader("pw", strings.NewReader("not json"))
	sealed, _ := io.ReadAll(enc)
	_ = enc.Close()
	_ = fa.FakeAdapter.Write(escrowPath, bytes.NewReader(sealed))
	if _, err := OpenRepoFromEscrow(r.db, fa, r.storageID, "pw"); err == nil || !strings.Contains(err.Error(), "decode escrow") {
		t.Fatalf("non-payload escrow: %v", err)
	}
	// An unreadable header is reported, not mistaken for a missing escrow.
	fa.failRead[repoConfigPath] = true
	if _, err := OpenRepoFromEscrow(r.db, fa, r.storageID, "pw"); !errors.Is(err, errInjected) {
		t.Fatalf("header read failure: %v", err)
	}
}

// TestRewrapStorageFailures checks every failure point of a rewrap leaves a
// usable original: the backup is written first and a bad result is caught.
func TestRewrapStorageFailures(t *testing.T) {
	newKey := bytes.Repeat([]byte{0x22}, SecretSize)
	oldKey := bytes.Repeat([]byte{0xee}, SecretSize)
	now := time.Now()

	t.Run("backup write fails", func(t *testing.T) {
		r, fa, cleanup := newFaultyRepo(t)
		defer cleanup()
		fa.failWrite[repoConfigPath+"."] = true
		if err := RewrapMaster(fa, newKey, "pw", now); err == nil || !strings.Contains(err.Error(), "back up repo.json") {
			t.Fatalf("err = %v", err)
		}
		if _, err := OpenRepo(r.db, fa, r.storageID, oldKey); err != nil {
			t.Fatalf("original no longer opens: %v", err)
		}
	})
	t.Run("header write fails", func(t *testing.T) {
		r, fa, cleanup := newFaultyRepo(t)
		defer cleanup()
		// Fail only the header itself; its backup is written first.
		f2 := &exactWriteFail{faultyAdapter: fa, path: repoConfigPath}
		if err := RewrapMaster(f2, newKey, "pw", now); err == nil || !strings.Contains(err.Error(), "original kept at") {
			t.Fatalf("err = %v", err)
		}
		if _, err := OpenRepo(r.db, fa, r.storageID, oldKey); err != nil {
			t.Fatalf("original no longer opens: %v", err)
		}
	})
	t.Run("result does not verify", func(t *testing.T) {
		_, fa, cleanup := newFaultyRepo(t)
		defer cleanup()
		fa.corruptAfterWrite = true
		err := RewrapMaster(fa, newKey, "pw", now)
		if err == nil || !strings.Contains(err.Error(), "did not verify") {
			t.Fatalf("err = %v", err)
		}
		// A failed rewrap keeps the rollback copy, which opens with the old key.
		backups, err := HeaderBackups(fa)
		if err != nil || len(backups) != 1 {
			t.Fatalf("rollback copies = %v, %v", backups, err)
		}
		rc, err := fa.Read(backups[0])
		if err != nil {
			t.Fatal(err)
		}
		defer rc.Close()
		var cfg repoConfig
		if err := json.NewDecoder(rc).Decode(&cfg); err != nil {
			t.Fatal(err)
		}
		if _, err := UnsealMaster(oldKey, cfg.SealedMaster); err != nil {
			t.Fatalf("rollback copy is not the original: %v", err)
		}
	})
	t.Run("no header backups when the repository root is missing", func(t *testing.T) {
		got, err := HeaderBackups(NewFakeAdapter())
		if err != nil || len(got) != 0 {
			t.Fatalf("HeaderBackups on empty storage = %v, %v", got, err)
		}
	})
}

// exactWriteFail fails writes to one exact path.
type exactWriteFail struct {
	*faultyAdapter
	path string
}

func (e *exactWriteFail) Write(p string, r io.Reader) error {
	if p == e.path {
		return errInjected
	}
	return e.faultyAdapter.Write(p, r)
}

// blockingHeaderWrite parks the first repo.json write, so a test can look at
// what else runs during a rewrap.
type blockingHeaderWrite struct {
	storage.Adapter
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (b *blockingHeaderWrite) Write(p string, r io.Reader) error {
	if p == repoConfigPath {
		b.once.Do(func() {
			close(b.entered)
			<-b.release
		})
	}
	return b.Adapter.Write(p, r)
}

// A sweep must not delete the rollback copy of a rewrap still in progress.
func TestRewrapKeepsRollbackCopyAgainstConcurrentSweep(t *testing.T) {
	r, fa, cleanup := newFaultyRepo(t)
	defer cleanup()
	blocker := &blockingHeaderWrite{Adapter: fa, entered: make(chan struct{}), release: make(chan struct{})}
	first := make(chan error, 1)
	go func() {
		first <- RewrapMaster(blocker, bytes.Repeat([]byte{0x22}, SecretSize), "pw", time.Now())
	}()
	<-blocker.entered

	sweep := make(chan error, 1)
	r.adapter = fa
	go func() { sweep <- r.RemoveRetiredHeaders() }()
	select {
	case err := <-sweep:
		t.Fatalf("sweep ran during a rewrap: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	// A stalled rewrap holds up only its own repository.
	other, _, cleanupOther := newTestRepo(t)
	defer cleanupOther()
	otherDone := make(chan error, 1)
	go func() { otherDone <- other.RemoveRetiredHeaders() }()
	select {
	case err := <-otherDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("another repository waited for this rewrap")
	}
	if backups, err := HeaderBackups(fa); err != nil || len(backups) != 1 {
		t.Fatalf("rollback copies during rewrap = %v, %v", backups, err)
	}
	close(blocker.release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-sweep; err != nil {
		t.Fatal(err)
	}
}

// replacedRepoAdapter changes repo.json's UUID after the first read, as if the
// repository were removed and recreated while a rewrap waited for its lock.
type replacedRepoAdapter struct {
	storage.Adapter
	reads int
}

func (a *replacedRepoAdapter) Read(p string) (io.ReadCloser, error) {
	if p != repoConfigPath {
		return a.Adapter.Read(p)
	}
	a.reads++
	rc, err := a.Adapter.Read(p)
	if err != nil || a.reads == 1 {
		return rc, err
	}
	defer rc.Close()
	var cfg repoConfig
	if err := json.NewDecoder(rc).Decode(&cfg); err != nil {
		return nil, err
	}
	cfg.UUID = "replaced"
	body, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(body)), nil
}

func TestRewrapRefusesReplacedRepository(t *testing.T) {
	_, fa, cleanup := newFaultyRepo(t)
	defer cleanup()
	err := RewrapMaster(&replacedRepoAdapter{Adapter: fa}, bytes.Repeat([]byte{0x22}, SecretSize), "pw", time.Now())
	if err == nil || !strings.Contains(err.Error(), "replaced") {
		t.Fatalf("err = %v", err)
	}
	if backups, err := HeaderBackups(fa); err != nil || len(backups) != 0 {
		t.Fatalf("wrote despite the replaced repository: %v, %v", backups, err)
	}
}
