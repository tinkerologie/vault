package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	mounttypes "github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

func TestClassicRestoreRecreatesStructuredBindMount(t *testing.T) {
	for _, alternate := range []bool{false, true} {
		name := "original"
		if alternate {
			name = "alternate"
		}
		t.Run(name, func(t *testing.T) {
			checkClassicStructuredBindRestore(t, alternate)
		})
	}
}

func checkClassicStructuredBindRestore(t *testing.T, alternate bool) {
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
	if err := handler.Restore(reviewedCtrcovContext(t, review, nil), item, fixture.sourceDir, noopProgress); err != nil {
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
