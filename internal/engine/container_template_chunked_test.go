package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	containertypes "github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/ruaan-deysel/vault/internal/dedup"
)

// withTestTemplatesDir points the Unraid template directory at a temporary
// one for the duration of a test.
func withTestTemplatesDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	orig := dockerTemplatesDir
	dockerTemplatesDir = dir
	t.Cleanup(func() { dockerTemplatesDir = orig })
	return dir
}

func TestContainerTemplatePath(t *testing.T) {
	dir := withTestTemplatesDir(t)
	if got, want := containerTemplatePath("plex"), filepath.Join(dir, "my-plex.xml"); got != want {
		t.Errorf("containerTemplatePath() = %q, want %q", got, want)
	}
}

// TestWriteChunkedRestoreSidecars is the restore half of issue #379. The
// chunked format keeps the template in the manifest rather than as a file, so
// it has to be materialised into a directory shaped like a classic backup's —
// otherwise the shared restore path finds nothing and the Docker page's Edit
// form keeps pointing at whatever template happened to still be on the flash
// drive.
func TestWriteChunkedRestoreSidecars(t *testing.T) {
	const templateXML = `<Container><Name>plex</Name></Container>`
	const dumpSQL = "-- logical dump\nSELECT 1;\n"

	cases := []struct {
		name         string
		template     string
		dump         string
		replay       bool
		wantDir      bool
		wantTemplate string
		wantDump     string
		wantReplay   bool
	}{
		{
			name: "no sidecars materialises nothing",
		},
		{
			name:         "template only",
			template:     templateXML,
			wantDir:      true,
			wantTemplate: templateXML,
		},
		{
			name:     "dump only",
			dump:     dumpSQL,
			wantDir:  true,
			wantDump: dumpSQL,
		},
		{
			name:         "both share one directory",
			template:     templateXML,
			dump:         dumpSQL,
			replay:       true,
			wantDir:      true,
			wantTemplate: templateXML,
			wantDump:     dumpSQL,
			wantReplay:   true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, _, cleanup := dedup.NewTestRepoForEngine(t)
			defer cleanup()

			m := dedup.Manifest{Files: map[string]dedup.ManifestEntry{}}
			staging := t.TempDir()
			if tc.template != "" {
				m.Files[containerTemplateKey] = chunkTestFile(t, repo, filepath.Join(staging, "template"), tc.template)
			}
			if tc.dump != "" {
				m.Files[ContainerDBDumpKey] = chunkTestFile(t, repo, filepath.Join(staging, "dump"), tc.dump)
			}
			if tc.replay {
				m.Files[ContainerDBReplayKey] = dedup.ManifestEntry{}
			}
			if err := repo.Flush(); err != nil {
				t.Fatal(err)
			}

			dir, cleanupDir, err := writeChunkedRestoreSidecars(repo, m)
			if err != nil {
				t.Fatalf("writeChunkedRestoreSidecars: %v", err)
			}
			if cleanupDir != nil {
				defer cleanupDir()
			}
			if !tc.wantDir {
				if dir != "" {
					t.Fatalf("expected no directory, got %q", dir)
				}
				return
			}
			if dir == "" {
				t.Fatal("expected a materialised directory")
			}

			assertFile := func(name, want string) {
				t.Helper()
				data, readErr := os.ReadFile(filepath.Join(dir, name))
				if want == "" {
					if readErr == nil {
						t.Errorf("%s should not have been written", name)
					}
					return
				}
				if readErr != nil {
					t.Errorf("reading %s: %v", name, readErr)
					return
				}
				if string(data) != want {
					t.Errorf("%s = %q, want %q", name, data, want)
				}
			}
			assertFile(containerTemplateFile, tc.wantTemplate)
			assertFile(DatabaseDumpFile, tc.wantDump)

			// The replay marker decides whether the dump is pushed back into
			// the live server, so it has to survive the materialisation.
			_, markerErr := os.Stat(filepath.Join(dir, DatabaseReplayMarker))
			if tc.wantReplay && markerErr != nil {
				t.Errorf("replay marker missing: %v", markerErr)
			}
			if !tc.wantReplay && markerErr == nil {
				t.Error("replay marker should not have been written")
			}
		})
	}
}

// chunkTestFile writes content to path and chunks it into repo, returning the
// manifest entry a backup would have recorded.
func chunkTestFile(t *testing.T, repo *dedup.Repo, path, content string) dedup.ManifestEntry {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	entry, err := chunkFileIntoRepo(repo, path)
	if err != nil {
		t.Fatalf("chunkFileIntoRepo: %v", err)
	}
	return entry
}

// The sidecar staging happens between the volume restore and the container
// recreate, so a failure here must abort rather than let the container come
// back with the wrong template or no dump.
func TestWriteChunkedRestoreSidecarsFailures(t *testing.T) {
	repo, _, cleanup := dedup.NewTestRepoForEngine(t)
	defer cleanup()

	good := chunkTestFile(t, repo, filepath.Join(t.TempDir(), "t.xml"), "<Container/>")
	if err := repo.Flush(); err != nil {
		t.Fatal(err)
	}
	// A manifest entry whose chunk was never written: the repo cannot serve
	// it, which is what a truncated or partially uploaded pack looks like.
	var missing dedup.ID
	missing[0] = 0x7f
	broken := dedup.ManifestEntry{Chunks: []dedup.ID{missing}}

	t.Run("no temp directory available", func(t *testing.T) {
		// MkdirTemp reads TMPDIR; pointing it at a path that does not exist
		// is the portable way to make it fail.
		t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "not-created"))
		_, _, err := writeChunkedRestoreSidecars(repo, dedup.Manifest{Files: map[string]dedup.ManifestEntry{
			containerTemplateKey: good,
		}})
		if err == nil {
			t.Fatal("a restore that cannot stage its sidecars must fail")
		}
		if !strings.Contains(err.Error(), "restore sidecar directory") {
			t.Errorf("error %q should name the sidecar directory", err)
		}
	})

	cases := []struct {
		name  string
		files map[string]dedup.ManifestEntry
		want  string
	}{
		{
			name:  "the template chunk cannot be read",
			files: map[string]dedup.ManifestEntry{containerTemplateKey: broken},
			want:  "writing template xml",
		},
		{
			name:  "the database dump chunk cannot be read",
			files: map[string]dedup.ManifestEntry{ContainerDBDumpKey: broken},
			want:  "writing database dump",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, cleanupFn, err := writeChunkedRestoreSidecars(repo, dedup.Manifest{Files: tc.files})
			if cleanupFn != nil {
				cleanupFn()
			}
			if err == nil {
				t.Fatalf("expected a failure, got dir %q", dir)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q should contain %q", err, tc.want)
			}
			// The half-written directory must not be left behind for the
			// restore path to read a truncated template out of.
			if dir != "" {
				if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
					t.Errorf("the sidecar directory should have been cleaned up (err=%v)", statErr)
				}
			}
		})
	}
}

func TestWriteChunkedEntryToFileFailures(t *testing.T) {
	repo, _, cleanup := dedup.NewTestRepoForEngine(t)
	defer cleanup()

	dir := t.TempDir()
	t.Run("the destination cannot be created", func(t *testing.T) {
		// A directory where a file should go: os.Create cannot win.
		blocked := filepath.Join(dir, "blocked")
		if err := os.MkdirAll(blocked, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := writeChunkedEntryToFile(repo, dedup.ManifestEntry{}, blocked); err == nil {
			t.Error("writing over a directory should fail")
		}
	})

	t.Run("a chunk is missing from the repo", func(t *testing.T) {
		var missing dedup.ID
		missing[0] = 0x7f
		err := writeChunkedEntryToFile(repo, dedup.ManifestEntry{Chunks: []dedup.ID{missing}}, filepath.Join(dir, "out.xml"))
		if err == nil {
			t.Fatal("a chunk the repo cannot serve should fail the write")
		}
		if !strings.Contains(err.Error(), "reading chunk") {
			t.Errorf("error %q should say the chunk could not be read", err)
		}
	})
}

// The backup half of issue #379: the template is chunked into the repo under
// its own synthetic key, and a container with no template is not an error.
func TestContainerBackupChunkedCapturesTemplate(t *testing.T) {
	dir := withTestTemplatesDir(t)
	const templateXML = `<Container><Name>plex</Name></Container>`
	if err := os.WriteFile(filepath.Join(dir, "my-plex.xml"), []byte(templateXML), 0o644); err != nil {
		t.Fatal(err)
	}

	backup := func(t *testing.T, name string) dedup.Manifest {
		t.Helper()
		repo, _, cleanup := dedup.NewTestRepoForEngine(t)
		t.Cleanup(cleanup)
		h := &ContainerHandler{cli: &mockDockerClient{
			inspectResp: client.ContainerInspectResult{
				Container: containertypes.InspectResponse{
					ID:     "deadbeef",
					Name:   "/" + name,
					Image:  "nginx:latest",
					Config: &containertypes.Config{Image: "nginx:latest"},
					State:  &containertypes.State{Running: false},
				},
			},
		}}
		id, err := h.BackupChunked(context.Background(), BackupItem{
			Name: name, Settings: map[string]any{"id": "deadbeef"},
		}, repo, nil, func(string, int, string) {})
		if err != nil {
			t.Fatalf("BackupChunked: %v", err)
		}
		if err := repo.Flush(); err != nil {
			t.Fatal(err)
		}
		m, err := repo.GetManifest(id)
		if err != nil {
			t.Fatalf("GetManifest: %v", err)
		}
		return m
	}

	t.Run("a template is captured", func(t *testing.T) {
		m := backup(t, "plex")
		entry, ok := m.Files[containerTemplateKey]
		if !ok {
			t.Fatal("the manifest should carry the Unraid template")
		}
		if len(entry.Chunks) == 0 {
			t.Error("the template entry has no chunks")
		}
	})

	t.Run("no template is not a failure", func(t *testing.T) {
		// A container installed outside Community Apps simply has none.
		m := backup(t, "no-template-here")
		if _, ok := m.Files[containerTemplateKey]; ok {
			t.Error("a container with no template must not get an empty entry")
		}
	})

	t.Run("an unreadable template is reported, not fatal", func(t *testing.T) {
		// A directory where the template should be: chunkFileIntoRepo fails
		// with something other than NotExist, which must not fail the backup.
		if err := os.MkdirAll(filepath.Join(dir, "my-broken.xml"), 0o755); err != nil {
			t.Fatal(err)
		}
		m := backup(t, "broken")
		if _, ok := m.Files[containerTemplateKey]; ok {
			t.Error("an unreadable template must not land in the manifest")
		}
	})
}

// Both formats restore the template through one path: the classic backup
// leaves it in the staging directory and the chunked restore materialises it
// into a directory of the same shape. This drives that shared step.
func TestRecreateAndStartContainerRestoresTemplate(t *testing.T) {
	templatesDir := withTestTemplatesDir(t)
	const templateXML = `<Container><Config Target="/config">/mnt/user/appdata/plex</Config></Container>`

	cases := []struct {
		name        string
		restoreDest string
		wantContain string
	}{
		{
			name:        "no remap copies the template back as it was",
			wantContain: "/mnt/user/appdata/plex",
		},
		{
			name: "a remapped volume is rewritten so Edit agrees with the container",
			// The bind is rewritten to <dest>/<basename>, and the template
			// has to follow or the Docker page's Edit form would hand back
			// the old path the moment the user pressed Apply (issue #336).
			restoreDest: "/tmp/vault-restore-target",
			wantContain: "/tmp/vault-restore-target/plex",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sourceDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(sourceDir, containerTemplateFile), []byte(templateXML), 0o644); err != nil {
				t.Fatal(err)
			}

			mock := &mockDockerClient{createOK: true, inspectErr: os.ErrNotExist}
			h := &ContainerHandler{cli: mock}
			inspect := inspectFromJSON(t, `{
				"Name": "/plex",
				"Config": {"Image": "linuxserver/plex:latest"},
				"State": {"Running": false},
				"HostConfig": {"Binds": ["/mnt/user/appdata/plex:/config:rw"]},
				"Mounts": [{"Type": "bind", "Source": "/mnt/user/appdata/plex", "Destination": "/config"}]
			}`)

			err := h.recreateAndStartContainer(context.Background(),
				BackupItem{Name: "plex", Type: "container"}, inspect,
				tc.restoreDest, sourceDir, func(string, int, string) {})
			if err != nil {
				t.Fatalf("recreateAndStartContainer: %v", err)
			}

			written, readErr := os.ReadFile(filepath.Join(templatesDir, "my-plex.xml"))
			if readErr != nil {
				t.Fatalf("the template should have been restored: %v", readErr)
			}
			if !strings.Contains(string(written), tc.wantContain) {
				t.Errorf("restored template %q should contain %q", written, tc.wantContain)
			}
		})
	}
}

// End-to-end through RestoreChunked: the manifest's sidecars are staged into
// a classic-shaped directory and handed to the shared recreate step, which is
// what makes a dedup restore produce the same template and dump handling as a
// tar restore (issue #379).
func TestRestoreChunkedStagesSidecarsForTheRecreate(t *testing.T) {
	templatesDir := withTestTemplatesDir(t)
	repo, _, cleanup := dedup.NewTestRepoForEngine(t)
	defer cleanup()

	const templateXML = `<Container><Name>plex</Name></Container>`
	inspectJSON := `{
		"Name": "/plex",
		"Config": {"Image": "linuxserver/plex:latest"},
		"State": {"Running": false},
		"HostConfig": {},
		"Mounts": []
	}`

	work := t.TempDir()
	m := dedup.Manifest{Files: map[string]dedup.ManifestEntry{
		containerInspectKey:  chunkTestFile(t, repo, filepath.Join(work, "inspect.json"), inspectJSON),
		containerTemplateKey: chunkTestFile(t, repo, filepath.Join(work, "my-plex.xml"), templateXML),
	}}
	manifestID, err := repo.PutManifest("plex", m)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Flush(); err != nil {
		t.Fatal(err)
	}

	h := &ContainerHandler{cli: &mockDockerClient{createOK: true, inspectErr: os.ErrNotExist}}
	item := BackupItem{Name: "plex", Type: "container"}
	if err := h.RestoreChunked(context.Background(), item, repo, manifestID, "", func(string, int, string) {}); err != nil {
		t.Fatalf("RestoreChunked: %v", err)
	}

	written, err := os.ReadFile(filepath.Join(templatesDir, "my-plex.xml"))
	if err != nil {
		t.Fatalf("the template captured in the manifest should have been restored: %v", err)
	}
	if string(written) != templateXML {
		t.Errorf("restored template = %q, want %q", written, templateXML)
	}
}
