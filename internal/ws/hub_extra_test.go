package ws

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestUnregisterRegistered exercises the Unregister branch where the
// client is currently in the map.
func TestUnregisterRegistered(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	ch := make(chan []byte, 1)
	client := &Client{hub: hub, send: ch}
	hub.Register(client)
	time.Sleep(10 * time.Millisecond)

	hub.mu.RLock()
	registered := hub.clients[client]
	hub.mu.RUnlock()
	if !registered {
		t.Fatal("client was not registered")
	}

	hub.Unregister(client)
	// Wait for the goroutine to process the unregister.
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		hub.mu.RLock()
		_, ok := hub.clients[client]
		hub.mu.RUnlock()
		if !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("client still present after Unregister")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// send channel should be closed; reading from it yields ok=false.
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("send channel returned a value rather than being closed")
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("send channel not closed promptly after Unregister")
	}
}

// TestUnregisterUnknown exercises the branch where Unregister is called
// for a client that was never registered. Must not panic and must not
// close the stranger's send channel.
func TestUnregisterUnknown(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	stranger := &Client{hub: hub, send: make(chan []byte, 1)}
	hub.Unregister(stranger) // must not panic / close on stranger.send
	time.Sleep(20 * time.Millisecond)

	// stranger.send must still be open.
	select {
	case stranger.send <- []byte("ok"):
	case <-time.After(50 * time.Millisecond):
		t.Error("stranger.send unusable after Unregister of non-member")
	}
}

// TestHandleWSEndToEnd connects a coder/websocket client to HandleWS and
// verifies Broadcast() reaches the wire, covering writePump + readPump
// transitively. Uses an httptest server so no real network egress occurs.
func TestHandleWSEndToEnd(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	srv := httptest.NewServer(http.HandlerFunc(hub.HandleWS))
	defer srv.Close()

	// Convert http://… to ws://…
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.CloseNow()

	// Wait for server-side Register to land in the hub map.
	deadline := time.Now().Add(time.Second)
	for {
		hub.mu.RLock()
		n := len(hub.clients)
		hub.mu.RUnlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not register client, clients=%d", n)
		}
		time.Sleep(5 * time.Millisecond)
	}

	hub.Broadcast([]byte(`{"hello":"world"}`))

	readCtx, readCancel := context.WithTimeout(ctx, time.Second)
	defer readCancel()
	mt, data, err := conn.Read(readCtx)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if mt != websocket.MessageText {
		t.Errorf("message type = %v, want Text", mt)
	}
	if string(data) != `{"hello":"world"}` {
		t.Errorf("payload = %q, want %q", string(data), `{"hello":"world"}`)
	}

	// Close the client side; the server's readPump should see the close
	// frame, call Unregister, and exit. Wait for the map to shrink to 0
	// so writePump's `for msg := range c.send` loop also exits — covering
	// both pumps.
	_ = conn.Close(websocket.StatusNormalClosure, "bye")
	deadline = time.Now().Add(time.Second)
	for {
		hub.mu.RLock()
		n := len(hub.clients)
		hub.mu.RUnlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not unregister after client close, clients=%d", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestHandleWSBadOrigin exercises the error path in HandleWS when
// websocket.Accept fails (cross-origin without matching OriginPatterns).
func TestHandleWSBadOrigin(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	srv := httptest.NewServer(http.HandlerFunc(hub.HandleWS))
	defer srv.Close()

	// Build a raw HTTP request with an unrelated Origin so Accept rejects.
	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	req.Header.Set("Origin", "https://evil.example.com")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	// On rejected origin, Accept writes a 403 (and the handler returns).
	if resp.StatusCode == http.StatusSwitchingProtocols {
		t.Errorf("expected upgrade refusal, got 101")
	}
}
