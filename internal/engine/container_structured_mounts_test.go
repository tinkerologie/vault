package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	mounttypes "github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/tinkerologie/vault/internal/dedup"
)

func TestClassicRestoreRecreatesStructuredBindMount(t *testing.T) {
	for _, tc := range []struct {
		name      string
		alternate bool
		retarget  bool
	}{
		{name: "original"},
		{name: "alternate", alternate: true},
		{name: "alternate changed before Docker create", alternate: true, retarget: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkClassicStructuredBindRestore(t, tc.alternate, tc.retarget)
		})
	}
}

func checkClassicStructuredBindRestore(t *testing.T, alternate, retarget bool) {
	t.Helper()
	fixture := newRestoreFixture458(t, true)
	templates := withTestTemplatesDir(t)
	writeFile458(t, filepath.Join(fixture.sourceDir, containerTemplateFile), fmt.Sprintf(
		`<Container><Name>app</Name><Config Type="Path" Target="/config">%s</Config></Container>`, fixture.volDir))
	path := filepath.Join(fixture.sourceDir, "config.json")
	var saved map[string]any
	if err := json.Unmarshal([]byte(readFile458(t, path)), &saved); err != nil {
		t.Fatal(err)
	}
	host := saved["HostConfig"].(map[string]any)
	host["Binds"] = []string{fixture.fileSrc + ":/hook.sh:ro", "appvol:/data"}
	host["Mounts"] = []map[string]any{{
		"Type": "bind", "Source": fixture.volDir, "Target": "/config", "ReadOnly": true,
		"BindOptions": map[string]bool{"NonRecursive": true},
	}, {
		"Type": "tmpfs", "Target": "/tmp",
	}}
	data, err := json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	writeFile458(t, path, string(data))
	fixture.wipe(t)
	docker, recorded := newRestoreDaemon458()
	create := docker.containerCreate
	(&ctrcovLiveContainer{}).install(docker)
	docker.containerCreate = create
	handler := &ContainerHandler{cli: docker}
	item := BackupItem{Name: "app", Type: "container"}
	source := fixture.volDir
	var destination string
	if alternate {
		destination = t.TempDir()
		item.Settings = map[string]any{"restore_destination": destination}
		source = filepath.Join(destination, filepath.Base(fixture.volDir))
	}
	metadata, err := readStagedContainerMetadata(fixture.sourceDir)
	if err != nil {
		t.Fatal(err)
	}
	review, err := handler.ReviewClassicRestore(sameServerRestore(t), item, []ContainerBackupMetadata{metadata}, destination, false)
	if err != nil {
		t.Fatal(err)
	}
	progress := noopProgress
	if retarget {
		progress = func(_ string, _ int, message string) {
			if message != "recreating container" {
				return
			}
			newLocation := filepath.Join(destination, "moved-volume")
			if err := os.Rename(source, newLocation); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(newLocation, source); err != nil {
				t.Fatal(err)
			}
		}
	}
	err = handler.Restore(reviewedCtrcovContext(t, review, nil), item, fixture.sourceDir, progress)
	if retarget {
		if !errors.Is(err, ErrContainerReviewChanged) || len(recorded.created) != 0 || len(recorded.removed) != 0 {
			t.Fatalf("retargeted restore created or removed a container: %v", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(recorded.created) != 1 {
		t.Fatalf("expected one Docker create, got %d", len(recorded.created))
	}
	config := recorded.created[0].HostConfig
	mounts := config.Mounts
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		t.Fatal(err)
	}
	if len(mounts) != 1 || mounts[0].Type != mounttypes.TypeBind ||
		mounts[0].Source != source || mounts[0].Target != "/config" || !mounts[0].ReadOnly ||
		mounts[0].BindOptions == nil || !mounts[0].BindOptions.NonRecursive {
		t.Errorf("Docker create lost the saved structured bind: %+v", mounts)
	}
	if !strings.Contains(strings.Join(config.Binds, ","), "appvol:/data") {
		t.Error("Docker create lost a legacy named volume")
	}
	if got := readFile458(t, filepath.Join(source, "a.txt")); got != "alpha" {
		t.Errorf("restored appdata = %q", got)
	}
	if !slices.ContainsFunc(review.Effects.Volumes, func(volume ContainerVolumeEffect) bool {
		return volume.Mount == "/config" && volume.Target == source
	}) {
		t.Errorf("review did not include the mount sent to Docker")
	}
	if got := readFile458(t, filepath.Join(templates, "my-app.xml")); alternate && !strings.Contains(got, source) {
		t.Errorf("template did not point to the alternate bind: %s", got)
	}
}

func TestApprovedChunkedRestoreRecreatesStructuredBindMount(t *testing.T) {
	dir := t.TempDir()
	repo, id := ctrcovChunked(t, dir, []string{"nginx@" + pinDigest})
	payload := repo.manifests[id]
	source := filepath.Join(dir, "data")
	var config map[string]any
	if err := json.Unmarshal(repo.chunks[payload.Files[containerInspectKey].Chunks[0]], &config); err != nil {
		t.Fatal(err)
	}
	config["HostConfig"].(map[string]any)["Mounts"] = []map[string]any{{
		"Type": "bind", "Source": source, "Target": "/data", "ReadOnly": true,
	}}
	payload.Files[containerInspectKey] = repo.blob(1, ctrcovJSON(t, config))
	daemon := newCtrcovChunkedDaemon(&ctrcovLiveContainer{})
	var created client.ContainerCreateOptions
	daemon.containerCreate = func(_ context.Context, options client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
		created = options
		return client.ContainerCreateResult{ID: "new-id"}, nil
	}
	handler := &ContainerHandler{cli: daemon}
	item := ctrcovItem()
	review, err := handler.ReviewChunkedRestore(sameServerRestore(t), item, payload, repo, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.RestorePreparedChunked(reviewedCtrcovContext(t, review, nil), item, repo, id, "", noopProgress); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(source)
	if err != nil {
		t.Fatal(err)
	}
	mounts := created.HostConfig.Mounts
	if len(mounts) != 1 || mounts[0].Target != "/data" || mounts[0].Source != resolved || !mounts[0].ReadOnly {
		t.Fatalf("Docker create lost the reviewed structured bind: %+v", mounts)
	}
	if got := readFile458(t, filepath.Join(source, "a.txt")); got != "payload" {
		t.Errorf("restored appdata = %q", got)
	}
}

func TestStructuredBindReviewRejectsInconsistentMetadata(t *testing.T) {
	const saved = `{
		"Name": "/fixture",
		"Config": {"Image": "example.invalid/fixture:1"},
		"HostConfig": {"Mounts": [{"Type": "bind", "Source": "/mnt/cache/fixture-a", "Target": "/config"}]},
		"Mounts": [{"Type": "bind", "Source": "/mnt/cache/fixture-b", "Destination": "/config"}]
	}`
	handler := &ContainerHandler{}
	_, err := handler.ReviewClassicRestore(sameServerRestore(t), BackupItem{Name: "fixture"}, []ContainerBackupMetadata{{Inspect: []byte(saved), Files: []string{"image.tar"}}}, "", true)
	if err == nil || !strings.Contains(err.Error(), "does not match the saved container mounts") {
		t.Fatalf("expected inconsistent-bind refusal, got %v", err)
	}
}

func TestStructuredBindReviewRejectsDuplicateEffectiveTarget(t *testing.T) {
	const saved = `{
		"Name": "/fixture",
		"Config": {"Image": "example.invalid/fixture:1"},
		"HostConfig": {"Mounts": [{"Type": "bind", "Source": "/mnt/cache/fixture", "Target": "/config"}]},
		"Mounts": [
			{"Type": "bind", "Source": "/mnt/cache/fixture", "Destination": "/config"},
			{"Type": "bind", "Source": "/mnt/cache/fixture", "Destination": "/config"}
		]
	}`
	_, err := (&ContainerHandler{}).ReviewClassicRestore(sameServerRestore(t), BackupItem{Name: "fixture"},
		[]ContainerBackupMetadata{{Inspect: []byte(saved), Files: []string{"image.tar"}}}, "", true)
	if err == nil || !strings.Contains(err.Error(), "does not match the saved container mounts") {
		t.Fatalf("expected duplicate-effective-mount refusal, got %v", err)
	}
}

func TestStructuredBindReviewRefusesSourceOutsideRestoreLocations(t *testing.T) {
	const saved = `{
		"Name": "/fixture",
		"Config": {"Image": "example.invalid/fixture:1"},
		"HostConfig": {"Mounts": [{"Type": "bind", "Source": "/etc", "Target": "/config"}]},
		"Mounts": [{"Type": "bind", "Source": "/etc", "Destination": "/config"}]
	}`
	_, err := (&ContainerHandler{}).ReviewClassicRestore(sameServerRestore(t), BackupItem{Name: "fixture"},
		[]ContainerBackupMetadata{{Inspect: []byte(saved), Files: []string{"image.tar"}}}, "", true)
	if err == nil || !strings.Contains(err.Error(), "structured bind source") {
		t.Fatalf("expected structured-bind path-policy refusal, got %v", err)
	}
}

func TestStructuredBindReviewRetainsHostAccessPolicy(t *testing.T) {
	const saved = `{
		"Name": "/fixture",
		"Config": {"Image": "example.invalid/fixture:1"},
		"HostConfig": {"Mounts": [{"Type": "bind", "Source": "/mnt/cache/fixture", "Target": "/config", "BindOptions": {"Propagation": "rshared"}}]},
		"Mounts": [{"Type": "bind", "Source": "/mnt/cache/fixture", "Destination": "/config"}]
	}`
	handler := &ContainerHandler{}
	_, err := handler.ReviewClassicRestore(t.Context(), BackupItem{Name: "fixture"}, []ContainerBackupMetadata{{Inspect: []byte(saved), Files: []string{"image.tar", "volume_0.tar"}}}, "", true)
	if !errors.Is(err, ErrRestoreNeedsApproval) {
		t.Fatalf("expected shared propagation to require host approval, got %v", err)
	}
}

func TestStructuredBindRestoreRefusesInconsistentMountBeforeDocker(t *testing.T) {
	const saved = `{
		"Name": "/fixture",
		"Config": {"Image": "example.invalid/fixture:1"},
		"HostConfig": {"Mounts": [{"Type": "bind", "Source": "/mnt/cache/fixture-a", "Target": "/config"}]},
		"Mounts": [{"Type": "bind", "Source": "/mnt/cache/fixture-b", "Destination": "/config"}]
	}`
	item := BackupItem{Name: "fixture"}
	for _, format := range []string{"classic", "chunked"} {
		t.Run(format, func(t *testing.T) {
			docker := &fakeDocker458{
				imageLoad: func(context.Context, io.Reader) (client.ImageLoadResult, error) {
					t.Error("loaded an image for an inconsistent configuration")
					return nil, errFake458
				},
				imagePull: func(context.Context, string) (client.ImagePullResponse, error) {
					t.Error("pulled an image for an inconsistent configuration")
					return nil, errFake458
				},
			}
			handler := &ContainerHandler{cli: docker}
			ctx := sameServerRestore(t)
			var err error
			if format == "classic" {
				stage := t.TempDir()
				writeFile458(t, filepath.Join(stage, "config.json"), saved)
				writeFile458(t, filepath.Join(stage, "image.tar"), "image fixture")
				err = handler.Restore(ctx, item, stage, noopProgress)
			} else {
				repo := newCtrcovRepo()
				id := dedup.ID{40}
				repo.manifests[id] = dedup.Manifest{Files: map[string]dedup.ManifestEntry{
					containerInspectKey: repo.blob(1, []byte(saved)),
				}}
				err = handler.RestorePreparedChunked(ctx, item, repo, id, "", noopProgress)
			}
			if err == nil || !strings.Contains(err.Error(), "does not match the saved container mounts") {
				t.Fatalf("expected inconsistent-bind refusal, got %v", err)
			}
		})
	}
}

func TestStructuredBindReviewRefusesAlternateAliasOutsideDestination(t *testing.T) {
	const saved = `{
		"Name": "/fixture",
		"Config": {"Image": "example.invalid/fixture:1"},
		"HostConfig": {"Mounts": [{"Type": "bind", "Source": "/mnt/cache/data", "Target": "/config"}]},
		"Mounts": [{"Type": "bind", "Source": "/mnt/cache/data", "Destination": "/config"}]
	}`
	destination, sibling := t.TempDir(), t.TempDir()
	if err := os.Symlink(sibling, filepath.Join(destination, "data")); err != nil {
		t.Fatal(err)
	}
	handler := &ContainerHandler{}
	_, err := handler.ReviewClassicRestore(sameServerRestore(t), BackupItem{Name: "fixture"},
		[]ContainerBackupMetadata{{Inspect: []byte(saved), Files: []string{"image.tar", "volume_0.tar"}}}, destination, false)
	if err == nil || !strings.Contains(err.Error(), "structured bind source resolves outside the alternate destination") {
		t.Fatalf("expected refusal for the alternate alias, got %v", err)
	}
}

func TestStructuredBindReviewChecksExcludedSource(t *testing.T) {
	source := filepath.Join(t.TempDir(), "excluded")
	for _, tc := range []struct {
		name        string
		create      bool
		wantBlocked bool
	}{
		{name: "missing bind", wantBlocked: true},
		{name: "Docker can create the source", create: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			saved := structuredBindFixture(source, tc.create)
			manifest := ctrcovJSON(t, []volumeManifestEntry{{Index: 0, Source: source, SkipReason: "excluded by selection"}})
			_, err := (&ContainerHandler{}).ReviewClassicRestore(sameServerRestore(t), BackupItem{Name: "fixture"},
				[]ContainerBackupMetadata{{Inspect: []byte(saved), Volumes: manifest, Files: []string{"image.tar"}}}, "", false)
			if tc.wantBlocked {
				if err == nil || !strings.Contains(err.Error(), "structured bind source unavailable") {
					t.Fatalf("expected missing-source refusal, got %v", err)
				}
			} else if err != nil {
				t.Fatalf("review of creatable source: %v", err)
			}
		})
	}
}

func TestRecreationChecksResolvedStructuredBindsBeforeRemoval(t *testing.T) {
	t.Run("missing required source", func(t *testing.T) {
		inspect := inspectFromJSON(t, structuredBindFixture(filepath.Join(t.TempDir(), "missing"), false))
		docker, recorded := newRestoreDaemon458()
		err := (&ContainerHandler{cli: docker}).recreateAndStartContainer(sameServerRestore(t), BackupItem{Name: "fixture"}, inspect, "", "", noopProgress)
		if !errors.Is(err, os.ErrNotExist) || len(recorded.removed) != 0 || len(recorded.created) != 0 {
			t.Fatalf("missing source must fail before replacement: %v", err)
		}
	})
	t.Run("unapproved resolved host source", func(t *testing.T) {
		inspect := inspectFromJSON(t, structuredBindFixture(t.TempDir(), false))
		docker, recorded := newRestoreDaemon458()
		err := (&ContainerHandler{cli: docker}).recreateAndStartContainer(t.Context(), BackupItem{Name: "fixture"}, inspect, "", "", noopProgress)
		if !errors.Is(err, ErrRestoreNeedsApproval) || len(recorded.removed) != 0 || len(recorded.created) != 0 {
			t.Fatalf("resolved host source must require approval before replacement: %v", err)
		}
	})
}

func TestStructuredBindRestoreRefusesUnapprovedAlternateBeforeImageLoad(t *testing.T) {
	item := BackupItem{Name: "fixture", Settings: map[string]any{"restore_destination": t.TempDir()}}
	stage := t.TempDir()
	writeFile458(t, filepath.Join(stage, "config.json"), structuredBindFixture("/mnt/cache/fixture", false))
	writeFile458(t, filepath.Join(stage, "image.tar"), "image fixture")
	docker, recorded := newRestoreDaemon458()
	err := (&ContainerHandler{cli: docker}).Restore(t.Context(), item, stage, noopProgress)
	if !errors.Is(err, ErrRestoreNeedsApproval) || recorded.loaded || len(recorded.removed) != 0 {
		t.Fatalf("alternate host source must require approval before Docker mutation: %v", err)
	}
}

func TestStructuredBindRevalidationRefusesMissingExcludedSource(t *testing.T) {
	source := filepath.Join(t.TempDir(), "excluded")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	saved := structuredBindFixture(source, false)
	manifest := ctrcovJSON(t, []volumeManifestEntry{{Index: 0, Source: source, SkipReason: "excluded by selection"}})
	review, err := (&ContainerHandler{}).ReviewClassicRestore(sameServerRestore(t), BackupItem{Name: "fixture"},
		[]ContainerBackupMetadata{{Inspect: []byte(saved), Volumes: manifest, Files: []string{"image.tar"}}}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if err := checkReviewedContainerPaths(reviewedCtrcovContext(t, review, nil), inspectFromJSON(t, saved), ""); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected missing-source refusal before stopping the container, got %v", err)
	}
}

func structuredBindFixture(source string, create bool) string {
	return fmt.Sprintf(`{
		"Name": "/fixture",
		"Config": {"Image": "example.invalid/fixture:1"},
		"HostConfig": {"Mounts": [{"Type": "bind", "Source": %[1]q, "Target": "/config", "BindOptions": {"CreateMountpoint": %[2]t}}]},
		"Mounts": [{"Type": "bind", "Source": %[1]q, "Destination": "/config"}]
	}`, source, create)
}
