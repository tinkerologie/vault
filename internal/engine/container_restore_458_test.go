package engine

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	containertypes "github.com/moby/moby/api/types/container"
	mounttypes "github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// restoreFixture458 is a classic (tar-on-disk) container backup produced by
// running the real Backup against the fake daemon, plus the host paths its
// mounts point at.
type restoreFixture458 struct {
	sourceDir string // the backup, as Restore receives it
	volDir    string // bind-mounted directory
	fileSrc   string // bind-mounted single file
	namedDir  string // named volume's host directory
}

func writeFile458(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile458(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) // #nosec G304 -- test path
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(b)
}

// newRestoreFixture458 backs up a container with a directory bind, a file
// bind and a named volume, then replaces the saved config.json with a rich
// inspect document (ports, networks, devices, DNS — including malformed
// entries) so the recreate step has every conversion branch to walk.
func newRestoreFixture458(t *testing.T, running bool) restoreFixture458 {
	t.Helper()
	fx := restoreFixture458{
		sourceDir: t.TempDir(),
		volDir:    t.TempDir(),
		namedDir:  t.TempDir(),
	}
	fx.fileSrc = filepath.Join(t.TempDir(), "hook.sh")
	writeFile458(t, filepath.Join(fx.volDir, "a.txt"), "alpha")
	writeFile458(t, filepath.Join(fx.volDir, "sub", "b.txt"), "bravo")
	writeFile458(t, fx.fileSrc, "#!/bin/sh\n")
	writeFile458(t, filepath.Join(fx.namedDir, "v.txt"), "volume")

	mounts := []containertypes.MountPoint{
		{Type: mounttypes.TypeBind, Source: fx.volDir, Destination: "/config"},
		{Type: mounttypes.TypeBind, Source: fx.fileSrc, Destination: "/hook.sh"},
		{Type: mounttypes.TypeVolume, Name: "appvol", Source: fx.namedDir, Destination: "/data"},
		{Type: mounttypes.TypeTmpfs, Destination: "/tmp"},
	}
	backupMock := &mockDockerClient{
		inspectResp: client.ContainerInspectResult{Container: containertypes.InspectResponse{
			ID: "abc123", Name: "/app",
			Config: &containertypes.Config{Image: "nginx:latest"},
			State:  &containertypes.State{Running: running},
			Mounts: mounts,
		}},
	}
	h := &ContainerHandler{cli: backupMock}
	item := BackupItem{Name: "app", Settings: map[string]any{"id": "abc123", "no_stop": true}}
	if _, err := h.Backup(t.Context(), item, fx.sourceDir, noopProgress); err != nil {
		t.Fatalf("fixture Backup: %v", err)
	}

	cfg := map[string]any{
		"Name": "/app",
		"Config": map[string]any{
			"Hostname": "app", "Image": "nginx:latest",
			"Env":          []string{"TZ=UTC"},
			"ExposedPorts": map[string]any{"80/tcp": map[string]any{}, "bogus/port/x": map[string]any{}},
		},
		"HostConfig": map[string]any{
			"Binds":       []string{fx.volDir + ":/config", fx.fileSrc + ":/hook.sh:ro", "appvol:/data", "nocolon"},
			"NetworkMode": "br0",
			"PortBindings": map[string]any{
				"80/tcp":       []map[string]string{{"HostIp": "0.0.0.0", "HostPort": "8080"}, {"HostIp": "not-an-ip", "HostPort": "8081"}, {"HostPort": "8082"}},
				"bogus/port/x": []map[string]string{{"HostPort": "1"}},
			},
			"RestartPolicy": map[string]any{"Name": "unless-stopped"},
			"Dns":           []string{"1.1.1.1", "nope"},
			"Devices":       []map[string]string{{"PathOnHost": "/dev/dri", "PathInContainer": "/dev/dri", "CgroupPermissions": "rwm"}},
		},
		"NetworkSettings": map[string]any{"Networks": map[string]any{
			"br0":    map[string]any{"IPAMConfig": map[string]string{"IPv4Address": "192.168.1.50"}},
			"bridge": map[string]any{"IPAMConfig": map[string]string{"IPv4Address": "bad"}},
			"host":   map[string]any{},
		}},
		"State": map[string]any{"Running": running},
		"Mounts": []map[string]string{
			{"Type": "bind", "Source": fx.volDir, "Destination": "/config"},
			{"Type": "bind", "Source": fx.fileSrc, "Destination": "/hook.sh"},
			{"Type": "volume", "Name": "appvol", "Source": fx.namedDir, "Destination": "/data"},
			{"Type": "tmpfs", "Destination": "/tmp"},
		},
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	writeFile458(t, filepath.Join(fx.sourceDir, "config.json"), string(data))
	return fx
}

// wipe removes the live data so a restore has to put it back.
func (fx restoreFixture458) wipe(t *testing.T) {
	t.Helper()
	for _, p := range []string{filepath.Join(fx.volDir, "a.txt"), filepath.Join(fx.volDir, "sub"), fx.fileSrc, filepath.Join(fx.namedDir, "v.txt")} {
		if err := os.RemoveAll(p); err != nil {
			t.Fatal(err)
		}
	}
}

// restoreDaemon458 records what Restore asked the daemon to do.
type restoreDaemon458 struct {
	mu       sync.Mutex
	created  []client.ContainerCreateOptions
	started  []string
	stopped  []string
	removed  []string
	pulled   []string
	loaded   bool
	existing bool // a container with the same name already exists and runs
}

func newRestoreDaemon458() (*fakeDocker458, *restoreDaemon458) {
	d := &restoreDaemon458{existing: true}
	f := &fakeDocker458{}
	f.imageLoad = func(ctx context.Context, r io.Reader) (client.ImageLoadResult, error) {
		d.mu.Lock()
		d.loaded = true
		d.mu.Unlock()
		return okImageLoad(ctx, r)
	}
	f.containerInspect = func(context.Context, string) (client.ContainerInspectResult, error) {
		if !d.existing {
			return client.ContainerInspectResult{}, errInspectMissing
		}
		return client.ContainerInspectResult{Container: containertypes.InspectResponse{
			ID: "old-id", State: &containertypes.State{Running: true},
		}}, nil
	}
	f.containerStop = func(_ context.Context, id string) (client.ContainerStopResult, error) {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.stopped = append(d.stopped, id)
		return client.ContainerStopResult{}, nil
	}
	f.containerRemove = func(_ context.Context, id string) (client.ContainerRemoveResult, error) {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.removed = append(d.removed, id)
		return client.ContainerRemoveResult{}, nil
	}
	f.containerCreate = func(_ context.Context, o client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.created = append(d.created, o)
		return client.ContainerCreateResult{ID: "new-id"}, nil
	}
	f.containerStart = func(_ context.Context, id string) (client.ContainerStartResult, error) {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.started = append(d.started, id)
		return client.ContainerStartResult{}, nil
	}
	f.imagePull = func(_ context.Context, ref string) (client.ImagePullResponse, error) {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.pulled = append(d.pulled, ref)
		return nil, errFake458 // offline: restore falls back to image_meta seeding
	}
	return f, d
}

func TestContainerRestoreRoundTripInPlace458(t *testing.T) {
	t.Parallel()

	fx := newRestoreFixture458(t, true)
	fx.wipe(t)
	f, d := newRestoreDaemon458()
	h := &ContainerHandler{cli: f}

	var steps []string
	progress := func(_ string, _ int, msg string) { steps = append(steps, msg) }
	if err := h.Restore(t.Context(), BackupItem{Name: "app", Settings: map[string]any{}}, fx.sourceDir, progress); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	if got := readFile458(t, filepath.Join(fx.volDir, "a.txt")); got != "alpha" {
		t.Errorf("a.txt = %q", got)
	}
	if got := readFile458(t, filepath.Join(fx.volDir, "sub", "b.txt")); got != "bravo" {
		t.Errorf("sub/b.txt = %q", got)
	}
	if got := readFile458(t, fx.fileSrc); got != "#!/bin/sh\n" {
		t.Errorf("file mount = %q", got)
	}
	if got := readFile458(t, filepath.Join(fx.namedDir, "v.txt")); got != "volume" {
		t.Errorf("named volume = %q", got)
	}

	if !d.loaded {
		t.Error("image was not loaded")
	}
	if len(d.stopped) != 1 || len(d.removed) != 1 {
		t.Errorf("existing container: stopped=%v removed=%v", d.stopped, d.removed)
	}
	if len(d.started) != 1 || d.started[0] != "new-id" {
		t.Errorf("started = %v", d.started)
	}
	if len(d.pulled) != 1 || d.pulled[0] != "nginx:latest" {
		t.Errorf("pulled = %v", d.pulled)
	}
	if len(d.created) != 1 {
		t.Fatalf("created %d containers", len(d.created))
	}
	o := d.created[0]
	if o.Name != "app" {
		t.Errorf("Name = %q", o.Name)
	}
	if len(o.Config.ExposedPorts) != 1 {
		t.Errorf("ExposedPorts = %v (malformed key should be dropped)", o.Config.ExposedPorts)
	}
	if len(o.HostConfig.PortBindings) != 1 {
		t.Errorf("PortBindings = %v", o.HostConfig.PortBindings)
	}
	for _, bindings := range o.HostConfig.PortBindings {
		if len(bindings) != 3 || bindings[0].HostIP.String() != "0.0.0.0" || bindings[1].HostIP.IsValid() {
			t.Errorf("bindings = %+v", bindings)
		}
	}
	if len(o.HostConfig.DNS) != 1 {
		t.Errorf("DNS = %v", o.HostConfig.DNS)
	}
	if len(o.HostConfig.Devices) != 1 || o.HostConfig.Devices[0].PathOnHost != "/dev/dri" {
		t.Errorf("Devices = %v", o.HostConfig.Devices)
	}
	eps := o.NetworkingConfig.EndpointsConfig
	if len(eps) != 3 || eps["br0"].IPAMConfig == nil || eps["bridge"].IPAMConfig != nil {
		t.Errorf("EndpointsConfig = %+v", eps)
	}
	if o.HostConfig.Binds[0] != fx.volDir+":/config" {
		t.Errorf("in-place restore must not rewrite binds: %v", o.HostConfig.Binds)
	}
	if steps[len(steps)-1] != "restore complete" {
		t.Errorf("last progress = %q", steps[len(steps)-1])
	}
}

func TestContainerRestoreToAlternateDestination458(t *testing.T) {
	tmplDir := withTestTemplatesDir(t)

	fx := newRestoreFixture458(t, false)
	// A template captured by the backup, naming the original host paths.
	writeFile458(t, filepath.Join(fx.sourceDir, containerTemplateFile),
		`<Container><Config Type="Path" Target="/config">`+fx.volDir+`</Config></Container>`)
	f, d := newRestoreDaemon458()
	d.existing = false
	h := &ContainerHandler{cli: f}

	dest := t.TempDir()
	item := BackupItem{Name: "app", Settings: map[string]any{"restore_destination": dest, SettingCleanDestination: true}}
	if err := h.Restore(t.Context(), item, fx.sourceDir, noopProgress); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	normDest, err := normalizeRestorePath(dest)
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile458(t, filepath.Join(normDest, filepath.Base(fx.volDir), "a.txt")); got != "alpha" {
		t.Errorf("relocated a.txt = %q", got)
	}
	if got := readFile458(t, filepath.Join(normDest, "appvol", "v.txt")); got != "volume" {
		t.Errorf("named volume restored to %q", got)
	}
	if len(d.started) != 0 {
		t.Errorf("container was not running at backup; started = %v", d.started)
	}
	if len(d.stopped)+len(d.removed) != 0 {
		t.Errorf("no existing container, yet stop/remove called")
	}
	binds := d.created[0].HostConfig.Binds
	if !strings.HasPrefix(binds[0], filepath.Join(normDest, filepath.Base(fx.volDir))+":") || binds[3] != "nocolon" {
		t.Errorf("binds not rewritten: %v", binds)
	}
	tmpl := readFile458(t, filepath.Join(tmplDir, "my-app.xml"))
	if strings.Contains(tmpl, ">"+fx.volDir+"<") {
		t.Errorf("template still names the original path: %s", tmpl)
	}
}

func TestContainerRestoreFileSelectionSkipsOtherVolumes458(t *testing.T) {
	t.Parallel()

	fx := newRestoreFixture458(t, false)
	fx.wipe(t)
	f, _ := newRestoreDaemon458()
	h := &ContainerHandler{cli: f}
	item := BackupItem{Name: "app", Settings: map[string]any{"restore_file_paths": []any{"/config/a.txt"}}}
	if err := h.Restore(t.Context(), item, fx.sourceDir, noopProgress); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := readFile458(t, filepath.Join(fx.volDir, "a.txt")); got != "alpha" {
		t.Errorf("selected file = %q", got)
	}
	if _, err := os.Stat(filepath.Join(fx.volDir, "sub", "b.txt")); err == nil {
		t.Error("unselected file was restored")
	}
	if _, err := os.Stat(filepath.Join(fx.namedDir, "v.txt")); err == nil {
		t.Error("volume outside the selection was restored")
	}
}

func TestContainerRestoreMissingArchivesAreSkipped458(t *testing.T) {
	t.Parallel()

	fx := newRestoreFixture458(t, false)
	// Mark the named volume as excluded at backup time, and drop every
	// archive so the bind mounts have nothing to resolve either.
	manifest := []volumeManifestEntry{
		{Index: 0, Source: fx.volDir, Destination: "/config", BackedUp: true, Archive: "gone.tar"},
		{Index: 2, Source: fx.namedDir, Destination: "/data", BackedUp: false, SkipReason: "matches exclusion pattern"},
	}
	data, _ := json.Marshal(manifest)
	writeFile458(t, filepath.Join(fx.sourceDir, "volumes.json"), string(data))
	entries, err := os.ReadDir(fx.sourceDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "volume_") {
			if err := os.Remove(filepath.Join(fx.sourceDir, e.Name())); err != nil {
				t.Fatal(err)
			}
		}
	}
	f, d := newRestoreDaemon458()
	h := &ContainerHandler{cli: f}
	if err := h.Restore(t.Context(), BackupItem{Name: "app"}, fx.sourceDir, noopProgress); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if len(d.created) != 1 {
		t.Error("container should still be recreated")
	}
}

func TestContainerRestoreErrors458(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(t *testing.T, fx restoreFixture458, f *fakeDocker458)
		item    BackupItem
		wantErr string
	}{
		{
			name: "no image archive",
			mutate: func(t *testing.T, fx restoreFixture458, _ *fakeDocker458) {
				removeGlob458(t, fx.sourceDir, "image.tar*")
			},
			wantErr: "locating image archive",
		},
		{
			name: "corrupt compressed image",
			mutate: func(t *testing.T, fx restoreFixture458, _ *fakeDocker458) {
				removeGlob458(t, fx.sourceDir, "image.tar*")
				writeFile458(t, filepath.Join(fx.sourceDir, "image.tar.gz"), "\x1f\x8b")
			},
			wantErr: "gzip",
		},
		{
			name: "missing config",
			mutate: func(t *testing.T, fx restoreFixture458, _ *fakeDocker458) {
				removeGlob458(t, fx.sourceDir, "config.json")
			},
			wantErr: "reading config",
		},
		{
			name: "malformed config",
			mutate: func(t *testing.T, fx restoreFixture458, _ *fakeDocker458) {
				writeFile458(t, filepath.Join(fx.sourceDir, "config.json"), "{not json")
			},
			wantErr: "parsing config",
		},
		{
			name:    "restore destination outside approved roots",
			item:    BackupItem{Name: "app", Settings: map[string]any{"restore_destination": "relative/path"}},
			wantErr: "invalid restore path",
		},
		{
			name: "malformed volumes manifest",
			mutate: func(t *testing.T, fx restoreFixture458, _ *fakeDocker458) {
				writeFile458(t, filepath.Join(fx.sourceDir, "volumes.json"), "[")
			},
			wantErr: "volumes manifest for app",
		},
		{
			name: "create fails",
			mutate: func(_ *testing.T, _ restoreFixture458, f *fakeDocker458) {
				f.containerCreate = func(context.Context, client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
					return client.ContainerCreateResult{}, errFake458
				}
			},
			wantErr: "creating container",
		},
		{
			name: "start fails",
			mutate: func(_ *testing.T, _ restoreFixture458, f *fakeDocker458) {
				f.containerStart = func(context.Context, string) (client.ContainerStartResult, error) {
					return client.ContainerStartResult{}, errFake458
				}
			},
			wantErr: "starting restored container",
		},
		{
			name: "unsafe container name",
			mutate: func(t *testing.T, fx restoreFixture458, _ *fakeDocker458) {
				rewriteConfig458(t, fx.sourceDir, func(m map[string]any) { m["Name"] = "/.." })
			},
			wantErr: "invalid restore name",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fx := newRestoreFixture458(t, true)
			f, _ := newRestoreDaemon458()
			if tt.mutate != nil {
				tt.mutate(t, fx, f)
			}
			item := tt.item
			if item.Name == "" {
				item = BackupItem{Name: "app", Settings: map[string]any{}}
			}
			err := (&ContainerHandler{cli: f}).Restore(t.Context(), item, fx.sourceDir, noopProgress)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func removeGlob458(t *testing.T, dir, pattern string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range matches {
		if err := os.Remove(m); err != nil {
			t.Fatal(err)
		}
	}
}

func rewriteConfig458(t *testing.T, sourceDir string, fn func(map[string]any)) {
	t.Helper()
	path := filepath.Join(sourceDir, "config.json")
	var m map[string]any
	if err := json.Unmarshal([]byte(readFile458(t, path)), &m); err != nil {
		t.Fatal(err)
	}
	fn(m)
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	writeFile458(t, path, string(data))
}

// A backup that carried a database dump and the replay marker reloads the
// dump into the freshly started server.
func TestContainerRestoreReplaysDatabaseDump458(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		scripts  []execScript
		wantErr  string
		wantExec int
	}{
		{name: "reload succeeds", scripts: []execScript{{}, {halfClose: true}}, wantExec: 2},
		{name: "reload fails", scripts: []execScript{{}, {exitCode: 1, output: muxFrame(2, "ERROR")}}, wantErr: "partially reloaded", wantExec: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fx := newRestoreFixture458(t, true)
			rewriteConfig458(t, fx.sourceDir, func(m map[string]any) {
				cfg := m["Config"].(map[string]any)
				cfg["Image"] = "postgres:16"
				cfg["Env"] = []string{"POSTGRES_PASSWORD=pw"}
			})
			writeFile458(t, filepath.Join(fx.sourceDir, DatabaseDumpFile), "CREATE TABLE t();\n")
			writeFile458(t, filepath.Join(fx.sourceDir, DatabaseReplayMarker), "")
			f, _ := newRestoreDaemon458()
			rec := installExec(f, tt.scripts...)
			err := (&ContainerHandler{cli: f}).Restore(t.Context(), BackupItem{Name: "db"}, fx.sourceDir, noopProgress)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("Restore: %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
			if rec.createCount() != tt.wantExec {
				t.Errorf("execs = %d, want %d", rec.createCount(), tt.wantExec)
			}
			if tt.wantErr == "" {
				if got := rec.lastConn().stdinString(); got != "CREATE TABLE t();\n" {
					t.Errorf("dump fed to psql = %q", got)
				}
			}
		})
	}
}

// A dump that is not replayed — the container was not running — is copied
// beside the first restored directory mount so it outlives the staging dir.
func TestContainerRestorePreservesUnreplayedDump458(t *testing.T) {
	t.Parallel()

	fx := newRestoreFixture458(t, false)
	writeFile458(t, filepath.Join(fx.sourceDir, DatabaseDumpFile+".gz"), "dump-bytes")
	f, _ := newRestoreDaemon458()
	var saved string
	progress := func(_ string, _ int, msg string) {
		if after, ok := strings.CutPrefix(msg, "database dump saved to "); ok {
			saved = after
		}
	}
	if err := (&ContainerHandler{cli: f}).Restore(t.Context(), BackupItem{Name: "app"}, fx.sourceDir, progress); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if saved == "" {
		t.Fatal("dump was not preserved")
	}
	if got := readFile458(t, saved); got != "dump-bytes" {
		t.Errorf("preserved dump = %q", got)
	}
	if filepath.Base(saved) != "app-"+DatabaseDumpFile+".gz" {
		t.Errorf("preserved name = %q", filepath.Base(saved))
	}
}

// A running server that never becomes ready leaves the volume restore
// standing; the dump is preserved instead of replayed.
func TestContainerRestoreDatabaseNotReady458(t *testing.T) {
	t.Parallel()

	fx := newRestoreFixture458(t, true)
	rewriteConfig458(t, fx.sourceDir, func(m map[string]any) {
		m["Config"].(map[string]any)["Image"] = "postgres:16"
	})
	writeFile458(t, filepath.Join(fx.sourceDir, DatabaseDumpFile), "x")
	writeFile458(t, filepath.Join(fx.sourceDir, DatabaseReplayMarker), "")
	f, _ := newRestoreDaemon458()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f.execCreate = func(context.Context, string, client.ExecCreateOptions) (client.ExecCreateResult, error) {
		cancel() // the readiness wait is abandoned
		return client.ExecCreateResult{}, errFake458
	}
	// The cancelled context also stops the dump copy, which is logged, not
	// returned: the restore itself still succeeds.
	if err := (&ContainerHandler{cli: f}).Restore(ctx, BackupItem{Name: "db"}, fx.sourceDir, noopProgress); err != nil {
		t.Fatalf("Restore: %v", err)
	}
}
