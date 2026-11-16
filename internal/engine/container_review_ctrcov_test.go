package engine

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cerrdefs "github.com/containerd/errdefs"
	containertypes "github.com/moby/moby/api/types/container"
	imagetypes "github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"

	"github.com/tinkerologie/vault/internal/dedup"
)

// ctrcovMount is one saved mount in a test inspect document.
type ctrcovMount struct {
	Type        string `json:"Type"`
	Name        string `json:"Name,omitempty"`
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
}

// ctrcovConfig renders a saved container inspect document.
func ctrcovConfig(t *testing.T, name, image string, running bool, binds []string, mounts ...ctrcovMount) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"Name":       name,
		"Config":     map[string]any{"Image": image},
		"HostConfig": map[string]any{"Binds": binds},
		"State":      map[string]any{"Running": running},
		"Mounts":     mounts,
	})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func ctrcovJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func trustedCtrcovContext(t *testing.T) context.Context {
	t.Helper()
	return WithRestoreTrust(t.Context(), RestoreTrust{ArtifactTrusted: true})
}

func TestCtrcovReviewClassicRestoreDescribesEachVolumeEffect(t *testing.T) {
	dir := t.TempDir()
	deep := filepath.Join(dir, "a", "b", "config")
	if err := os.MkdirAll(deep, 0o750); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "hook.sh")
	skipped := filepath.Join(dir, "cache")
	config := ctrcovConfig(t, "/app", "nginx:1", true, nil,
		ctrcovMount{Type: "bind", Source: deep, Destination: "/config"},
		ctrcovMount{Type: "bind", Source: file, Destination: "/hook.sh"},
		ctrcovMount{Type: "bind", Source: skipped, Destination: "/cache"},
		ctrcovMount{Type: "tmpfs", Destination: "/tmp"},
	)
	volumes := ctrcovJSON(t, []volumeManifestEntry{
		{Index: 0, Source: deep, BackedUp: true, Archive: volumeArchiveBase(deep) + ".zst"},
		{Index: 1, Source: file, BackedUp: true, IsFile: true, Archive: volumeArchiveBase(file)},
		{Index: 2, Source: skipped, SkipReason: "excluded by label"},
	})
	step := ContainerBackupMetadata{Inspect: config, Volumes: volumes, Template: []byte("<Container/>"), Files: []string{
		"config.json", "volumes.json", "image.tar.zst", volumeArchiveBase(deep) + ".zst", volumeArchiveBase(file),
		// A non-archive file with a reserved prefix is ignored, not counted.
		"volume_notes.txt", DatabaseDumpFile + ".gz", DatabaseReplayMarker,
	}}

	h := &ContainerHandler{cli: &fakeDocker458{}}
	review, err := h.ReviewClassicRestore(trustedCtrcovContext(t), BackupItem{Name: "app"}, []ContainerBackupMetadata{step}, "", true)
	if err != nil {
		t.Fatalf("ReviewClassicRestore: %v", err)
	}
	effects := review.Effects
	if effects.Name != "app" || effects.Image != "nginx:1" || !effects.Start || !effects.Template || !effects.DatabaseDump || !effects.DatabaseReplay {
		t.Fatalf("effects = %+v", effects)
	}
	modes := map[string]string{}
	for _, v := range effects.Volumes {
		modes[v.Mount] = v.WriteMode
	}
	want := map[string]string{"/config": "replace", "/hook.sh": "overwrite_file", "/cache": "configuration_only"}
	for mount, mode := range want {
		if modes[mount] != mode {
			t.Errorf("%s write mode = %q, want %q (all: %v)", mount, modes[mount], mode, modes)
		}
	}
	if len(effects.Volumes) != 3 {
		t.Errorf("volumes = %+v, want tmpfs left out", effects.Volumes)
	}

	// Without the clean flag the same directory is merged.
	merged, err := h.ReviewClassicRestore(trustedCtrcovContext(t), BackupItem{Name: "app"}, []ContainerBackupMetadata{step}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if merged.Effects.Volumes[0].WriteMode != "merge" {
		t.Errorf("unclean directory mode = %q, want merge", merged.Effects.Volumes[0].WriteMode)
	}
	if review.matches(merged) {
		t.Error("reviews with different write modes must not match")
	}
	if !review.matches(review) {
		t.Error("a review must match itself")
	}
}

func TestCtrcovReviewClassicChainMapsArchivesBySource(t *testing.T) {
	dir := t.TempDir()
	config := ctrcovConfig(t, "/app", "nginx:1", false, nil, ctrcovMount{Type: "bind", Source: dir, Destination: "/config"})
	// The base full stored the volume under its legacy positional name; the
	// chain merge re-keys it by source so the differential still pairs up.
	base := ContainerBackupMetadata{
		Inspect: config,
		Volumes: ctrcovJSON(t, []volumeManifestEntry{{Index: 0, Source: dir, BackedUp: true, Archive: "volume_0.tar"}}),
		Files:   []string{"image.tar", "volume_0.tar"},
	}
	diff := ContainerBackupMetadata{
		Volumes: ctrcovJSON(t, []volumeManifestEntry{{Index: 0, Source: dir, BackedUp: true, Archive: "volume_0.tar"}}),
		Files:   []string{"volume_0.tar.gz"},
	}
	h := &ContainerHandler{cli: &fakeDocker458{}}
	review, err := h.ReviewClassicRestore(trustedCtrcovContext(t), BackupItem{Name: "app"}, []ContainerBackupMetadata{base, diff}, "", false)
	if err != nil {
		t.Fatalf("ReviewClassicRestore: %v", err)
	}
	if len(review.Effects.Volumes) != 1 || review.Effects.Volumes[0].WriteMode != "merge" || review.Effects.Start {
		t.Fatalf("effects = %+v", review.Effects)
	}
	if review.Effects.DatabaseDump {
		t.Error("a chain must not report a database dump")
	}
}

func TestCtrcovReviewClassicRestoreRefusals(t *testing.T) {
	dir := t.TempDir()
	dest := t.TempDir()
	plain := func(mounts ...ctrcovMount) []byte {
		return ctrcovConfig(t, "/app", "nginx:1", false, nil, mounts...)
	}
	cases := []struct {
		name        string
		steps       []ContainerBackupMetadata
		destination string
		want        string
	}{
		{name: "no steps", want: "missing required container backup data"},
		{name: "corrupt volume manifest", steps: []ContainerBackupMetadata{{Inspect: plain(), Volumes: []byte("{"), Files: []string{"image.tar"}}}, want: "unexpected end"},
		{name: "no image", steps: []ContainerBackupMetadata{{Inspect: plain(), Files: []string{"config.json"}}}, want: "no container image archive"},
		{name: "recorded archive missing", steps: []ContainerBackupMetadata{{
			Inspect: plain(), Files: []string{"image.tar"},
			Volumes: ctrcovJSON(t, []volumeManifestEntry{{Source: dir, BackedUp: true, Archive: volumeArchiveBase(dir)}}),
		}}, want: "recorded container volume archive is missing"},
		{name: "required archive missing", steps: []ContainerBackupMetadata{{
			Inspect: plain(ctrcovMount{Type: "bind", Source: dir, Destination: "/config"}), Files: []string{"image.tar"},
		}}, want: "required container volume archive is missing"},
		{name: "corrupt inspect", steps: []ContainerBackupMetadata{{Inspect: []byte("{"), Files: []string{"image.tar"}}}, want: "unexpected end"},
		{name: "unsafe container name", steps: []ContainerBackupMetadata{{Inspect: ctrcovConfig(t, "/..", "nginx:1", false, nil), Files: []string{"image.tar"}}}, want: "invalid restore name"},
		{name: "invalid image", steps: []ContainerBackupMetadata{{Inspect: ctrcovConfig(t, "/app", "Not A Valid Image", false, nil), Files: []string{"image.tar"}}}, want: "invalid Docker image name"},
		{name: "destination outside policy", steps: []ContainerBackupMetadata{{Inspect: plain(), Files: []string{"image.tar"}}}, destination: "/etc/vault-restore", want: "restore destination"},
		{name: "unsafe bind under destination", steps: []ContainerBackupMetadata{{
			Inspect: ctrcovConfig(t, "/app", "nginx:1", false, []string{"/mnt/..:/x"}), Files: []string{"image.tar"},
		}}, destination: dest, want: "invalid restore name"},
		{name: "colliding targets", steps: []ContainerBackupMetadata{{
			Inspect: plain(
				ctrcovMount{Type: "bind", Source: "/mnt/a/config", Destination: "/a"},
				ctrcovMount{Type: "bind", Source: "/mnt/b/config", Destination: "/b"},
			), Files: []string{"image.tar"},
		}}, destination: dest, want: "config"},
		{name: "unsafe volume name under destination", steps: []ContainerBackupMetadata{{
			Inspect: plain(ctrcovMount{Type: "volume", Name: "..", Source: "/var/lib/docker/volumes/x/_data", Destination: "/data"}),
			Files:   []string{"image.tar"},
		}}, destination: dest, want: "invalid restore name"},
		{name: "in-place source outside policy", steps: []ContainerBackupMetadata{{
			Inspect: plain(ctrcovMount{Type: "bind", Source: "/root/secret", Destination: "/secret"}),
			Files:   []string{"image.tar", volumeArchiveBase("/root/secret")},
		}}, want: "refusing in-place restore"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := &ContainerHandler{cli: &fakeDocker458{containerInspect: func(context.Context, string) (client.ContainerInspectResult, error) {
				return client.ContainerInspectResult{}, cerrdefs.ErrNotFound
			}}}
			_, err := h.ReviewClassicRestore(trustedCtrcovContext(t), BackupItem{Name: "app"}, c.steps, c.destination, false)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
}

func TestCtrcovReviewClassicRestoreReportsUntrustedHostAccess(t *testing.T) {
	config := ctrcovConfig(t, "/app", "nginx:1", false, []string{"/etc:/host-etc"})
	h := &ContainerHandler{cli: &fakeDocker458{}}
	review, err := h.ReviewClassicRestore(t.Context(), BackupItem{Name: "app"},
		[]ContainerBackupMetadata{{Inspect: config, Files: []string{"image.tar"}}}, "", false)
	if !errors.Is(err, ErrRestoreNeedsApproval) {
		t.Fatalf("err = %v, want approval required", err)
	}
	if review.Effects.Name != "app" || len(review.Effects.Binds) != 1 {
		t.Fatalf("the review must still describe the effects: %+v", review.Effects)
	}
}

func TestCtrcovReviewClassicRestoreToDestinationRewritesTargets(t *testing.T) {
	dest := t.TempDir()
	config := ctrcovConfig(t, "/app", "nginx:1", false, []string{"/mnt/user/appdata/app:/config", "named:/data"},
		ctrcovMount{Type: "bind", Source: "/mnt/user/appdata/app", Destination: "/config"},
		ctrcovMount{Type: "volume", Name: "named", Source: "/var/lib/docker/volumes/named/_data", Destination: "/data"},
	)
	h := &ContainerHandler{cli: &fakeDocker458{}}
	review, err := h.ReviewClassicRestore(trustedCtrcovContext(t), BackupItem{Name: "app"},
		[]ContainerBackupMetadata{{Inspect: config, Files: []string{"image.tar", volumeArchiveBase("/mnt/user/appdata/app")},
			Volumes: ctrcovJSON(t, []volumeManifestEntry{
				{Index: 0, Source: "/mnt/user/appdata/app", BackedUp: true, Archive: volumeArchiveBase("/mnt/user/appdata/app")},
				{Index: 1, Source: "/var/lib/docker/volumes/named/_data", SkipReason: "excluded by label"},
			})}}, dest, false)
	if err != nil {
		t.Fatalf("ReviewClassicRestore: %v", err)
	}
	resolved, err := normalizeRestorePath(dest)
	if err != nil {
		t.Fatal(err)
	}
	if review.Effects.Destination != resolved {
		t.Fatalf("destination = %q, want %q", review.Effects.Destination, resolved)
	}
	if review.Effects.Binds[0] != filepath.Join(resolved, "app")+":/config" || review.Effects.Binds[1] != filepath.Join(resolved, "named")+":/data" {
		t.Fatalf("binds = %v", review.Effects.Binds)
	}
	targets := map[string]ContainerVolumeEffect{}
	for _, v := range review.Effects.Volumes {
		targets[v.Mount] = v
	}
	if targets["/config"].Target != filepath.Join(resolved, "app") || targets["/config"].WriteMode != "merge" {
		t.Errorf("/config = %+v", targets["/config"])
	}
	if targets["/data"].Target != filepath.Join(resolved, "named") || targets["/data"].WriteMode != "configuration_only" {
		t.Errorf("/data = %+v", targets["/data"])
	}
}

// ctrcovRepo is an in-memory ContainerRestoreRepository.
type ctrcovRepo struct {
	chunks    map[dedup.ID][]byte
	manifests map[dedup.ID]dedup.Manifest
	getErr    error
}

func newCtrcovRepo() *ctrcovRepo {
	return &ctrcovRepo{chunks: map[dedup.ID][]byte{}, manifests: map[dedup.ID]dedup.Manifest{}}
}

func (r *ctrcovRepo) Get(id dedup.ID) ([]byte, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	data, ok := r.chunks[id]
	if !ok {
		return nil, errors.New("ctrcov: missing chunk")
	}
	return data, nil
}

func (r *ctrcovRepo) GetManifest(id dedup.ID) (dedup.Manifest, error) {
	m, ok := r.manifests[id]
	if !ok {
		return dedup.Manifest{}, errors.New("ctrcov: missing manifest")
	}
	return m, nil
}

func (r *ctrcovRepo) blob(id byte, data []byte) dedup.ManifestEntry {
	r.chunks[dedup.ID{id}] = data
	return dedup.ManifestEntry{Size: int64(len(data)), Chunks: []dedup.ID{{id}}}
}

func TestCtrcovReviewChunkedRestoreDescribesEffects(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "hook.sh")
	data := filepath.Join(dir, "data")
	empty := filepath.Join(dir, "empty.conf")
	skipped := filepath.Join(dir, "skipped")
	repo := newCtrcovRepo()
	m := dedup.Manifest{Files: map[string]dedup.ManifestEntry{
		containerInspectKey: repo.blob(1, ctrcovConfig(t, "/app", "nginx:1", true, nil,
			ctrcovMount{Type: "bind", Source: file, Destination: "/hook.sh"},
			ctrcovMount{Type: "bind", Source: data, Destination: "/data"},
			ctrcovMount{Type: "bind", Source: empty, Destination: "/empty.conf"},
			ctrcovMount{Type: "bind", Source: skipped, Destination: "/skipped"},
		)),
		containerTemplateKey:                   repo.blob(2, []byte("<Container/>")),
		containerImageMetaKey:                  repo.blob(3, ctrcovJSON(t, imageMeta{RepoDigests: []string{"nginx@" + pinDigest}})),
		containerVolFilePrefix + "/hook.sh":    {Size: 3, Chunks: []dedup.ID{{9}}},
		containerVolFilePrefix + "/empty.conf": {Size: 0},
		containerVolPrefix + "/data":           {Size: 10, Chunks: []dedup.ID{{8}}},
		containerVolPrefix + "/skipped":        {Size: volumeSkippedSize},
		ContainerDBDumpKey:                     {Size: 1, Chunks: []dedup.ID{{7}}},
		ContainerDBReplayKey:                   {Size: 0},
	}}
	h := &ContainerHandler{cli: &fakeDocker458{}}
	review, err := h.ReviewChunkedRestore(trustedCtrcovContext(t), BackupItem{Name: "app"}, m, repo, "", false)
	if err != nil {
		t.Fatalf("ReviewChunkedRestore: %v", err)
	}
	if review.Effects.ImageDigest == "" || !review.Effects.Template || !review.Effects.DatabaseReplay {
		t.Fatalf("effects = %+v", review.Effects)
	}
	modes := map[string]string{}
	for _, v := range review.Effects.Volumes {
		modes[v.Mount] = v.WriteMode
	}
	want := map[string]string{"/hook.sh": "overwrite_file", "/empty.conf": "overwrite_file", "/data": "merge", "/skipped": "configuration_only"}
	for mount, mode := range want {
		if modes[mount] != mode {
			t.Errorf("%s = %q, want %q", mount, modes[mount], mode)
		}
	}
}

func TestCtrcovReviewChunkedRestoreRefusals(t *testing.T) {
	config := ctrcovConfig(t, "/app", "nginx:1", false, nil)
	cases := []struct {
		name  string
		build func(*ctrcovRepo) dedup.Manifest
		want  string
		is    error
	}{
		{name: "no inspect", build: func(*ctrcovRepo) dedup.Manifest { return dedup.Manifest{} }, want: "cannot read saved container inspect data"},
		{name: "inspect with several chunks", build: func(r *ctrcovRepo) dedup.Manifest {
			e := r.blob(1, config)
			e.Chunks = append(e.Chunks, dedup.ID{2})
			return dedup.Manifest{Files: map[string]dedup.ManifestEntry{containerInspectKey: e}}
		}, want: "cannot read saved container inspect data"},
		{name: "oversized template", build: func(r *ctrcovRepo) dedup.Manifest {
			return dedup.Manifest{Files: map[string]dedup.ManifestEntry{
				containerInspectKey:  r.blob(1, config),
				containerTemplateKey: {Size: ContainerReviewMetadataLimit + 1, Chunks: []dedup.ID{{2}}},
			}}
		}, want: "invalid container restore metadata entry"},
		{name: "template length differs", build: func(r *ctrcovRepo) dedup.Manifest {
			e := r.blob(2, []byte("<Container/>"))
			e.Size++
			return dedup.Manifest{Files: map[string]dedup.ManifestEntry{containerInspectKey: r.blob(1, config), containerTemplateKey: e}}
		}, want: "invalid container restore metadata length"},
		{name: "image meta unreadable", build: func(r *ctrcovRepo) dedup.Manifest {
			return dedup.Manifest{Files: map[string]dedup.ManifestEntry{
				containerInspectKey:   r.blob(1, config),
				containerImageMetaKey: {Size: 4, Chunks: []dedup.ID{{3}}},
			}}
		}, want: "missing chunk"},
		{name: "image meta corrupt", build: func(r *ctrcovRepo) dedup.Manifest {
			return dedup.Manifest{Files: map[string]dedup.ManifestEntry{
				containerInspectKey:   r.blob(1, config),
				containerImageMetaKey: r.blob(3, []byte("{")),
			}}
		}, want: "unexpected end"},
		{name: "no backup-time digest", build: func(r *ctrcovRepo) dedup.Manifest {
			return dedup.Manifest{Files: map[string]dedup.ManifestEntry{containerInspectKey: r.blob(1, config)}}
		}, is: ErrRestoreNeedsApproval},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo := newCtrcovRepo()
			m := c.build(repo)
			_, err := (&ContainerHandler{cli: &fakeDocker458{}}).ReviewChunkedRestore(trustedCtrcovContext(t), BackupItem{Name: "app"}, m, repo, "", false)
			if c.is != nil {
				if !errors.Is(err, c.is) {
					t.Fatalf("err = %v, want %v", err, c.is)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
}

func TestCtrcovReadStagedContainerMetadata(t *testing.T) {
	t.Run("reads sidecars and lists regular files", func(t *testing.T) {
		dir := t.TempDir()
		writeFile458(t, filepath.Join(dir, "config.json"), "{}")
		writeFile458(t, filepath.Join(dir, "image.tar"), "img")
		if err := os.Mkdir(filepath.Join(dir, "nested"), 0o700); err != nil {
			t.Fatal(err)
		}
		got, err := readStagedContainerMetadata(dir)
		if err != nil {
			t.Fatal(err)
		}
		if string(got.Inspect) != "{}" || got.Volumes != nil || got.Template != nil || len(got.Files) != 2 {
			t.Fatalf("metadata = %+v", got)
		}
	})
	t.Run("missing staging", func(t *testing.T) {
		if _, err := readStagedContainerMetadata(filepath.Join(t.TempDir(), "absent")); err == nil {
			t.Fatal("expected a missing staging directory to fail")
		}
	})
	t.Run("sidecar is a directory", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, "volumes.json"), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := readStagedContainerMetadata(dir); err == nil {
			t.Fatal("expected an unreadable sidecar to fail")
		}
	})
	t.Run("oversized sidecar", func(t *testing.T) {
		dir := t.TempDir()
		big := make([]byte, ContainerReviewMetadataLimit+1)
		if err := os.WriteFile(filepath.Join(dir, "config.json"), big, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readStagedContainerMetadata(dir); err == nil {
			t.Fatal("expected an oversized sidecar to fail")
		}
	})
	t.Run("unreadable sidecar", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores file permissions")
		}
		dir := t.TempDir()
		writeFile458(t, filepath.Join(dir, containerTemplateFile), "<x/>")
		if err := os.Chmod(filepath.Join(dir, containerTemplateFile), 0); err != nil {
			t.Fatal(err)
		}
		if _, err := readStagedContainerMetadata(dir); !errors.Is(err, os.ErrPermission) {
			t.Fatalf("err = %v, want permission denied", err)
		}
	})
}

func TestCtrcovCheckContainerImageLoad(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{name: "success with blank lines", body: "\n{\"stream\":\"Loaded image\"}\n\n"},
		{name: "not json", body: "garbage\n", want: "invalid image load response"},
		{name: "error field", body: `{"error":"no space left"}`, want: "saved image rejected"},
		{name: "error detail", body: `{"errorDetail":{"message":"bad layer"}}`, want: "saved image rejected"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := checkContainerImageLoad(strings.NewReader(c.body))
			if c.want == "" {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
	if err := checkContainerImageLoad(strings.NewReader(strings.Repeat("x", 2<<20))); err == nil {
		t.Fatal("an oversized response line must fail")
	}
}

func TestCtrcovCheckReviewedClassicImage(t *testing.T) {
	inspect := restoreInspect{ImageID: "sha256:saved"}
	inspect.Config.Image = "nginx:1"
	image := func(id string, err error) *ContainerHandler {
		return &ContainerHandler{cli: &fakeDocker458{imageInspect: func(context.Context, string) (client.ImageInspectResult, error) {
			return client.ImageInspectResult{InspectResponse: imagetypes.InspectResponse{ID: id}}, err
		}}}
	}
	if err := image("", errFake458).checkReviewedClassicImage(t.Context(), restoreInspect{}); err != nil {
		t.Fatalf("no saved image ID must skip the check: %v", err)
	}
	if err := image("", errFake458).checkReviewedClassicImage(t.Context(), inspect); !errors.Is(err, errFake458) {
		t.Fatalf("inspect failure = %v", err)
	}
	if err := image("sha256:other", nil).checkReviewedClassicImage(t.Context(), inspect); err == nil || !strings.Contains(err.Error(), "differs") {
		t.Fatalf("different image = %v", err)
	}
	if err := image("sha256:saved", nil).checkReviewedClassicImage(t.Context(), inspect); err != nil {
		t.Fatalf("matching image = %v", err)
	}
}

func reviewedCtrcovContext(t *testing.T, review ContainerRestoreReview, checks *[]string) context.Context {
	t.Helper()
	return WithReviewedContainerRestore(trustedCtrcovContext(t), review, func(check string) {
		if checks != nil {
			*checks = append(*checks, check)
		}
	})
}

func ctrcovExisting(id string, state *containertypes.State, err error) func(context.Context, string) (client.ContainerInspectResult, error) {
	return func(context.Context, string) (client.ContainerInspectResult, error) {
		return client.ContainerInspectResult{Container: containertypes.InspectResponse{ID: id, State: state}}, err
	}
}

func TestCtrcovStopReviewedContainer(t *testing.T) {
	review := ContainerRestoreReview{Effects: ContainerRestoreEffects{Name: "app"}}
	if err := (&ContainerHandler{cli: &fakeDocker458{}}).stopReviewedContainer(t.Context()); err != nil {
		t.Fatalf("unreviewed restore must not inspect: %v", err)
	}
	cases := []struct {
		name    string
		inspect func(context.Context, string) (client.ContainerInspectResult, error)
		stopErr error
		want    string
		stopped bool
		id      string
	}{
		{name: "absent", inspect: ctrcovExisting("", nil, cerrdefs.ErrNotFound)},
		{name: "inspect fails", inspect: ctrcovExisting("", nil, errFake458), want: "inspecting the container"},
		{name: "no state", inspect: ctrcovExisting("old", nil, nil), want: "no identity or state"},
		{name: "stopped already", inspect: ctrcovExisting("old", &containertypes.State{}, nil), id: "old"},
		{name: "running", inspect: ctrcovExisting("old", &containertypes.State{Running: true}, nil), stopped: true, id: "old"},
		{name: "stop fails", inspect: ctrcovExisting("old", &containertypes.State{Running: true}, nil), stopErr: errFake458, stopped: true, want: "stopping the container", id: "old"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var stopped []string
			f := &fakeDocker458{containerInspect: c.inspect, containerStop: func(_ context.Context, id string) (client.ContainerStopResult, error) {
				stopped = append(stopped, id)
				return client.ContainerStopResult{}, c.stopErr
			}}
			ctx := reviewedCtrcovContext(t, review, nil)
			err := (&ContainerHandler{cli: f}).stopReviewedContainer(ctx)
			if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
			if (len(stopped) == 1) != c.stopped {
				t.Errorf("stopped = %v", stopped)
			}
			reviewed, _ := reviewedContainerFrom(ctx)
			if reviewed.existingID != c.id {
				t.Errorf("existing ID = %q, want %q", reviewed.existingID, c.id)
			}
		})
	}
}

func TestCtrcovRemoveReviewedContainer(t *testing.T) {
	cases := []struct {
		name      string
		existing  string
		inspect   func(context.Context, string) (client.ContainerInspectResult, error)
		removeErr error
		want      string
		is        error
		removed   bool
	}{
		{name: "never existed", inspect: ctrcovExisting("", nil, cerrdefs.ErrNotFound)},
		{name: "vanished since stopping", existing: "old", inspect: ctrcovExisting("", nil, cerrdefs.ErrNotFound), want: "rechecking the existing container"},
		{name: "appeared since review", inspect: ctrcovExisting("new", &containertypes.State{}, nil), is: ErrContainerReviewChanged},
		{name: "replaced since stopping", existing: "old", inspect: ctrcovExisting("other", &containertypes.State{}, nil), is: ErrContainerReviewChanged},
		{name: "restarted since stopping", existing: "old", inspect: ctrcovExisting("old", &containertypes.State{Running: true}, nil), is: ErrContainerReviewChanged},
		{name: "no state", existing: "old", inspect: ctrcovExisting("old", nil, nil), is: ErrContainerReviewChanged},
		{name: "remove fails", existing: "old", inspect: ctrcovExisting("old", &containertypes.State{}, nil), removeErr: errFake458, want: "removing the existing container", removed: true},
		{name: "removed", existing: "old", inspect: ctrcovExisting("old", &containertypes.State{}, nil), removed: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var removed []string
			f := &fakeDocker458{containerInspect: c.inspect, containerRemove: func(_ context.Context, id string) (client.ContainerRemoveResult, error) {
				removed = append(removed, id)
				return client.ContainerRemoveResult{}, c.removeErr
			}}
			reviewed := &reviewedContainerRestore{review: ContainerRestoreReview{Effects: ContainerRestoreEffects{Name: "app"}}, existingID: c.existing}
			err := (&ContainerHandler{cli: f}).removeReviewedContainer(t.Context(), reviewed)
			switch {
			case c.is != nil:
				if !errors.Is(err, c.is) {
					t.Fatalf("err = %v, want %v", err, c.is)
				}
			case c.want != "":
				if err == nil || !strings.Contains(err.Error(), c.want) {
					t.Fatalf("err = %v, want %q", err, c.want)
				}
			case err != nil:
				t.Fatalf("err = %v", err)
			}
			if (len(removed) == 1) != c.removed || c.removed && removed[0] != c.existing {
				t.Errorf("removed = %v", removed)
			}
		})
	}
}

func TestCtrcovReviewedPathAndTargetChecks(t *testing.T) {
	dest := t.TempDir()
	resolved, err := normalizeRestorePath(dest)
	if err != nil {
		t.Fatal(err)
	}
	inspect := inspectFromJSON(t, `{"Mounts":[{"Type":"bind","Source":"/mnt/user/appdata/app","Destination":"/config"},
		{"Type":"bind","Source":"/mnt/user/appdata/cache","Destination":"/cache"}]}`)
	review := ContainerRestoreReview{Effects: ContainerRestoreEffects{Destination: resolved, Volumes: []ContainerVolumeEffect{
		{Mount: "/config", Target: filepath.Join(resolved, "app"), WriteMode: "merge"},
		{Mount: "/cache", Target: filepath.Join(resolved, "cache"), WriteMode: "configuration_only"},
	}}}
	ctx := reviewedCtrcovContext(t, review, nil)

	if err := checkReviewedContainerPaths(t.Context(), inspect, "anything"); err != nil {
		t.Fatalf("an unreviewed restore has nothing to compare: %v", err)
	}
	if err := checkReviewedContainerPaths(ctx, inspect, resolved); err != nil {
		t.Fatalf("matching paths: %v", err)
	}
	if err := checkReviewedContainerPaths(ctx, inspect, ""); !errors.Is(err, ErrContainerReviewChanged) {
		t.Fatalf("a different destination = %v", err)
	}
	if err := checkReviewedContainerPaths(ctx, inspectFromJSON(t, `{"Mounts":[{"Type":"bind","Source":"/mnt/user/appdata/other","Destination":"/config"}]}`), resolved); !errors.Is(err, ErrContainerReviewChanged) {
		t.Fatalf("a moved volume target = %v", err)
	}
	if err := checkReviewedContainerPaths(ctx, inspectFromJSON(t, `{"Mounts":[{"Type":"volume","Name":"..","Source":"/x","Destination":"/config"}]}`), resolved); !errors.Is(err, ErrContainerReviewChanged) {
		t.Fatalf("an unusable volume name = %v", err)
	}
	// A destination that is not in its resolved form is not the reviewed one.
	unresolved := filepath.Join(dest, "x", "..")
	unresolvedCtx := reviewedCtrcovContext(t, ContainerRestoreReview{Effects: ContainerRestoreEffects{Destination: unresolved}}, nil)
	if err := checkReviewedContainerPaths(unresolvedCtx, restoreInspect{}, unresolved); !errors.Is(err, ErrContainerReviewChanged) {
		t.Fatalf("an unresolved destination = %v", err)
	}

	if err := checkReviewedVolumeTarget(t.Context(), "/config", "/anywhere"); err != nil {
		t.Fatalf("unreviewed target = %v", err)
	}
	if err := checkReviewedVolumeTarget(ctx, "/config", filepath.Join(resolved, "app")); err != nil {
		t.Fatalf("reviewed target = %v", err)
	}
	if err := checkReviewedVolumeTarget(ctx, "/cache", filepath.Join(resolved, "cache")); !errors.Is(err, ErrContainerReviewChanged) {
		t.Fatalf("a configuration-only mount must not be written: %v", err)
	}
	if err := checkReviewedVolumeTarget(ctx, "/config", filepath.Join(resolved, "elsewhere")); !errors.Is(err, ErrContainerReviewChanged) {
		t.Fatalf("an unreviewed target = %v", err)
	}
}

func TestCtrcovValidateReviewedContainerArchives(t *testing.T) {
	volume := t.TempDir()
	inspect := inspectFromJSON(t, `{"Mounts":[{"Type":"bind","Source":"`+volume+`","Destination":"/config"},
		{"Type":"bind","Source":"/mnt/user/unused","Destination":"/unused"}]}`)
	review := ContainerRestoreReview{Effects: ContainerRestoreEffects{Volumes: []ContainerVolumeEffect{
		{Mount: "/config", Target: volume, WriteMode: "merge"},
		{Mount: "/unused", Target: "/mnt/user/unused", WriteMode: "configuration_only"},
	}}}
	t.Run("corrupt manifest", func(t *testing.T) {
		dir := t.TempDir()
		writeFile458(t, filepath.Join(dir, "volumes.json"), "{")
		if err := validateReviewedContainerArchives(t.Context(), dir, inspect, review); err == nil || !strings.Contains(err.Error(), "volumes.json") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("archive missing", func(t *testing.T) {
		dir := t.TempDir()
		if err := validateReviewedContainerArchives(t.Context(), dir, inspect, review); err == nil {
			t.Fatal("expected a missing archive to fail")
		}
	})
	t.Run("archive escapes the target", func(t *testing.T) {
		dir := t.TempDir()
		ctrcovTar(t, filepath.Join(dir, volumeArchiveBase(volume)), "../escape.txt")
		if err := validateReviewedContainerArchives(t.Context(), dir, inspect, review); err == nil || !strings.Contains(err.Error(), "escape") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("valid archive", func(t *testing.T) {
		dir := t.TempDir()
		ctrcovTar(t, filepath.Join(dir, volumeArchiveBase(volume)), "ok.txt")
		if err := validateReviewedContainerArchives(t.Context(), dir, inspect, review); err != nil {
			t.Fatalf("err = %v", err)
		}
	})
}

// ctrcovTar writes a one-entry tar archive at path.
func ctrcovTar(t *testing.T, path, entry string) {
	t.Helper()
	f, err := os.Create(path) // #nosec G304 -- test path
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := tar.NewWriter(f)
	if err := w.WriteHeader(&tar.Header{Name: entry, Typeflag: tar.TypeReg, Mode: 0o600, Size: 4}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("data")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}
