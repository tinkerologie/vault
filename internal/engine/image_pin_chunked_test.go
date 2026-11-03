package engine

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

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
