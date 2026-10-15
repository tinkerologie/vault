//go:build linux

package mount

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/tinkerologie/vault/internal/dedup"
)

// fakeFuseServer stands in for a kernel FUSE session (#458). Wait blocks
// until Unmount is called, like the real server's serve loop.
type fakeFuseServer struct {
	unmountErr   error
	waitMountErr error
	unmounts     atomic.Int32
	stop         chan struct{}
	once         sync.Once
}

func newFakeFuseServer() *fakeFuseServer { return &fakeFuseServer{stop: make(chan struct{})} }

func (s *fakeFuseServer) Unmount() error {
	s.unmounts.Add(1)
	s.once.Do(func() { close(s.stop) })
	return s.unmountErr
}

func (s *fakeFuseServer) WaitMount() error { return s.waitMountErr }
func (s *fakeFuseServer) Wait()            { <-s.stop }

// fakeMounter records fuseMount calls. When build is true it initialises the
// node tree in memory (fs.NewNodeFS runs the OnAdd hook, exactly as a real
// mount would) without touching the kernel.
type fakeMounter struct {
	mu        sync.Mutex
	server    *fakeFuseServer
	errs      []error // per-call errors, consumed in order
	build     bool
	root      *fs.Inode
	allowSeen []bool
}

func (m *fakeMounter) mount(_ string, root fs.InodeEmbedder, opts *fs.Options) (fuseServer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.allowSeen = append(m.allowSeen, opts.MountOptions.AllowOther)
	if len(m.errs) > 0 {
		err := m.errs[0]
		m.errs = m.errs[1:]
		if err != nil {
			return nil, err
		}
	}
	if m.build {
		fs.NewNodeFS(root, opts)
		m.root = root.EmbeddedInode()
	}
	return m.server, nil
}

func withFakeMounter(t *testing.T, m *fakeMounter) {
	t.Helper()
	old := fuseMount
	fuseMount = m.mount
	t.Cleanup(func() { fuseMount = old })
}

// putFile stores content in the repo and returns a manifest entry for it.
func putFile(t *testing.T, repo *dedup.Repo, content string) dedup.ManifestEntry {
	t.Helper()
	id, err := repo.Put([]byte(content))
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Flush(); err != nil {
		t.Fatal(err)
	}
	return dedup.ManifestEntry{Mode: 0o644, Size: int64(len(content)), Chunks: []dedup.ID{id}, ModTime: "2024-01-02T03:04:05Z"}
}

// readNode reads the whole file behind an inode in the built tree.
func readNode(t *testing.T, root *fs.Inode, path string) string {
	t.Helper()
	node := root
	for _, part := range strings.Split(path, "/") {
		node = node.GetChild(part)
		if node == nil {
			t.Fatalf("%s: missing component %q", path, part)
		}
	}
	file, ok := node.Operations().(*fuseFile)
	if !ok {
		t.Fatalf("%s is %T, not a file", path, node.Operations())
	}
	buf := make([]byte, 256)
	res, errno := file.Read(context.Background(), nil, buf, 0)
	if errno != fs.OK {
		t.Fatalf("%s: read errno %v", path, errno)
	}
	data, _ := res.Bytes(nil)
	return string(data)
}

func TestMountManifestsBuildsTree458(t *testing.T) {
	repo, cleanup := newTestDedupRepo(t)
	defer cleanup()

	sub := dedup.Manifest{Files: map[string]dedup.ManifestEntry{"c.txt": putFile(t, repo, "volume file")}}
	subID, err := repo.PutManifest("sub", sub)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Flush(); err != nil {
		t.Fatal(err)
	}
	manifest := dedup.Manifest{Files: map[string]dedup.ManifestEntry{
		"a.txt":              putFile(t, repo, "alpha"),
		"dir":                {IsDir: true, ModTime: "not-a-time"},
		"dir/b.txt":          putFile(t, repo, "bravo"),
		"__vol__/data":       {IsDir: true, Chunks: []dedup.ID{subID}},
		"__vol__/empty":      {IsDir: true},
		"__volfile__/etc/cf": putFile(t, repo, "config"),
		"../escape":          putFile(t, repo, "nope"),
	}}

	m := &fakeMounter{server: newFakeFuseServer(), build: true}
	withFakeMounter(t, m)
	activity := atomic.Int32{}
	mountPath := filepath.Join(t.TempDir(), "mnt")

	h, err := MountManifests(context.Background(), repo, map[string]dedup.Manifest{"": manifest}, mountPath, func() { activity.Add(1) })
	if err != nil {
		t.Fatalf("MountManifests: %v", err)
	}
	if h.MountPath() != mountPath {
		t.Fatalf("MountPath = %q", h.MountPath())
	}
	for path, want := range map[string]string{
		"a.txt":      "alpha",
		"dir/b.txt":  "bravo",
		"data/c.txt": "volume file",
		"etc/cf":     "config",
	} {
		if got := readNode(t, m.root, path); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
	if m.root.GetChild("empty") == nil {
		t.Error("empty volume mount point missing")
	}
	if m.root.GetChild("escape") != nil || m.root.GetChild("..") != nil {
		t.Error("path escaping the root was added to the tree")
	}
	if activity.Load() == 0 {
		t.Error("reads did not report activity")
	}
	if !m.allowSeen[0] {
		t.Error("first mount attempt did not request allow_other")
	}

	if err := h.Unmount(); err != nil {
		t.Fatalf("Unmount: %v", err)
	}
	if err := h.Unmount(); err != nil {
		t.Fatalf("second Unmount: %v", err)
	}
	if got := m.server.unmounts.Load(); got != 1 {
		t.Fatalf("server unmounts = %d, want 1", got)
	}
}

func TestMountManifestsItemDirectories458(t *testing.T) {
	repo, cleanup := newTestDedupRepo(t)
	defer cleanup()
	one := dedup.Manifest{Files: map[string]dedup.ManifestEntry{"f.txt": putFile(t, repo, "one")}}
	two := dedup.Manifest{Files: map[string]dedup.ManifestEntry{"g.txt": putFile(t, repo, "two")}}

	t.Run("single named item", func(t *testing.T) {
		m := &fakeMounter{server: newFakeFuseServer(), build: true}
		withFakeMounter(t, m)
		h, err := MountManifests(context.Background(), repo, map[string]dedup.Manifest{"web": one}, filepath.Join(t.TempDir(), "mnt"), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer h.Unmount()
		if got := readNode(t, m.root, "web/f.txt"); got != "one" {
			t.Fatalf("web/f.txt = %q", got)
		}
	})

	t.Run("multiple items", func(t *testing.T) {
		m := &fakeMounter{server: newFakeFuseServer(), build: true}
		withFakeMounter(t, m)
		h, err := MountManifests(context.Background(), repo, map[string]dedup.Manifest{"a": one, "b": two}, filepath.Join(t.TempDir(), "mnt"), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer h.Unmount()
		if readNode(t, m.root, "a/f.txt") != "one" || readNode(t, m.root, "b/g.txt") != "two" {
			t.Fatal("item directories not populated")
		}
	})
}

func TestMountManifestsRetriesWithoutAllowOther458(t *testing.T) {
	repo, cleanup := newTestDedupRepo(t)
	defer cleanup()
	m := &fakeMounter{server: newFakeFuseServer(), build: true, errs: []error{errors.New("allow_other not permitted")}}
	withFakeMounter(t, m)
	h, err := MountManifests(context.Background(), repo, map[string]dedup.Manifest{"": {}}, filepath.Join(t.TempDir(), "mnt"), nil)
	if err != nil {
		t.Fatalf("MountManifests: %v", err)
	}
	defer h.Unmount()
	if len(m.allowSeen) != 2 || !m.allowSeen[0] || m.allowSeen[1] {
		t.Fatalf("allow_other per attempt = %v, want [true false]", m.allowSeen)
	}
}

func TestMountManifestsErrors458(t *testing.T) {
	repo, cleanup := newTestDedupRepo(t)
	defer cleanup()
	corrupt := dedup.Manifest{Files: map[string]dedup.ManifestEntry{"bad": {Size: 10}}}

	t.Run("mountpoint cannot be created", func(t *testing.T) {
		withFakeMounter(t, &fakeMounter{server: newFakeFuseServer()})
		file := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := MountManifests(context.Background(), repo, nil, filepath.Join(file, "mnt"), nil)
		if err == nil || !strings.HasPrefix(err.Error(), "mount: create mountpoint") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("both mount attempts fail", func(t *testing.T) {
		m := &fakeMounter{server: newFakeFuseServer(), errs: []error{errors.New("e1"), errors.New("no fuse device")}}
		withFakeMounter(t, m)
		_, err := MountManifests(context.Background(), repo, nil, filepath.Join(t.TempDir(), "mnt"), nil)
		if err == nil || !strings.Contains(err.Error(), "failed: no fuse device") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("wait mount fails", func(t *testing.T) {
		srv := newFakeFuseServer()
		srv.waitMountErr = errors.New("kernel said no")
		withFakeMounter(t, &fakeMounter{server: srv, build: true})
		_, err := MountManifests(context.Background(), repo, map[string]dedup.Manifest{"": {}}, filepath.Join(t.TempDir(), "mnt"), nil)
		if err == nil || !strings.Contains(err.Error(), "server.WaitMount") || srv.unmounts.Load() != 1 {
			t.Fatalf("err = %v unmounts=%d", err, srv.unmounts.Load())
		}
	})

	t.Run("tree build fails", func(t *testing.T) {
		srv := newFakeFuseServer()
		withFakeMounter(t, &fakeMounter{server: srv, build: true})
		_, err := MountManifests(context.Background(), repo, map[string]dedup.Manifest{"": corrupt}, filepath.Join(t.TempDir(), "mnt"), nil)
		if err == nil || !strings.HasPrefix(err.Error(), "mount: build tree:") || srv.unmounts.Load() != 1 {
			t.Fatalf("err = %v unmounts=%d", err, srv.unmounts.Load())
		}
	})

	t.Run("cancelled before tree is built", func(t *testing.T) {
		srv := newFakeFuseServer()
		withFakeMounter(t, &fakeMounter{server: srv}) // never runs OnAdd
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := MountManifests(ctx, repo, map[string]dedup.Manifest{"": {}}, filepath.Join(t.TempDir(), "mnt"), nil)
		if !errors.Is(err, context.Canceled) || srv.unmounts.Load() != 1 {
			t.Fatalf("err = %v unmounts=%d", err, srv.unmounts.Load())
		}
	})
}

func TestBuildTreeErrors458(t *testing.T) {
	repo, cleanup := newTestDedupRepo(t)
	defer cleanup()
	corrupt := dedup.Manifest{Files: map[string]dedup.ManifestEntry{"bad": {Size: 10}}}
	badVolFile := dedup.Manifest{Files: map[string]dedup.ManifestEntry{"__volfile__/x": {Size: 10}}}
	badSub := dedup.Manifest{Files: map[string]dedup.ManifestEntry{"__vol__/x": {IsDir: true, Chunks: []dedup.ID{{0xde, 0xad}}}}}
	nestedBad := dedup.Manifest{Files: map[string]dedup.ManifestEntry{}}
	nestedID, err := repo.PutManifest("nested", corrupt)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Flush(); err != nil {
		t.Fatal(err)
	}
	nestedBad.Files["__vol__/y"] = dedup.ManifestEntry{IsDir: true, Chunks: []dedup.ID{nestedID}}

	tests := []struct {
		name      string
		manifests map[string]dedup.Manifest
		want      string
	}{
		{name: "root manifest", manifests: map[string]dedup.Manifest{"": corrupt}, want: "corrupt manifest entry"},
		{name: "named item", manifests: map[string]dedup.Manifest{"item": corrupt}, want: "corrupt manifest entry"},
		{name: "multiple items", manifests: map[string]dedup.Manifest{"a": {}, "b": corrupt}, want: "corrupt manifest entry"},
		{name: "volume file", manifests: map[string]dedup.Manifest{"": badVolFile}, want: "corrupt manifest entry"},
		{name: "missing sub-manifest", manifests: map[string]dedup.Manifest{"": badSub}, want: "mount: get sub-manifest for x"},
		{name: "corrupt sub-manifest", manifests: map[string]dedup.Manifest{"": nestedBad}, want: "corrupt manifest entry"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := &fuseRoot{}
			fs.NewNodeFS(root, &fs.Options{})
			err := buildTree(context.Background(), repo, NewChunkCache(0), &root.Inode, tt.manifests, nil)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestAddDirToTreeRejectsEscapes458(t *testing.T) {
	root := &fuseRoot{}
	fs.NewNodeFS(root, &fs.Options{})
	for _, p := range []string{"", ".", "../up", "/abs"} {
		if got := addDirToTree(context.Background(), &root.Inode, p, time.Now(), nil); got != &root.Inode {
			t.Errorf("addDirToTree(%q) created a node", p)
		}
	}
	if got := addDirToTree(context.Background(), &root.Inode, "a/b", time.Now(), nil); got == &root.Inode || root.GetChild("a").GetChild("b") != got {
		t.Fatal("addDirToTree did not create nested directories")
	}
}

func TestFuseNodes458(t *testing.T) {
	repo, cleanup := newTestDedupRepo(t)
	defer cleanup()
	ctx := context.Background()
	var activity atomic.Int32
	touch := func() { activity.Add(1) }

	root := &fuseRoot{onActivity: touch}
	var out fuse.AttrOut
	if root.Getattr(ctx, nil, &out) != fs.OK || out.Mode != 0o555|syscall.S_IFDIR {
		t.Fatalf("root mode = %o", out.Mode)
	}
	if root.Access(ctx, 2) != syscall.EROFS || root.Access(ctx, 4) != fs.OK || root.Opendir(ctx) != fs.OK {
		t.Fatal("root access/opendir mismatch")
	}

	mod := time.Unix(1_700_000_000, 0)
	dir := &fuseDir{modTime: mod, onActivity: touch}
	out = fuse.AttrOut{}
	if dir.Getattr(ctx, nil, &out) != fs.OK || out.Mode != 0o555|syscall.S_IFDIR || out.Mtime != 1_700_000_000 || out.Ctime != out.Mtime {
		t.Fatalf("dir attr = %+v", out)
	}
	if dir.Access(ctx, 2) != syscall.EROFS || dir.Access(ctx, 1) != fs.OK || dir.Opendir(ctx) != fs.OK {
		t.Fatal("dir access/opendir mismatch")
	}

	entry := putFile(t, repo, "hello")
	reader, err := NewFileReader(repo, entry, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		mode uint32
		want uint32
	}{
		{mode: 0o644, want: 0o444 | syscall.S_IFREG},
		{mode: 0o755, want: 0o555 | syscall.S_IFREG},
		{mode: 0o200, want: 0o444 | syscall.S_IFREG}, // write-only becomes readable
	} {
		f := &fuseFile{reader: reader, modTime: mod, mode: tc.mode, onActivity: touch}
		out = fuse.AttrOut{}
		if f.Getattr(ctx, nil, &out) != fs.OK || out.Mode != tc.want || out.Size != 5 || out.Mtime != 1_700_000_000 {
			t.Errorf("mode %o: attr = %+v, want mode %o", tc.mode, out, tc.want)
		}
	}

	f := &fuseFile{reader: reader, onActivity: touch}
	if f.Access(ctx, 2) != syscall.EROFS || f.Access(ctx, 4) != fs.OK {
		t.Fatal("file access mismatch")
	}
	for _, flags := range []uint32{syscall.O_WRONLY, syscall.O_RDWR, syscall.O_APPEND, syscall.O_TRUNC} {
		if _, _, errno := f.Open(ctx, flags); errno != syscall.EROFS {
			t.Errorf("Open(%#x) errno = %v, want EROFS", flags, errno)
		}
	}
	if _, _, errno := f.Open(ctx, syscall.O_RDONLY); errno != fs.OK {
		t.Fatalf("Open(O_RDONLY) errno = %v", errno)
	}
	buf := make([]byte, 3)
	res, errno := f.Read(ctx, nil, buf, 1)
	if errno != fs.OK {
		t.Fatalf("Read errno = %v", errno)
	}
	if data, _ := res.Bytes(nil); string(data) != "ell" {
		t.Fatalf("Read = %q", data)
	}

	missing, err := NewFileReader(repo, dedup.ManifestEntry{Size: 4, Chunks: []dedup.ID{{0xbe, 0xef}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	broken := &fuseFile{reader: missing}
	if _, errno := broken.Read(ctx, nil, make([]byte, 4), 0); errno != syscall.EIO {
		t.Fatalf("Read of missing chunk errno = %v, want EIO", errno)
	}

	if activity.Load() == 0 {
		t.Fatal("node callbacks did not report activity")
	}
}

// withUnmountSeams replaces the fusermount runner and unmount syscall.
func withUnmountSeams(t *testing.T, fusermount func(bin, path string) error, unmount func(string, int) error) *[]string {
	t.Helper()
	var calls []string
	oldRun, oldUnmount := runFusermount, sysUnmount
	runFusermount = func(bin, path string) error {
		calls = append(calls, bin)
		return fusermount(bin, path)
	}
	sysUnmount = func(path string, flags int) error {
		calls = append(calls, "umount")
		return unmount(path, flags)
	}
	t.Cleanup(func() { runFusermount, sysUnmount = oldRun, oldUnmount })
	return &calls
}

func failing(bins ...string) func(string, string) error {
	return func(bin, _ string) error {
		for _, b := range bins {
			if b == bin {
				return errors.New(bin + " failed")
			}
		}
		return nil
	}
}

func TestUnmountPlatform458(t *testing.T) {
	ok := func(string, int) error { return nil }

	t.Run("missing path", func(t *testing.T) {
		calls := withUnmountSeams(t, failing(), ok)
		if err := unmountPlatform(filepath.Join(t.TempDir(), "gone")); err != nil || len(*calls) != 0 {
			t.Fatalf("err=%v calls=%v", err, *calls)
		}
	})

	tests := []struct {
		name      string
		run       func(string, string) error
		unmount   func(string, int) error
		wantErr   error
		wantCalls string
	}{
		{name: "fusermount3", run: failing(), unmount: ok, wantCalls: "fusermount3"},
		{name: "fusermount", run: failing("fusermount3"), unmount: ok, wantCalls: "fusermount3,fusermount"},
		{name: "syscall", run: failing("fusermount3", "fusermount"), unmount: ok, wantCalls: "fusermount3,fusermount,umount"},
		{name: "not mounted", run: failing("fusermount3", "fusermount"), unmount: func(string, int) error { return syscall.EINVAL }, wantCalls: "fusermount3,fusermount,umount"},
		{name: "vanished", run: failing("fusermount3", "fusermount"), unmount: func(string, int) error { return syscall.ENOENT }, wantCalls: "fusermount3,fusermount,umount"},
		{name: "busy", run: failing("fusermount3", "fusermount"), unmount: func(string, int) error { return syscall.EBUSY }, wantErr: syscall.EBUSY, wantCalls: "fusermount3,fusermount,umount"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := withUnmountSeams(t, tt.run, tt.unmount)
			err := unmountPlatform(t.TempDir())
			if !errors.Is(err, tt.wantErr) || (tt.wantErr == nil && err != nil) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if got := strings.Join(*calls, ","); got != tt.wantCalls {
				t.Fatalf("calls = %s, want %s", got, tt.wantCalls)
			}
		})
	}
}

func TestLinuxMountHandleUnmountFallback458(t *testing.T) {
	serverErr := errors.New("server unmount failed")
	tests := []struct {
		name      string
		server    bool
		run       func(string, string) error
		wantErr   error
		wantCalls string
	}{
		{name: "no server", run: failing()},
		{name: "fusermount3 rescues", server: true, run: failing(), wantCalls: "fusermount3"},
		{name: "fusermount rescues", server: true, run: failing("fusermount3"), wantCalls: "fusermount3,fusermount"},
		{name: "nothing works", server: true, run: failing("fusermount3", "fusermount"), wantErr: serverErr, wantCalls: "fusermount3,fusermount"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := withUnmountSeams(t, tt.run, func(string, int) error { return nil })
			done := make(chan struct{})
			close(done)
			h := &linuxMountHandle{mountPath: "/mnt/vault-test", done: done}
			if tt.server {
				srv := newFakeFuseServer()
				srv.unmountErr = serverErr
				h.server = srv
			}
			err := h.Unmount()
			if !errors.Is(err, tt.wantErr) || (tt.wantErr == nil && err != nil) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if got := strings.Join(*calls, ","); got != tt.wantCalls {
				t.Fatalf("calls = %s, want %s", got, tt.wantCalls)
			}
		})
	}
}
