package engine

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	containertypes "github.com/moby/moby/api/types/container"
	imagetypes "github.com/moby/moby/api/types/image"
	mounttypes "github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/ruaan-deysel/vault/internal/dedup"
)

func TestTarDirectory(t *testing.T) {
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "file1.txt"), []byte("hello"), 0644)
	os.MkdirAll(filepath.Join(src, "subdir"), 0755)
	os.WriteFile(filepath.Join(src, "subdir", "file2.txt"), []byte("world"), 0644)

	dst := filepath.Join(t.TempDir(), "test.tar.gz")
	if err := tarDirectory(context.Background(), src, dst, nil, CompressionGzip); err != nil {
		t.Fatalf("tarDirectory() error = %v", err)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("tar file not created: %v", err)
	}
	if info.Size() == 0 {
		t.Error("tar file is empty")
	}
}

func TestTarAndUntarRoundtrip(t *testing.T) {
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "data.txt"), []byte("vault backup"), 0644)

	tarPath := filepath.Join(t.TempDir(), "backup.tar.gz")
	if err := tarDirectory(context.Background(), src, tarPath, nil, CompressionGzip); err != nil {
		t.Fatalf("tarDirectory() error = %v", err)
	}

	restored := t.TempDir()
	if err := untarDirectory(context.Background(), tarPath, restored); err != nil {
		t.Fatalf("untarDirectory() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(restored, "data.txt"))
	if err != nil {
		t.Fatalf("restored file not found: %v", err)
	}
	if string(data) != "vault backup" {
		t.Errorf("data = %q, want %q", string(data), "vault backup")
	}
}

func TestUntarDirectoryRejectsTraversal(t *testing.T) {
	t.Parallel()

	archivePath := filepath.Join(t.TempDir(), "bad.tar.gz")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	defer file.Close()

	gzw := gzip.NewWriter(file)
	tw := tar.NewWriter(gzw)
	if err := tw.WriteHeader(&tar.Header{Name: "../evil.txt", Mode: 0o644, Size: int64(len("oops"))}); err != nil {
		t.Fatalf("WriteHeader() error = %v", err)
	}
	if _, err := io.WriteString(tw, "oops"); err != nil {
		t.Fatalf("WriteString() error = %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close error = %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("gzip close error = %v", err)
	}

	if err := untarDirectory(context.Background(), archivePath, t.TempDir()); err == nil {
		t.Fatal("untarDirectory() should reject traversal archive entries")
	}
}

func TestShouldSkipVolume(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		source     string
		wantSkip   bool
		wantReason string
	}{
		// Appdata — always backed up.
		{"appdata cache", "/mnt/cache/appdata/plex", false, ""},
		{"appdata user share", "/mnt/user/appdata/radarr", false, ""},
		{"boot config", "/boot/config/plugins/vault", false, ""},

		// Shared data — always skipped.
		{"media movies", "/mnt/user/media/movies", true, "shared data volume (/mnt/user/media)"},
		{"media tv", "/mnt/user/media/tv", true, "shared data volume (/mnt/user/media)"},
		{"downloads", "/mnt/user/downloads/complete", true, "shared data volume (/mnt/user/downloads)"},
		{"isos", "/mnt/user/isos", true, "shared data volume (/mnt/user/isos)"},
		{"domains", "/mnt/user/domains/win10", true, "shared data volume (/mnt/user/domains)"},
		{"backups", "/mnt/user/backups/vault", true, "shared data volume (/mnt/user/backups)"},
		{"remotes", "/mnt/remotes/nas", true, "shared data volume (/mnt/remotes)"},

		// Direct disk access — skipped.
		{"disk1", "/mnt/disk1/share", true, "direct disk volume"},
		{"disk12", "/mnt/disk12/data", true, "direct disk volume"},

		// Unassigned Devices (/mnt/disks/, plural) — backed up, not direct disk.
		{"unassigned devices appdata", "/mnt/disks/SSD-Device/appdata/Jellyfin", false, ""},
		{"unassigned devices share", "/mnt/disks/SSD-Device/data", false, ""},

		// Root /mnt — skipped.
		{"root mnt", "/mnt", true, "root /mnt mount"},

		// Device and virtual filesystem paths — skipped.
		{"dev rtc", "/dev/rtc", true, "device/virtual path (/dev)"},
		{"dev dri", "/dev/dri", true, "device/virtual path (/dev)"},
		{"proc", "/proc/self/fd", true, "device/virtual path (/proc)"},
		{"sys", "/sys/class/net", true, "device/virtual path (/sys)"},
		{"run", "/run/udev", true, "device/virtual path (/run)"},

		// Other system paths — backed up.
		{"tmp", "/tmp/something", false, ""},
		{"etc", "/etc/localtime", false, ""},
		{"custom", "/opt/myapp/config", false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotSkip, gotReason := shouldSkipVolume(tt.source)
			if gotSkip != tt.wantSkip {
				t.Errorf("shouldSkipVolume(%q) skip = %v, want %v", tt.source, gotSkip, tt.wantSkip)
			}
			if tt.wantReason != "" && gotReason != tt.wantReason {
				t.Errorf("shouldSkipVolume(%q) reason = %q, want %q", tt.source, gotReason, tt.wantReason)
			}
		})
	}
}

func TestRunWithRestartRestartsAfterBackupFailure(t *testing.T) {
	t.Parallel()

	backupErr := errors.New("backup failed")
	var restartCalled bool

	err := runWithRestart(true, "plex", func(string, int, string) {}, func() error {
		return backupErr
	}, func() error {
		restartCalled = true
		return nil
	})

	if !restartCalled {
		t.Fatal("expected restart to be attempted after backup failure")
	}
	if !errors.Is(err, backupErr) {
		t.Fatalf("expected backup error, got %v", err)
	}
}

func TestRunWithRestartJoinsRestartError(t *testing.T) {
	t.Parallel()

	backupErr := errors.New("backup failed")
	restartErr := errors.New("start failed")

	err := runWithRestart(true, "plex", func(string, int, string) {}, func() error {
		return backupErr
	}, func() error {
		return restartErr
	})

	if !errors.Is(err, backupErr) {
		t.Fatalf("expected joined error to include backup failure, got %v", err)
	}
	if !errors.Is(err, restartErr) {
		t.Fatalf("expected joined error to include restart failure, got %v", err)
	}
}

func TestRunWithRestartSkipsRestartWhenNotNeeded(t *testing.T) {
	t.Parallel()

	var restartCalled bool

	err := runWithRestart(false, "plex", func(string, int, string) {}, func() error {
		return nil
	}, func() error {
		restartCalled = true
		return nil
	})

	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if restartCalled {
		t.Fatal("expected restart to be skipped")
	}
}

func TestTarFileAndUntarFileRoundtrip(t *testing.T) {
	t.Parallel()

	// Create a source file to archive.
	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "hook_file")
	content := []byte("tailscale container hook content")
	if err := os.WriteFile(srcFile, content, 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	// Archive the single file.
	tarPath := filepath.Join(t.TempDir(), "volume.tar.gz")
	if err := tarFile(context.Background(), srcFile, tarPath, CompressionGzip); err != nil {
		t.Fatalf("tarFile() error = %v", err)
	}

	info, err := os.Stat(tarPath)
	if err != nil {
		t.Fatalf("tar file not created: %v", err)
	}
	if info.Size() == 0 {
		t.Error("tar file is empty")
	}

	// Verify the archive contains exactly one file with the correct name.
	f, err := os.Open(tarPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip.NewReader() error = %v", err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	header, err := tr.Next()
	if err != nil {
		t.Fatalf("tar.Next() error = %v", err)
	}
	if header.Name != "hook_file" {
		t.Errorf("tar entry name = %q, want %q", header.Name, "hook_file")
	}
	if _, err := tr.Next(); err != io.EOF {
		t.Error("expected exactly one entry in tar archive")
	}

	// Restore the file.
	restorePath := filepath.Join(t.TempDir(), "restored_hook")
	if err := untarFile(context.Background(), tarPath, restorePath); err != nil {
		t.Fatalf("untarFile() error = %v", err)
	}

	restored, err := os.ReadFile(restorePath)
	if err != nil {
		t.Fatalf("restored file not found: %v", err)
	}
	if string(restored) != string(content) {
		t.Errorf("restored content = %q, want %q", string(restored), string(content))
	}
}

func TestShouldExcludePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		relPath    string
		exclusions []string
		want       bool
	}{
		// Prefix matching.
		{"exact dir match", "Cache", []string{"Cache"}, true},
		{"nested under excluded dir", "Cache/Transcode/file.mp4", []string{"Cache"}, true},
		{"no match", "config/settings.xml", []string{"Cache"}, false},
		{"empty exclusions", "anything", nil, false},
		{"empty exclusions slice", "anything", []string{}, false},

		// Leading slash normalization.
		{"exclusion with leading slash", "Cache/foo", []string{"/Cache"}, true},
		{"exclusion without leading slash", "Cache/foo", []string{"Cache"}, true},

		// Glob matching.
		{"glob star-dot-log matches file", "app.log", []string{"*.log"}, true},
		{"glob star-dot-log matches nested", "subdir/debug.log", []string{"*.log"}, true},
		{"glob no match", "app.txt", []string{"*.log"}, false},
		{"glob question mark", "file1.tmp", []string{"file?.tmp"}, true},

		// Mixed prefix and glob.
		{"prefix and glob combined", "logs/app.log", []string{"Cache", "*.log"}, true},
		{"prefix match in mixed", "Cache/data", []string{"Cache", "*.log"}, true},
		{"no match in mixed", "config/app.conf", []string{"Cache", "*.log"}, false},

		// Edge cases.
		{"root path dot", ".", []string{"Cache"}, false},
		{"empty relpath", "", []string{"Cache"}, false},
		{"trailing slash on exclusion", "Cache/foo", []string{"Cache/"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := shouldExcludePath(tt.relPath, tt.exclusions)
			if got != tt.want {
				t.Errorf("shouldExcludePath(%q, %v) = %v, want %v", tt.relPath, tt.exclusions, got, tt.want)
			}
		})
	}
}

func TestTarFilePreservesPermissions(t *testing.T) {
	t.Parallel()

	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "executable")
	if err := os.WriteFile(srcFile, []byte("#!/bin/sh"), 0755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	tarPath := filepath.Join(t.TempDir(), "exec.tar.gz")
	if err := tarFile(context.Background(), srcFile, tarPath, CompressionGzip); err != nil {
		t.Fatalf("tarFile() error = %v", err)
	}

	restorePath := filepath.Join(t.TempDir(), "restored_exec")
	if err := untarFile(context.Background(), tarPath, restorePath); err != nil {
		t.Fatalf("untarFile() error = %v", err)
	}

	info, err := os.Stat(restorePath)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if info.Mode().Perm()&0111 == 0 {
		t.Errorf("expected executable permission bits, got %v", info.Mode().Perm())
	}
}

func TestMapExclusionsToVolume(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		exclusions       []string
		mountDestination string
		want             []string
	}{
		{
			"matching prefix stripped",
			[]string{"/config/Cache", "/config/Logs"},
			"/config",
			[]string{"Cache", "Logs"},
		},
		{
			"non-matching prefix dropped",
			[]string{"/data/stuff"},
			"/config",
			nil,
		},
		{
			"glob patterns pass through",
			[]string{"*.log", "/config/Cache"},
			"/config",
			[]string{"*.log", "Cache"},
		},
		{
			"mixed match and non-match",
			[]string{"/config/Cache", "/data/stuff", "*.tmp"},
			"/config",
			[]string{"Cache", "*.tmp"},
		},
		{
			"empty exclusions",
			nil,
			"/config",
			nil,
		},
		{
			"root mount",
			[]string{"/config/Cache"},
			"/",
			[]string{"config/Cache"},
		},
		{
			"exact mount match excluded",
			[]string{"/config"},
			"/config",
			[]string{"."},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := mapExclusionsToVolume(tt.exclusions, tt.mountDestination)
			if len(got) != len(tt.want) {
				t.Fatalf("mapExclusionsToVolume() = %v (len %d), want %v (len %d)", got, len(got), tt.want, len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("mapExclusionsToVolume()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestTarDirectoryWithExclusions(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "keep.txt"), []byte("keep"), 0644)
	os.MkdirAll(filepath.Join(src, "Cache"), 0755)
	os.WriteFile(filepath.Join(src, "Cache", "temp.dat"), []byte("temp"), 0644)
	os.MkdirAll(filepath.Join(src, "logs"), 0755)
	os.WriteFile(filepath.Join(src, "logs", "app.log"), []byte("log"), 0644)
	os.WriteFile(filepath.Join(src, "debug.log"), []byte("debug"), 0644)

	dst := filepath.Join(t.TempDir(), "test.tar.gz")
	if err := tarDirectory(context.Background(), src, dst, []string{"Cache", "*.log"}, CompressionGzip); err != nil {
		t.Fatalf("tarDirectory() error = %v", err)
	}

	names := tarEntryNames(t, dst)

	if !containsEntry(names, "keep.txt") {
		t.Error("expected keep.txt in archive")
	}
	if !containsEntry(names, "logs") {
		t.Error("expected logs/ directory in archive")
	}
	for _, name := range names {
		if strings.HasPrefix(name, "Cache") {
			t.Errorf("unexpected Cache entry in archive: %s", name)
		}
	}
	for _, name := range names {
		if strings.HasSuffix(name, ".log") {
			t.Errorf("unexpected .log file in archive: %s", name)
		}
	}
}

func TestTarDirectoryNilExclusions(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "file.txt"), []byte("data"), 0644)

	dst := filepath.Join(t.TempDir(), "test.tar.gz")
	if err := tarDirectory(context.Background(), src, dst, nil, CompressionGzip); err != nil {
		t.Fatalf("tarDirectory() with nil exclusions error = %v", err)
	}

	names := tarEntryNames(t, dst)
	if !containsEntry(names, "file.txt") {
		t.Error("expected file.txt in archive with nil exclusions")
	}
}

func tarEntryNames(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip.NewReader() error = %v", err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	var names []string
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar.Next() error = %v", err)
		}
		names = append(names, header.Name)
	}
	return names
}

func containsEntry(names []string, target string) bool {
	for _, n := range names {
		if n == target || strings.TrimSuffix(n, "/") == target {
			return true
		}
	}
	return false
}

// TestShouldExcludeMount covers the exclusion semantics for bind mounts
// (issue #70). The original report identified six pattern variants that all
// failed silently when applied to /var/run/docker.sock; the follow-up report
// identified that directory mounts (e.g. recursive `/` -> `/rootfs`) were
// also not honoured. This table exercises each form for both single-file
// and directory destinations.
func TestShouldExcludeMount(t *testing.T) {
	tests := []struct {
		name        string
		exclusions  []string
		destination string
		want        bool
	}{
		{"no exclusions", nil, "/var/run/docker.sock", false},
		{"empty destination", []string{"/var/run/docker.sock"}, "", false},
		{"exact absolute path", []string{"/var/run/docker.sock"}, "/var/run/docker.sock", true},
		{"absolute path + glob", []string{"/var/run/*"}, "/var/run/docker.sock", true},
		{"absolute directory path", []string{"/var/run"}, "/var/run/docker.sock", true},
		{"basename only", []string{"docker.sock"}, "/var/run/docker.sock", true},
		{"pure wildcard glob", []string{"*docker.sock*"}, "/var/run/docker.sock", true},
		{"basename glob", []string{"*.sock"}, "/var/run/docker.sock", true},
		{"non-matching pattern", []string{"/etc/passwd"}, "/var/run/docker.sock", false},
		{"non-matching glob", []string{"*.log"}, "/var/run/docker.sock", false},
		{"empty pattern entries skipped", []string{"", "/var/run/docker.sock"}, "/var/run/docker.sock", true},
		{"trailing slash on parent", []string{"/var/run/"}, "/var/run/docker.sock", true},
		{"sibling not excluded by parent prefix string", []string{"/var/runtime"}, "/var/run/docker.sock", false},
		// Directory-mount cases (issue #70 follow-up): containers like
		// Telegraf bind-mount `/` -> `/rootfs` and the user expects an
		// exclusion of `/rootfs` to skip the whole volume.
		{"directory mount exact match", []string{"/rootfs"}, "/rootfs", true},
		{"directory mount with trailing slash", []string{"/rootfs/"}, "/rootfs", true},
		{"directory mount basename", []string{"rootfs"}, "/rootfs", true},
		{"directory mount glob", []string{"/rootfs*"}, "/rootfs", true},
		{"unrelated directory not excluded", []string{"/rootfs"}, "/config", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldExcludeMount(tt.exclusions, tt.destination)
			if got != tt.want {
				t.Errorf("shouldExcludeMount(%v, %q) = %v, want %v",
					tt.exclusions, tt.destination, got, tt.want)
			}
		})
	}
}

// mockDockerClient is a stub dockerClient that returns canned responses for
// the methods exercised by BackupChunked tests. Methods not invoked by the
// test return zero values + a sentinel error so accidental call sites fail
// loudly. To extend the mock for restore-side coverage, supply a real
// ImagePull / ContainerCreate / ContainerStart implementation in the
// respective fields below.
type mockDockerClient struct {
	inspectResp client.ContainerInspectResult
	inspectErr  error
	imageResp   client.ImageInspectResult
}

func (m *mockDockerClient) ContainerInspect(ctx context.Context, _ string, _ client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	return m.inspectResp, m.inspectErr
}
func (m *mockDockerClient) ImageInspect(ctx context.Context, _ string, _ ...client.ImageInspectOption) (client.ImageInspectResult, error) {
	return m.imageResp, nil
}

// The remaining methods are unreachable for BackupChunked; they return
// canned zero-value + sentinel errors so any unexpected call surfaces in
// the test output.
func (m *mockDockerClient) ContainerList(ctx context.Context, _ client.ContainerListOptions) (client.ContainerListResult, error) {
	return client.ContainerListResult{}, errors.New("mockDockerClient: ContainerList not implemented")
}
func (m *mockDockerClient) ContainerCreate(ctx context.Context, _ client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	return client.ContainerCreateResult{}, errors.New("mockDockerClient: ContainerCreate not implemented")
}
func (m *mockDockerClient) ContainerStart(ctx context.Context, _ string, _ client.ContainerStartOptions) (client.ContainerStartResult, error) {
	return client.ContainerStartResult{}, errors.New("mockDockerClient: ContainerStart not implemented")
}
func (m *mockDockerClient) ContainerStop(ctx context.Context, _ string, _ client.ContainerStopOptions) (client.ContainerStopResult, error) {
	return client.ContainerStopResult{}, errors.New("mockDockerClient: ContainerStop not implemented")
}
func (m *mockDockerClient) ContainerRemove(ctx context.Context, _ string, _ client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
	return client.ContainerRemoveResult{}, errors.New("mockDockerClient: ContainerRemove not implemented")
}
func (m *mockDockerClient) ImageSave(ctx context.Context, _ []string, _ ...client.ImageSaveOption) (client.ImageSaveResult, error) {
	return nil, errors.New("mockDockerClient: ImageSave not implemented")
}
func (m *mockDockerClient) ImageLoad(ctx context.Context, _ io.Reader, _ ...client.ImageLoadOption) (client.ImageLoadResult, error) {
	return nil, errors.New("mockDockerClient: ImageLoad not implemented")
}
func (m *mockDockerClient) ImagePull(ctx context.Context, _ string, _ client.ImagePullOptions) (client.ImagePullResponse, error) {
	return nil, errors.New("mockDockerClient: ImagePull not implemented")
}

// Compile-time guard so a missed method on dockerClient breaks the test
// build rather than triggering at runtime.
var _ dockerClient = (*mockDockerClient)(nil)

// TestContainerChunkedBackupCapturesVolumesAndMeta exercises BackupChunked
// against a mock docker client with one synthetic bind mount and verifies
// the resulting manifest carries the expected __inspect / __image_meta /
// __vol__* entries. Restore round-trip is deferred to Task 16's end-to-end
// smoke because the mock can't easily satisfy ImagePull's complex
// ImagePullResponse interface (iter.Seq2 over jsonstream.Message).
func TestContainerChunkedBackupCapturesVolumesAndMeta(t *testing.T) {
	// Synthetic bind-mount source — populated with a couple of small files
	// so FolderHandler.BackupChunked has real bytes to chunk.
	volSrc := t.TempDir()
	if err := os.WriteFile(filepath.Join(volSrc, "config.yml"), []byte("foo: bar\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(volSrc, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(volSrc, "data", "blob.bin"), []byte("payload bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	mock := &mockDockerClient{
		inspectResp: client.ContainerInspectResult{
			Container: containertypes.InspectResponse{
				ID:   "deadbeef",
				Name: "/test-container",
				Config: &containertypes.Config{
					Image: "nginx:latest",
				},
				State: &containertypes.State{Running: false},
				Mounts: []containertypes.MountPoint{
					{
						Type:        mounttypes.TypeBind,
						Source:      volSrc,
						Destination: "/etc/myapp",
					},
				},
			},
		},
		imageResp: client.ImageInspectResult{
			InspectResponse: imagetypes.InspectResponse{
				RepoDigests: []string{"nginx@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
			},
		},
	}

	r, _, cleanup := dedup.NewTestRepoForEngine(t)
	defer cleanup()

	h := &ContainerHandler{cli: mock}
	item := BackupItem{
		Name:     "test-container",
		Type:     "container",
		Settings: map[string]any{"id": "deadbeef"},
	}

	manifestID, err := h.BackupChunked(context.Background(), item, r, nil)
	if err != nil {
		t.Fatalf("BackupChunked() error = %v", err)
	}
	if err := r.Flush(); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}

	m, err := r.GetManifest(manifestID)
	if err != nil {
		t.Fatalf("GetManifest() error = %v", err)
	}

	// Required: __inspect entry must always be present.
	inspectEntry, ok := m.Files[containerInspectKey]
	if !ok {
		t.Fatalf("manifest missing %s entry", containerInspectKey)
	}
	if len(inspectEntry.Chunks) != 1 {
		t.Errorf("__inspect entry chunks = %d, want 1", len(inspectEntry.Chunks))
	}

	// __image_meta entry should be present because mock.imageResp has a
	// non-empty RepoDigests slice → buildImageMeta returns a non-empty
	// meta blob that we always persist.
	if _, ok := m.Files[containerImageMetaKey]; !ok {
		t.Errorf("manifest missing %s entry", containerImageMetaKey)
	}

	// At least one __vol__* entry; the one we synthesised should point at
	// /etc/myapp and carry a single sub-manifest chunk ID.
	volKey := containerVolPrefix + "/etc/myapp"
	volEntry, ok := m.Files[volKey]
	if !ok {
		t.Fatalf("manifest missing %s entry", volKey)
	}
	if len(volEntry.Chunks) != 1 {
		t.Errorf("%s entry chunks = %d, want 1 (sub-manifest ID)", volKey, len(volEntry.Chunks))
	}

	// Spot-check the sub-manifest decodes and lists the files we created.
	subManifest, err := r.GetManifest(volEntry.Chunks[0])
	if err != nil {
		t.Fatalf("GetManifest(sub) error = %v", err)
	}
	if _, ok := subManifest.Files["config.yml"]; !ok {
		t.Errorf("sub-manifest missing config.yml (have keys: %v)", manifestKeys(subManifest))
	}
	if _, ok := subManifest.Files["data/blob.bin"]; !ok {
		t.Errorf("sub-manifest missing data/blob.bin (have keys: %v)", manifestKeys(subManifest))
	}

	// Count the __vol__* entries — defensive, the test only synthesised
	// one bind mount but make the assertion explicit for future-proofing.
	volCount := 0
	for k := range m.Files {
		if strings.HasPrefix(k, containerVolPrefix) {
			volCount++
		}
	}
	if volCount == 0 {
		t.Errorf("manifest has no %s* entries", containerVolPrefix)
	}
}

// TestContainerChunkedBackupSkipsNonBindMounts verifies the volumes-only
// scope rule: anonymous / named volumes (mount.Type != "bind") and tmpfs
// mounts are silently excluded from the manifest, matching the classic
// Backup's bind-only logic. Without this guard the runner would attempt to
// chunk arbitrary Docker volume paths under /var/lib/docker/volumes/
// (often shared between containers) and silently bloat the repo.
func TestContainerChunkedBackupSkipsNonBindMounts(t *testing.T) {
	volSrc := t.TempDir()
	if err := os.WriteFile(filepath.Join(volSrc, "a.txt"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	mock := &mockDockerClient{
		inspectResp: client.ContainerInspectResult{
			Container: containertypes.InspectResponse{
				ID:     "deadbeef",
				Name:   "/svc",
				Config: &containertypes.Config{Image: "redis:7"},
				State:  &containertypes.State{Running: false},
				Mounts: []containertypes.MountPoint{
					{Type: mounttypes.TypeBind, Source: volSrc, Destination: "/data"},
					{Type: mounttypes.TypeVolume, Source: "/var/lib/docker/volumes/foo/_data", Destination: "/cache"},
					{Type: mounttypes.TypeTmpfs, Source: "", Destination: "/tmp"},
				},
			},
		},
	}
	r, _, cleanup := dedup.NewTestRepoForEngine(t)
	defer cleanup()
	h := &ContainerHandler{cli: mock}
	item := BackupItem{Name: "svc", Type: "container", Settings: map[string]any{"id": "deadbeef"}}
	mID, err := h.BackupChunked(context.Background(), item, r, nil)
	if err != nil {
		t.Fatalf("BackupChunked() error = %v", err)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	m, err := r.GetManifest(mID)
	if err != nil {
		t.Fatal(err)
	}
	// Only one __vol__* entry — the bind mount. The volume + tmpfs must
	// not appear.
	if _, ok := m.Files[containerVolPrefix+"/data"]; !ok {
		t.Errorf("manifest missing expected bind volume %s/data", containerVolPrefix)
	}
	if _, ok := m.Files[containerVolPrefix+"/cache"]; ok {
		t.Errorf("manifest unexpectedly contains volume-typed mount %s/cache", containerVolPrefix)
	}
	if _, ok := m.Files[containerVolPrefix+"/tmp"]; ok {
		t.Errorf("manifest unexpectedly contains tmpfs mount %s/tmp", containerVolPrefix)
	}
}

// TestContainerChunkedHonoursExclusions verifies the dedup container backup
// path applies user exclude_paths the same way the classic tar Backup does:
//   - a whole bind mount whose destination matches an exclusion (e.g.
//     "/rootfs" for host-monitoring containers like Glances/Telegraf) is
//     recorded as skipped and NOT chunked (shouldExcludeMount, issue #70);
//   - glob exclusions (e.g. "*.log") are mapped into kept volumes and applied
//     during the chunked walk (mapExclusionsToVolume → FolderHandler).
//
// Regression test: previously BackupChunked ignored exclude_paths entirely, so
// a "/rootfs" bind mount caused the entire host filesystem to be chunked.
func TestContainerChunkedHonoursExclusions(t *testing.T) {
	// Mount #1: simulated host root, must be skipped wholesale.
	rootfsSrc := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootfsSrc, "huge.bin"), []byte("host filesystem content"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Mount #2: kept volume containing one file to keep and one to glob-exclude.
	keepSrc := t.TempDir()
	if err := os.WriteFile(filepath.Join(keepSrc, "config.yml"), []byte("foo: bar\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(keepSrc, "app.log"), []byte("noisy log"), 0o644); err != nil {
		t.Fatal(err)
	}

	mock := &mockDockerClient{
		inspectResp: client.ContainerInspectResult{
			Container: containertypes.InspectResponse{
				ID:     "deadbeef",
				Name:   "/glances",
				Config: &containertypes.Config{Image: "nicolargo/glances:latest"},
				State:  &containertypes.State{Running: false},
				Mounts: []containertypes.MountPoint{
					{Type: mounttypes.TypeBind, Source: rootfsSrc, Destination: "/rootfs"},
					{Type: mounttypes.TypeBind, Source: keepSrc, Destination: "/etc/glances"},
				},
			},
		},
	}

	r, _, cleanup := dedup.NewTestRepoForEngine(t)
	defer cleanup()

	h := &ContainerHandler{cli: mock}
	item := BackupItem{
		Name: "glances",
		Type: "container",
		Settings: map[string]any{
			"id":            "deadbeef",
			"exclude_paths": []string{"/rootfs", "*.log"},
		},
	}

	mID, err := h.BackupChunked(context.Background(), item, r, nil)
	if err != nil {
		t.Fatalf("BackupChunked() error = %v", err)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	m, err := r.GetManifest(mID)
	if err != nil {
		t.Fatal(err)
	}

	// /rootfs must be recorded as skipped (sentinel size, no chunks).
	rootEntry, ok := m.Files[containerVolPrefix+"/rootfs"]
	if !ok {
		t.Fatalf("manifest missing %s/rootfs entry", containerVolPrefix)
	}
	if rootEntry.Size != volumeSkippedSize {
		t.Errorf("/rootfs Size = %d, want sentinel %d (excluded mount must not be chunked)", rootEntry.Size, volumeSkippedSize)
	}
	if len(rootEntry.Chunks) != 0 {
		t.Errorf("/rootfs has %d chunks, want 0 (excluded mount must not be chunked)", len(rootEntry.Chunks))
	}

	// /etc/glances must be backed up, but its sub-manifest must honour *.log.
	keepEntry, ok := m.Files[containerVolPrefix+"/etc/glances"]
	if !ok {
		t.Fatalf("manifest missing %s/etc/glances entry", containerVolPrefix)
	}
	if len(keepEntry.Chunks) != 1 {
		t.Fatalf("/etc/glances chunks = %d, want 1 (sub-manifest ID)", len(keepEntry.Chunks))
	}
	sub, err := r.GetManifest(keepEntry.Chunks[0])
	if err != nil {
		t.Fatalf("GetManifest(sub) error = %v", err)
	}
	if _, ok := sub.Files["config.yml"]; !ok {
		t.Errorf("sub-manifest missing config.yml (have %v)", manifestKeys(sub))
	}
	if _, ok := sub.Files["app.log"]; ok {
		t.Errorf("sub-manifest unexpectedly contains glob-excluded app.log")
	}
}

func manifestKeys(m dedup.Manifest) []string {
	keys := make([]string, 0, len(m.Files))
	for k := range m.Files {
		keys = append(keys, k)
	}
	return keys
}

// TestContainerChunkedGCKeepsNestedVolumeData is an INVESTIGATION test: it
// confirms whether GC, given only the top-level container manifest as "live"
// (which is what runner.collectLiveManifestIDs returns), preserves the data
// chunks that live one level down inside each volume's sub-manifest. A
// container manifest's __vol__ entries reference a sub-manifest ID, and that
// sub-manifest references the actual file-data chunks. If GC's mark phase
// doesn't recurse into sub-manifests, those data chunks are swept → silent
// data loss on the next container dedup restore.
func TestContainerChunkedGCKeepsNestedVolumeData(t *testing.T) {
	volSrc := t.TempDir()
	// Incompressible, large payload so the volume's data chunks span MANY
	// 24 MiB packs. Packs that contain only data chunks (no manifest chunk)
	// are the ones a non-recursive GC mark phase would sweep.
	payload := make([]byte, 80<<20) // 80 MiB
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(volSrc, "data.bin"), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	mock := &mockDockerClient{
		inspectResp: client.ContainerInspectResult{
			Container: containertypes.InspectResponse{
				ID:     "deadbeef",
				Name:   "/svc",
				Config: &containertypes.Config{Image: "redis:7"},
				State:  &containertypes.State{Running: false},
				Mounts: []containertypes.MountPoint{
					{Type: mounttypes.TypeBind, Source: volSrc, Destination: "/data"},
				},
			},
		},
	}
	r, _, cleanup := dedup.NewTestRepoForEngine(t)
	defer cleanup()
	h := &ContainerHandler{cli: mock}
	item := BackupItem{Name: "svc", Type: "container", Settings: map[string]any{"id": "deadbeef"}}
	topID, err := h.BackupChunked(context.Background(), item, r, nil)
	if err != nil {
		t.Fatalf("BackupChunked() error = %v", err)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}

	// Walk top manifest → volume sub-manifest → collect leaf data chunk IDs.
	top, err := r.GetManifest(topID)
	if err != nil {
		t.Fatal(err)
	}
	volEntry, ok := top.Files[containerVolPrefix+"/data"]
	if !ok || len(volEntry.Chunks) != 1 {
		t.Fatalf("expected one __vol__/data sub-manifest chunk, got %+v", volEntry)
	}
	sub, err := r.GetManifest(volEntry.Chunks[0])
	if err != nil {
		t.Fatalf("sub-manifest unreadable: %v", err)
	}
	var dataChunks []dedup.ID
	for _, e := range sub.Files {
		dataChunks = append(dataChunks, e.Chunks...)
	}
	if len(dataChunks) == 0 {
		t.Fatal("sub-manifest had no data chunks — test setup wrong")
	}
	// Sanity: data chunks are present before GC.
	for _, c := range dataChunks {
		if _, err := r.Get(c); err != nil {
			t.Fatalf("data chunk %x unreadable before GC: %v", c[:8], err)
		}
	}

	// The fix: expand the live set through sub-manifests before GC, exactly
	// as runner.collectLiveManifestIDs now does. Without this expansion GC
	// sweeps the data-only packs (the bug this test was written to catch).
	liveManifests, _, err := WalkManifestClosure(r, []dedup.ID{topID})
	if err != nil {
		t.Fatalf("WalkManifestClosure error = %v", err)
	}
	if _, err := dedup.RunGC(r, liveManifests, dedup.GCOptions{}); err != nil {
		t.Fatalf("RunGC error = %v", err)
	}

	// THE ASSERTION: every leaf data chunk must survive GC.
	for _, c := range dataChunks {
		if _, err := r.Get(c); err != nil {
			t.Fatalf("DATA LOSS: volume data chunk %x was swept by GC: %v", c[:8], err)
		}
	}
}
