package dedup

import (
	"crypto/rand"
	"errors"
	"io"
	"strings"
	"testing"
)

// gcFaultAdapter wraps FakeAdapter with per-operation fault hooks so the GC
// and compaction error branches can be driven deterministically.
type gcFaultAdapter struct {
	*FakeAdapter
	writeErr   func(path string) error
	deleteErr  error
	badRead    bool // ReadRange bodies fail mid-read after the first call
	readCalls  int
	readFailed int
}

func (a *gcFaultAdapter) Write(path string, r io.Reader) error {
	if a.writeErr != nil {
		if err := a.writeErr(path); err != nil {
			_, _ = io.Copy(io.Discard, r)
			return err
		}
	}
	return a.FakeAdapter.Write(path, r)
}

func (a *gcFaultAdapter) Delete(path string) error {
	if a.deleteErr != nil {
		return a.deleteErr
	}
	return a.FakeAdapter.Delete(path)
}

func (a *gcFaultAdapter) ReadRange(path string, off, n int64) (io.ReadCloser, error) {
	a.readCalls++
	if a.badRead && a.readCalls > 1 { // the first read loads the live manifest
		a.readFailed++
		return io.NopCloser(&failAfterReader{}), nil
	}
	return a.FakeAdapter.ReadRange(path, off, n)
}

type failAfterReader struct{}

func (*failAfterReader) Read([]byte) (int, error) { return 0, errors.New("body broke") }

func installFaultAdapter(r *Repo) *gcFaultAdapter {
	a := &gcFaultAdapter{FakeAdapter: r.adapter.(*FakeAdapter)}
	r.adapter = a
	r.idx.adapter = a
	r.packer.adapter = a
	return a
}

// trigger installs a SQLite trigger that aborts the matching statement,
// injecting a DB failure at an exact step without any production seam.
func trigger(t *testing.T, r *Repo, name, spec string) {
	t.Helper()
	if _, err := r.db.Exec(`CREATE TRIGGER ` + name + ` ` + spec + ` BEGIN SELECT RAISE(ABORT, 'injected ` + name + `'); END`); err != nil {
		t.Fatalf("create trigger %s: %v", name, err)
	}
}

func dropTrigger(t *testing.T, r *Repo, name string) {
	t.Helper()
	if _, err := r.db.Exec(`DROP TRIGGER ` + name); err != nil {
		t.Fatalf("drop trigger %s: %v", name, err)
	}
}

// deadPack writes one unreferenced chunk into its own pack.
func deadPack(t *testing.T, r *Repo) {
	t.Helper()
	b := make([]byte, 2048)
	_, _ = rand.Read(b)
	if _, err := r.Put(b); err != nil {
		t.Fatal(err)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
}

func hasErr(res GCResult, sub string) bool {
	for _, e := range res.Errors {
		if strings.Contains(e, sub) {
			return true
		}
	}
	return false
}

func TestGC458_SweepDBFailures(t *testing.T) {
	cases := []struct {
		name, spec, want string
	}{
		{"mark", "BEFORE UPDATE OF pending_delete ON dedup_packs WHEN NEW.pending_delete = 1", "mark pending delete"},
		{"tomb", "BEFORE UPDATE OF pending_delete ON dedup_packs WHEN NEW.pending_delete = 2", "record tombstone"},
		{"del", "BEFORE DELETE ON dedup_packs", "db delete pack"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, _, cleanup := newTestRepo(t)
			defer cleanup()
			deadPack(t, r)
			trigger(t, r, "f_"+tc.name, tc.spec)
			res, err := RunGC(r, nil, GCOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if !hasErr(res, tc.want) || res.FreedPacks != 0 {
				t.Fatalf("errors=%v freed=%d, want %q", res.Errors, res.FreedPacks, tc.want)
			}
		})
	}
}

func TestGC458_StorageDeleteFailureAndStatsPersistFailure(t *testing.T) {
	r, _, cleanup := newTestRepo(t)
	defer cleanup()
	deadPack(t, r)
	a := installFaultAdapter(r)
	a.deleteErr = errors.New("remote says no")
	trigger(t, r, "f_gcrun", "BEFORE INSERT ON dedup_gc_runs")
	res, err := RunGC(r, nil, GCOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !hasErr(res, "remote says no") {
		t.Fatalf("errors = %v", res.Errors)
	}
}

func TestGC458_ListFailuresAndMissingManifest(t *testing.T) {
	r, _, cleanup := newTestRepo(t)
	defer cleanup()
	deadPack(t, r)

	var bogus ID
	bogus[0] = 0xee
	if _, err := RunGC(r, []ID{bogus}, GCOptions{}); err == nil || !strings.Contains(err.Error(), "load manifest") {
		t.Errorf("unknown live manifest: %v", err)
	}

	if _, err := r.db.Exec(`ALTER TABLE dedup_chunks RENAME TO dedup_chunks_hidden`); err != nil {
		t.Fatal(err)
	}
	res, err := RunGC(r, nil, GCOptions{})
	if err != nil || !hasErr(res, "list chunks for pack") {
		t.Errorf("chunk list failure: %v %v", res.Errors, err)
	}
	if _, err := r.db.Exec(`ALTER TABLE dedup_packs RENAME TO dedup_packs_hidden`); err != nil {
		t.Fatal(err)
	}
	if _, err := RunGC(r, nil, GCOptions{}); err == nil || !strings.Contains(err.Error(), "list packs") {
		t.Errorf("pack list failure: %v", err)
	}
}

func TestGC458_InstallerChunksStayReachable(t *testing.T) {
	r, _, cleanup := newTestRepo(t)
	defer cleanup()
	payload := make([]byte, 1024)
	_, _ = rand.Read(payload)
	cid, err := r.Put(payload)
	if err != nil {
		t.Fatal(err)
	}
	mid, err := r.PutManifest("plg", Manifest{Version: ManifestVersion, Item: "plg", Files: map[string]ManifestEntry{},
		Installer: &ManifestEntry{Size: 1024, Chunks: []ID{cid}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	res, err := RunGC(r, []ID{mid}, GCOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.FreedPacks != 0 || res.Reachable != 2 {
		t.Fatalf("res = %+v", res)
	}
	if _, err := r.Get(cid); err != nil {
		t.Fatalf("installer chunk swept: %v", err)
	}
}

// compactCase builds a mixed pack and returns the repo and live manifests.
func compactCase(t *testing.T) (*Repo, []ID, func()) {
	t.Helper()
	r, _, cleanup := newTestRepo(t)
	liveM, _ := buildOneMixedPack(t, r, 4, 4)
	return r, liveM, cleanup
}

func TestGC458_CompactionFlushCallbackFailures(t *testing.T) {
	t.Run("index append", func(t *testing.T) {
		r, live, cleanup := compactCase(t)
		defer cleanup()
		a := installFaultAdapter(r)
		a.writeErr = func(p string) error {
			if strings.HasPrefix(p, indexRootPath) {
				return errors.New("index write blocked")
			}
			return nil
		}
		res, err := RunGC(r, live, GCOptions{CompactMinDeadRatio: 0.4})
		if err != nil {
			t.Fatal(err)
		}
		if !hasErr(res, "compact index pack") || !hasErr(res, "not all chunks moved") || res.CompactedPacks != 0 {
			t.Fatalf("res = %+v", res)
		}
	})
	t.Run("register", func(t *testing.T) {
		r, live, cleanup := compactCase(t)
		defer cleanup()
		trigger(t, r, "f_reg", "BEFORE INSERT ON dedup_packs")
		res, err := RunGC(r, live, GCOptions{CompactMinDeadRatio: 0.4})
		if err != nil {
			t.Fatal(err)
		}
		if !hasErr(res, "compact register pack") || res.CompactedPacks != 0 {
			t.Fatalf("res = %+v", res)
		}
	})
	t.Run("repoint", func(t *testing.T) {
		r, live, cleanup := compactCase(t)
		defer cleanup()
		trigger(t, r, "f_repoint", "BEFORE UPDATE OF pack_id ON dedup_chunks")
		res, err := RunGC(r, live, GCOptions{CompactMinDeadRatio: 0.4})
		if err != nil {
			t.Fatal(err)
		}
		if !hasErr(res, "compact repoint") || res.CompactedPacks != 0 {
			t.Fatalf("res = %+v", res)
		}
	})
	t.Run("final flush", func(t *testing.T) {
		r, live, cleanup := compactCase(t)
		defer cleanup()
		a := installFaultAdapter(r)
		a.writeErr = func(p string) error {
			if strings.HasPrefix(p, packsRoot) {
				return errors.New("pack write failed")
			}
			return nil
		}
		res, err := RunGC(r, live, GCOptions{CompactMinDeadRatio: 0.4})
		if err != nil {
			t.Fatal(err)
		}
		if !hasErr(res, "compact final flush") {
			t.Fatalf("res = %+v", res)
		}
	})
	t.Run("read body", func(t *testing.T) {
		r, live, cleanup := compactCase(t)
		defer cleanup()
		a := installFaultAdapter(r)
		a.badRead = true
		res, err := RunGC(r, live, GCOptions{CompactMinDeadRatio: 0.4})
		if err != nil {
			t.Fatal(err)
		}
		if !hasErr(res, "compact read body") || a.readFailed == 0 {
			t.Fatalf("res = %+v", res)
		}
	})
}

func TestGC458_CompactionOldPackRetirementFailures(t *testing.T) {
	cases := []struct {
		name, spec, want string
	}{
		{"mark", "BEFORE UPDATE OF pending_delete ON dedup_packs WHEN NEW.pending_delete = 1", "compact mark pending delete"},
		{"tomb", "BEFORE UPDATE OF pending_delete ON dedup_packs WHEN NEW.pending_delete = 2", "compact record tombstone"},
		{"del", "BEFORE DELETE ON dedup_packs", "compact db delete"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, live, cleanup := compactCase(t)
			defer cleanup()
			trigger(t, r, "f_"+tc.name, tc.spec)
			res, err := RunGC(r, live, GCOptions{CompactMinDeadRatio: 0.4})
			if err != nil {
				t.Fatal(err)
			}
			if !hasErr(res, tc.want) || res.CompactedPacks != 0 {
				t.Fatalf("errors=%v compacted=%d, want %q", res.Errors, res.CompactedPacks, tc.want)
			}
			// Once the fault clears, the next sweep finishes the job and the
			// already-tombstoned state is not re-tombstoned.
			dropTrigger(t, r, "f_"+tc.name)
			if _, err := RunGC(r, live, GCOptions{CompactMinDeadRatio: 0.4}); err != nil {
				t.Fatal(err)
			}
		})
	}

	t.Run("tombstone write", func(t *testing.T) {
		r, live, cleanup := compactCase(t)
		defer cleanup()
		a := installFaultAdapter(r)
		writes := 0
		a.writeErr = func(p string) error {
			// Allow the new pack and its index add-line; block the tombstone.
			if strings.HasPrefix(p, indexRootPath) {
				writes++
				if writes > 1 {
					return errors.New("tombstone blocked")
				}
			}
			return nil
		}
		res, err := RunGC(r, live, GCOptions{CompactMinDeadRatio: 0.4})
		if err != nil {
			t.Fatal(err)
		}
		if !hasErr(res, "compact tombstone") {
			t.Fatalf("res = %+v", res)
		}
	})

	t.Run("storage delete", func(t *testing.T) {
		r, live, cleanup := compactCase(t)
		defer cleanup()
		a := installFaultAdapter(r)
		a.deleteErr = errors.New("remote delete refused")
		res, err := RunGC(r, live, GCOptions{CompactMinDeadRatio: 0.4})
		if err != nil {
			t.Fatal(err)
		}
		if !hasErr(res, "compact storage delete") {
			t.Fatalf("res = %+v", res)
		}
	})
}

func TestGC458_CompactSkipsZeroSizedCandidates(t *testing.T) {
	r, _, cleanup := newTestRepo(t)
	defer cleanup()
	var res GCResult
	if err := r.compactMixedPacks([]mixedCandidate{{dead: 10}}, 0.1, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) != 0 || res.CompactedPacks != 0 {
		t.Fatalf("res = %+v", res)
	}
}
