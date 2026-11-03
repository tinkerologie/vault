package engine

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	containertypes "github.com/moby/moby/api/types/container"
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
// restore goes on, so an approved override does not run the backup-time image,
// but only while the tag still names the image the pin tagged: a tag someone
// else moved in between keeps their change (Greptile and Aikido review of #606).
func TestFailedPinCheckPutsTheTagBack(t *testing.T) {
	t.Run("check fails while the tag names the pinned image", func(t *testing.T) {
		r := newImageRegistry()
		r.tags["nginx:1"] = "new-image"
		r.tags[pinRef] = "old-image"
		base := r.imageInspect
		tagInspects := 0
		r.imageInspect = func(ctx context.Context, ref string) (client.ImageInspectResult, error) {
			if ref == "nginx:1" {
				if tagInspects++; tagInspects == 2 { // the check right after tagging
					return client.ImageInspectResult{}, errFake458
				}
			}
			return base(ctx, ref)
		}
		if err := pinBackupImage(t.Context(), r, "nginx:1", pinRef); !errors.Is(err, errFake458) {
			t.Fatalf("err = %v", err)
		}
		if got := r.tags["nginx:1"]; got != "new-image" {
			t.Errorf("tag left on %q after a failed pin", got)
		}
	})
	t.Run("someone moved the tag after Vault tagged it", func(t *testing.T) {
		r := newImageRegistry()
		r.tags["nginx:1"] = "new-image"
		r.tags[pinRef] = "old-image"
		r.tags["someone/else:2"] = "elsewhere"
		f := &tagLandsElsewhere{imageRegistry: r}
		if err := pinBackupImage(t.Context(), f, "nginx:1", pinRef); err == nil || !strings.Contains(err.Error(), "backup-time image") {
			t.Fatalf("err = %v", err)
		}
		if got := r.tags["nginx:1"]; got != "elsewhere" {
			t.Errorf("rollback overwrote a tag someone else moved: %q", got)
		}
	})
	r := newImageRegistry()
	// A failure to put it back is logged, not raised over the pin error.
	r.tagErr = errFake458
	restoreImageTag(t.Context(), r, "nginx:1", "new-image", "")
	for _, ref := range []string{"nginx@" + pinDigest, "NOT A REF", "missing:1"} {
		if id, err := tagTarget(t.Context(), r, ref); id != "" || err != nil {
			t.Errorf("tagTarget(%q) = %q, %v", ref, id, err)
		}
	}
}

// A tag that cannot be inspected for any reason other than "not found" stops
// the pin before the tag is touched, so a failed restore is never left unable
// to put it back (Aikido review of #606).
func TestUninspectableTagStopsThePin(t *testing.T) {
	broken := func() *imageRegistry {
		r := newImageRegistry()
		r.tags["nginx:1"] = "new-image"
		r.tags[pinRef] = "old-image"
		base := r.imageInspect
		r.imageInspect = func(ctx context.Context, ref string) (client.ImageInspectResult, error) {
			if ref == "nginx:1" {
				return client.ImageInspectResult{}, errFake458
			}
			return base(ctx, ref)
		}
		return r
	}
	r := broken()
	if _, err := tagTarget(t.Context(), r, "nginx:1"); !errors.Is(err, errFake458) {
		t.Errorf("tagTarget err = %v", err)
	}
	if err := pinBackupImage(t.Context(), r, "nginx:1", pinRef); !errors.Is(err, errFake458) || len(r.tagged) != 0 {
		t.Errorf("pin err = %v, tagged = %v", err, r.tagged)
	}

	repo, id := chunkedImageFixture(t, false)
	r = broken()
	ctx := WithRestoreTrust(t.Context(), RestoreTrust{ArtifactTrusted: true, AllowUnpinnedImage: true})
	err := (&ContainerHandler{cli: r}).RestoreChunked(ctx, BackupItem{Name: "web"}, repo, id, "", noopProgress)
	if !errors.Is(err, errFake458) || !strings.Contains(err.Error(), "nothing was changed") || len(r.tagged) != 0 || len(r.pulled) != 0 {
		t.Fatalf("err = %v, tagged = %v, pulled = %v", err, r.tagged, r.pulled)
	}
}

// The rollback after a failed restore undoes only Vault's own pin: a tag that
// someone moved while the restore ran is left where they put it (Aikido review
// of #606).
func TestFailedRestoreLeavesATagMovedByOthers(t *testing.T) {
	repo, id := chunkedImageFixture(t, false)
	r := newImageRegistry()
	r.tags["nginx:1"] = "new-image"
	r.remote[pinRef] = "old-image"
	r.tags["someone/else:2"] = "their-image"
	r.containerCreate = func(context.Context, client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
		r.tags["nginx:1"] = "their-image" // an update lands mid-restore
		return client.ContainerCreateResult{}, errFake458
	}
	ctx := WithRestoreTrust(t.Context(), RestoreTrust{ArtifactTrusted: true})
	if err := (&ContainerHandler{cli: r}).RestoreChunked(ctx, BackupItem{Name: "web"}, repo, id, "", noopProgress); err == nil {
		t.Fatal("restore succeeded")
	}
	if got := r.tags["nginx:1"]; got != "their-image" {
		t.Errorf("rollback overwrote a tag moved by someone else: %q", got)
	}
	// Nor is it rolled back when the tag can no longer be checked.
	before := len(r.tagged)
	delete(r.tags, "nginx:1")
	restoreImageTag(t.Context(), r, "nginx:1", "new-image", "old-image")
	if len(r.tagged) != before {
		t.Errorf("retagged without checking the tag: %v", r.tagged[before:])
	}
}

// trackCreatedContainer makes the fake daemon remember the image a restore
// created the container from, and report it the way ContainerInspect does for
// the created ID. Looked up by name it reports a replacement container, so a
// seed that used the name would record the wrong image (Aikido review of #606).
func trackCreatedContainer(r *imageRegistry, onStart func()) {
	var created string
	r.containerCreate = func(_ context.Context, o client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
		created = r.tags[o.Config.Image]
		return client.ContainerCreateResult{ID: "c1"}, nil
	}
	r.containerStart = func(context.Context, string) (client.ContainerStartResult, error) {
		if onStart != nil {
			onStart()
		}
		return client.ContainerStartResult{}, nil
	}
	r.containerInspect = func(_ context.Context, id string) (client.ContainerInspectResult, error) {
		switch {
		case created == "":
			return client.ContainerInspectResult{}, errInspectMissing
		case id == "c1":
			return client.ContainerInspectResult{Container: containertypes.InspectResponse{ID: "c1", Image: created}}, nil
		default:
			// By name, a replacement container someone created meanwhile.
			return client.ContainerInspectResult{Container: containertypes.InspectResponse{ID: "c2", Image: "replacement-image"}}, nil
		}
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
	const newer = "sha256:" + "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	cases := []struct {
		name      string
		pinned    bool
		digests   []string // the current image's digests, for an override
		moveAfter bool     // a pull moves the tag after the container is created
		want      any
	}{
		{name: "pinned", pinned: true, want: pinDigest},
		{name: "pinned, tag moved after create", pinned: true, moveAfter: true, want: pinDigest},
		{name: "override keeps the current tag", digests: []string{"other/repo@" + pinDigest, "nginx@" + current}, want: current},
		{name: "override, tag moved after create", digests: []string{"nginx@" + current}, moveAfter: true, want: current},
		{name: "override with no digest for the image", want: nil},
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
				switch ref {
				case "current-image":
					// The image outlives the tag moving off it.
					res, err = client.ImageInspectResult{}, nil
					res.ID, res.RepoDigests = ref, tc.digests
				case "replacement-image":
					res, err = client.ImageInspectResult{}, nil
					res.ID, res.RepoDigests = ref, []string{"nginx@" + newer}
				case "newer-image", "nginx:1":
					if r.tags["nginx:1"] == "newer-image" {
						res.RepoDigests = []string{"nginx@" + newer}
					}
				}
				return res, err
			}
			trackCreatedContainer(r, func() {
				if tc.moveAfter {
					r.tags["nginx:1"] = "newer-image"
				}
			})
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
	seedRestoredImageStatus(t.Context(), nil, "", "web")
	seedRestoredImageStatus(t.Context(), nil, "nginx:1", "web")
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
		restoreImageTag(ctx, h, "nginx:1", "new-image", "")
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

// A restore cancelled right after pinning still puts the tag back: the pinned
// image's ID is read under the rollback's own bounded, uncancellable context
// (Greptile review of #606).
func TestCancelledRestorePutsTheTagBack(t *testing.T) {
	repo, id := chunkedImageFixture(t, false)
	r := newImageRegistry()
	r.tags["nginx:1"] = "new-image"
	r.remote[pinRef] = "old-image"
	base := r.imageInspect
	r.imageInspect = func(ctx context.Context, ref string) (client.ImageInspectResult, error) {
		if err := ctx.Err(); err != nil {
			return client.ImageInspectResult{}, err
		}
		return base(ctx, ref)
	}
	ctx, cancel := context.WithCancel(WithRestoreTrust(t.Context(), RestoreTrust{ArtifactTrusted: true}))
	defer cancel()
	ctx = WithApprovalsSettled(ctx, cancel) // cancelled the moment the pin is in place
	if err := (&ContainerHandler{cli: r}).RestoreChunked(ctx, BackupItem{Name: "web"}, repo, id, "", noopProgress); err == nil {
		t.Fatal("cancelled restore succeeded")
	}
	if got := r.tags["nginx:1"]; got != "new-image" {
		t.Errorf("tag left on %q after a cancelled restore", got)
	}
}

// Issue #611: when the restored container is gone by the time the status is
// written, something replaced it, so the entry describes that container now
// and is left alone instead of being cleared.
func TestSeedLeavesTheEntryOfAReplacedContainer(t *testing.T) {
	const before = `{"library/nginx:1":{"local":"sha256:live","remote":"sha256:live","status":"true"}}`
	content := before
	statusPath := withUpdateStatusPath458(t, &content)
	r := newImageRegistry()
	r.containerInspect = func(context.Context, string) (client.ContainerInspectResult, error) {
		return client.ContainerInspectResult{}, errInspectMissing // removed by a later restore
	}
	seedRestoredImageStatus(t.Context(), r, "nginx:1", "c1")
	if got := readFile458(t, statusPath); got != before {
		t.Errorf("entry of the live container changed: %s", got)
	}
	// A container that is still there but has no known digest is cleared.
	trackCreatedContainer(r, nil)
	r.tags["nginx:1"] = "plain-image"
	if _, err := r.ContainerCreate(t.Context(), client.ContainerCreateOptions{Config: &containertypes.Config{Image: "nginx:1"}}); err != nil {
		t.Fatal(err)
	}
	seedRestoredImageStatus(t.Context(), r, "nginx:1", "c1")
	if got := readStatus458(t, statusPath)["library/nginx:1"]["local"]; got != nil {
		t.Errorf("local = %v, want cleared", got)
	}
}
