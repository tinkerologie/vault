package engine

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"
	"github.com/tinkerologie/vault/internal/dedup"
)

// chunkedImageFixture stores a running container whose tag has moved since the
// backup recorded its digest.
func chunkedImageFixture(t *testing.T, privileged bool) (*dedup.Repo, dedup.ID) {
	t.Helper()
	withTestTemplatesDir(t)
	repo, _, cleanup := dedup.NewTestRepoForEngine(t)
	t.Cleanup(cleanup)
	hostConfig := `{}`
	if privileged {
		hostConfig = `{"Privileged":true}`
	}
	work := t.TempDir()
	m := dedup.Manifest{Files: map[string]dedup.ManifestEntry{
		containerInspectKey:   chunkTestFile(t, repo, filepath.Join(work, "inspect.json"), `{"Name":"/web","Config":{"Image":"nginx:1"},"State":{"Running":true},"HostConfig":`+hostConfig+`}`),
		containerImageMetaKey: chunkTestFile(t, repo, filepath.Join(work, "meta.json"), `{"image_tag":"nginx:1","repo_digests":["nginx@`+pinDigest+`"]}`),
	}}
	id, err := repo.PutManifest("web", m)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Flush(); err != nil {
		t.Fatal(err)
	}
	return repo, id
}

func TestRestoreChunkedRunsTheBackupTimeImage(t *testing.T) {
	repo, id := chunkedImageFixture(t, false)
	r := newImageRegistry()
	r.tags["nginx:1"] = "new-image"
	r.remote[pinRef] = "old-image"
	var createdWith, startedWith string
	r.containerCreate = func(_ context.Context, o client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
		createdWith = r.tags[o.Config.Image]
		return client.ContainerCreateResult{ID: "c1"}, nil
	}
	r.containerStart = func(context.Context, string) (client.ContainerStartResult, error) {
		startedWith = r.tags["nginx:1"]
		return client.ContainerStartResult{}, nil
	}
	ctx := WithRestoreTrust(t.Context(), RestoreTrust{ArtifactTrusted: true})
	if err := (&ContainerHandler{cli: r}).RestoreChunked(ctx, BackupItem{Name: "web"}, repo, id, "", noopProgress); err != nil {
		t.Fatal(err)
	}
	if createdWith != "old-image" || startedWith != "old-image" {
		t.Fatalf("created with %q, started with %q", createdWith, startedWith)
	}
	for _, p := range r.pulled {
		if p == "nginx:1" {
			t.Errorf("pulled the moved tag: %v", r.pulled)
		}
	}
}

func TestRestoreChunkedStopsBeforeChangingAnything(t *testing.T) {
	t.Run("unavailable digest", func(t *testing.T) {
		repo, id := chunkedImageFixture(t, false)
		r := newImageRegistry()
		r.pullErr = errFake458
		created := false
		r.containerCreate = func(context.Context, client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
			created = true
			return client.ContainerCreateResult{}, nil
		}
		ctx := WithRestoreTrust(t.Context(), RestoreTrust{ArtifactTrusted: true})
		err := (&ContainerHandler{cli: r}).RestoreChunked(ctx, BackupItem{Name: "web"}, repo, id, "", noopProgress)
		if err == nil || !strings.Contains(err.Error(), "allow_unpinned_image") || created {
			t.Fatalf("err = %v, created = %v", err, created)
		}
	})
	t.Run("untrusted privileges", func(t *testing.T) {
		repo, id := chunkedImageFixture(t, true)
		r := newImageRegistry()
		err := (&ContainerHandler{cli: r}).RestoreChunked(t.Context(), BackupItem{Name: "web"}, repo, id, "", noopProgress)
		if err == nil || !strings.Contains(err.Error(), "privileged mode") || len(r.pulled) != 0 {
			t.Fatalf("err = %v, pulled = %v", err, r.pulled)
		}
	})
}

// The digest recorded at backup must be the image the container runs: a pulled
// update moves the tag before the container is recreated.
func TestBackupChunkedRecordsTheRunningImage(t *testing.T) {
	c := dbContainer458(true, false)
	c.Image = "sha256:running"
	c.Config.Image = "nginx:1"
	f := backupFake458(c)
	f.containerInspect = func(context.Context, string) (client.ContainerInspectResult, error) {
		return client.ContainerInspectResult{Container: c}, nil
	}
	var inspected []string
	f.imageInspect = func(_ context.Context, ref string) (client.ImageInspectResult, error) {
		inspected = append(inspected, ref)
		var res client.ImageInspectResult
		if ref == "sha256:running" {
			res.RepoDigests = []string{"nginx@" + pinDigest}
		}
		return res, nil
	}
	repo := newChunkTestRepo(t)
	id, err := (&ContainerHandler{cli: f}).BackupChunked(t.Context(), BackupItem{Name: "db", Settings: map[string]any{"no_stop": true}}, repo, nil, noopProgress)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Flush(); err != nil {
		t.Fatal(err)
	}
	m, err := repo.GetManifest(id)
	if err != nil {
		t.Fatal(err)
	}
	body, err := repo.Get(m.Files[containerImageMetaKey].Chunks[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), pinDigest) || !strings.Contains(string(body), `"nginx:1"`) {
		t.Fatalf("image_meta = %s (inspected %v)", body, inspected)
	}
}

// Issue #599: a chunked restore that fails after pinning puts the tag back on
// the image it named before, and leaves Unraid's update badge alone.
func TestFailedChunkedRestorePutsTheTagBack(t *testing.T) {
	const before = `{"library/nginx:1":{"local":"sha256:prev","remote":"sha256:prev","status":"true"}}`
	content := before
	statusPath := withUpdateStatusPath458(t, &content)
	repo, id := chunkedImageFixture(t, false)
	r := newImageRegistry()
	r.tags["nginx:1"] = "new-image"
	r.remote[pinRef] = "old-image"
	r.containerCreate = func(context.Context, client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
		if r.tags["nginx:1"] != "old-image" {
			t.Errorf("created while the tag named %q", r.tags["nginx:1"])
		}
		return client.ContainerCreateResult{}, errFake458
	}
	ctx := WithRestoreTrust(t.Context(), RestoreTrust{ArtifactTrusted: true})
	if err := (&ContainerHandler{cli: r}).RestoreChunked(ctx, BackupItem{Name: "web"}, repo, id, "", noopProgress); err == nil {
		t.Fatal("restore succeeded")
	}
	if got := r.tags["nginx:1"]; got != "new-image" {
		t.Errorf("tag left on %q after a failed restore", got)
	}
	if got := readFile458(t, statusPath); got != before {
		t.Errorf("update status changed by a failed restore: %s", got)
	}
}

// A pin whose own check fails after moving the tag puts it back before the
// restore goes on, so an approved override does not run the backup-time image.
func TestFailedPinCheckPutsTheTagBack(t *testing.T) {
	r := newImageRegistry()
	r.tags["nginx:1"] = "new-image"
	r.tags[pinRef] = "old-image"
	f := &tagLandsElsewhere{imageRegistry: r}
	if err := pinBackupImage(t.Context(), f, "nginx:1", pinRef); err == nil || !strings.Contains(err.Error(), "backup-time image") {
		t.Fatalf("err = %v", err)
	}
	if got := r.tags["nginx:1"]; got != "new-image" {
		t.Errorf("tag left on %q after a failed pin", got)
	}
	// A failure to put it back is logged, not raised over the pin error.
	r.tagErr = errFake458
	restoreImageTag(t.Context(), r, "nginx:1", "new-image")
	if tagTarget(t.Context(), r, "nginx@"+pinDigest) != "" || tagTarget(t.Context(), r, "NOT A REF") != "" || tagTarget(t.Context(), r, "missing:1") != "" {
		t.Error("tagTarget named an image for a digest, bad or missing reference")
	}
}

// tagLandsElsewhere moves the tag to the digest's image somewhere other than
// the pinned one on the first request, then behaves normally.
type tagLandsElsewhere struct {
	*imageRegistry
	moved bool
}

func (s *tagLandsElsewhere) ImageTag(ctx context.Context, o client.ImageTagOptions) (client.ImageTagResult, error) {
	if !s.moved {
		s.moved = true
		s.tags[o.Target] = "elsewhere"
		return client.ImageTagResult{}, nil
	}
	return s.imageRegistry.ImageTag(ctx, o)
}

// Issue #599: after a chunked restore, Unraid's update-status cache names the
// digest of the image the container runs, not the backup-time one.
func TestChunkedRestoreSeedsTheRunningDigest(t *testing.T) {
	const current = "sha256:" + "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	cases := []struct {
		name      string
		pinned    bool
		tagDigest []string
		want      any
	}{
		{name: "pinned", pinned: true, want: pinDigest},
		{name: "override keeps the current tag", tagDigest: []string{"other/repo@" + pinDigest, "nginx@" + current}, want: current},
		{name: "override with no digest for the tag", want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content := `{"library/nginx:1":{"local":"sha256:stale","remote":"` + current + `","status":"false"}}`
			statusPath := withUpdateStatusPath458(t, &content)
			repo, id := chunkedImageFixture(t, false)
			r := newImageRegistry()
			r.tags["nginx:1"] = "current-image"
			ctx := WithRestoreTrust(t.Context(), RestoreTrust{ArtifactTrusted: true, AllowUnpinnedImage: true})
			if tc.pinned {
				r.remote[pinRef] = "old-image"
			} else {
				// The backup-time digest cannot be reached, nor the registry.
				r.pullErr = errFake458
			}
			base := r.imageInspect
			r.imageInspect = func(ctx context.Context, ref string) (client.ImageInspectResult, error) {
				res, err := base(ctx, ref)
				if ref == "nginx:1" && !tc.pinned {
					res.RepoDigests = tc.tagDigest
				}
				return res, err
			}
			r.containerCreate = func(context.Context, client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
				return client.ContainerCreateResult{ID: "c1"}, nil
			}
			if err := (&ContainerHandler{cli: r}).RestoreChunked(ctx, BackupItem{Name: "web"}, repo, id, "", noopProgress); err != nil {
				t.Fatal(err)
			}
			entry := readStatus458(t, statusPath)["library/nginx:1"]
			if entry["local"] != tc.want {
				t.Errorf("local = %v, want %v", entry["local"], tc.want)
			}
			wantStatus := "false"
			if tc.want == current {
				wantStatus = "true"
			} else if tc.want == nil {
				wantStatus = "undef"
			}
			if entry["status"] != wantStatus {
				t.Errorf("status = %v, want %s", entry["status"], wantStatus)
			}
		})
	}
}

// Issue #601: a restore reports that its approval checks are settled only once
// it is past them, never when it was refused for want of approval.
func TestRestoreReportsApprovalsSettledOnlyAfterTheGates(t *testing.T) {
	settledCtx := func(t *testing.T, trust RestoreTrust) (context.Context, *bool) {
		t.Helper()
		settled := new(bool)
		return WithApprovalsSettled(WithRestoreTrust(t.Context(), trust), func() { *settled = true }), settled
	}
	t.Run("chunked refusal for an unavailable digest", func(t *testing.T) {
		repo, id := chunkedImageFixture(t, false)
		r := newImageRegistry()
		r.pullErr = errFake458
		ctx, settled := settledCtx(t, RestoreTrust{ArtifactTrusted: true})
		err := (&ContainerHandler{cli: r}).RestoreChunked(ctx, BackupItem{Name: "web"}, repo, id, "", noopProgress)
		if !errors.Is(err, ErrRestoreNeedsApproval) || *settled {
			t.Fatalf("err = %v, settled = %v", err, *settled)
		}
	})
	t.Run("chunked refusal for host privileges", func(t *testing.T) {
		repo, id := chunkedImageFixture(t, true)
		ctx, settled := settledCtx(t, RestoreTrust{})
		err := (&ContainerHandler{cli: newImageRegistry()}).RestoreChunked(ctx, BackupItem{Name: "web"}, repo, id, "", noopProgress)
		if !errors.Is(err, ErrRestoreNeedsApproval) || *settled {
			t.Fatalf("err = %v, settled = %v", err, *settled)
		}
	})
	t.Run("classic refusal for host privileges", func(t *testing.T) {
		dir := t.TempDir()
		writeFile458(t, filepath.Join(dir, "image.tar"), "x")
		writeFile458(t, filepath.Join(dir, "config.json"), `{"Name":"/x","Config":{"Image":"a:1"},"HostConfig":{"Privileged":true}}`)
		ctx, settled := settledCtx(t, RestoreTrust{})
		err := (&ContainerHandler{cli: &fakeDocker458{}}).Restore(ctx, BackupItem{Name: "x"}, dir, noopProgress)
		if !errors.Is(err, ErrRestoreNeedsApproval) || *settled {
			t.Fatalf("err = %v, settled = %v", err, *settled)
		}
	})
	t.Run("chunked restore past the gates", func(t *testing.T) {
		repo, id := chunkedImageFixture(t, false)
		r := newImageRegistry()
		r.remote[pinRef] = "old-image"
		ctx, settled := settledCtx(t, RestoreTrust{ArtifactTrusted: true})
		var settledAtCreate bool
		r.containerCreate = func(context.Context, client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
			settledAtCreate = *settled
			return client.ContainerCreateResult{ID: "c1"}, nil
		}
		if err := (&ContainerHandler{cli: r}).RestoreChunked(ctx, BackupItem{Name: "web"}, repo, id, "", noopProgress); err != nil {
			t.Fatal(err)
		}
		if !settledAtCreate {
			t.Error("approvals not settled before the container was created")
		}
	})
	ApprovalsSettled(t.Context()) // nothing registered: a no-op
}

// Clearing a digest never creates an entry, and the guards are no-ops.
func TestForgetUnraidLocalDigest(t *testing.T) {
	content := `{"library/other:1":{"local":"sha256:x","remote":"sha256:x","status":"true"}}`
	statusPath := withUpdateStatusPath458(t, &content)
	forgetUnraidLocalDigest("")
	forgetUnraidLocalDigest("nginx:1")
	seedRestoredImageStatus(t.Context(), nil, "", "")
	seedRestoredImageStatus(t.Context(), nil, "nginx:1", "")
	if got := readFile458(t, statusPath); got != content {
		t.Errorf("status file changed: %s", got)
	}
}

// hungTagDaemon never answers a tag request until its context ends.
type hungTagDaemon struct {
	*imageRegistry
	sawDeadline bool
}

func (h *hungTagDaemon) ImageTag(ctx context.Context, _ client.ImageTagOptions) (client.ImageTagResult, error) {
	_, h.sawDeadline = ctx.Deadline()
	<-ctx.Done()
	return client.ImageTagResult{}, ctx.Err()
}

// The tag rollback after a cancelled restore gives up within its bound when
// Docker hangs, instead of holding the restore forever.
func TestImageTagRollbackIsBounded(t *testing.T) {
	orig := imageTagRollbackTimeout
	imageTagRollbackTimeout = 50 * time.Millisecond
	t.Cleanup(func() { imageTagRollbackTimeout = orig })
	ctx, cancel := context.WithCancel(t.Context())
	cancel() // the restore was cancelled after pinning
	h := &hungTagDaemon{imageRegistry: newImageRegistry()}
	done := make(chan struct{})
	go func() {
		defer close(done)
		restoreImageTag(ctx, h, "nginx:1", "new-image")
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("rollback blocked on a hung Docker daemon")
	}
	if !h.sawDeadline {
		t.Error("rollback ran without a deadline")
	}
}
