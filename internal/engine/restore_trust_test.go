package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moby/moby/client"
)

// sameServerRestore models an ordinary restore of this server's own backup that
// also accepts the current tag, for tests about other parts of restore.
func sameServerRestore(t *testing.T) context.Context {
	t.Helper()
	return WithRestoreTrust(t.Context(), RestoreTrust{ArtifactTrusted: true, AllowUnpinnedImage: true})
}

func privilegedInspect(t *testing.T) restoreInspect {
	t.Helper()
	var ri restoreInspect
	if err := json.Unmarshal([]byte(`{"Name":"/x","Config":{"Image":"nginx:1"},"HostConfig":{
		"Privileged":true,"NetworkMode":"host","IpcMode":"host","PidMode":"host","CapAdd":["SYS_ADMIN"],
		"Devices":[{"PathOnHost":"/dev/sda"}],
		"Binds":["/var/run/docker.sock:/var/run/docker.sock","/mnt/user/appdata/x:/config","named:/data","/mnt/../etc:/etc2"]}}`), &ri); err != nil {
		t.Fatal(err)
	}
	return ri
}

func TestHostPrivilegesListsEverythingThatReachesTheHost(t *testing.T) {
	got := strings.Join(hostPrivileges(privilegedInspect(t)), "; ")
	for _, want := range []string{"privileged mode", "host network", "host IPC", "host PID", `"SYS_ADMIN"`, `"/dev/sda"`, `"/var/run/docker.sock"`, `"/etc"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	for _, unwanted := range []string{"appdata", "named"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("ordinary bind %q listed: %q", unwanted, got)
		}
	}
}

func TestEnforceRestorePrivileges(t *testing.T) {
	item := BackupItem{Name: "x"}
	ri := privilegedInspect(t)

	t.Run("same-server restore of a privileged container still works", func(t *testing.T) {
		ctx := WithRestoreTrust(t.Context(), RestoreTrust{ArtifactTrusted: true})
		if err := enforceRestorePrivileges(ctx, item, ri); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("an untrusted artifact is refused and told what it asks for", func(t *testing.T) {
		err := enforceRestorePrivileges(t.Context(), item, ri)
		if err == nil || !strings.Contains(err.Error(), "privileged mode") || !strings.Contains(err.Error(), "allow_host_privileges") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("explicit approval lets it through unstripped", func(t *testing.T) {
		ctx := WithRestoreTrust(t.Context(), RestoreTrust{AllowHostPrivileges: true})
		if err := enforceRestorePrivileges(ctx, item, ri); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("an untrusted artifact with ordinary settings needs no approval", func(t *testing.T) {
		if err := enforceRestorePrivileges(t.Context(), item, restoreInspect{}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestClassicRestoreRefusesUntrustedPrivilegesBeforeCreating(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "image.tar"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"Name":"/x","Config":{"Image":"a:1"},"HostConfig":{"Privileged":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	created := false
	f := &fakeDocker458{imageLoad: okImageLoad, containerCreate: func(context.Context, client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
		created = true
		return client.ContainerCreateResult{}, nil
	}}
	err := (&ContainerHandler{cli: f}).Restore(t.Context(), BackupItem{Name: "x"}, dir, noopProgress)
	if err == nil || !strings.Contains(err.Error(), "privileged mode") || created {
		t.Fatalf("err = %v, created = %v", err, created)
	}
}

const (
	pinDigest = "sha256:" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	pinRef    = "docker.io/library/nginx@" + pinDigest
)

func TestBackupImageRef(t *testing.T) {
	cases := []struct {
		name, image string
		digests     []string
		want        string
		wantErr     bool
	}{
		{name: "familiar digest for the same repo", image: "nginx:1", digests: []string{"nginx@" + pinDigest}, want: pinRef},
		{name: "digest from another repo is ignored", image: "nginx:1", digests: []string{"evil.example/nginx@" + pinDigest}, wantErr: true},
		{name: "malformed digest is ignored", image: "nginx:1", digests: []string{"nginx@sha256:nope"}, wantErr: true},
		{name: "no digest", image: "nginx:1", wantErr: true},
		{name: "config already pinned", image: "nginx@" + pinDigest, want: pinRef},
		{name: "unparseable image", image: "UPPER CASE", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := backupImageRef(c.image, c.digests)
			if (err != nil) != c.wantErr || got != c.want {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}
}

// imageRegistry fakes the daemon's image store. tags maps a reference to an ID.
type imageRegistry struct {
	*fakeDocker458
	tags    map[string]string
	pulled  []string
	tagged  []client.ImageTagOptions
	tagErr  error
	pullErr error
	// digestsOnPull is written to the image a digest pull brings in.
	remote map[string]string
}

func newImageRegistry() *imageRegistry {
	r := &imageRegistry{fakeDocker458: &fakeDocker458{}, tags: map[string]string{}, remote: map[string]string{}}
	r.imageInspect = func(_ context.Context, ref string) (client.ImageInspectResult, error) {
		id, ok := r.tags[ref]
		if !ok {
			return client.ImageInspectResult{}, errFake458
		}
		var res client.ImageInspectResult
		res.ID = id
		if strings.Contains(ref, "@") {
			res.RepoDigests = []string{familiarRef(ref)}
		}
		return res, nil
	}
	r.imagePull = func(_ context.Context, ref string) (client.ImagePullResponse, error) {
		r.pulled = append(r.pulled, ref)
		if r.pullErr != nil {
			return nil, r.pullErr
		}
		if id, ok := r.remote[ref]; ok {
			r.tags[ref] = id
		}
		return &fakePullResponse{Reader: strings.NewReader("")}, nil
	}
	return r
}

func (r *imageRegistry) ImageTag(_ context.Context, o client.ImageTagOptions) (client.ImageTagResult, error) {
	r.tagged = append(r.tagged, o)
	if r.tagErr != nil {
		return client.ImageTagResult{}, r.tagErr
	}
	r.tags[o.Target] = r.tags[o.Source]
	return client.ImageTagResult{}, nil
}

func TestResolveChunkedImage(t *testing.T) {
	item := BackupItem{Name: "web"}
	digests := []string{"nginx@" + pinDigest}

	t.Run("a moved tag is put back on the backup-time image", func(t *testing.T) {
		r := newImageRegistry()
		r.tags["nginx:1"] = "new-image"
		r.remote[pinRef] = "old-image"
		pinned, err := (&ContainerHandler{cli: r}).resolveChunkedImage(t.Context(), item, "nginx:1", digests, noopProgress)
		if err != nil || !pinned {
			t.Fatalf("pinned = %v, err = %v", pinned, err)
		}
		if r.tags["nginx:1"] != "old-image" {
			t.Errorf("tag resolves to %q", r.tags["nginx:1"])
		}
		if len(r.pulled) != 1 || r.pulled[0] != pinRef {
			t.Errorf("pulled %v, want only the digest", r.pulled)
		}
	})
	t.Run("a local copy works offline", func(t *testing.T) {
		r := newImageRegistry()
		r.tags[pinRef] = "old-image"
		r.pullErr = errFake458
		pinned, err := (&ContainerHandler{cli: r}).resolveChunkedImage(t.Context(), item, "nginx:1", digests, nil)
		if err != nil || !pinned || len(r.pulled) != 0 {
			t.Fatalf("pinned = %v, err = %v, pulled = %v", pinned, err, r.pulled)
		}
	})
	refusals := []struct {
		name    string
		digests []string
		setup   func(*imageRegistry)
		want    string
	}{
		{name: "missing digest", want: "recorded no digest"},
		{name: "unpullable digest", digests: digests, setup: func(r *imageRegistry) { r.pullErr = errFake458 }, want: "pulling"},
		{name: "pull leaves the digest absent", digests: digests, want: "after pull"},
		{name: "tag cannot be moved", digests: digests, setup: func(r *imageRegistry) { r.remote[pinRef] = "old"; r.tagErr = errFake458 }, want: "pointing"},
	}
	for _, c := range refusals {
		t.Run(c.name+" stops without touching the tag", func(t *testing.T) {
			r := newImageRegistry()
			if c.setup != nil {
				c.setup(r)
			}
			before := len(r.pulled)
			_, err := (&ContainerHandler{cli: r}).resolveChunkedImage(t.Context(), item, "nginx:1", c.digests, noopProgress)
			if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "allow_unpinned_image") {
				t.Fatalf("err = %v", err)
			}
			if c.digests == nil && len(r.pulled) != before {
				t.Errorf("pulled the mutable tag: %v", r.pulled)
			}
		})
		t.Run(c.name+" is accepted with the override", func(t *testing.T) {
			r := newImageRegistry()
			if c.setup != nil {
				c.setup(r)
			}
			ctx := WithRestoreTrust(t.Context(), RestoreTrust{AllowUnpinnedImage: true})
			pinned, err := (&ContainerHandler{cli: r}).resolveChunkedImage(ctx, item, "nginx:1", c.digests, noopProgress)
			if err != nil || pinned {
				t.Fatalf("pinned = %v, err = %v", pinned, err)
			}
			if r.pulled[len(r.pulled)-1] != "nginx:1" {
				t.Errorf("pulled %v, want the tag last", r.pulled)
			}
		})
	}
	t.Run("a tag that will not land on the digest is refused", func(t *testing.T) {
		r := newImageRegistry()
		r.tags[pinRef] = "old"
		h := &stuckTagRegistry{imageRegistry: r}
		if _, err := (&ContainerHandler{cli: h}).resolveChunkedImage(t.Context(), item, "nginx:1", digests, nil); err == nil || !strings.Contains(err.Error(), "backup-time image") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("no image means nothing to do", func(t *testing.T) {
		if pinned, err := (&ContainerHandler{cli: newImageRegistry()}).resolveChunkedImage(t.Context(), item, "", nil, nil); pinned || err != nil {
			t.Fatalf("pinned = %v, err = %v", pinned, err)
		}
	})
	t.Run("a pull that reports failure while waiting is refused", func(t *testing.T) {
		r := newImageRegistry()
		r.imagePull = func(context.Context, string) (client.ImagePullResponse, error) {
			return &fakePullResponse{Reader: strings.NewReader(""), waitErr: errors.New("manifest unknown")}, nil
		}
		if _, err := (&ContainerHandler{cli: r}).resolveChunkedImage(t.Context(), item, "nginx:1", digests, nil); err == nil || !strings.Contains(err.Error(), "manifest unknown") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("the override tolerates an unreachable registry", func(t *testing.T) {
		r := newImageRegistry()
		r.pullErr = errFake458
		ctx := WithRestoreTrust(t.Context(), RestoreTrust{AllowUnpinnedImage: true})
		if _, err := (&ContainerHandler{cli: r}).resolveChunkedImage(ctx, item, "nginx:1", nil, nil); err != nil {
			t.Fatal(err)
		}
	})
}

// stuckTagRegistry accepts a tag request but leaves the tag on another image.
type stuckTagRegistry struct{ *imageRegistry }

func (s *stuckTagRegistry) ImageTag(context.Context, client.ImageTagOptions) (client.ImageTagResult, error) {
	s.tags["nginx:1"] = "elsewhere"
	return client.ImageTagResult{}, nil
}

func TestPinnedRecreateDoesNotPullTheTagAgain(t *testing.T) {
	r := newImageRegistry()
	r.containerCreate = func(context.Context, client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
		return client.ContainerCreateResult{ID: "c"}, nil
	}
	var ri restoreInspect
	ri.Name = "/web"
	ri.Config.Image = "nginx:1"
	ri.imagePinned = true
	if err := (&ContainerHandler{cli: r}).recreateAndStartContainer(t.Context(), BackupItem{Name: "web"}, ri, "", "", noopProgress); err != nil {
		t.Fatal(err)
	}
	if len(r.pulled) != 0 {
		t.Errorf("pulled %v after pinning", r.pulled)
	}
	ri.imagePinned = false
	if err := (&ContainerHandler{cli: r}).recreateAndStartContainer(t.Context(), BackupItem{Name: "web"}, ri, "", "", noopProgress); err != nil {
		t.Fatal(err)
	}
	if len(r.pulled) != 1 {
		t.Errorf("unpinned recreate pulled %v", r.pulled)
	}
}

func TestPinBackupImageFailures(t *testing.T) {
	t.Run("tag not inspectable after moving it", func(t *testing.T) {
		r := newImageRegistry()
		r.tags[pinRef] = "old"
		f := &tagVanishes{imageRegistry: r}
		if err := pinBackupImage(t.Context(), f, "nginx:1", pinRef); err == nil || !strings.Contains(err.Error(), "inspecting nginx:1") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("pulled image without the digest", func(t *testing.T) {
		r := newImageRegistry()
		r.imagePull = func(context.Context, string) (client.ImagePullResponse, error) {
			r.tags[pinRef] = "other"
			return &fakePullResponse{Reader: strings.NewReader("")}, nil
		}
		base := r.imageInspect
		r.imageInspect = func(ctx context.Context, ref string) (client.ImageInspectResult, error) {
			res, err := base(ctx, ref)
			res.RepoDigests = nil
			return res, err
		}
		if err := pinBackupImage(t.Context(), r, "nginx:1", pinRef); err == nil || !strings.Contains(err.Error(), "does not carry") {
			t.Fatalf("err = %v", err)
		}
	})
	if got := familiarRef("NOT A REF"); got != "NOT A REF" {
		t.Errorf("familiarRef = %q", got)
	}
}

// tagVanishes moves the tag but then cannot inspect it.
type tagVanishes struct{ *imageRegistry }

func (v *tagVanishes) ImageTag(context.Context, client.ImageTagOptions) (client.ImageTagResult, error) {
	delete(v.tags, "nginx:1")
	return client.ImageTagResult{}, nil
}
