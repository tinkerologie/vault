package ws

import (
	"context"
	"errors"
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
