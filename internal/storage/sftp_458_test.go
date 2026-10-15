package storage

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

const (
	sftp458User     = "vault"
	sftp458Password = "secret"
)

// sftp458Server is an in-process SSH server (loopback listener only) that
// serves the sftp subsystem from the local filesystem. It lets the adapter's
// real dial path (ssh.Dial + sftp.NewClient) and every operation run end to
// end without an external service.
type sftp458Server struct {
	port        int
	fingerprint string
	clientPriv  ed25519.PrivateKey
}

func startSFTP458Server(t *testing.T) *sftp458Server {
	t.Helper()
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("host key: %v", err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatalf("host signer: %v", err)
	}
	_, clientPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("client key: %v", err)
	}
	clientSigner, err := ssh.NewSignerFromKey(clientPriv)
	if err != nil {
		t.Fatalf("client signer: %v", err)
	}
	authorized := clientSigner.PublicKey().Marshal()

	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if c.User() == sftp458User && string(pw) == sftp458Password {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
		PublicKeyCallback: func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if c.User() == sftp458User && bytes.Equal(key.Marshal(), authorized) {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
	}
	cfg.AddHostKey(hostSigner)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var (
		mu    sync.Mutex
		conns []net.Conn
		wg    sync.WaitGroup
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				serveSFTP458Conn(c, cfg)
			}()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		for _, c := range conns {
			_ = c.Close()
		}
		mu.Unlock()
		wg.Wait()
	})

	sum := sha256.Sum256(hostSigner.PublicKey().Marshal())
	return &sftp458Server{
		port:        ln.Addr().(*net.TCPAddr).Port,
		fingerprint: "SHA256:" + hex.EncodeToString(sum[:]),
		clientPriv:  clientPriv,
	}
}

func serveSFTP458Conn(c net.Conn, cfg *ssh.ServerConfig) {
	_, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		_ = c.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	var wg sync.WaitGroup
	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			_ = newCh.Reject(ssh.UnknownChannelType, "unsupported")
			continue
		}
		ch, chReqs, err := newCh.Accept()
		if err != nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { _ = ch.Close() }()
			for req := range chReqs {
				ok := req.Type == "subsystem" && len(req.Payload) > 4 && string(req.Payload[4:]) == "sftp"
				_ = req.Reply(ok, nil)
				if !ok {
					continue
				}
				srv, err := sftp.NewServer(ch)
				if err != nil {
					return
				}
				_ = srv.Serve()
				_ = srv.Close()
				return
			}
		}()
	}
	wg.Wait()
}

func (s *sftp458Server) adapter(t *testing.T, base string) *SFTPAdapter {
	t.Helper()
	a, err := NewSFTPAdapter(SFTPConfig{
		Host:     "127.0.0.1",
		Port:     s.port,
		User:     sftp458User,
		Password: sftp458Password,
		BasePath: base,
		HostKey:  s.fingerprint,
	})
	if err != nil {
		t.Fatalf("NewSFTPAdapter: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a
}

func TestSFTP458_RoundTripOverSSH(t *testing.T) {
	t.Parallel()
	srv := startSFTP458Server(t)
	base := t.TempDir()
	a := srv.adapter(t, base)

	if err := a.TestConnection(); err != nil {
		t.Fatalf("TestConnection: %v", err)
	}
	if err := a.Write("job/run/data.bin", strings.NewReader("hello sftp world")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := a.WriteFrom("job/run/meta.json", func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("{}")), nil
	}); err != nil {
		t.Fatalf("WriteFrom: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(base, "job", "run", "data.bin")); err != nil || string(b) != "hello sftp world" {
		t.Fatalf("on-disk content = %q, %v", b, err)
	}

	rc, err := a.Read("job/run/data.bin")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	got, err := io.ReadAll(rc)
	if err != nil || string(got) != "hello sftp world" {
		t.Fatalf("Read content = %q, %v", got, err)
	}
	if err := rc.Close(); err != nil {
		t.Fatalf("Read Close: %v", err)
	}

	rr, err := a.ReadRange("job/run/data.bin", 6, 4)
	if err != nil {
		t.Fatalf("ReadRange: %v", err)
	}
	got, _ = io.ReadAll(rr)
	if err := rr.Close(); err != nil {
		t.Fatalf("ReadRange Close: %v", err)
	}
	if string(got) != "sftp" {
		t.Fatalf("ReadRange = %q", got)
	}
	if _, err := a.ReadRange("job/run/data.bin", 1000, 1); err == nil || !strings.Contains(err.Error(), "past EOF") {
		t.Fatalf("ReadRange past EOF: %v", err)
	}

	st, err := a.Stat("job/run/data.bin")
	if err != nil || st.Size != 16 || st.IsDir || st.Path != "job/run/data.bin" {
		t.Fatalf("Stat = %+v, %v", st, err)
	}
	files, err := a.List("job/run")
	if err != nil || len(files) != 2 {
		t.Fatalf("List = %+v, %v", files, err)
	}

	c, err := a.GetCapacity(context.Background())
	if err != nil || c.Source != "sftp-statvfs" {
		t.Fatalf("GetCapacity = %+v, %v", c, err)
	}

	if err := a.RemoveEmptyDir("job/run"); err == nil {
		t.Fatal("RemoveEmptyDir on non-empty dir: want error")
	}
	if err := a.Delete("job/run/data.bin"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := a.Delete("job/run/meta.json"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := a.RemoveEmptyDir("job/run"); err != nil {
		t.Fatalf("RemoveEmptyDir: %v", err)
	}

	// Not-found classification on every read-side operation.
	if _, err := a.Stat("job/run/data.bin"); !isSFTPNotFound(err) {
		t.Errorf("Stat missing: want not-found, got %v", err)
	}
	if _, err := a.Read("job/run/data.bin"); !isSFTPNotFound(err) {
		t.Errorf("Read missing: want not-found, got %v", err)
	}
	if _, err := a.ReadRange("job/run/data.bin", 0, 1); !isSFTPNotFound(err) {
		t.Errorf("ReadRange missing: want not-found, got %v", err)
	}
	if err := a.Delete("job/run/data.bin"); !isSFTPNotFound(err) {
		t.Errorf("Delete missing: want not-found, got %v", err)
	}
	if _, err := a.List("job/run"); !IsNotExist(err) {
		t.Errorf("List missing: want not-exist, got %v", err)
	}
}

func TestSFTP458_WriteFailures(t *testing.T) {
	t.Parallel()
	srv := startSFTP458Server(t)
	base := t.TempDir()
	a := srv.adapter(t, base)

	// MkdirAll fails: a regular file sits where the parent dir must go.
	if err := os.WriteFile(filepath.Join(base, "blocker"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.Write("blocker/child.bin", strings.NewReader("x")); err == nil || !strings.Contains(err.Error(), "mkdir") {
		t.Errorf("Write under file: want mkdir error, got %v", err)
	}

	// Create fails: the target is an existing directory.
	if err := os.MkdirAll(filepath.Join(base, "adir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := a.Write("adir", strings.NewReader("x")); err == nil || !strings.Contains(err.Error(), "create") {
		t.Errorf("Write onto dir: want create error, got %v", err)
	}

	// Copy fails: the source reader errors; the partial file is removed.
	boom := errors.New("reader boom")
	if err := a.Write("partial.bin", io.MultiReader(strings.NewReader("abc"), iotestErrReader{boom})); !errors.Is(err, boom) {
		t.Errorf("Write with failing reader: want %v, got %v", boom, err)
	}
	if _, err := os.Stat(filepath.Join(base, "partial.bin")); !os.IsNotExist(err) {
		t.Errorf("partial file not cleaned up: %v", err)
	}
}

type iotestErrReader struct{ err error }

func (r iotestErrReader) Read([]byte) (int, error) { return 0, r.err }

func TestSFTP458_InvalidPathsRejected(t *testing.T) {
	t.Parallel()
	srv := startSFTP458Server(t)
	a := srv.adapter(t, t.TempDir())
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
	// The adapter must still work after every rejected call.
	if err := a.TestConnection(); err != nil {
		t.Fatalf("TestConnection after rejections: %v", err)
	}
}

func TestSFTP458_KeyFileAuth(t *testing.T) {
	t.Parallel()
	srv := startSFTP458Server(t)
	keyPath := filepath.Join(t.TempDir(), "id_ed25519")
	pemBlock, err := ssh.MarshalPrivateKey(srv.clientPriv, "")
	if err != nil {
		t.Fatalf("MarshalPrivateKey: %v", err)
	}
	if err := os.WriteFile(keyPath, encodePEM(pemBlock), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := NewSFTPAdapter(SFTPConfig{
		Host: "127.0.0.1", Port: srv.port, User: sftp458User,
		KeyFile: keyPath, BasePath: t.TempDir(), HostKey: srv.fingerprint,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	if err := a.TestConnection(); err != nil {
		t.Fatalf("TestConnection with key auth: %v", err)
	}
}

func TestSFTP458_AuthAndHostKeyFailures(t *testing.T) {
	t.Parallel()
	srv := startSFTP458Server(t)

	wrongPW, err := NewSFTPAdapter(SFTPConfig{
		Host: "127.0.0.1", Port: srv.port, User: sftp458User, Password: "nope",
		HostKey: srv.fingerprint,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := wrongPW.TestConnection(); err == nil || !strings.Contains(err.Error(), "ssh dial") {
		t.Errorf("wrong password: want ssh dial error, got %v", err)
	}

	wrongHost, err := NewSFTPAdapter(SFTPConfig{
		Host: "127.0.0.1", Port: srv.port, User: sftp458User, Password: sftp458Password,
		HostKey: "SHA256:" + strings.Repeat("00", 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := wrongHost.TestConnection(); err == nil || !strings.Contains(err.Error(), "host key mismatch") {
		t.Errorf("wrong host key: want mismatch error, got %v", err)
	}
}

func TestSFTP458_ClosedAdapterRejectsOperations(t *testing.T) {
	t.Parallel()
	srv := startSFTP458Server(t)
	a := srv.adapter(t, t.TempDir())
	if err := a.TestConnection(); err != nil { // leaves one idle pooled conn
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil { // idempotent
		t.Fatal(err)
	}
	checks := map[string]error{
		"Write":          a.Write("x", strings.NewReader("x")),
		"Delete":         a.Delete("x"),
		"RemoveEmptyDir": a.RemoveEmptyDir("x"),
		"TestConnection": a.TestConnection(),
	}
	_, checks["Read"] = a.Read("x")
	_, checks["ReadRange"] = a.ReadRange("x", 0, 1)
	_, checks["Stat"] = a.Stat("x")
	_, checks["List"] = a.List("")
	_, checks["GetCapacity"] = a.GetCapacity(context.Background())
	for op, err := range checks {
		if !errors.Is(err, errSFTPPoolClosed) {
			t.Errorf("%s after Close: want errSFTPPoolClosed, got %v", op, err)
		}
	}
}

// ---- symlink containment via a request server with a custom RealPath ----

// realPathLister overrides RealPath on an in-memory sftp handler so the
// containment walk in verifyRemoteNoSymlinkEscape can be steered.
type realPathLister struct {
	sftp.FileLister
	fn func(string) (string, error)
}

func (l realPathLister) RealPath(p string) (string, error) { return l.fn(p) }

func newRealPathSFTP(t *testing.T, base string, fn func(string) (string, error)) *SFTPAdapter {
	t.Helper()
	h := sftp.InMemHandler()
	h.FileList = realPathLister{FileLister: h.FileList, fn: fn}
	dial := func() (sftpConn, error) {
		cConn, sConn := net.Pipe()
		srv := sftp.NewRequestServer(sConn, h)
		go func() { _ = srv.Serve() }()
		t.Cleanup(func() { _ = srv.Close() })
		client, err := sftp.NewClientPipe(cConn, cConn)
		if err != nil {
			return nil, err
		}
		return &sftpConnection{sftp: client}, nil
	}
	a := &SFTPAdapter{config: SFTPConfig{BasePath: base}, pool: newSFTPPool(1, dial)}
	t.Cleanup(func() { _ = a.Close() })
	return a
}

func TestSFTP458_SymlinkContainment(t *testing.T) {
	t.Parallel()

	t.Run("escape is rejected", func(t *testing.T) {
		t.Parallel()
		a := newRealPathSFTP(t, "/base", func(p string) (string, error) {
			if p == "/base" {
				return "/base", nil
			}
			return "/etc/shadow", nil
		})
		if err := a.Write("evil", strings.NewReader("x")); err == nil || !strings.Contains(err.Error(), "outside the destination root") {
			t.Fatalf("Write via escaping symlink: got %v", err)
		}
		if _, err := a.Read("evil"); err == nil {
			t.Error("Read: want escape error")
		}
		if _, err := a.ReadRange("evil", 0, 1); err == nil {
			t.Error("ReadRange: want escape error")
		}
		if err := a.Delete("evil"); err == nil {
			t.Error("Delete: want escape error")
		}
		if _, err := a.List("evil"); err == nil {
			t.Error("List: want escape error")
		}
		if _, err := a.Stat("evil"); err == nil {
			t.Error("Stat: want escape error")
		}
		if err := a.RemoveEmptyDir("evil"); err == nil {
			t.Error("RemoveEmptyDir: want escape error")
		}
	})

	t.Run("unresolvable base proceeds", func(t *testing.T) {
		t.Parallel()
		a := newRealPathSFTP(t, "/base", func(string) (string, error) {
			return "", errors.New("realpath refused")
		})
		if err := a.Write("ok.bin", strings.NewReader("data")); err != nil {
			t.Fatalf("Write with REALPATH refused: %v", err)
		}
	})

	t.Run("walks up to the nearest existing ancestor", func(t *testing.T) {
		t.Parallel()
		a := newRealPathSFTP(t, "/base", func(p string) (string, error) {
			if p == "/base" {
				return "/base", nil
			}
			return "", errors.New("no such file")
		})
		if err := a.Write("new/deep/file.bin", strings.NewReader("data")); err != nil {
			t.Fatalf("Write fresh path: %v", err)
		}
	})

	t.Run("walk reaching root accepts a fresh path", func(t *testing.T) {
		t.Parallel()
		var calls atomic.Int32
		a := newRealPathSFTP(t, "/base", func(p string) (string, error) {
			if calls.Add(1) == 1 && p == "/base" {
				return "/base", nil
			}
			return "", errors.New("no such file")
		})
		if err := a.Write("fresh.bin", strings.NewReader("data")); err != nil {
			t.Fatalf("Write: %v", err)
		}
	})
}

// ---- pool edge cases --------------------------------------------------

func TestSFTP458_PoolEdgeCases(t *testing.T) {
	t.Parallel()
	p := newSFTPPool(0, func() (sftpConn, error) { return &fakeSFTPConn{}, nil })
	if cap(p.sem) != 1 {
		t.Fatalf("size<1 should clamp to 1, got %d", cap(p.sem))
	}
	c, err := p.get()
	if err != nil {
		t.Fatal(err)
	}
	p.closeAll()
	p.put(c, nil) // returned after close => closed, not pooled
	if !c.(*fakeSFTPConn).closed {
		t.Error("conn returned after closeAll should be closed")
	}

	p2 := newSFTPPool(1, func() (sftpConn, error) { return nil, errors.New("dial failed") })
	if _, err := p2.get(); err == nil {
		t.Fatal("want dial error")
	}
	// The failed dial must release its slot.
	p2.dial = func() (sftpConn, error) { return &fakeSFTPConn{}, nil }
	c2, err := p2.get()
	if err != nil {
		t.Fatalf("get after failed dial: %v", err)
	}
	p2.put(nil, nil) // nil conn just releases the slot
	_ = c2
}
