package api

import (
	"bufio"
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/coder/websocket"
)

func TestRuntimeStartupFailureLeavesMountStateAlone(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	for _, tc := range []struct {
		name string
		cfg  ServerConfig
	}{
		{"occupied listener", ServerConfig{Addr: listener.Addr().String()}},
		{"invalid TLS", ServerConfig{Addr: "127.0.0.1:0", TLSCert: "missing.crt", TLSKey: "missing.key"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := testDB(t)
			root := t.TempDir()
			if err := d.SetSetting("fuse_mount_base_dir", root); err != nil {
				t.Fatal(err)
			}
			orphan := filepath.Join(root, "mount-42")
			if err := os.Mkdir(orphan, 0o700); err != nil {
				t.Fatal(err)
			}
			srv := NewServer(d, tc.cfg)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if err := srv.StartWithContext(ctx); err == nil {
				t.Fatal("expected startup error")
			}
			if _, err := os.Stat(orphan); err != nil {
				t.Fatalf("failed startup touched mount state: %v", err)
			}
		})
	}
}

func TestRuntimeWaitsForInflightHTTP(t *testing.T) {
	d := testDB(t)
	root := t.TempDir()
	if err := d.SetSetting("fuse_mount_base_dir", root); err != nil {
		t.Fatal(err)
	}
	synctest.Test(t, func(t *testing.T) {
		s := NewServer(d, ServerConfig{})
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		listener := &runtimePipeListener{accept: make(chan net.Conn), closed: make(chan struct{})}
		entered, release := make(chan struct{}), make(chan struct{})
		unblock := sync.OnceFunc(func() { close(release) })
		defer unblock()
		workerStopped := make(chan struct{})
		server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			close(entered)
			<-release
			w.WriteHeader(http.StatusNoContent)
		})}
		returned := make(chan error, 1)
		go func() {
			returned <- s.serveRuntime(ctx, listener, server, false, func(context.Context) WorkerShutdown {
				return WorkerShutdown{Join: func() { close(workerStopped) }}
			})
		}()
		client, peer := net.Pipe()
		defer client.Close()
		listener.accept <- peer
		if _, err := io.WriteString(client, "GET / HTTP/1.1\r\nHost: localhost\r\n\r\n"); err != nil {
			t.Fatal(err)
		}
		<-entered
		cancel()
		<-listener.closed
		synctest.Wait()
		select {
		case <-workerStopped:
			t.Error("runtime stopped workers while the HTTP handler was active")
		default:
		}
		select {
		case err := <-returned:
			t.Fatalf("runtime returned while the HTTP handler was active: %v", err)
		default:
		}
		unblock()
		resp, err := http.ReadResponse(bufio.NewReader(client), nil)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("status = %d, want 204", resp.StatusCode)
		}
		if err := <-returned; !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("runtime error = %v, want http.ErrServerClosed", err)
		}
		select {
		case <-workerStopped:
		default:
			t.Fatal("runtime returned before worker cleanup")
		}
	})
}

type runtimePipeListener struct {
	accept chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func (l *runtimePipeListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.accept:
		return conn, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *runtimePipeListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *runtimePipeListener) Addr() net.Addr { return &net.TCPAddr{} }

func TestRuntimeClosesHijackedWebSocket(t *testing.T) {
	d := testDB(t)
	if err := d.SetSetting("fuse_mount_base_dir", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	s := NewServer(d, ServerConfig{})
	server := &http.Server{Handler: http.HandlerFunc(s.hub.HandleWS)}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	serving, stop := context.WithCancel(ctx)
	defer stop()
	returned := make(chan error, 1)
	go func() { returned <- s.serveRuntime(serving, listener, server, false, nil) }()

	conn, _, err := websocket.Dial(ctx, "ws://"+listener.Addr().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	stop()
	select {
	case err := <-returned:
		if !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("runtime returned %v, want http.ErrServerClosed", err)
		}
	case <-ctx.Done():
		t.Fatal("runtime waited for the client to close its WebSocket")
	}
	if _, _, err := conn.Read(ctx); err == nil || ctx.Err() != nil {
		t.Fatalf("WebSocket still open after runtime returned: %v", err)
	}
}

func TestRuntimeUnwindsAfterServeFails(t *testing.T) {
	d := testDB(t)
	if err := d.SetSetting("fuse_mount_base_dir", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	s := NewServer(d, ServerConfig{})
	wantErr := errors.New("listener failed")
	listener := &failedRuntimeListener{
		runtimePipeListener: runtimePipeListener{closed: make(chan struct{})},
		err:                 wantErr,
	}
	joined := false
	err := s.serveRuntime(t.Context(), listener, &http.Server{}, false, func(ctx context.Context) WorkerShutdown {
		return WorkerShutdown{Join: func() {
			if ctx.Err() == nil {
				t.Error("worker context was still live during cleanup")
			}
			joined = true
		}}
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("runtime error = %v, want %v", err, wantErr)
	}
	if !joined {
		t.Fatal("runtime returned without joining workers")
	}
}

type failedRuntimeListener struct {
	runtimePipeListener
	err error
}

func (l *failedRuntimeListener) Accept() (net.Conn, error) { return nil, l.err }

func TestRuntimeClosesMCPStreams(t *testing.T) {
	d := testDB(t)
	if err := d.SetSetting("fuse_mount_base_dir", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	s := NewServer(d, ServerConfig{})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	serving, stop := context.WithCancel(ctx)
	defer stop()
	returned := make(chan error, 1)
	go func() {
		returned <- s.serveRuntime(serving, listener, &http.Server{Handler: s.router}, false, nil)
	}()

	url := "http://" + listener.Addr().String() + "/api/v1/mcp"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"runtime-test","version":"1"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	session := resp.Header.Get("Mcp-Session-Id")
	if err != nil || resp.StatusCode != http.StatusOK || session == "" {
		t.Fatalf("initialize status %d, session present %t, read error %v", resp.StatusCode, session != "", err)
	}
	stream, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	stream.Header.Set("Accept", "text/event-stream")
	stream.Header.Set("Mcp-Session-Id", session)
	resp, err = http.DefaultClient.Do(stream)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("stream status %d, content type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}

	// Legacy sessions also support a long-lived POST subscription.
	post := func(body string) *http.Response {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Mcp-Session-Id", session)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	initialized := post(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	_ = initialized.Body.Close()
	if initialized.StatusCode != http.StatusAccepted {
		t.Fatalf("initialized status = %d, want 202", initialized.StatusCode)
	}
	subscription := post(`{"jsonrpc":"2.0","id":2,"method":"subscriptions/listen","params":{"notifications":{"toolsListChanged":true}}}`)
	defer subscription.Body.Close()
	if subscription.StatusCode != http.StatusOK || !strings.HasPrefix(subscription.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("subscription status %d, content type %q", subscription.StatusCode, subscription.Header.Get("Content-Type"))
	}

	stop()
	select {
	case err := <-returned:
		if !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("runtime returned %v, want http.ErrServerClosed", err)
		}
	case <-ctx.Done():
		t.Fatal("open MCP stream blocked runtime shutdown")
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatalf("MCP stream did not end cleanly: %v", err)
	}
	if _, err := io.Copy(io.Discard, subscription.Body); err != nil {
		t.Fatalf("MCP subscription did not end cleanly: %v", err)
	}
}

func TestTimedReadRemainsOwnedAfterResponse(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := &Server{}
		release := make(chan struct{})
		unblock := sync.OnceFunc(func() { close(release) })
		defer unblock()
		handler := s.trackTimedRead(func(http.ResponseWriter, *http.Request) {
			<-release
		}, time.Second, "test timeout")
		rec := httptest.NewRecorder()
		handler(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", rec.Code)
		}
		joined := make(chan struct{})
		go func() {
			s.timedReads.Wait()
			close(joined)
		}()
		synctest.Wait()
		select {
		case <-joined:
			t.Fatal("runtime lost ownership of the active read after its response timed out")
		default:
		}
		unblock()
		<-joined
	})
}

func TestRuntimeStartsWithValidTLS(t *testing.T) {
	d := testDB(t)
	if err := d.SetSetting("fuse_mount_base_dir", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	peer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	cert := peer.TLS.Certificates[0]
	peer.Close()
	key, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0o600); err != nil {
		t.Fatal(err)
	}

	s := NewServer(d, ServerConfig{Addr: "127.0.0.1:0", TLSCert: certPath, TLSKey: keyPath})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	err = s.Run(ctx, func(context.Context) WorkerShutdown {
		cancel()
		return WorkerShutdown{}
	})
	if !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("TLS runtime returned %v, want http.ErrServerClosed", err)
	}
	if err := s.Run(ctx, nil); err == nil || errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("second start returned %v, want single-use error", err)
	}
}

func TestRunnerDrainContinuesAfterGracePeriod(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		drainRunner(t.Context(), func(ctx context.Context) error {
			calls++
			if calls == 1 {
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("first wait has no grace period")
				}
				<-ctx.Done()
				return ctx.Err()
			}
			if _, ok := ctx.Deadline(); ok {
				t.Fatal("final join must wait even after the grace period")
			}
			return nil
		})
		if calls != 2 {
			t.Fatalf("drain called %d times, want a grace period followed by a join", calls)
		}
	})
}
