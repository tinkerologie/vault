package engine

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	cerrdefs "github.com/containerd/errdefs"
	containertypes "github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/tinkerologie/vault/internal/dedup"
)

// ctrcovLiveContainer is the existing container a reviewed restore replaces.
// It runs until stopped; the container the restore creates is never visible
// to inspect, so the post-restore image status seeding leaves the host alone.
type ctrcovLiveContainer struct {
	mu      sync.Mutex
	running bool
	absent  bool
	stopped []string
	removed []string
	created int
	started []string
	onFirst func()
	calls   int
}

func (c *ctrcovLiveContainer) install(f *fakeDocker458) {
	c.running = true
	f.containerInspect = func(_ context.Context, id string) (client.ContainerInspectResult, error) {
		c.mu.Lock()
		c.calls++
		first := c.calls == 1
		c.mu.Unlock()
		if first && c.onFirst != nil {
			c.onFirst()
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.absent || id == "new-id" {
			return client.ContainerInspectResult{}, cerrdefs.ErrNotFound
		}
		return client.ContainerInspectResult{Container: containertypes.InspectResponse{
			ID: "old-id", State: &containertypes.State{Running: c.running},
		}}, nil
	}
	f.containerStop = func(_ context.Context, id string) (client.ContainerStopResult, error) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.running = false
		c.stopped = append(c.stopped, id)
		return client.ContainerStopResult{}, nil
	}
	f.containerRemove = func(_ context.Context, id string) (client.ContainerRemoveResult, error) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.removed = append(c.removed, id)
		return client.ContainerRemoveResult{}, nil
	}
	f.containerCreate = func(context.Context, client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.created++
		return client.ContainerCreateResult{ID: "new-id"}, nil
	}
	f.containerStart = func(_ context.Context, id string) (client.ContainerStartResult, error) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.started = append(c.started, id)
		return client.ContainerStartResult{}, nil
	}
	if f.imageLoad == nil {
		f.imageLoad = okImageLoad
	}
	f.imagePull = func(context.Context, string) (client.ImagePullResponse, error) {
		return nil, errors.New("ctrcov: a reviewed restore must not pull the tag")
	}
}

// reviewCtrcovFixture reviews the classic fixture exactly as the runner would.
func reviewCtrcovFixture(t *testing.T, sourceDir string) ContainerRestoreReview {
	t.Helper()
	metadata, err := readStagedContainerMetadata(sourceDir)
	if err != nil {
		t.Fatal(err)
	}
	review, err := (&ContainerHandler{cli: &fakeDocker458{}}).ReviewClassicRestore(trustedCtrcovContext(t), BackupItem{Name: "app"},
		[]ContainerBackupMetadata{metadata}, "", false)
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	return review
}

func ctrcovItem() BackupItem {
	return BackupItem{Name: "app", Settings: map[string]any{"restore_destination": "", SettingCleanDestination: false}}
}

func TestCtrcovReviewedClassicRestoreReplacesStoppedContainer(t *testing.T) {
	fx := newRestoreFixture458(t, true)
	review := reviewCtrcovFixture(t, fx.sourceDir)
	fx.wipe(t)
	f := &fakeDocker458{}
	live := &ctrcovLiveContainer{}
	live.install(f)
	var checks []string
	err := (&ContainerHandler{cli: f}).Restore(reviewedCtrcovContext(t, review, &checks), ctrcovItem(), fx.sourceDir, noopProgress)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := readFile458(t, filepath.Join(fx.volDir, "a.txt")); got != "alpha" {
		t.Errorf("a.txt = %q", got)
	}
	if !slices.Equal(live.stopped, []string{"old-id"}) || !slices.Equal(live.removed, []string{"old-id"}) || !slices.Equal(live.started, []string{"new-id"}) {
		t.Errorf("stopped=%v removed=%v started=%v", live.stopped, live.removed, live.started)
	}
	if !slices.Equal(checks, []string{ContainerStarted}) {
		t.Errorf("checks = %v", checks)
	}
}

func TestCtrcovReviewedClassicRestoreRecordsFailedStart(t *testing.T) {
	fx := newRestoreFixture458(t, true)
	review := reviewCtrcovFixture(t, fx.sourceDir)
	f := &fakeDocker458{}
	(&ctrcovLiveContainer{}).install(f)
	f.containerStart = func(context.Context, string) (client.ContainerStartResult, error) {
		return client.ContainerStartResult{}, errFake458
	}
	var checks []string
	err := (&ContainerHandler{cli: f}).Restore(reviewedCtrcovContext(t, review, &checks), ctrcovItem(), fx.sourceDir, noopProgress)
	if !errors.Is(err, errFake458) || !slices.Equal(checks, []string{ContainerStartFailed}) {
		t.Fatalf("err = %v, checks = %v", err, checks)
	}
}

func TestCtrcovReviewedClassicRestoreRecordsDatabaseNotReady(t *testing.T) {
	fx := newRestoreFixture458(t, true)
	rewriteConfig458(t, fx.sourceDir, func(m map[string]any) {
		m["Config"].(map[string]any)["Image"] = "postgres:16"
		m["Config"].(map[string]any)["Env"] = []string{"POSTGRES_USER=app"}
	})
	writeFile458(t, filepath.Join(fx.sourceDir, DatabaseDumpFile), "x")
	writeFile458(t, filepath.Join(fx.sourceDir, DatabaseReplayMarker), "")
	review := reviewCtrcovFixture(t, fx.sourceDir)
	if !review.Effects.DatabaseReplay {
		t.Fatalf("review must report the replay: %+v", review.Effects)
	}
	f := &fakeDocker458{}
	(&ctrcovLiveContainer{}).install(f)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f.execCreate = func(context.Context, string, client.ExecCreateOptions) (client.ExecCreateResult, error) {
		cancel()
		return client.ExecCreateResult{}, errFake458
	}
	var checks []string
	ctx = WithReviewedContainerRestore(WithRestoreTrust(ctx, RestoreTrust{ArtifactTrusted: true}), review, func(c string) { checks = append(checks, c) })
	if err := (&ContainerHandler{cli: f}).Restore(ctx, ctrcovItem(), fx.sourceDir, noopProgress); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if !slices.Equal(checks, []string{ContainerStarted, ContainerDatabaseNotReady}) {
		t.Fatalf("checks = %v", checks)
	}
}

func TestCtrcovReviewedClassicRestoreRefusesBeforeDocker(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, fx restoreFixture458)
		source func(fx restoreFixture458) string
		want   string
		is     error
	}{
		{name: "staging unreadable", source: func(fx restoreFixture458) string { return filepath.Join(fx.sourceDir, "absent") }, want: "no such file"},
		{name: "image archive removed", mutate: func(t *testing.T, fx restoreFixture458) { removeGlob458(t, fx.sourceDir, "image.tar*") }, want: "no container image archive"},
		{name: "configuration changed", mutate: func(t *testing.T, fx restoreFixture458) {
			rewriteConfig458(t, fx.sourceDir, func(m map[string]any) { m["Config"].(map[string]any)["Image"] = "nginx:2" })
		}, is: ErrContainerReviewChanged},
		{name: "volume archive escapes its target", mutate: func(t *testing.T, fx restoreFixture458) {
			matches, err := filepath.Glob(filepath.Join(fx.sourceDir, "volume_*"))
			if err != nil || len(matches) == 0 {
				t.Fatalf("volume archives: %v %v", matches, err)
			}
			for _, archive := range matches {
				ctrcovTar(t, archive, "../escape.txt")
			}
		}, want: "escape"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fx := newRestoreFixture458(t, true)
			review := reviewCtrcovFixture(t, fx.sourceDir)
			if c.mutate != nil {
				c.mutate(t, fx)
			}
			source := fx.sourceDir
			if c.source != nil {
				source = c.source(fx)
			}
			f := &fakeDocker458{}
			live := &ctrcovLiveContainer{}
			live.install(f)
			loaded := false
			f.imageLoad = func(ctx context.Context, r io.Reader) (client.ImageLoadResult, error) {
				loaded = true
				return okImageLoad(ctx, r)
			}
			err := (&ContainerHandler{cli: f}).Restore(reviewedCtrcovContext(t, review, nil), ctrcovItem(), source, noopProgress)
			if c.is != nil && !errors.Is(err, c.is) || c.is == nil && (err == nil || !strings.Contains(err.Error(), c.want)) {
				t.Fatalf("err = %v", err)
			}
			if loaded || len(live.stopped) != 0 {
				t.Fatalf("refusal reached Docker: loaded=%v stopped=%v", loaded, live.stopped)
			}
		})
	}
}

func TestCtrcovReviewedClassicRestoreRefusesAfterImageLoad(t *testing.T) {
	cases := []struct {
		name     string
		setup    func(t *testing.T, fx restoreFixture458, f *fakeDocker458, live *ctrcovLiveContainer)
		config   func(m map[string]any)
		progress func(t *testing.T, fx restoreFixture458) ProgressFunc
		want     string
		is       error
	}{
		{name: "image rejected", setup: func(_ *testing.T, _ restoreFixture458, f *fakeDocker458, _ *ctrcovLiveContainer) {
			f.imageLoad = func(_ context.Context, r io.Reader) (client.ImageLoadResult, error) {
				_, _ = io.Copy(io.Discard, r)
				return io.NopCloser(strings.NewReader(`{"error":"bad layer"}`)), nil
			}
		}, want: "loading the reviewed image"},
		{name: "loaded image differs", config: func(m map[string]any) { m["Image"] = "sha256:saved" }, want: "differs"},
		{name: "volume retargeted by a symlink", setup: func(t *testing.T, fx restoreFixture458, f *fakeDocker458, _ *ctrcovLiveContainer) {
			elsewhere := t.TempDir()
			f.imageLoad = func(ctx context.Context, r io.Reader) (client.ImageLoadResult, error) {
				if err := os.Rename(fx.volDir, fx.volDir+".moved"); err != nil {
					t.Error(err)
				}
				if err := os.Symlink(elsewhere, fx.volDir); err != nil {
					t.Error(err)
				}
				return okImageLoad(ctx, r)
			}
			t.Cleanup(func() {
				entries, _ := os.ReadDir(elsewhere)
				if len(entries) != 0 {
					t.Errorf("restore wrote through the swapped link: %v", entries)
				}
			})
		}, is: ErrContainerReviewChanged},
		{name: "volume retargeted while the container stops", setup: func(t *testing.T, fx restoreFixture458, f *fakeDocker458, live *ctrcovLiveContainer) {
			stop := f.containerStop
			f.containerStop = func(ctx context.Context, id string) (client.ContainerStopResult, error) {
				ctrcovSwap(t, fx.volDir)
				return stop(ctx, id)
			}
		}, is: ErrContainerReviewChanged},
		{name: "volume retargeted before recreation", progress: func(t *testing.T, fx restoreFixture458) ProgressFunc {
			return func(_ string, _ int, msg string) {
				if msg == "recreating container" {
					ctrcovSwap(t, fx.volDir)
				}
			}
		}, is: ErrContainerReviewChanged},
		{name: "image retagged before recreation", config: func(m map[string]any) { m["Image"] = "sha256:saved" }, setup: func(_ *testing.T, _ restoreFixture458, f *fakeDocker458, _ *ctrcovLiveContainer) {
			calls := 0
			f.imageInspect = func(context.Context, string) (client.ImageInspectResult, error) {
				calls++
				var res client.ImageInspectResult
				res.ID = "sha256:saved"
				if calls > 1 {
					res.ID = "sha256:retagged"
				}
				return res, nil
			}
		}, want: "differs"},
		{name: "container restarted after stopping", setup: func(_ *testing.T, _ restoreFixture458, f *fakeDocker458, live *ctrcovLiveContainer) {
			f.containerStop = func(_ context.Context, id string) (client.ContainerStopResult, error) {
				live.stopped = append(live.stopped, id)
				return client.ContainerStopResult{}, nil
			}
		}, is: ErrContainerReviewChanged},
		{name: "existing container cannot be inspected", setup: func(_ *testing.T, _ restoreFixture458, f *fakeDocker458, _ *ctrcovLiveContainer) {
			f.containerInspect = func(context.Context, string) (client.ContainerInspectResult, error) {
				return client.ContainerInspectResult{}, errFake458
			}
		}, want: "inspecting the container"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fx := newRestoreFixture458(t, true)
			if c.config != nil {
				rewriteConfig458(t, fx.sourceDir, c.config)
			}
			review := reviewCtrcovFixture(t, fx.sourceDir)
			f := &fakeDocker458{}
			live := &ctrcovLiveContainer{}
			live.install(f)
			f.imageInspect = func(context.Context, string) (client.ImageInspectResult, error) {
				var res client.ImageInspectResult
				res.ID = "sha256:current"
				return res, nil
			}
			if c.setup != nil {
				c.setup(t, fx, f, live)
			}
			progress := noopProgress
			if c.progress != nil {
				progress = c.progress(t, fx)
			}
			err := (&ContainerHandler{cli: f}).Restore(reviewedCtrcovContext(t, review, nil), ctrcovItem(), fx.sourceDir, progress)
			if c.is != nil && !errors.Is(err, c.is) || c.is == nil && (err == nil || !strings.Contains(err.Error(), c.want)) {
				t.Fatalf("err = %v", err)
			}
			if live.created != 0 || len(live.removed) != 0 {
				t.Fatalf("refusal recreated the container: created=%d removed=%v", live.created, live.removed)
			}
		})
	}
}

// ctrcovChunked builds a chunked container backup with one file mount and
// one directory mount, both under dir.
func ctrcovChunked(t *testing.T, dir string, digests []string) (*ctrcovRepo, dedup.ID) {
	t.Helper()
	repo := newCtrcovRepo()
	file := filepath.Join(dir, "etc", "hook.sh")
	data := filepath.Join(dir, "data")
	for _, d := range []string{filepath.Dir(file), data} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	repo.chunks[dedup.ID{20}] = []byte("#!/bin/sh\n")
	repo.chunks[dedup.ID{21}] = []byte("payload")
	repo.manifests[dedup.ID{30}] = dedup.Manifest{Files: map[string]dedup.ManifestEntry{
		"a.txt": {Size: 7, Mode: 0o600, Chunks: []dedup.ID{{21}}},
	}}
	files := map[string]dedup.ManifestEntry{
		containerInspectKey: repo.blob(1, ctrcovConfig(t, "/app", "nginx:1", true, nil,
			ctrcovMount{Type: "bind", Source: file, Destination: "/hook.sh"},
			ctrcovMount{Type: "bind", Source: data, Destination: "/data"},
		)),
		containerVolFilePrefix + "/hook.sh": {Size: 10, Mode: 0o700, Chunks: []dedup.ID{{20}}},
		containerVolPrefix + "/data":        {Size: 7, Chunks: []dedup.ID{{30}}},
	}
	if digests != nil {
		files[containerImageMetaKey] = repo.blob(2, ctrcovJSON(t, imageMeta{RepoDigests: digests}))
	}
	repo.manifests[dedup.ID{40}] = dedup.Manifest{Item: "app", Files: files}
	return repo, dedup.ID{40}
}

func newCtrcovChunkedDaemon(live *ctrcovLiveContainer) *imageRegistry {
	r := newImageRegistry()
	r.tags["nginx:1"] = "current-image"
	r.tags[pinRef] = "backup-image"
	pull := r.imagePull
	live.install(r.fakeDocker458)
	r.imagePull = pull
	return r
}

func TestCtrcovReviewedChunkedRestore(t *testing.T) {
	digests := []string{"nginx@" + pinDigest}
	review := func(t *testing.T, repo *ctrcovRepo, id dedup.ID) ContainerRestoreReview {
		t.Helper()
		review, err := (&ContainerHandler{cli: &fakeDocker458{}}).ReviewChunkedRestore(trustedCtrcovContext(t), BackupItem{Name: "app"}, repo.manifests[id], repo, "", false)
		if err != nil {
			t.Fatalf("review: %v", err)
		}
		return review
	}

	t.Run("restores the reviewed volumes", func(t *testing.T) {
		dir := t.TempDir()
		repo, id := ctrcovChunked(t, dir, digests)
		reviewed := review(t, repo, id)
		live := &ctrcovLiveContainer{}
		r := newCtrcovChunkedDaemon(live)
		var checks []string
		err := (&ContainerHandler{cli: r}).RestorePreparedChunked(reviewedCtrcovContext(t, reviewed, &checks), ctrcovItem(), repo, id, "", noopProgress)
		if err != nil {
			t.Fatalf("RestorePreparedChunked: %v", err)
		}
		if got := readFile458(t, filepath.Join(dir, "etc", "hook.sh")); got != "#!/bin/sh\n" {
			t.Errorf("file mount = %q", got)
		}
		if got := readFile458(t, filepath.Join(dir, "data", "a.txt")); got != "payload" {
			t.Errorf("directory mount = %q", got)
		}
		if !slices.Equal(checks, []string{ContainerStarted}) || !slices.Equal(live.removed, []string{"old-id"}) {
			t.Errorf("checks = %v removed = %v", checks, live.removed)
		}
	})

	t.Run("refuses a changed review", func(t *testing.T) {
		repo, id := ctrcovChunked(t, t.TempDir(), digests)
		reviewed := review(t, repo, id)
		reviewed.Effects.Start = false
		live := &ctrcovLiveContainer{}
		err := (&ContainerHandler{cli: newCtrcovChunkedDaemon(live)}).RestorePreparedChunked(reviewedCtrcovContext(t, reviewed, nil), ctrcovItem(), repo, id, "", noopProgress)
		if !errors.Is(err, ErrContainerReviewChanged) || live.calls != 0 {
			t.Fatalf("err = %v, inspect calls = %d", err, live.calls)
		}
	})

	t.Run("refuses a backup that can no longer be reviewed", func(t *testing.T) {
		repo, id := ctrcovChunked(t, t.TempDir(), digests)
		reviewed := review(t, repo, id)
		delete(repo.manifests[id].Files, containerImageMetaKey)
		err := (&ContainerHandler{cli: newCtrcovChunkedDaemon(&ctrcovLiveContainer{})}).RestorePreparedChunked(reviewedCtrcovContext(t, reviewed, nil), ctrcovItem(), repo, id, "", noopProgress)
		if !errors.Is(err, ErrRestoreNeedsApproval) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("refuses a volume retargeted after review", func(t *testing.T) {
		dir := t.TempDir()
		repo, id := ctrcovChunked(t, dir, digests)
		reviewed := review(t, repo, id)
		elsewhere := t.TempDir()
		live := &ctrcovLiveContainer{onFirst: func() {
			if err := os.Rename(dir, dir+".moved"); err != nil {
				t.Error(err)
			}
			if err := os.Symlink(elsewhere, dir); err != nil {
				t.Error(err)
			}
		}}
		err := (&ContainerHandler{cli: newCtrcovChunkedDaemon(live)}).RestorePreparedChunked(reviewedCtrcovContext(t, reviewed, nil), ctrcovItem(), repo, id, "", noopProgress)
		if !errors.Is(err, ErrContainerReviewChanged) {
			t.Fatalf("err = %v", err)
		}
		if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 || len(live.stopped) != 0 {
			t.Fatalf("wrote %v / stopped %v after the target moved", entries, live.stopped)
		}
	})

	for _, volume := range []string{"data", "etc"} {
		t.Run("refuses a "+volume+" volume retargeted while the container stops", func(t *testing.T) {
			dir := t.TempDir()
			repo, id := ctrcovChunked(t, dir, digests)
			reviewed := review(t, repo, id)
			live := &ctrcovLiveContainer{}
			r := newCtrcovChunkedDaemon(live)
			stop := r.containerStop
			r.containerStop = func(ctx context.Context, id string) (client.ContainerStopResult, error) {
				ctrcovSwap(t, filepath.Join(dir, volume))
				return stop(ctx, id)
			}
			err := (&ContainerHandler{cli: r}).RestorePreparedChunked(reviewedCtrcovContext(t, reviewed, nil), ctrcovItem(), repo, id, "", noopProgress)
			if !errors.Is(err, ErrContainerReviewChanged) || live.created != 0 {
				t.Fatalf("err = %v, created = %d", err, live.created)
			}
		})
	}

	t.Run("stops when the existing container cannot be inspected", func(t *testing.T) {
		repo, id := ctrcovChunked(t, t.TempDir(), digests)
		reviewed := review(t, repo, id)
		r := newCtrcovChunkedDaemon(&ctrcovLiveContainer{})
		r.containerInspect = func(context.Context, string) (client.ContainerInspectResult, error) {
			return client.ContainerInspectResult{}, errFake458
		}
		err := (&ContainerHandler{cli: r}).RestorePreparedChunked(reviewedCtrcovContext(t, reviewed, nil), ctrcovItem(), repo, id, "", noopProgress)
		if err == nil || !strings.Contains(err.Error(), "inspecting the container") {
			t.Fatalf("err = %v", err)
		}
	})
}

// ctrcovSwap replaces dir with a symlink to an empty directory, and fails the
// test if the restore later writes anything through that link.
func ctrcovSwap(t *testing.T, dir string) {
	t.Helper()
	elsewhere := t.TempDir()
	if err := os.Rename(dir, dir+".moved"); err != nil {
		t.Error(err)
	}
	if err := os.Symlink(elsewhere, dir); err != nil {
		t.Error(err)
	}
	t.Cleanup(func() {
		if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
			t.Errorf("restore wrote through the swapped link: %v", entries)
		}
	})
}
