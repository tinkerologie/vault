package engine

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	containertypes "github.com/moby/moby/api/types/container"
	mounttypes "github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// backupFake458 returns a fake whose ContainerInspect reports the given
// container, with ImageSave succeeding (the embedded mock's default).
func backupFake458(c containertypes.InspectResponse) *fakeDocker458 {
	f := &fakeDocker458{}
	f.inspectResp = client.ContainerInspectResult{Container: c}
	return f
}

func dbContainer458(running, restarting bool, mounts ...containertypes.MountPoint) containertypes.InspectResponse {
	return containertypes.InspectResponse{
		ID: "dbid", Name: "/db",
		Config: &containertypes.Config{Image: "postgres:16", Env: []string{"POSTGRES_PASSWORD=pw"}},
		State:  &containertypes.State{Running: running, Restarting: restarting, Status: containertypes.ContainerState("running")},
		Mounts: mounts,
	}
}

func TestContainerBackupDatabaseDump458(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		running    bool
		restarting bool
		script     execScript
		wantErr    string
	}{
		{name: "stopped container", wantErr: "a dump needs the server running"},
		{name: "crash-looping container", running: true, restarting: true, wantErr: "it may be in a crash loop"},
		{name: "dump fails", running: true, script: execScript{exitCode: 1}, wantErr: "dumping postgres database for db"},
		{name: "dump taken live", running: true, script: execScript{output: muxFrame(1, "-- dump\n")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			vol := t.TempDir()
			writeFile458(t, filepath.Join(vol, "pgdata"), "x")
			f := backupFake458(dbContainer458(tt.running, tt.restarting,
				containertypes.MountPoint{Type: mounttypes.TypeBind, Source: vol, Destination: "/var/lib/postgresql/data"}))
			installExec(f, tt.script)
			dest := t.TempDir()
			item := BackupItem{Name: "db", Settings: map[string]any{"id": "dbid", "database_dump": true, "no_stop": true}}
			res, err := (&ContainerHandler{cli: f}).Backup(t.Context(), item, dest, noopProgress)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Backup: %v", err)
			}
			names := map[string]bool{}
			for _, bf := range res.Files {
				names[bf.Name] = true
			}
			// no_stop means the volume was read live, so the dump is the
			// consistent copy and must be marked for replay.
			if !names[DatabaseDumpFile] || !names[DatabaseReplayMarker] {
				t.Errorf("files = %v, want dump and replay marker", names)
			}
		})
	}
}

func TestContainerBackupVolumeBranches458(t *testing.T) {
	t.Parallel()

	keep := t.TempDir()
	writeFile458(t, filepath.Join(keep, "keep.txt"), "k")
	excluded := t.TempDir()
	writeFile458(t, filepath.Join(excluded, "big.bin"), "b")

	c := containertypes.InspectResponse{
		ID: "cid", Name: "/svc",
		Config: &containertypes.Config{Image: "svc:1"},
		State:  &containertypes.State{Running: false},
		Mounts: []containertypes.MountPoint{
			{Type: mounttypes.TypeBind, Source: keep, Destination: "/config"},
			{Type: mounttypes.TypeBind, Source: excluded, Destination: "/cache"},
			{Type: mounttypes.TypeVolume, Name: strings.Repeat("ab", 32), Source: t.TempDir(), Destination: "/anon"},
			{Type: mounttypes.TypeBind, Source: "", Destination: "/empty"},
		},
	}
	f := backupFake458(c)
	// The stored ID is stale; the container is found again by name.
	f.containerInspect = func(_ context.Context, id string) (client.ContainerInspectResult, error) {
		if id == "stale" {
			return client.ContainerInspectResult{}, errInspectMissing
		}
		return client.ContainerInspectResult{Container: c}, nil
	}
	dest := t.TempDir()
	item := BackupItem{Name: "svc", Settings: map[string]any{"id": "stale", "exclude_paths": []any{"/cache"}}}
	if _, err := (&ContainerHandler{cli: f}).Backup(t.Context(), item, dest, noopProgress); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	manifest := readManifestForTest(t, dest)
	reasons := map[string]string{}
	for _, e := range manifest {
		reasons[e.Destination] = e.SkipReason
	}
	if reasons["/config"] != "" {
		t.Errorf("/config should be backed up, got skip reason %q", reasons["/config"])
	}
	if reasons["/cache"] != "matches exclusion pattern" {
		t.Errorf("/cache reason = %q", reasons["/cache"])
	}
	if !strings.Contains(reasons["/anon"], "anonymous volume") {
		t.Errorf("/anon reason = %q", reasons["/anon"])
	}
}

func TestContainerBackupErrors458(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		setup   func(t *testing.T) (*fakeDocker458, string, BackupItem)
		wantErr string
	}{
		{
			name: "missing container id",
			setup: func(t *testing.T) (*fakeDocker458, string, BackupItem) {
				return &fakeDocker458{}, t.TempDir(), BackupItem{Name: "x", Settings: map[string]any{}}
			},
			wantErr: "container id not found",
		},
		{
			name: "destination cannot be created",
			setup: func(t *testing.T) (*fakeDocker458, string, BackupItem) {
				blocker := filepath.Join(t.TempDir(), "file")
				writeFile458(t, blocker, "")
				return &fakeDocker458{}, filepath.Join(blocker, "dest"), BackupItem{Name: "x", Settings: map[string]any{"id": "x"}}
			},
			wantErr: "creating dest dir",
		},
		{
			name: "volume source vanished",
			setup: func(t *testing.T) (*fakeDocker458, string, BackupItem) {
				f := backupFake458(containertypes.InspectResponse{
					ID: "x", Name: "/x", Config: &containertypes.Config{Image: "x"}, State: &containertypes.State{},
					Mounts: []containertypes.MountPoint{{Type: mounttypes.TypeBind, Source: filepath.Join(t.TempDir(), "gone"), Destination: "/data"}},
				})
				return f, t.TempDir(), BackupItem{Name: "x", Settings: map[string]any{"id": "x"}}
			},
			wantErr: "stat volume",
		},
		{
			name: "image stream breaks",
			setup: func(t *testing.T) (*fakeDocker458, string, BackupItem) {
				f := backupFake458(containertypes.InspectResponse{
					ID: "x", Name: "/x", Config: &containertypes.Config{Image: "x"}, State: &containertypes.State{},
				})
				f.imageSave = func(context.Context, []string) (client.ImageSaveResult, error) {
					return io.NopCloser(io.MultiReader(strings.NewReader("partial"), errReader458{})), nil
				}
				return f, t.TempDir(), BackupItem{Name: "x", Settings: map[string]any{"id": "x"}, Compression: CompressionGzip}
			},
			wantErr: "writing image",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f, dest, item := tt.setup(t)
			_, err := (&ContainerHandler{cli: f}).Backup(t.Context(), item, dest, noopProgress)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

type errReader458 struct{}

func (errReader458) Read([]byte) (int, error) { return 0, errFake458 }

// The Unraid template is captured by a full backup and, on a differential,
// only when it changed since the reference.
func TestContainerBackupTemplateCapture458(t *testing.T) {
	tmplDir := withTestTemplatesDir(t)
	tmpl := filepath.Join(tmplDir, "my-tpl.xml")
	writeFile458(t, tmpl, "<Container/>")
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(tmpl, old, old); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name         string
		changedSince string
		wantTemplate bool
	}{
		{name: "full backup", wantTemplate: true},
		{name: "differential, template unchanged", changedSince: time.Now().Add(-time.Hour).Format(time.RFC3339), wantTemplate: false},
		{name: "differential, template changed", changedSince: old.Add(-time.Hour).Format(time.RFC3339), wantTemplate: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := backupFake458(containertypes.InspectResponse{
				ID: "tid", Name: "/tpl", Config: &containertypes.Config{Image: "tpl"}, State: &containertypes.State{},
			})
			settings := map[string]any{"id": "tid"}
			if tt.changedSince != "" {
				settings["changed_since"] = tt.changedSince
			}
			dest := t.TempDir()
			if _, err := (&ContainerHandler{cli: f}).Backup(t.Context(), BackupItem{Name: "tpl", Settings: settings}, dest, noopProgress); err != nil {
				t.Fatalf("Backup: %v", err)
			}
			_, statErr := os.Stat(filepath.Join(dest, "template.xml"))
			if got := statErr == nil; got != tt.wantTemplate {
				t.Errorf("template captured = %v, want %v", got, tt.wantTemplate)
			}
		})
	}
}
