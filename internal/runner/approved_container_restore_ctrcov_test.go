package runner

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/tinkerologie/vault/internal/bounded"
	"github.com/tinkerologie/vault/internal/crypto"
	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/dedup"
	"github.com/tinkerologie/vault/internal/engine"
	"github.com/tinkerologie/vault/internal/storage"
)

const ctrcovConfig = `{"Name":"/app","Config":{"Image":"fixture/app:backup"},"State":{"Running":true}}`

// ctrcovRunnerFixture is a job on local storage whose restore points hold
// classic container backups of the item "app".
type ctrcovRunnerFixture struct {
	r     *Runner
	d     *db.DB
	root  string
	dest  db.StorageDestination
	job   db.Job
	runID int64
}

func newCtrcovRunnerFixture(t *testing.T) *ctrcovRunnerFixture {
	t.Helper()
	r, d, root := setupTestRunner(t)
	dest := createLocalDest(t, d, root)
	job := db.Job{Name: "containers", Enabled: true, StorageDestID: dest.ID}
	var err error
	if job.ID, err = d.CreateJob(job); err != nil {
		t.Fatal(err)
	}
	runID, err := d.CreateJobRun(db.JobRun{JobID: job.ID, Status: "completed"})
	if err != nil {
		t.Fatal(err)
	}
	return &ctrcovRunnerFixture{r: r, d: d, root: root, dest: dest, job: job, runID: runID}
}

// point records a restore point whose item directory holds files, and
// returns it with the objects a reviewed plan would list for it.
func (fx *ctrcovRunnerFixture) point(t *testing.T, name, metadata string, parent int64, files map[string][]byte) ApprovedRestoreStep {
	t.Helper()
	dir := filepath.Join(fx.root, "containers", name, "app")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for file, data := range files {
		if err := os.WriteFile(filepath.Join(dir, file), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if metadata == "" {
		metadata = `{"item_sizes":{"app":20},"item_types":{"app":"container"}}`
	}
	rp := db.RestorePoint{JobRunID: fx.runID, JobID: fx.job.ID, BackupType: "full", StoragePath: "containers/" + name,
		ParentRestorePointID: parent, Metadata: metadata}
	id, err := fx.d.CreateRestorePoint(rp)
	if err != nil {
		t.Fatal(err)
	}
	rp.ID = id
	var objects []storage.FileInfo
	if len(files) > 0 {
		adapter, err := storage.NewAdapter(fx.dest.Type, fx.dest.Config)
		if err != nil {
			t.Fatal(err)
		}
		defer storage.CloseAdapter(adapter)
		if objects, err = adapter.List(filepath.Join(rp.StoragePath, "app")); err != nil {
			t.Fatal(err)
		}
	}
	return ApprovedRestoreStep{Point: rp, Objects: objects}
}

func (fx *ctrcovRunnerFixture) plan(steps ...ApprovedRestoreStep) ApprovedRestorePlan {
	return ApprovedRestorePlan{Job: fx.job, Destination: fx.dest, Point: steps[len(steps)-1].Point, Steps: steps,
		Item: ItemSelection{ItemType: "container", ItemName: "app"}}
}

func ctrcovClassicFiles() map[string][]byte {
	return map[string][]byte{"config.json": []byte(ctrcovConfig), "image.tar": []byte("fixture image")}
}

// ctrcovDockerDaemon serves the Docker API calls a reviewed classic restore
// of "app" makes, and records them.
func ctrcovDockerDaemon(t *testing.T) func() []string {
	t.Helper()
	var mu sync.Mutex
	var calls []string
	running := true
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		p := r.URL.Path
		if strings.HasPrefix(p, "/v1.") {
			_, rest, _ := strings.Cut(p[1:], "/")
			p = "/" + rest
		}
		switch {
		case p == "/_ping":
			w.Header().Set("API-Version", "1.53")
			w.Header().Set("OSType", "linux")
		case p == "/images/load":
			_, _ = io.Copy(io.Discard, r.Body)
			calls = append(calls, "image_load")
			_, _ = fmt.Fprintln(w, `{"stream":"Loaded image: fixture/app:backup"}`)
		case p == "/containers/app/json":
			_, _ = fmt.Fprintf(w, `{"Id":"original-app","State":{"Running":%t}}`, running)
		case p == "/containers/original-app/stop":
			calls = append(calls, "stop")
			running = false
			w.WriteHeader(http.StatusNoContent)
		case p == "/containers/original-app" && r.Method == http.MethodDelete:
			calls = append(calls, "remove")
			w.WriteHeader(http.StatusNoContent)
		case p == "/containers/create":
			calls = append(calls, "create")
			_, _ = fmt.Fprintln(w, `{"Id":"restored-app"}`)
		case p == "/containers/restored-app/start":
			calls = append(calls, "start")
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(daemon.Close)
	t.Setenv("DOCKER_HOST", "tcp://"+strings.TrimPrefix(daemon.URL, "http://"))
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(calls)
	}
}

func TestCtrcovReviewContainerRestoreClassic(t *testing.T) {
	ctrcovDockerDaemon(t)
	fx := newCtrcovRunnerFixture(t)
	step := fx.point(t, "full", "", 0, ctrcovClassicFiles())
	work, err := fx.r.ReviewContainerRestore(t.Context(), fx.plan(step), "", "", false)
	if err != nil {
		t.Fatalf("ReviewContainerRestore: %v", err)
	}
	if !work.Trusted || work.NeedsPassphrase || work.RequiresPassphrase() || work.Reviewed.Effects.Name != "app" || !work.Reviewed.Effects.Start {
		t.Fatalf("work = %+v", work)
	}
}

func TestCtrcovReviewContainerRestoreRefusals(t *testing.T) {
	ctrcovDockerDaemon(t)
	gz := func(data string) []byte {
		var buf bytes.Buffer
		w := gzip.NewWriter(&buf)
		_, _ = w.Write([]byte(data))
		_ = w.Close()
		return buf.Bytes()
	}
	encrypted := func(t *testing.T, data string) []byte {
		t.Helper()
		reader, err := crypto.EncryptReader("right passphrase", strings.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		out, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	cases := []struct {
		name       string
		files      func(t *testing.T) map[string][]byte
		mutate     func(t *testing.T, fx *ctrcovRunnerFixture, plan *ApprovedRestorePlan)
		passphrase string
		is         error
		want       string
	}{
		{name: "unknown storage type", mutate: func(_ *testing.T, _ *ctrcovRunnerFixture, plan *ApprovedRestorePlan) {
			plan.Destination.Type = "ctrcov-unknown"
		}, is: ErrApprovedStorageUnavailable},
		{name: "corrupt item manifest", mutate: func(_ *testing.T, _ *ctrcovRunnerFixture, plan *ApprovedRestorePlan) {
			plan.Point.Metadata = `{"item_manifests":{"app":"zz"}}`
		}, want: "reading container item manifest"},
		{name: "encrypted metadata without a passphrase", files: func(t *testing.T) map[string][]byte {
			return map[string][]byte{"config.json.age": encrypted(t, ctrcovConfig), "image.tar": []byte("x")}
		}, is: ErrRestorePassphrase},
		{name: "wrong passphrase", files: func(t *testing.T) map[string][]byte {
			return map[string][]byte{"config.json.age": encrypted(t, ctrcovConfig), "image.tar": []byte("x")}
		}, passphrase: "wrong passphrase", is: ErrRestorePassphrase},
		{name: "corrupt encrypted metadata", files: func(*testing.T) map[string][]byte {
			return map[string][]byte{"config.json.age": []byte("not an age file"), "image.tar": []byte("x")}
		}, passphrase: "any passphrase", want: "opening container metadata"},
		{name: "metadata removed since review", mutate: func(t *testing.T, fx *ctrcovRunnerFixture, _ *ApprovedRestorePlan) {
			if err := os.Remove(filepath.Join(fx.root, "containers", "full", "app", "config.json")); err != nil {
				t.Fatal(err)
			}
		}, is: ErrReviewedRestoreChanged},
		{name: "metadata larger than the review limit", mutate: func(_ *testing.T, _ *ctrcovRunnerFixture, plan *ApprovedRestorePlan) {
			for i, object := range plan.Steps[0].Objects {
				if filepath.Base(object.Path) == "config.json" {
					plan.Steps[0].Objects[i].Size = 3 * engine.ContainerReviewMetadataLimit
				}
			}
		}, is: bounded.ErrTooLarge},
		{name: "no image archive", files: func(*testing.T) map[string][]byte {
			return map[string][]byte{"config.json.gz": gz(ctrcovConfig)}
		}, want: "reviewing container restore"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fx := newCtrcovRunnerFixture(t)
			files := ctrcovClassicFiles()
			if c.files != nil {
				files = c.files(t)
			}
			plan := fx.plan(fx.point(t, "full", "", 0, files))
			if c.mutate != nil {
				c.mutate(t, fx, &plan)
			}
			_, err := fx.r.ReviewContainerRestore(t.Context(), plan, "", c.passphrase, false)
			if c.is != nil && !errors.Is(err, c.is) || c.is == nil && (err == nil || !strings.Contains(err.Error(), c.want)) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestCtrcovReviewContainerRestoreBusyAndHostless(t *testing.T) {
	fx := newCtrcovRunnerFixture(t)
	plan := fx.plan(fx.point(t, "full", "", 0, ctrcovClassicFiles()))

	for range cap(approvedReadSlots) {
		approvedReadSlots <- struct{}{}
	}
	_, err := fx.r.ReviewContainerRestore(t.Context(), plan, "", "", false)
	for range cap(approvedReadSlots) {
		<-approvedReadSlots
	}
	if !errors.Is(err, ErrRunnerBusy) {
		t.Fatalf("err = %v, want busy", err)
	}

	t.Setenv("DOCKER_CERT_PATH", filepath.Join(t.TempDir(), "absent"))
	if _, err := fx.r.ReviewContainerRestore(t.Context(), plan, "", "", false); !errors.Is(err, ErrApprovedStorageUnavailable) {
		t.Fatalf("err = %v, want unavailable", err)
	}
}

// ctrcovDedupPoint stores a chunked container manifest for "app" in a repo
// sealed with key and returns a step pointing at it.
func (fx *ctrcovRunnerFixture) dedupPoint(t *testing.T, key []byte, build func(repo *dedup.Repo) dedup.Manifest) (ApprovedRestoreStep, dedup.ID) {
	t.Helper()
	adapter, err := storage.NewAdapter(fx.dest.Type, fx.dest.Config)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.CloseAdapter(adapter)
	repo, err := dedup.InitRepo(fx.d, adapter, fx.dest.ID, key)
	if err != nil {
		t.Fatal(err)
	}
	id, err := repo.PutManifest("app", build(repo))
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Flush(); err != nil {
		t.Fatal(err)
	}
	return fx.point(t, "chunked", fmt.Sprintf(`{"item_manifests":{"app":"%x"},"item_types":{"app":"container"}}`, id[:]), 0, nil), id
}

func ctrcovPut(t *testing.T, repo *dedup.Repo, data []byte) dedup.ManifestEntry {
	t.Helper()
	id, err := repo.Put(data)
	if err != nil {
		t.Fatal(err)
	}
	return dedup.ManifestEntry{Size: int64(len(data)), Chunks: []dedup.ID{id}}
}

func TestCtrcovReviewChunkedContainerRestore(t *testing.T) {
	ctrcovDockerDaemon(t)
	inspectOnly := func(repo *dedup.Repo) dedup.Manifest {
		return dedup.Manifest{Files: map[string]dedup.ManifestEntry{engine.ContainerInspectKey: ctrcovPut(t, repo, []byte(ctrcovConfig))}}
	}

	t.Run("no repository", func(t *testing.T) {
		fx := newCtrcovRunnerFixture(t)
		step := fx.point(t, "chunked", `{"item_manifests":{"app":"`+strings.Repeat("ab", 32)+`"}}`, 0, nil)
		if _, err := fx.r.ReviewContainerRestore(t.Context(), fx.plan(step), "", "", false); !errors.Is(err, ErrApprovedStorageUnavailable) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("repository sealed with another key", func(t *testing.T) {
		fx := newCtrcovRunnerFixture(t)
		fx.r.serverKey = bytes.Repeat([]byte{1}, 32)
		step, _ := fx.dedupPoint(t, bytes.Repeat([]byte{2}, 32), inspectOnly)
		work, err := fx.r.ReviewContainerRestore(t.Context(), fx.plan(step), "", "", false)
		if !errors.Is(err, ErrRestorePassphrase) || !work.NeedsPassphrase {
			t.Fatalf("err = %v, work = %+v", err, work)
		}
	})
	t.Run("missing manifest", func(t *testing.T) {
		fx := newCtrcovRunnerFixture(t)
		fx.r.serverKey = bytes.Repeat([]byte{1}, 32)
		fx.dedupPoint(t, fx.r.serverKey, inspectOnly)
		step := fx.point(t, "other", `{"item_manifests":{"app":"`+strings.Repeat("ab", 32)+`"}}`, 0, nil)
		if _, err := fx.r.ReviewContainerRestore(t.Context(), fx.plan(step), "", "", false); err == nil || !strings.Contains(err.Error(), "loading container manifest") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("no backup-time digest", func(t *testing.T) {
		fx := newCtrcovRunnerFixture(t)
		fx.r.serverKey = bytes.Repeat([]byte{1}, 32)
		step, _ := fx.dedupPoint(t, fx.r.serverKey, inspectOnly)
		if _, err := fx.r.ReviewContainerRestore(t.Context(), fx.plan(step), "", "", false); !errors.Is(err, engine.ErrRestoreNeedsApproval) {
			t.Fatalf("err = %v", err)
		}
	})
}

func ctrcovApproved(plan ApprovedRestorePlan, revalidate error) *ApprovedRestore {
	return &ApprovedRestore{Plan: plan, Revalidate: func(context.Context) error { return revalidate }}
}

func (fx *ctrcovRunnerFixture) review(t *testing.T, plan ApprovedRestorePlan) ContainerRestoreWork {
	t.Helper()
	work, err := fx.r.ReviewContainerRestore(t.Context(), plan, "", "", false)
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	return work
}

func TestCtrcovApprovedClassicContainerRestore(t *testing.T) {
	t.Run("single restore point", func(t *testing.T) {
		calls := ctrcovDockerDaemon(t)
		fx := newCtrcovRunnerFixture(t)
		plan := fx.plan(fx.point(t, "full", "", 0, ctrcovClassicFiles()))
		work := fx.review(t, plan)
		approved := ctrcovApproved(plan, nil)
		if err := work.restore(t.Context(), fx.r, approved, "", restoreProgressReporter{}); err != nil {
			t.Fatalf("restore: %v", err)
		}
		if want := []string{"image_load", "stop", "remove", "create", "start"}; !slices.Equal(calls(), want) {
			t.Fatalf("calls = %v, want %v", calls(), want)
		}
		if want := []string{FolderDataStaged, FolderReviewRevalidated, engine.ContainerStarted}; !slices.Equal(approved.checks, want) {
			t.Fatalf("checks = %v, want %v", approved.checks, want)
		}
	})
	t.Run("backup chain is merged", func(t *testing.T) {
		calls := ctrcovDockerDaemon(t)
		fx := newCtrcovRunnerFixture(t)
		base := fx.point(t, "full", "", 0, ctrcovClassicFiles())
		diff := fx.point(t, "diff", "", base.Point.ID, map[string][]byte{"config.json": []byte(ctrcovConfig)})
		plan := fx.plan(base, diff)
		work := fx.review(t, plan)
		if err := work.restore(t.Context(), fx.r, ctrcovApproved(plan, nil), "", restoreProgressReporter{}); err != nil {
			t.Fatalf("restore: %v", err)
		}
		if !slices.Contains(calls(), "start") {
			t.Fatalf("calls = %v", calls())
		}
	})
}

func TestCtrcovApprovedContainerRestoreRefusals(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, fx *ctrcovRunnerFixture, work *ContainerRestoreWork, plan *ApprovedRestorePlan)
		reval  error
		is     error
		want   string
	}{
		{name: "no staging outside the targets", mutate: func(_ *testing.T, _ *ctrcovRunnerFixture, work *ContainerRestoreWork, _ *ApprovedRestorePlan) {
			work.Reviewed.Effects.Destination = "/"
		}, want: "creating container restore staging"},
		{name: "no container host", mutate: func(t *testing.T, _ *ctrcovRunnerFixture, _ *ContainerRestoreWork, _ *ApprovedRestorePlan) {
			t.Setenv("DOCKER_CERT_PATH", filepath.Join(t.TempDir(), "absent"))
		}, want: "opening container restore host"},
		{name: "corrupt item manifest", mutate: func(_ *testing.T, _ *ctrcovRunnerFixture, _ *ContainerRestoreWork, plan *ApprovedRestorePlan) {
			plan.Point.Metadata = `{"item_manifests":{"app":"zz"}}`
		}, is: ErrReviewedRestoreChanged},
		{name: "no restore points", mutate: func(_ *testing.T, _ *ctrcovRunnerFixture, _ *ContainerRestoreWork, plan *ApprovedRestorePlan) {
			plan.Steps = nil
		}, is: ErrReviewedRestoreChanged},
		{name: "restore point lists no objects", mutate: func(_ *testing.T, _ *ctrcovRunnerFixture, _ *ContainerRestoreWork, plan *ApprovedRestorePlan) {
			plan.Steps[0].Objects = nil
		}, is: ErrReviewedRestoreChanged},
		{name: "object removed since review", mutate: func(t *testing.T, fx *ctrcovRunnerFixture, _ *ContainerRestoreWork, _ *ApprovedRestorePlan) {
			if err := os.Remove(filepath.Join(fx.root, "containers", "full", "app", "image.tar")); err != nil {
				t.Fatal(err)
			}
		}, is: ErrReviewedRestoreChanged},
		{name: "revalidation fails", reval: errors.New("ctrcov: request withdrawn"), want: "revalidating staged container restore"},
		{name: "configuration changed since review", mutate: func(_ *testing.T, _ *ctrcovRunnerFixture, work *ContainerRestoreWork, _ *ApprovedRestorePlan) {
			work.Reviewed.Effects.Start = false
		}, is: ErrReviewedRestoreChanged},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			calls := ctrcovDockerDaemon(t)
			fx := newCtrcovRunnerFixture(t)
			plan := fx.plan(fx.point(t, "full", "", 0, ctrcovClassicFiles()))
			work := fx.review(t, plan)
			if c.mutate != nil {
				c.mutate(t, fx, &work, &plan)
			}
			err := work.restore(t.Context(), fx.r, ctrcovApproved(plan, c.reval), "", restoreProgressReporter{})
			if c.is != nil && !errors.Is(err, c.is) || c.is == nil && (err == nil || !strings.Contains(err.Error(), c.want)) {
				t.Fatalf("err = %v", err)
			}
			if len(calls()) != 0 {
				t.Fatalf("refusal reached Docker: %v", calls())
			}
		})
	}
}

func TestCtrcovStageApprovedContainerChunks(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	volume := func(t *testing.T, repo *dedup.Repo, files map[string]dedup.ManifestEntry) dedup.ManifestEntry {
		t.Helper()
		id, err := repo.PutManifest("volume", dedup.Manifest{Files: files})
		if err != nil {
			t.Fatal(err)
		}
		return dedup.ManifestEntry{Size: 1, Chunks: []dedup.ID{id}}
	}
	t.Run("stages metadata and volume chunks once", func(t *testing.T) {
		fx := newCtrcovRunnerFixture(t)
		fx.r.serverKey = key
		var shared dedup.ManifestEntry
		step, id := fx.dedupPoint(t, key, func(repo *dedup.Repo) dedup.Manifest {
			shared = ctrcovPut(t, repo, []byte("shared"))
			return dedup.Manifest{Files: map[string]dedup.ManifestEntry{
				engine.ContainerInspectKey: ctrcovPut(t, repo, []byte(ctrcovConfig)),
				"__vol__/config": volume(t, repo, map[string]dedup.ManifestEntry{
					"dir":       {IsDir: true, Size: -1},
					"dir/a.txt": shared,
					"b.txt":     shared,
				}),
				"__vol__/skipped": {Size: -1},
			}}
		})
		prepared, err := fx.r.stageApprovedContainerChunks(t.Context(), fx.plan(step), id, "", t.TempDir(), restoreProgressReporter{})
		if err != nil {
			t.Fatalf("stage: %v", err)
		}
		defer prepared.cache.Close()
		data, err := prepared.Get(shared.Chunks[0])
		if err != nil || string(data) != "shared" {
			t.Fatalf("staged chunk = %q, %v", data, err)
		}
		if len(prepared.chunks) != 2 || len(prepared.manifests) != 2 {
			t.Fatalf("chunks = %d manifests = %d", len(prepared.chunks), len(prepared.manifests))
		}
		if _, err := prepared.GetManifest(dedup.ID{9}); !errors.Is(err, ErrReviewedRestoreChanged) {
			t.Fatalf("unstaged manifest = %v", err)
		}
	})
	refusals := []struct {
		name  string
		build func(t *testing.T, repo *dedup.Repo) map[string]dedup.ManifestEntry
		want  error
	}{
		{name: "metadata size differs", build: func(t *testing.T, repo *dedup.Repo) map[string]dedup.ManifestEntry {
			e := ctrcovPut(t, repo, []byte(ctrcovConfig))
			e.Size++
			return map[string]dedup.ManifestEntry{engine.ContainerInspectKey: e}
		}, want: ErrReviewedRestoreChanged},
		{name: "volume with several sub-manifests", build: func(t *testing.T, repo *dedup.Repo) map[string]dedup.ManifestEntry {
			e := volume(t, repo, map[string]dedup.ManifestEntry{})
			e.Chunks = append(e.Chunks, e.Chunks[0])
			return map[string]dedup.ManifestEntry{"__vol__/config": e}
		}, want: ErrReviewedRestoreChanged},
		{name: "volume sub-manifest missing", build: func(*testing.T, *dedup.Repo) map[string]dedup.ManifestEntry {
			return map[string]dedup.ManifestEntry{"__vol__/config": {Size: 1, Chunks: []dedup.ID{{9}}}}
		}, want: ErrReviewedRestoreChanged},
		{name: "volume path escapes", build: func(t *testing.T, repo *dedup.Repo) map[string]dedup.ManifestEntry {
			return map[string]dedup.ManifestEntry{"__vol__/config": volume(t, repo, map[string]dedup.ManifestEntry{"../escape": ctrcovPut(t, repo, []byte("x"))})}
		}, want: ErrReviewedRestoreChanged},
		{name: "chunk missing", build: func(*testing.T, *dedup.Repo) map[string]dedup.ManifestEntry {
			return map[string]dedup.ManifestEntry{engine.ContainerInspectKey: {Size: 1, Chunks: []dedup.ID{{8}}}}
		}, want: ErrReviewedRestoreChanged},
	}
	for _, c := range refusals {
		t.Run(c.name, func(t *testing.T) {
			fx := newCtrcovRunnerFixture(t)
			fx.r.serverKey = key
			step, id := fx.dedupPoint(t, key, func(repo *dedup.Repo) dedup.Manifest { return dedup.Manifest{Files: c.build(t, repo)} })
			_, err := fx.r.stageApprovedContainerChunks(t.Context(), fx.plan(step), id, "", t.TempDir(), restoreProgressReporter{})
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}
	t.Run("cancelled", func(t *testing.T) {
		fx := newCtrcovRunnerFixture(t)
		fx.r.serverKey = key
		step, id := fx.dedupPoint(t, key, func(repo *dedup.Repo) dedup.Manifest {
			return dedup.Manifest{Files: map[string]dedup.ManifestEntry{engine.ContainerInspectKey: ctrcovPut(t, repo, []byte(ctrcovConfig))}}
		})
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := fx.r.stageApprovedContainerChunks(ctx, fx.plan(step), id, "", t.TempDir(), restoreProgressReporter{}); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("repository unreadable", func(t *testing.T) {
		fx := newCtrcovRunnerFixture(t)
		fx.r.serverKey = key
		step, id := fx.dedupPoint(t, bytes.Repeat([]byte{8}, 32), func(repo *dedup.Repo) dedup.Manifest {
			return dedup.Manifest{Files: map[string]dedup.ManifestEntry{}}
		})
		if _, err := fx.r.stageApprovedContainerChunks(t.Context(), fx.plan(step), id, "", t.TempDir(), restoreProgressReporter{}); !errors.Is(err, ErrReviewedRestoreChanged) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("manifest missing", func(t *testing.T) {
		fx := newCtrcovRunnerFixture(t)
		fx.r.serverKey = key
		step, _ := fx.dedupPoint(t, key, func(repo *dedup.Repo) dedup.Manifest { return dedup.Manifest{} })
		if _, err := fx.r.stageApprovedContainerChunks(t.Context(), fx.plan(step), dedup.ID{9}, "", t.TempDir(), restoreProgressReporter{}); !errors.Is(err, ErrReviewedRestoreChanged) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("staging directory missing", func(t *testing.T) {
		fx := newCtrcovRunnerFixture(t)
		fx.r.serverKey = key
		step, id := fx.dedupPoint(t, key, func(repo *dedup.Repo) dedup.Manifest { return dedup.Manifest{} })
		if _, err := fx.r.stageApprovedContainerChunks(t.Context(), fx.plan(step), id, "", filepath.Join(t.TempDir(), "absent"), restoreProgressReporter{}); err == nil || !strings.Contains(err.Error(), "chunk cache") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("unknown storage type", func(t *testing.T) {
		fx := newCtrcovRunnerFixture(t)
		plan := fx.plan(fx.point(t, "x", "", 0, nil))
		plan.Destination.Type = "ctrcov-unknown"
		if _, err := fx.r.stageApprovedContainerChunks(t.Context(), plan, dedup.ID{1}, "", t.TempDir(), restoreProgressReporter{}); !errors.Is(err, ErrReviewedRestoreChanged) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestCtrcovApprovedChunkedContainerRestoreRefusals(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	build := func(t *testing.T) func(repo *dedup.Repo) dedup.Manifest {
		return func(repo *dedup.Repo) dedup.Manifest {
			return dedup.Manifest{Files: map[string]dedup.ManifestEntry{engine.ContainerInspectKey: ctrcovPut(t, repo, []byte(ctrcovConfig))}}
		}
	}
	t.Run("staging fails", func(t *testing.T) {
		ctrcovDockerDaemon(t)
		fx := newCtrcovRunnerFixture(t)
		fx.r.serverKey = key
		step, _ := fx.dedupPoint(t, bytes.Repeat([]byte{8}, 32), build(t))
		if err := (ContainerRestoreWork{}).restore(t.Context(), fx.r, ctrcovApproved(fx.plan(step), nil), "", restoreProgressReporter{}); err == nil || !strings.Contains(err.Error(), "staging container chunks") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("revalidation fails", func(t *testing.T) {
		calls := ctrcovDockerDaemon(t)
		fx := newCtrcovRunnerFixture(t)
		fx.r.serverKey = key
		step, _ := fx.dedupPoint(t, key, build(t))
		err := (ContainerRestoreWork{}).restore(t.Context(), fx.r, ctrcovApproved(fx.plan(step), errors.New("ctrcov: withdrawn")), "", restoreProgressReporter{})
		if err == nil || !strings.Contains(err.Error(), "revalidating staged container restore") || len(calls()) != 0 {
			t.Fatalf("err = %v, calls = %v", err, calls())
		}
	})
}
