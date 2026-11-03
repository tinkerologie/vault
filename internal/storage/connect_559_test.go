package storage

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// silentSFTPServer completes the SSH handshake and accepts the session, then
// never answers the SFTP version exchange.
func silentSFTPServer(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = c.Close() })
			go func() {
				_, chans, reqs, err := ssh.NewServerConn(c, cfg)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(reqs)
				for newCh := range chans {
					ch, chReqs, err := newCh.Accept()
					if err != nil {
						return
					}
					defer ch.Close()
					go func() {
						for r := range chReqs {
							_ = r.Reply(true, nil)
						}
					}()
				}
			}()
		}
	}()
	return ln.Addr().String()
}

// A server that goes quiet at any stage of setup used to hold the dial forever.
func TestDialSFTP_SetupTimesOut(t *testing.T) {
	t.Parallel()
	quiet, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer quiet.Close()
	go func() {
		c, err := quiet.Accept()
		if err == nil {
			defer c.Close()
			time.Sleep(10 * time.Second)
		}
	}()
	for name, addr := range map[string]string{
		"no ssh handshake": quiet.Addr().String(),
		"no sftp version":  silentSFTPServer(t),
	} {
		done := make(chan error, 1)
		go func() {
			_, err := dialSFTP(addr, &ssh.ClientConfig{HostKeyCallback: ssh.InsecureIgnoreHostKey()}, 300*time.Millisecond) // #nosec G106 -- test server
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil {
				t.Errorf("%s: want timeout", name)
			}
		case <-time.After(5 * time.Second):
			t.Errorf("%s: still blocked after the timeout", name)
		}
	}
}

// Closing after a timeout must not wait out a stalled mount, and what that
// mount leaves behind must still be unmounted.
func TestNFSClose_DoesNotWaitForStalledMount(t *testing.T) {
	origMount, origUnmount := mountExport, unmountExport
	t.Cleanup(func() { mountExport, unmountExport = origMount, origUnmount })
	t.Setenv("TMPDIR", t.TempDir())
	entered, release, unmounted := make(chan struct{}), make(chan struct{}), make(chan string, 1)
	mountExport = func(...string) ([]byte, error) {
		close(entered)
		<-release
		return nil, nil
	}
	unmountExport = func(dir string) error { unmounted <- dir; return nil }
	n, err := NewNFSAdapter(NFSConfig{Host: "nas.invalid", Export: "/export", Version: "4"})
	if err != nil {
		t.Fatal(err)
	}
	mounted := make(chan error, 1)
	go func() { mounted <- n.mount() }()
	<-entered
	closed := make(chan error, 1)
	go func() { closed <- n.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close waited for the mount")
	}
	close(release)
	// The caller of the overtaken mount gets an error, not a torn-down adapter.
	if err := <-mounted; !errors.Is(err, errNFSClosed) {
		t.Fatalf("mount = %v", err)
	}
	select {
	case dir := <-unmounted:
		if dir == "" {
			t.Fatal("empty mount dir")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the late mount was never unmounted")
	}
	if n.local != nil || n.mounted {
		t.Fatal("adapter still looks mounted")
	}
	// Only that mount was affected: the adapter can mount again.
	mountExport = func(...string) ([]byte, error) { return nil, nil }
	unmountExport = func(string) error { return nil }
	if err := n.mount(); err != nil {
		t.Fatalf("mount after close: %v", err)
	}
	if err := n.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRunNFSCommand(t *testing.T) {
	if out, err := runNFSCommand(5*time.Second, "sh", "-c", "echo done"); err != nil || !strings.Contains(string(out), "done") {
		t.Fatalf("%q, %v", out, err)
	}
	start := time.Now()
	_, err := runNFSCommand(100*time.Millisecond, "sleep", "30")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("helper was not stopped")
	}
	if _, err := runNFSCommand(5*time.Second, "sh", "-c", "exit 3"); err == nil {
		t.Fatal("failure lost")
	}
}
