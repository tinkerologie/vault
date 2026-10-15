package storage

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudsoda/go-smb2"
)

// fakeSMBShare is an smbShare backed by a local directory, with per-operation
// error injection. It lets the SMB adapter's success and failure paths run
// without a live SMB server.
type fakeSMBShare struct {
	root string
	errs map[string]error // keyed by operation name
	fs   smb2.FileFsInfo
	file fakeSMBFileErrs

	umounts int
}

type fakeSMBFileErrs struct {
	write, sync, close, seek error
}

func (f *fakeSMBShare) p(name string) string { return filepath.Join(f.root, filepath.FromSlash(name)) }

func (f *fakeSMBShare) MkdirAll(path string, perm os.FileMode) error {
	if err := f.errs["mkdirall"]; err != nil {
		return err
	}
	return os.MkdirAll(f.p(path), perm)
}

func (f *fakeSMBShare) Create(name string) (smbFile, error) {
	if err := f.errs["create"]; err != nil {
		return nil, err
	}
	fh, err := os.Create(f.p(name))
	if err != nil {
		return nil, err
	}
	return &fakeSMBFile{fh: fh, errs: f.file}, nil
}

func (f *fakeSMBShare) Open(name string) (smbFile, error) {
	if err := f.errs["open"]; err != nil {
		return nil, err
	}
	fh, err := os.Open(f.p(name))
	if err != nil {
		return nil, err
	}
	return &fakeSMBFile{fh: fh, errs: f.file}, nil
}

func (f *fakeSMBShare) Remove(name string) error {
	if err := f.errs["remove"]; err != nil {
		return err
	}
	return os.Remove(f.p(name))
}

func (f *fakeSMBShare) ReadDir(dirname string) ([]os.FileInfo, error) {
	if err := f.errs["readdir"]; err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(f.p(dirname))
	if err != nil {
		return nil, err
	}
	out := make([]os.FileInfo, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			return nil, err
		}
		out = append(out, info)
	}
	return out, nil
}

func (f *fakeSMBShare) Stat(name string) (os.FileInfo, error) {
	if err := f.errs["stat"]; err != nil {
		return nil, err
	}
	return os.Stat(f.p(name))
}

func (f *fakeSMBShare) Statfs(string) (smb2.FileFsInfo, error) {
	if err := f.errs["statfs"]; err != nil {
		return nil, err
	}
	return f.fs, nil
}

func (f *fakeSMBShare) Umount() error {
	f.umounts++
	return f.errs["umount"]
}

type fakeSMBFile struct {
	fh   *os.File
	errs fakeSMBFileErrs
}

func (f *fakeSMBFile) Read(b []byte) (int, error) { return f.fh.Read(b) }

func (f *fakeSMBFile) Write(b []byte) (int, error) {
	if f.errs.write != nil {
		return 0, f.errs.write
	}
	return f.fh.Write(b)
}

func (f *fakeSMBFile) Sync() error {
	if f.errs.sync != nil {
		return f.errs.sync
	}
	return nil
}

func (f *fakeSMBFile) Close() error {
	_ = f.fh.Close()
	return f.errs.close
}

func (f *fakeSMBFile) Seek(off int64, whence int) (int64, error) {
	if f.errs.seek != nil {
		return 0, f.errs.seek
	}
	return f.fh.Seek(off, whence)
}

type fakeSMBSession struct {
	err     error
	logoffs int
}

func (s *fakeSMBSession) Logoff() error { s.logoffs++; return s.err }

func newFakeSMB(t *testing.T, base string) (*SMBAdapter, *fakeSMBShare, *fakeSMBSession) {
	t.Helper()
	share := &fakeSMBShare{root: t.TempDir(), errs: map[string]error{}}
	sess := &fakeSMBSession{}
	a, err := NewSMBAdapter(SMBConfig{Host: "h", Share: "s", BasePath: base})
	if err != nil {
		t.Fatalf("NewSMBAdapter: %v", err)
	}
	a.connectFn = func() (smbShare, smbSession, error) { return share, sess, nil }
	return a, share, sess
}

var errSMBBoom = errors.New("smb boom")

func TestSMB458_RoundTrip(t *testing.T) {
	t.Parallel()
	a, share, sess := newFakeSMB(t, "base")

	if err := a.Write("dir/file.bin", strings.NewReader("hello world")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := a.WriteFrom("dir/other.bin", func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("xyz")), nil
	}); err != nil {
		t.Fatalf("WriteFrom: %v", err)
	}

	rc, err := a.Read("dir/file.bin")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	got, _ := io.ReadAll(rc)
	if err := rc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if string(got) != "hello world" {
		t.Fatalf("Read = %q", got)
	}

	rr, err := a.ReadRange("dir/file.bin", 6, 3)
	if err != nil {
		t.Fatalf("ReadRange: %v", err)
	}
	got, _ = io.ReadAll(rr)
	_ = rr.Close()
	if string(got) != "wor" {
		t.Fatalf("ReadRange = %q", got)
	}

	if _, err := a.ReadRange("dir/file.bin", 100, 1); err == nil || !strings.Contains(err.Error(), "past EOF") {
		t.Fatalf("ReadRange past EOF: %v", err)
	}

	st, err := a.Stat("dir/file.bin")
	if err != nil || st.Size != 11 || st.IsDir || st.Path != "dir/file.bin" {
		t.Fatalf("Stat = %+v, %v", st, err)
	}

	files, err := a.List("dir")
	if err != nil || len(files) != 2 {
		t.Fatalf("List = %+v, %v", files, err)
	}

	if err := a.TestConnection(); err != nil {
		t.Fatalf("TestConnection: %v", err)
	}

	if err := a.Delete("dir/file.bin"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := a.Delete("dir/other.bin"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := a.RemoveEmptyDir("dir"); err != nil {
		t.Fatalf("RemoveEmptyDir: %v", err)
	}
	if _, err := a.List("dir"); !IsNotExist(err) {
		t.Fatalf("List after removal: want not-exist, got %v", err)
	}
	if share.umounts == 0 || sess.logoffs != share.umounts {
		t.Fatalf("umounts=%d logoffs=%d, want equal and non-zero", share.umounts, sess.logoffs)
	}
}

func TestSMB458_InvalidPaths(t *testing.T) {
	t.Parallel()
	a, _, _ := newFakeSMB(t, "base")
	bad := "../escape"
	if err := a.Write(bad, strings.NewReader("x")); err == nil {
		t.Error("Write: want invalid path error")
	}
	if _, err := a.Read(bad); err == nil {
		t.Error("Read: want invalid path error")
	}
	if _, err := a.ReadRange(bad, 0, 1); err == nil {
		t.Error("ReadRange: want invalid path error")
	}
	if err := a.Delete(bad); err == nil {
		t.Error("Delete: want invalid path error")
	}
	if _, err := a.List(bad); err == nil {
		t.Error("List: want invalid path error")
	}
	if _, err := a.Stat(bad); err == nil {
		t.Error("Stat: want invalid path error")
	}
	if err := a.RemoveEmptyDir(bad); err == nil {
		t.Error("RemoveEmptyDir: want invalid path error")
	}
}

func TestSMB458_WriteFailures(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		setup  func(*fakeSMBShare)
		wantIn string
	}{
		{"mkdir", func(s *fakeSMBShare) { s.errs["mkdirall"] = errSMBBoom }, "mkdir"},
		{"create", func(s *fakeSMBShare) { s.errs["create"] = errSMBBoom }, "create"},
		{"write", func(s *fakeSMBShare) { s.file.write = errSMBBoom }, "write"},
		{"sync", func(s *fakeSMBShare) { s.file.sync = errSMBBoom }, "sync"},
		{"close", func(s *fakeSMBShare) { s.file.close = errSMBBoom }, "close"},
		{"close+remove", func(s *fakeSMBShare) {
			s.file.close = errSMBBoom
			s.errs["remove"] = errors.New("remove failed")
		}, "cleanup remove failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, share, _ := newFakeSMB(t, "")
			tc.setup(share)
			err := a.Write("f.bin", strings.NewReader("data"))
			if err == nil || !strings.Contains(err.Error(), tc.wantIn) {
				t.Fatalf("Write err = %v, want containing %q", err, tc.wantIn)
			}
		})
	}
}

func TestSMB458_ReadFailures(t *testing.T) {
	t.Parallel()
	a, share, _ := newFakeSMB(t, "")
	if _, err := a.Read("missing"); err == nil {
		t.Error("Read missing: want error")
	}
	if err := a.Write("f.bin", strings.NewReader("0123456789")); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ReadRange("missing", 0, 1); err == nil {
		t.Error("ReadRange missing: want stat error")
	}

	share.errs["open"] = errSMBBoom
	if _, err := a.ReadRange("f.bin", 0, 1); !errors.Is(err, errSMBBoom) {
		t.Errorf("ReadRange open err = %v", err)
	}
	delete(share.errs, "open")

	share.file.seek = errSMBBoom
	if _, err := a.ReadRange("f.bin", 1, 1); !errors.Is(err, errSMBBoom) {
		t.Errorf("ReadRange seek err = %v", err)
	}
	share.file.seek = nil

	// Close surfaces file/umount/logoff failures joined together.
	rc, err := a.Read("f.bin")
	if err != nil {
		t.Fatal(err)
	}
	// Open copied the template into a new fakeSMBFile, so inject the close
	// error on the file this reader actually holds.
	rc.(*smbReadCloser).file.(*fakeSMBFile).errs.close = errSMBBoom
	share.errs["umount"] = errors.New("umount failed")
	rc.(*smbReadCloser).session.(*fakeSMBSession).err = errors.New("logoff failed")
	err = rc.Close()
	for _, want := range []string{"close smb file", "umount smb share", "logoff smb session"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Close err = %v, want containing %q", err, want)
		}
	}
}

func TestSMB458_ListStatErrors(t *testing.T) {
	t.Parallel()
	a, share, _ := newFakeSMB(t, "")
	if _, err := a.Stat("missing"); err == nil {
		t.Error("Stat missing: want error")
	}
	share.errs["readdir"] = errSMBBoom
	if _, err := a.List(""); err == nil || IsNotExist(err) {
		t.Errorf("List generic err = %v", err)
	}
	if err := a.TestConnection(); !errors.Is(err, errSMBBoom) {
		t.Errorf("TestConnection err = %v", err)
	}
}

func TestSMB458_GetCapacity(t *testing.T) {
	t.Parallel()
	a, share, _ := newFakeSMB(t, "base")

	share.fs = &fakeSMBFileFsInfo{blockSize: 4096, totalBlocks: 100, availBlocks: 25}
	c, err := a.GetCapacity(context.Background())
	if err != nil || c.TotalBytes != 409600 || c.FreeBytes != 102400 || c.Source != "smb-fsctl" {
		t.Fatalf("GetCapacity = %+v, %v", c, err)
	}

	share.fs = &fakeSMBFileFsInfo{blockSize: 0}
	if _, err := a.GetCapacity(context.Background()); err == nil {
		t.Fatal("GetCapacity zero block size: want error")
	}

	share.errs["statfs"] = errSMBBoom
	c, err = a.GetCapacity(context.Background())
	if err != nil || c.TotalBytes != 0 || c.Source != "smb-fsctl" {
		t.Fatalf("GetCapacity statfs refused = %+v, %v", c, err)
	}
}

func TestSMB458_ConnectFnErrorPropagates(t *testing.T) {
	t.Parallel()
	a, _, _ := newFakeSMB(t, "")
	a.connectFn = func() (smbShare, smbSession, error) { return nil, nil, errSMBBoom }
	if _, err := a.GetCapacity(context.Background()); !errors.Is(err, errSMBBoom) {
		t.Errorf("GetCapacity err = %v", err)
	}
	if err := a.RemoveEmptyDir("d"); !errors.Is(err, errSMBBoom) {
		t.Errorf("RemoveEmptyDir err = %v", err)
	}
}
