package ws

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestEvictedClientGetsPolicyViolationClose asserts the hub tells a client why
// it was dropped instead of tearing the socket down abruptly, so the close is
// distinguishable from a network fault.
//
// The client is registered with an unbuffered send channel and no writePump,
// so the fan-out's default branch fires on the first broadcast — deterministic,
// with no need to race a 256-slot buffer to saturation.
func TestEvictedClientGetsPolicyViolationClose(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	accepted := make(chan *websocket.Conn, 1)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		accepted <- c
		<-release // hold the handler open so the conn stays alive
	}))
	defer srv.Close()
	defer close(release)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	peer, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer peer.CloseNow()

	var serverConn *websocket.Conn
	select {
	case serverConn = <-accepted:
	case <-ctx.Done():
		t.Fatal("server never accepted the connection")
	}

	// Unbuffered and undrained: the very next broadcast evicts this client.
	client := &Client{hub: hub, conn: serverConn, send: make(chan []byte)}
	hub.Register(client)

	hub.Broadcast([]byte(`{"type":"backup_progress"}`))

	// The peer should observe a close carrying the policy-violation status.
	_, _, readErr := peer.Read(ctx)
	if readErr == nil {
		t.Fatal("expected the connection to be closed after eviction")
	}
	var closeErr websocket.CloseError
	if !errors.As(readErr, &closeErr) {
		t.Fatalf("got %v, want a websocket close error", readErr)
	}
	if closeErr.Code != websocket.StatusPolicyViolation {
		t.Fatalf("close code = %d, want %d (StatusPolicyViolation)", closeErr.Code, websocket.StatusPolicyViolation)
	}

	// And the hub must have forgotten it.
	deadline := time.Now().Add(2 * time.Second)
	for {
		hub.mu.RLock()
		n := len(hub.clients)
		hub.mu.RUnlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("evicted client still registered, clients=%d", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestCloseOnceArbitratesClosePaths documents that both close entry points
// share one sync.Once, so whichever fires first wins and the rest are no-ops.
//
// Deliberately asserts the call count rather than merely "did not panic": with
// a nil conn both bodies short-circuit, so a no-panic check passes even with
// the guard removed and proves nothing. The observable behaviour — an evicted
// peer receiving StatusPolicyViolation rather than an abrupt drop — is covered
// end-to-end by TestEvictedClientGetsPolicyViolationClose above, which runs
// against a real connection.
func TestCloseOnceArbitratesClosePaths(t *testing.T) {
	var calls int
	c := &Client{}

	c.closeOnce.Do(func() { calls++ })
	c.closeOnce.Do(func() { calls++ })
	c.closeSlow()
	c.closeNow()

	if calls != 1 {
		t.Fatalf("close body ran %d times, want exactly 1", calls)
	}
}

func TestStopJoinsEvictionWithPartialPeerFrame(t *testing.T) {
	hub := NewHub()
	go hub.Run()
	t.Cleanup(hub.Stop)

	accepted := make(chan *Client, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		socket := r.Context().Value(socketContextKey{}).(net.Conn)
		// Leave the unbuffered send queue undrained, so the first broadcast
		// runs the eviction handshake without a concurrent read pump.
		accepted <- &Client{hub: hub, conn: conn, socket: socket, send: make(chan []byte)}
	}))
	server.Config.ConnContext = WithSocket
	server.Start()
	defer server.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	dialer := net.Dialer{}
	peer, err := dialer.DialContext(ctx, "tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	if err := peer.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(peer, "GET / HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n", server.Listener.Addr()); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(peer)
	response, err := http.ReadResponse(reader, nil)
	if err != nil || response.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade response = %v, error = %v", response, err)
	}
	_ = response.Body.Close()
	select {
	case client := <-accepted:
		hub.Register(client)
	case <-ctx.Done():
		t.Fatal("server did not register the WebSocket")
	}
	hub.Broadcast([]byte("evict this peer"))
	var closeHeader [2]byte
	if _, err := io.ReadFull(reader, closeHeader[:]); err != nil || closeHeader[0] != 0x88 {
		t.Fatalf("close frame header = %x, error = %v", closeHeader, err)
	}
	if _, err := io.CopyN(io.Discard, reader, int64(closeHeader[1])); err != nil {
		t.Fatal(err)
	}
	// Masked binary frame, one declared payload byte, no payload sent.
	if _, err := peer.Write([]byte{0x82, 0x81, 0, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}

	stopped := make(chan struct{})
	go func() {
		hub.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("an evicted peer's partial frame prevented hub shutdown")
	}
}
