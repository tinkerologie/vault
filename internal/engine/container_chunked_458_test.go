package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	containertypes "github.com/moby/moby/api/types/container"
	mounttypes "github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/tinkerologie/vault/internal/dedup"
)

func TestContainerBackupChunkedErrors458(t *testing.T) {
	t.Parallel()

	h := &ContainerHandler{cli: &fakeDocker458{}}
	if _, err := h.BackupChunked(t.Context(), BackupItem{Name: "x"}, nil, nil, nil); err == nil || !strings.Contains(err.Error(), "dedup repo is nil") {
		t.Errorf("nil repo err = %v", err)
	}

	repo := newChunkTestRepo(t)
	f := &fakeDocker458{}
	f.inspectErr = errInspectMissing
	if _, err := (&ContainerHandler{cli: f}).BackupChunked(t.Context(), BackupItem{Name: "x"}, repo, nil, nil); err == nil || !strings.Contains(err.Error(), "inspecting container") {
		t.Errorf("inspect err = %v", err)
	}

	tests := []struct {
		name       string
		running    bool
		restarting bool
		script     execScript
		wantErr    string
	}{
		{name: "dump on a stopped container", wantErr: "a dump needs the server running"},
		{name: "dump fails", running: true, script: execScript{createErr: errFake458}, wantErr: "creating exec"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := backupFake458(dbContainer458(tt.running, tt.restarting))
			installExec(f, tt.script)
			item := BackupItem{Name: "db", Settings: map[string]any{"id": "dbid", "database_dump": true}}
			_, err := (&ContainerHandler{cli: f}).BackupChunked(t.Context(), item, newChunkTestRepo(t), nil, nil)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

// A chunked backup of a live database container (no_stop) carries the dump
// and the replay marker; restoring it pulls the image, restores the volume,
// recreates the container and replays the dump into it.
func TestContainerChunkedDatabaseRoundTrip458(t *testing.T) {
	t.Parallel()

	vol := t.TempDir()
	writeFile458(t, filepath.Join(vol, "PG_VERSION"), "16")
	anon := t.TempDir()
	c := dbContainer458(true, false,
		containertypes.MountPoint{Type: mounttypes.TypeBind, Source: vol, Destination: "/var/lib/postgresql/data"},
		containertypes.MountPoint{Type: mounttypes.TypeVolume, Name: strings.Repeat("cd", 32), Source: anon, Destination: "/anon"},
	)
	f := backupFake458(c)
	// The stored ID is stale; BackupChunked re-resolves by name.
	f.containerInspect = func(_ context.Context, id string) (client.ContainerInspectResult, error) {
		if id == "stale" {
			return client.ContainerInspectResult{}, errInspectMissing
		}
		return client.ContainerInspectResult{Container: c}, nil
	}
	rec := installExec(f, execScript{output: muxFrame(1, "CREATE TABLE t();\n")})

	repo := newChunkTestRepo(t)
	item := BackupItem{Name: "db", Settings: map[string]any{"id": "stale", "database_dump": true, "no_stop": true}}
	id, err := (&ContainerHandler{cli: f}).BackupChunked(t.Context(), item, repo, nil, noopProgress)
	if err != nil {
		t.Fatalf("BackupChunked: %v", err)
	}
	if err := repo.Flush(); err != nil {
		t.Fatal(err)
	}
	m, err := repo.GetManifest(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{ContainerDBDumpKey, ContainerDBReplayKey, containerVolPrefix + "/var/lib/postgresql/data"} {
		if _, ok := m.Files[key]; !ok {
			t.Errorf("manifest missing %s (keys %v)", key, manifestKeys(m))
		}
	}
	if e := m.Files[containerVolPrefix+"/anon"]; e.Size != volumeSkippedSize {
		t.Errorf("anonymous volume entry = %+v, want skipped", e)
	}
	if rec.createCount() != 1 {
		t.Fatalf("dump execs = %d", rec.createCount())
	}

	// Restore into a fresh daemon: probe ready, then reload.
	rf, d := newRestoreDaemon458()
	rf.imagePull = func(_ context.Context, ref string) (client.ImagePullResponse, error) {
		d.mu.Lock()
		d.pulled = append(d.pulled, ref)
		d.mu.Unlock()
		return &fakePullResponse{Reader: strings.NewReader(""), waitErr: errFake458}, nil
	}
	rrec := installExec(rf, execScript{}, execScript{halfClose: true})
	if err := os.Remove(filepath.Join(vol, "PG_VERSION")); err != nil {
		t.Fatal(err)
	}
	if err := (&ContainerHandler{cli: rf}).RestoreChunked(t.Context(), BackupItem{Name: "db"}, repo, id, "", noopProgress); err != nil {
		t.Fatalf("RestoreChunked: %v", err)
	}
	if got := readFile458(t, filepath.Join(vol, "PG_VERSION")); got != "16" {
		t.Errorf("volume file = %q", got)
	}
	if rrec.createCount() != 2 {
		t.Errorf("restore execs = %d, want probe + reload", rrec.createCount())
	}
	if got := rrec.lastConn().stdinString(); got != "CREATE TABLE t();\n" {
		t.Errorf("replayed dump = %q", got)
	}
	if len(d.pulled) < 1 || len(d.started) != 1 {
		t.Errorf("pulled=%v started=%v", d.pulled, d.started)
	}
}

func TestContainerRestoreChunkedManifestErrors458(t *testing.T) {
	t.Parallel()

	put := func(t *testing.T, repo *dedup.Repo, files map[string]string) dedup.ID {
		t.Helper()
		work := t.TempDir()
		m := dedup.Manifest{Files: map[string]dedup.ManifestEntry{}}
		for k, v := range files {
			m.Files[k] = chunkTestFile(t, repo, filepath.Join(work, strings.Trim(k, "_")), v)
		}
		id, err := repo.PutManifest("x", m)
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.Flush(); err != nil {
			t.Fatal(err)
		}
		return id
	}
	meta := `{"image_tag":"nginx","repo_digests":["nginx@` + sha458 + `"]}`
	tests := []struct {
		name    string
		files   map[string]string
		dest    string
		wantErr string
	}{
		{name: "no inspect entry", files: map[string]string{containerImageMetaKey: meta}, wantErr: "manifest missing __inspect"},
		{name: "inspect not json", files: map[string]string{containerInspectKey: "{"}, wantErr: "parse inspect"},
		{name: "bad restore destination", files: map[string]string{containerInspectKey: `{"Name":"/x","Config":{"Image":"nginx"}}`, containerImageMetaKey: meta}, dest: "not/absolute", wantErr: "invalid restore path"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := newChunkTestRepo(t)
			id := put(t, repo, tt.files)
			f, _ := newRestoreDaemon458()
			err := (&ContainerHandler{cli: f}).RestoreChunked(t.Context(), BackupItem{Name: "x"}, repo, id, tt.dest, nil)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
		})
	}
}
