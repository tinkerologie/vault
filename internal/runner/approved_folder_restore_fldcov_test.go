package runner

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/dedup"
	"github.com/tinkerologie/vault/internal/engine"
	"github.com/tinkerologie/vault/internal/storage"
)

// fldcovFixture is a local backup destination, Job, and Runner for driving
// FolderRestoreWork without the Restore Request service.
type fldcovFixture struct {
	r     *Runner
	db    *db.DB
	root  string
	dest  db.StorageDestination
	job   db.Job
	key   []byte
	runID int64
}

func newFldcovFixture(t *testing.T) *fldcovFixture {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	key := bytes.Repeat([]byte{7}, 32)
	root := t.TempDir()
	dest := createNamedLocalDest(t, database, "Folder coverage", root)
	jobID, err := database.CreateJob(db.Job{Name: "Folder coverage Job", StorageDestID: dest.ID})
	if err != nil {
		t.Fatal(err)
	}
	job, err := database.GetJob(jobID)
	if err != nil {
		t.Fatal(err)
	}
	return &fldcovFixture{r: New(database, nil, key), db: database, root: root, dest: dest, job: job, key: key}
}

func (f *fldcovFixture) point(t *testing.T, kind, path string, parent int64, metadata string) db.RestorePoint {
	t.Helper()
	run, err := f.db.CreateJobRun(db.JobRun{JobID: f.job.ID, Status: "completed", BackupType: kind})
	if err != nil {
		t.Fatal(err)
	}
	id, err := f.db.CreateRestorePoint(db.RestorePoint{
		JobID: f.job.ID, JobRunID: run, BackupType: kind, ParentRestorePointID: parent,
		StoragePath: path, Metadata: metadata,
	})
	if err != nil {
		t.Fatal(err)
	}
	point, err := f.db.GetRestorePoint(id)
	if err != nil {
		t.Fatal(err)
	}
	return point
}

type fldcovEntry struct {
	name string
	body string
}

func fldcovTar(t *testing.T, entries []fldcovEntry) []byte {
	t.Helper()
	var out bytes.Buffer
	writer := tar.NewWriter(&out)
	for _, entry := range entries {
		header := &tar.Header{Name: entry.name, Mode: 0o600, Size: int64(len(entry.body)), Uid: os.Getuid(), Gid: os.Getgid()}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(entry.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// writeStep stores an archive and optional sidecars for the item "archive"
// and returns the stored objects as the review recorded them.
func (f *fldcovFixture) writeStep(t *testing.T, storagePath string, archive []byte, sidecars map[string]engine.TarIndex) []storage.FileInfo {
	t.Helper()
	dir := filepath.Join(f.root, storagePath, "archive")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data.tar"), archive, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, index := range sidecars {
		data, err := json.Marshal(index)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	adapter, err := storage.NewAdapter(f.dest.Type, f.dest.Config)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.CloseAdapter(adapter)
	objects, err := adapter.List(filepath.Join(storagePath, "archive"))
	if err != nil || len(objects) == 0 {
		t.Fatalf("list staged objects: %v %v", objects, err)
	}
	return objects
}

func fldcovTarget(t *testing.T) engine.ReviewedFolderTarget {
	t.Helper()
	configured := filepath.Join(t.TempDir(), "restored")
	resolved, err := engine.ResolveFolderRestoreTarget(configured, false)
	if err != nil {
		t.Fatal(err)
	}
	return engine.ReviewedFolderTarget{Configured: configured, Resolved: resolved}
}

func (f *fldcovFixture) approved(point db.RestorePoint, steps []ApprovedRestoreStep, target engine.ReviewedFolderTarget) *ApprovedRestore {
	return &ApprovedRestore{
		RequestID: "fldcov",
		Plan: ApprovedRestorePlan{
			Job: f.job, Destination: f.dest, Point: point, Steps: steps,
			Item: ItemSelection{ItemName: "archive", ItemType: "folder"},
			Work: FolderRestoreWork{Target: target},
		},
		Revalidate: func(context.Context) error { return nil },
	}
}

func (f *fldcovFixture) restore(ctx context.Context, approved *ApprovedRestore) error {
	work := approved.Plan.Work.(FolderRestoreWork)
	reporter := restoreProgressReporter{JobID: f.job.ID, RunID: f.runID, ItemName: "archive", ItemType: "folder", ItemsTotal: 1}
	return work.restore(ctx, f.r, approved, "", reporter)
}

func requireFldcovUntouched(t *testing.T, target engine.ReviewedFolderTarget) {
	t.Helper()
	if entries, err := os.ReadDir(target.Resolved); err == nil && len(entries) != 0 {
		t.Fatalf("refused restore wrote to the reviewed target: %v", entries)
	}
}

func TestApprovedFolderRestoreRefusesBeforeWritingTarget(t *testing.T) {
	f := newFldcovFixture(t)
	good := fldcovTar(t, []fldcovEntry{{"restored.txt", "content"}})

	t.Run("staging cannot sit outside the target", func(t *testing.T) {
		point := f.point(t, "full", "stage/full", 0, "")
		target := engine.ReviewedFolderTarget{Configured: "/", Resolved: "/"}
		err := f.restore(t.Context(), f.approved(point, []ApprovedRestoreStep{{Point: point}}, target))
		if err == nil {
			t.Fatal("restore staged inside the filesystem root target")
		}
	})

	t.Run("manifest metadata changed", func(t *testing.T) {
		point := f.point(t, "full", "bad-manifest/full", 0, `{"item_manifests":{"archive":"not-hex"}}`)
		target := fldcovTarget(t)
		err := f.restore(t.Context(), f.approved(point, []ApprovedRestoreStep{{Point: point}}, target))
		if !errors.Is(err, ErrReviewedRestoreChanged) || !errors.Is(err, errInvalidItemManifest) {
			t.Fatalf("changed manifest: %v", err)
		}
		requireFldcovUntouched(t, target)
	})

	t.Run("classic step without reviewed objects", func(t *testing.T) {
		point := f.point(t, "full", "no-objects/full", 0, "")
		target := fldcovTarget(t)
		err := f.restore(t.Context(), f.approved(point, []ApprovedRestoreStep{{Point: point}}, target))
		if !errors.Is(err, ErrReviewedRestoreChanged) {
			t.Fatalf("missing objects: %v", err)
		}
		requireFldcovUntouched(t, target)
	})

	t.Run("reviewed object vanished from storage", func(t *testing.T) {
		point := f.point(t, "full", "vanished/full", 0, "")
		objects := f.writeStep(t, "vanished/full", good, nil)
		if err := os.Remove(filepath.Join(f.root, objects[0].Path)); err != nil {
			t.Fatal(err)
		}
		target := fldcovTarget(t)
		err := f.restore(t.Context(), f.approved(point, []ApprovedRestoreStep{{Point: point, Objects: objects}}, target))
		if !errors.Is(err, ErrReviewedRestoreChanged) {
			t.Fatalf("vanished object: %v", err)
		}
		requireFldcovUntouched(t, target)
	})

	t.Run("staged archive escapes the target", func(t *testing.T) {
		point := f.point(t, "full", "escape/full", 0, "")
		objects := f.writeStep(t, "escape/full", fldcovTar(t, []fldcovEntry{{"../outside.txt", "escape"}}), nil)
		target := fldcovTarget(t)
		err := f.restore(t.Context(), f.approved(point, []ApprovedRestoreStep{{Point: point, Objects: objects}}, target))
		if !errors.Is(err, ErrReviewedRestoreChanged) {
			t.Fatalf("escaping archive: %v", err)
		}
		requireFldcovUntouched(t, target)
		if _, err := os.Stat(filepath.Join(filepath.Dir(target.Resolved), "outside.txt")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("escaping entry was written: %v", err)
		}
	})

	t.Run("no steps", func(t *testing.T) {
		point := f.point(t, "full", "empty/full", 0, "")
		target := fldcovTarget(t)
		if err := f.restore(t.Context(), f.approved(point, nil, target)); !errors.Is(err, ErrReviewedRestoreChanged) {
			t.Fatalf("empty plan: %v", err)
		}
	})

	t.Run("revalidation after staging fails", func(t *testing.T) {
		point := f.point(t, "full", "revalidate/full", 0, "")
		objects := f.writeStep(t, "revalidate/full", good, nil)
		target := fldcovTarget(t)
		approved := f.approved(point, []ApprovedRestoreStep{{Point: point, Objects: objects}}, target)
		approved.Revalidate = func(context.Context) error { return ErrReviewedRestoreChanged }
		if err := f.restore(t.Context(), approved); !errors.Is(err, ErrReviewedRestoreChanged) {
			t.Fatalf("revalidation: %v", err)
		}
		if len(approved.checks) != 1 || approved.checks[0] != FolderDataStaged {
			t.Fatalf("checks after failed revalidation: %v", approved.checks)
		}
		requireFldcovUntouched(t, target)
	})

	t.Run("target retargeted at the write boundary", func(t *testing.T) {
		point := f.point(t, "full", "retarget/full", 0, "")
		objects := f.writeStep(t, "retarget/full", good, nil)
		target := fldcovTarget(t)
		moved := target
		moved.Resolved = filepath.Join(filepath.Dir(target.Resolved), "elsewhere")
		err := f.restore(t.Context(), f.approved(point, []ApprovedRestoreStep{{Point: point, Objects: objects}}, moved))
		if !errors.Is(err, ErrReviewedRestoreChanged) {
			t.Fatalf("retargeted folder: %v", err)
		}
		for _, dir := range []string{target.Resolved, moved.Resolved} {
			if _, err := os.Stat(filepath.Join(dir, "restored.txt")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("retargeted restore wrote %s: %v", dir, err)
			}
		}
	})

	t.Run("deduplicated step storage unavailable", func(t *testing.T) {
		manifest := strings.Repeat("ab", 32)
		point := f.point(t, "full", "chunked/full", 0, fmt.Sprintf(`{"item_manifests":{"archive":%q}}`, manifest))
		target := fldcovTarget(t)
		approved := f.approved(point, []ApprovedRestoreStep{{Point: point}}, target)
		approved.Plan.Destination.Type = "fixture-unknown-storage"
		if err := f.restore(t.Context(), approved); !errors.Is(err, ErrReviewedRestoreChanged) {
			t.Fatalf("unavailable chunk storage: %v", err)
		}
		requireFldcovUntouched(t, target)
	})
}

func fldcovIndex(paths ...string) engine.TarIndex {
	index := engine.TarIndex{Version: 1, Archive: "data.tar"}
	for _, path := range paths {
		index.Files = append(index.Files, engine.TarIndexEntry{Path: path, Size: int64(len(path)), Mode: "0600"})
	}
	return index
}

func TestApprovedFolderChainPrunesFilesDeletedByLaterStep(t *testing.T) {
	f := newFldcovFixture(t)
	f.runID = 1
	full := f.point(t, "full", "chain/full", 0, "")
	incr := f.point(t, "incremental", "chain/incr", full.ID, "")
	// Each body equals its path, so the index sizes match the written files.
	fullObjects := f.writeStep(t, "chain/full",
		fldcovTar(t, []fldcovEntry{{"kept.txt", "kept.txt"}, {"deleted.txt", "deleted.txt"}}),
		map[string]engine.TarIndex{"data.tar" + engine.IndexSuffix: fldcovIndex("kept.txt", "deleted.txt")})
	incrObjects := f.writeStep(t, "chain/incr",
		fldcovTar(t, []fldcovEntry{{"kept.txt", "kept.txt"}}),
		map[string]engine.TarIndex{
			"data.tar" + engine.IndexSuffix:   fldcovIndex("kept.txt"),
			"data.tar" + engine.ListingSuffix: fldcovIndex("kept.txt"),
		})
	target := fldcovTarget(t)
	target.Clean = true
	if err := os.MkdirAll(target.Resolved, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target.Resolved, "stale.txt"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	approved := f.approved(incr, []ApprovedRestoreStep{{Point: full, Objects: fullObjects}, {Point: incr, Objects: incrObjects}}, target)
	if err := f.restore(t.Context(), approved); err != nil {
		t.Fatalf("chain restore: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(target.Resolved, "kept.txt")); err != nil || string(data) != "kept.txt" {
		t.Fatalf("kept file: %q %v", data, err)
	}
	for _, gone := range []string{"deleted.txt", "stale.txt"} {
		if _, err := os.Stat(filepath.Join(target.Resolved, gone)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s survived the reviewed replace: %v", gone, err)
		}
	}
	want := []string{FolderDataStaged, FolderReviewRevalidated, FolderRestoreCompleted}
	if strings.Join(approved.checks, ",") != strings.Join(want, ",") {
		t.Fatalf("checks = %v", approved.checks)
	}
}

func TestApprovedFolderChainKeepsFilesWithoutTrustedSidecars(t *testing.T) {
	f := newFldcovFixture(t)
	full := f.point(t, "full", "nosidecar/full", 0, "")
	incr := f.point(t, "incremental", "nosidecar/incr", full.ID, "")
	fullObjects := f.writeStep(t, "nosidecar/full", fldcovTar(t, []fldcovEntry{{"old.txt", "old.txt"}}), nil)
	incrObjects := f.writeStep(t, "nosidecar/incr", fldcovTar(t, []fldcovEntry{{"new.txt", "new.txt"}}),
		map[string]engine.TarIndex{"data.tar" + engine.ListingSuffix: fldcovIndex("new.txt")})
	target := fldcovTarget(t)
	approved := f.approved(incr, []ApprovedRestoreStep{{Point: full, Objects: fullObjects}, {Point: incr, Objects: incrObjects}}, target)
	if err := f.restore(t.Context(), approved); err != nil {
		t.Fatalf("chain restore: %v", err)
	}
	// Without the earlier step's index nothing proves old.txt came from this
	// replay, so the prune must leave it in place.
	for _, name := range []string{"old.txt", "new.txt"} {
		if _, err := os.Stat(filepath.Join(target.Resolved, name)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestStagedFolderIndexRejectsMissingOrInvalidSidecars(t *testing.T) {
	if _, ok := stagedFolderIndex("", engine.IndexSuffix); ok {
		t.Fatal("chunked step produced an index")
	}
	if _, ok := stagedFolderIndex(filepath.Join(t.TempDir(), "missing"), engine.IndexSuffix); ok {
		t.Fatal("missing staging directory produced an index")
	}
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "data.tar"+engine.IndexSuffix), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.index.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := stagedFolderIndex(dir, engine.IndexSuffix); ok {
		t.Fatal("directory or non-archive sidecar produced an index")
	}
	invalid := t.TempDir()
	if err := os.WriteFile(filepath.Join(invalid, "data.tar"+engine.IndexSuffix), []byte(`{"version":99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := stagedFolderIndex(invalid, engine.IndexSuffix); ok {
		t.Fatal("unsupported index version was accepted")
	}
	if runtime := os.Getuid(); runtime != 0 {
		unreadable := t.TempDir()
		name := filepath.Join(unreadable, "data.tar"+engine.IndexSuffix)
		if err := os.WriteFile(name, []byte("{}"), 0o000); err != nil {
			t.Fatal(err)
		}
		if _, ok := stagedFolderIndex(unreadable, engine.IndexSuffix); ok {
			t.Fatal("unreadable sidecar produced an index")
		}
	}
	valid := t.TempDir()
	data, err := json.Marshal(fldcovIndex("a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(valid, "data.tar.zst"+engine.IndexSuffix+".age"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if index, ok := stagedFolderIndex(valid, engine.IndexSuffix); !ok || len(index.Files) != 1 || index.Files[0].Path != "a.txt" {
		t.Fatalf("valid index: %+v %v", index, ok)
	}
}

func TestPruneStagedFolderSkipsWithoutCompleteEvidence(t *testing.T) {
	writeIndex := func(t *testing.T, suffix string, paths ...string) string {
		t.Helper()
		dir := t.TempDir()
		data, err := json.Marshal(fldcovIndex(paths...))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "data.tar"+suffix), data, 0o600); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	target, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(target, "victim.txt")
	write := func() map[string]os.FileInfo {
		if err := os.WriteFile(victim, []byte("victim.txt"), 0o600); err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(victim)
		if err != nil {
			t.Fatal(err)
		}
		return map[string]os.FileInfo{"victim.txt": info}
	}
	earlier := writeIndex(t, engine.IndexSuffix, "victim.txt")
	later := writeIndex(t, engine.ListingSuffix)
	for name, steps := range map[string][]preparedFolderStep{
		"no listing":       {{dir: earlier}, {dir: t.TempDir()}},
		"no earlier index": {{dir: t.TempDir()}, {dir: later}},
	} {
		pruneStagedFolder(1, target, steps, write())
		if _, err := os.Stat(victim); err != nil {
			t.Fatalf("%s: pruned without evidence: %v", name, err)
		}
	}
	pruneStagedFolder(1, filepath.Join(target, "missing"), []preparedFolderStep{{dir: earlier}, {dir: later}}, write())
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("missing target pruned: %v", err)
	}
	pruneStagedFolder(1, target, []preparedFolderStep{{dir: earlier}, {dir: later}}, write())
	if _, err := os.Stat(victim); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("complete evidence did not prune: %v", err)
	}
}

func TestApprovedFolderProgressIgnoresUnknownPercent(t *testing.T) {
	f := newFldcovFixture(t)
	f.r.setRunStatus(&RunStatus{Active: true, JobID: f.job.ID, RunID: 9, CurrentItem: "archive"})
	progress := f.r.approvedFolderProgress(restoreProgressReporter{JobID: f.job.ID, RunID: 9, ItemType: "folder"}, 40, 100)
	progress("archive", -1, "")
	if got := f.r.Status().CurrentItemPercent; got != 0 {
		t.Fatalf("negative percent changed progress to %d", got)
	}
	progress("archive", 50, "")
	if got := f.r.Status().CurrentItemPercent; got != 70 {
		t.Fatalf("scaled percent = %d, want 70", got)
	}
}

func (f *fldcovFixture) dedupRepo(t *testing.T) (*dedup.Repo, func()) {
	t.Helper()
	adapter, err := storage.NewAdapter(f.dest.Type, f.dest.Config)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := dedup.InitRepo(f.db, adapter, f.dest.ID, f.key)
	if err != nil {
		t.Fatal(err)
	}
	return repo, func() { storage.CloseAdapter(adapter) }
}

func TestStageApprovedFolderChunksRefusesChangedManifests(t *testing.T) {
	f := newFldcovFixture(t)
	plan := ApprovedRestorePlan{Job: f.job, Destination: f.dest, Item: ItemSelection{ItemName: "archive", ItemType: "folder"}}
	target := fldcovTarget(t)

	if _, err := f.r.stageApprovedFolderChunks(t.Context(), plan, target.Resolved, dedup.ID{1}, t.TempDir(), ""); !errors.Is(err, ErrReviewedRestoreChanged) {
		t.Fatalf("uninitialized repository: %v", err)
	}

	repo, closeRepo := f.dedupRepo(t)
	defer closeRepo()
	chunk, err := repo.Put([]byte("chunk"))
	if err != nil {
		t.Fatal(err)
	}
	manifests := map[string]dedup.Manifest{
		"escape":   {Files: map[string]dedup.ManifestEntry{"../outside.txt": {Mode: 0o600, Size: 5, Chunks: []dedup.ID{chunk}}}},
		"size":     {Files: map[string]dedup.ManifestEntry{"short.txt": {Mode: 0o600, Size: 99, Chunks: []dedup.ID{chunk}}}},
		"missing":  {Files: map[string]dedup.ManifestEntry{"gone.txt": {Mode: 0o600, Size: 5, Chunks: []dedup.ID{{9, 9, 9}}}}},
		"complete": {Files: map[string]dedup.ManifestEntry{"a.txt": {Mode: 0o600, Size: 5, Chunks: []dedup.ID{chunk}}, "b.txt": {Mode: 0o600, Size: 10, Chunks: []dedup.ID{chunk, chunk}}, "dir": {Mode: 0o700, IsDir: true}}},
	}
	ids := make(map[string]dedup.ID, len(manifests))
	for name, manifest := range manifests {
		id, err := repo.PutManifest(name, manifest)
		if err != nil {
			t.Fatal(err)
		}
		ids[name] = id
	}
	if err := repo.Flush(); err != nil {
		t.Fatal(err)
	}

	stage := func(ctx context.Context, id dedup.ID) (*preparedFolderChunks, error) {
		return f.r.stageApprovedFolderChunks(ctx, plan, target.Resolved, id, t.TempDir(), "")
	}
	if _, err := stage(t.Context(), dedup.ID{4, 4}); !errors.Is(err, ErrReviewedRestoreChanged) {
		t.Fatalf("missing manifest: %v", err)
	}
	for _, name := range []string{"escape", "size", "missing"} {
		if _, err := stage(t.Context(), ids[name]); !errors.Is(err, ErrReviewedRestoreChanged) {
			t.Fatalf("%s manifest: %v", name, err)
		}
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := stage(cancelled, ids["complete"]); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled staging: %v", err)
	}
	if _, err := f.r.stageApprovedFolderChunks(t.Context(), plan, target.Resolved, ids["complete"], filepath.Join(t.TempDir(), "missing"), ""); err == nil {
		t.Fatal("staged chunks without a staging directory")
	}

	prepared, err := stage(t.Context(), ids["complete"])
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.cache.Close()
	if len(prepared.chunks) != 1 {
		t.Fatalf("shared chunk was staged %d times", len(prepared.chunks))
	}
	if data, err := prepared.get(chunk); err != nil || string(data) != "chunk" {
		t.Fatalf("staged chunk: %q %v", data, err)
	}
	if _, err := prepared.get(dedup.ID{8}); !errors.Is(err, ErrReviewedRestoreChanged) {
		t.Fatalf("unknown chunk: %v", err)
	}
}
