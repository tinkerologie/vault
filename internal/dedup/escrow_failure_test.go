package dedup

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/crypto"
	"github.com/ruaan-deysel/vault/internal/storage"
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
		if err := RewrapMaster(fa, newKey, "pw", now); err == nil || !strings.Contains(err.Error(), "did not verify") {
			t.Fatalf("err = %v", err)
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
