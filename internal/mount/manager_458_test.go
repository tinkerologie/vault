package mount

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/dedup"
	"github.com/tinkerologie/vault/internal/storage"
	"github.com/tinkerologie/vault/internal/ws"
)

// fakeHandle is a MountHandle that records Unmount calls.
type fakeHandle struct {
	path     string
	err      error
	unmounts int
}

func (h *fakeHandle) Unmount() error    { h.unmounts++; return h.err }
func (h *fakeHandle) MountPath() string { return h.path }

// stubMountManifests replaces the platform FUSE mount. Not parallel-safe.
func stubMountManifests(t *testing.T, fn func(ctx context.Context, repo *dedup.Repo, manifests map[string]dedup.Manifest, mountPath string, onActivity func()) (MountHandle, error)) {
	t.Helper()
	old := mountManifests
	mountManifests = fn
	t.Cleanup(func() { mountManifests = old })
}

type mount458Fixture struct {
	d         *db.DB
	dir       string
	destID    int64
	jobID     int64
	runID     int64
	serverKey []byte
	repo      *dedup.Repo
}

func newMount458Fixture(t *testing.T) *mount458Fixture {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	repoDir := filepath.Join(dir, "repo")
	cfg := `{"path":"` + repoDir + `"}`
	destID, err := d.CreateStorageDestination(db.StorageDestination{Name: "dd", Type: "local", Config: cfg, DedupEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := d.CreateJob(db.Job{Name: "job458", StorageDestID: destID, BackupTypeChain: "full"})
	if err != nil {
		t.Fatal(err)
	}
	runID, err := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "completed", BackupType: "full"})
	if err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{0x5a}, dedup.SecretSize)
	adapter, err := storage.NewAdapter("local", cfg)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := dedup.InitRepo(d, adapter, destID, key)
	if err != nil {
		t.Fatal(err)
	}
	return &mount458Fixture{d: d, dir: dir, destID: destID, jobID: jobID, runID: runID, serverKey: key, repo: repo}
}

func (f *mount458Fixture) putManifest(t *testing.T, item string) dedup.ID {
	t.Helper()
	id, err := f.repo.PutManifest(item, dedup.Manifest{Version: 1, Item: item, Files: map[string]dedup.ManifestEntry{
		"dir": {IsDir: true, Mode: 0o755, ModTime: time.Now().UTC().Format(time.RFC3339)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.repo.Flush(); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *mount458Fixture) restorePoint(t *testing.T, rp db.RestorePoint) int64 {
	t.Helper()
	rp.JobRunID, rp.JobID, rp.BackupType = f.runID, f.jobID, "full"
	if rp.StoragePath == "" {
		rp.StoragePath = "backups/x"
	}
	id, err := f.d.CreateRestorePoint(rp)
	if err != nil {
		t.Fatal(err)
	}
	if len(rp.ManifestID) > 0 {
		if err := f.d.SetRestorePointManifestID(id, rp.ManifestID); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func TestManager458_MountLifecycle(t *testing.T) {
	_, _, _ = redirectRoots(t)
	f := newMount458Fixture(t)
	mid := f.putManifest(t, "")
	rpID := f.restorePoint(t, db.RestorePoint{ManifestID: mid[:]})

	var handle *fakeHandle
	var gotManifests map[string]dedup.Manifest
	stubMountManifests(t, func(_ context.Context, _ *dedup.Repo, m map[string]dedup.Manifest, p string, onActivity func()) (MountHandle, error) {
		gotManifests = m
		onActivity() // first activity persists
		onActivity() // second within 15s is throttled
		handle = &fakeHandle{path: p}
		return handle, nil
	})
	var unmounted []string
	stubUnmount(t, func(p string) error { unmounted = append(unmounted, p); return nil })

	hub := ws.NewHub()
	mgr := NewManager(f.d, hub, f.serverKey)
	mgr.SetBaseMountDir(filepath.Join(f.dir, "mnt"))

	sess, err := mgr.MountRestorePoint(context.Background(), f.jobID, rpID)
	if err != nil {
		t.Fatalf("MountRestorePoint: %v", err)
	}
	if _, ok := gotManifests["job458"]; !ok {
		t.Errorf("manifest keyed by job name expected, got %v", gotManifests)
	}
	if sess.MountPath == "" || sess.Status != "active" {
		t.Fatalf("session = %+v", sess)
	}
	if info, err := os.Stat(sess.MountPath); err != nil || !info.IsDir() {
		t.Fatalf("mount dir not created: %v", err)
	}
	if got, err := mgr.Get(sess.ID); err != nil || got.ID != sess.ID {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	active, err := mgr.ListActive()
	if err != nil || len(active) != 1 {
		t.Fatalf("ListActive = %v, %v", active, err)
	}

	// A failing in-process unmount keeps the session registered.
	handle.err = errors.New("busy")
	if err := mgr.Unmount(context.Background(), sess.ID); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("Unmount busy: %v", err)
	}
	handle.err = nil
	if err := mgr.Unmount(context.Background(), sess.ID); err != nil {
		t.Fatalf("Unmount: %v", err)
	}
	if handle.unmounts != 2 || len(unmounted) != 0 {
		t.Errorf("handle unmounts=%d platform unmounts=%v", handle.unmounts, unmounted)
	}
	if _, err := os.Stat(sess.MountPath); !os.IsNotExist(err) {
		t.Errorf("mount dir should be removed: %v", err)
	}
	got, _ := mgr.Get(sess.ID)
	if got.Status != "stopped" {
		t.Errorf("status = %q, want stopped", got.Status)
	}

	// Orphaned session (row, no handle): platform unmount is used.
	if err := mgr.Unmount(context.Background(), sess.ID); err != nil {
		t.Fatalf("Unmount orphan: %v", err)
	}
	if len(unmounted) != 1 || unmounted[0] != sess.MountPath {
		t.Errorf("platform unmounts = %v", unmounted)
	}
	stubUnmount(t, func(string) error { return errors.New("EPERM") })
	if err := mgr.Unmount(context.Background(), sess.ID); err == nil || !strings.Contains(err.Error(), "EPERM") {
		t.Errorf("Unmount orphan EPERM: %v", err)
	}
}

func TestManager458_ItemManifestsAndStop(t *testing.T) {
	_, _, _ = redirectRoots(t)
	f := newMount458Fixture(t)
	a := f.putManifest(t, "alpha")
	b := f.putManifest(t, "beta")
	meta := `{"item_manifests":{"alpha":"` + hex.EncodeToString(a[:]) + `","beta":"` + hex.EncodeToString(b[:]) + `","junk":"zz","short":"abcd"}}`
	rpID := f.restorePoint(t, db.RestorePoint{Metadata: meta})

	handles := []*fakeHandle{}
	stubMountManifests(t, func(_ context.Context, _ *dedup.Repo, m map[string]dedup.Manifest, p string, _ func()) (MountHandle, error) {
		if len(m) != 2 {
			t.Errorf("manifests = %d, want 2 (junk entries skipped)", len(m))
		}
		h := &fakeHandle{path: p}
		handles = append(handles, h)
		return h, nil
	})
	stubUnmount(t, func(string) error { return nil })

	mgr := NewManager(f.d, nil, f.serverKey)
	target := filepath.Join(f.dir, "explicit")
	sess, err := mgr.MountRestorePointTo(context.Background(), f.jobID, rpID, target)
	if err != nil {
		t.Fatalf("MountRestorePointTo: %v", err)
	}
	if sess.MountPath != target {
		t.Errorf("MountPath = %q, want %q", sess.MountPath, target)
	}
	mgr.Stop(context.Background())
	mgr.Stop(context.Background()) // idempotent
	if len(handles) != 1 || handles[0].unmounts != 1 {
		t.Errorf("Stop should unmount the active session once")
	}
}

func TestManager458_MountFailures(t *testing.T) {
	_, _, _ = redirectRoots(t)
	f := newMount458Fixture(t)
	mid := f.putManifest(t, "solo")
	rpID := f.restorePoint(t, db.RestorePoint{ManifestID: mid[:]})
	stubUnmount(t, func(string) error { return nil })

	mgr := NewManager(f.d, ws.NewHub(), f.serverKey)
	mgr.SetBaseMountDir(filepath.Join(f.dir, "mnt"))

	// FUSE start failure marks the session failed.
	stubMountManifests(t, func(context.Context, *dedup.Repo, map[string]dedup.Manifest, string, func()) (MountHandle, error) {
		return nil, errors.New("no fuse")
	})
	if _, err := mgr.MountRestorePoint(context.Background(), f.jobID, rpID); err == nil || !strings.Contains(err.Error(), "start FUSE mount") {
		t.Fatalf("FUSE failure: %v", err)
	}

	// Mount directory cannot be created: a file blocks the target.
	blocker := filepath.Join(f.dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.MountRestorePointTo(context.Background(), f.jobID, rpID, filepath.Join(blocker, "sub")); err == nil || !strings.Contains(err.Error(), "create target") {
		t.Fatalf("target dir failure: %v", err)
	}

	// Unknown manifest IDs.
	var missing dedup.ID
	missing[0] = 0xff
	rpMissing := f.restorePoint(t, db.RestorePoint{ManifestID: missing[:]})
	if _, err := mgr.MountRestorePoint(context.Background(), f.jobID, rpMissing); err == nil || !strings.Contains(err.Error(), "get manifest") {
		t.Errorf("missing manifest: %v", err)
	}
	rpMissingItem := f.restorePoint(t, db.RestorePoint{Metadata: `{"item_manifests":{"x":"` + hex.EncodeToString(missing[:]) + `"}}`})
	if _, err := mgr.MountRestorePoint(context.Background(), f.jobID, rpMissingItem); err == nil || !strings.Contains(err.Error(), "for item x") {
		t.Errorf("missing item manifest: %v", err)
	}

	// Lookup failures.
	if _, err := mgr.MountRestorePoint(context.Background(), 99999, rpID); err == nil || !strings.Contains(err.Error(), "get job") {
		t.Errorf("missing job: %v", err)
	}
	if _, err := mgr.MountRestorePoint(context.Background(), f.jobID, 99999); err == nil || !strings.Contains(err.Error(), "get restore point") {
		t.Errorf("missing restore point: %v", err)
	}
}

func TestManager458_AdapterAndRepoFailures(t *testing.T) {
	_, _, _ = redirectRoots(t)
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	mk := func(typ, cfg string) (int64, int64) {
		destID, err := d.CreateStorageDestination(db.StorageDestination{Name: typ + cfg, Type: typ, Config: cfg, DedupEnabled: true})
		if err != nil {
			t.Fatal(err)
		}
		jobID, _ := d.CreateJob(db.Job{Name: "j" + typ, StorageDestID: destID, BackupTypeChain: "full"})
		runID, _ := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "completed", BackupType: "full"})
		rpID, _ := d.CreateRestorePoint(db.RestorePoint{JobRunID: runID, JobID: jobID, BackupType: "full", StoragePath: "x"})
		return jobID, rpID
	}
	mgr := NewManager(d, nil, bytes.Repeat([]byte{1}, dedup.SecretSize))

	jobID, rpID := mk("bogus-type", `{}`)
	if _, err := mgr.MountRestorePoint(context.Background(), jobID, rpID); err == nil || !strings.Contains(err.Error(), "init storage adapter") {
		t.Errorf("bad adapter type: %v", err)
	}
	jobID, rpID = mk("local", `{"path":"`+filepath.Join(dir, "norepo")+`"}`)
	if _, err := mgr.MountRestorePoint(context.Background(), jobID, rpID); err == nil || !strings.Contains(err.Error(), "open dedup repo") {
		t.Errorf("uninitialised repo: %v", err)
	}
}

func TestManager458_NilDB(t *testing.T) {
	_, _, _ = redirectRoots(t)
	mgr := NewManager(nil, nil, nil)
	if _, err := mgr.MountRestorePointTo(context.Background(), 1, 1, ""); err == nil {
		t.Error("MountRestorePointTo with nil db: want error")
	}
	if s, err := mgr.ListActive(); s != nil || err != nil {
		t.Errorf("ListActive nil db = %v, %v", s, err)
	}
	if _, err := mgr.Get(1); err == nil {
		t.Error("Get with nil db: want error")
	}
	if err := mgr.Unmount(context.Background(), 42); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("Unmount unknown: %v", err)
	}
	if got := mgr.getIdleTimeout(); got != DefaultIdleTimeout {
		t.Errorf("idle timeout nil db = %v", got)
	}
	mgr.CleanupStale(context.Background()) // no-op without a db
	mgr.sweepIdle(context.Background(), time.Now())
}

func TestManager458_IdleTimeoutInvalidValues(t *testing.T) {
	_, _, _ = redirectRoots(t)
	d, err := db.Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	mgr := NewManager(d, nil, nil)
	for _, v := range []string{"abc", "-5"} {
		_ = d.SetSetting("fuse_mount_idle_minutes", v)
		if got := mgr.getIdleTimeout(); got != DefaultIdleTimeout {
			t.Errorf("%q: timeout = %v, want default", v, got)
		}
	}
}

func TestManager458_SweepIdleUnmountsInactive(t *testing.T) {
	_, _, _ = redirectRoots(t)
	f := newMount458Fixture(t)
	mid := f.putManifest(t, "")
	rpID := f.restorePoint(t, db.RestorePoint{ManifestID: mid[:]})
	var h *fakeHandle
	stubMountManifests(t, func(_ context.Context, _ *dedup.Repo, _ map[string]dedup.Manifest, p string, _ func()) (MountHandle, error) {
		h = &fakeHandle{path: p}
		return h, nil
	})
	stubUnmount(t, func(string) error { return nil })
	mgr := NewManager(f.d, nil, f.serverKey)
	mgr.SetBaseMountDir(filepath.Join(f.dir, "mnt"))
	if _, err := mgr.MountRestorePoint(context.Background(), f.jobID, rpID); err != nil {
		t.Fatal(err)
	}

	// Disabled timeout: nothing happens.
	_ = f.d.SetSetting("fuse_mount_idle_minutes", "0")
	mgr.sweepIdle(context.Background(), time.Now().Add(24*time.Hour))
	if h.unmounts != 0 {
		t.Fatal("disabled sweep unmounted a session")
	}
	// Not yet idle.
	_ = f.d.SetSetting("fuse_mount_idle_minutes", "5")
	mgr.sweepIdle(context.Background(), time.Now())
	if h.unmounts != 0 {
		t.Fatal("fresh session unmounted")
	}
	// Idle well past the timeout.
	mgr.sweepIdle(context.Background(), time.Now().Add(time.Hour))
	if h.unmounts != 1 {
		t.Fatalf("idle session unmounts = %d, want 1", h.unmounts)
	}
}

func TestManager458_StartAndCancel(t *testing.T) {
	_, _, _ = redirectRoots(t)
	d, err := db.Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	stubUnmount(t, func(string) error { return nil })
	mgr := NewManager(d, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	mgr.Start(ctx)
	cancel()
	mgr.Stop(context.Background())
}

func TestManager458_BroadcastUnmarshalableIsDropped(t *testing.T) {
	hub := ws.NewHub()
	mgr := &Manager{hub: hub}
	mgr.broadcast("x", make(chan int)) // json.Marshal fails; must not panic or send
	mgr.broadcast("y", map[string]any{"ok": true})
}

// The sweeper goroutine itself runs sweepIdle on every tick.
func TestManager458_IdleSweeperTicks(t *testing.T) {
	_, _, _ = redirectRoots(t)
	f := newMount458Fixture(t)
	mid := f.putManifest(t, "")
	rpID := f.restorePoint(t, db.RestorePoint{ManifestID: mid[:]})
	stubMountManifests(t, func(_ context.Context, _ *dedup.Repo, _ map[string]dedup.Manifest, p string, _ func()) (MountHandle, error) {
		return &fakeHandle{path: p}, nil
	})
	stubUnmount(t, func(string) error { return nil })
	prev := idleSweepInterval
	idleSweepInterval = 5 * time.Millisecond
	t.Cleanup(func() { idleSweepInterval = prev })

	mgr := NewManager(f.d, nil, f.serverKey)
	mgr.SetBaseMountDir(filepath.Join(f.dir, "mnt"))
	sess, err := mgr.MountRestorePoint(context.Background(), f.jobID, rpID)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.d.SetSetting("fuse_mount_idle_minutes", "5")
	if _, err := f.d.Exec(`UPDATE mount_sessions SET last_activity_at = ? WHERE id = ?`, time.Now().UTC().Add(-time.Hour), sess.ID); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { mgr.runIdleSweeper(ctx); close(done) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		active, err := mgr.ListActive()
		if err != nil {
			t.Fatal(err)
		}
		if len(active) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("idle session still active after the sweeper ran")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
}

// A database error while listing sessions skips the sweep.
func TestManager458_SweepIdleListError(t *testing.T) {
	_, _, _ = redirectRoots(t)
	d, err := db.Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(d, nil, nil)
	_ = d.Close()
	stubUnmount(t, func(string) error {
		t.Error("unmount called despite the list error")
		return nil
	})
	mgr.sweepIdle(context.Background(), time.Now().Add(time.Hour))
}
