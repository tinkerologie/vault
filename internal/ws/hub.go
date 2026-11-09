package ws

import (
	"context"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

type Client struct {
	hub    *Hub
	conn   *websocket.Conn
	socket net.Conn
	send   chan []byte

	// closeOnce ensures exactly one of the close paths wins: the hub evicting
	// a slow client, or either pump unwinding. Without it the eviction's
	// status-carrying Close would race the pumps' CloseNow.
	closeOnce sync.Once
}

// closeSlow closes the connection with a policy-violation status so the client
// can tell it was evicted for not keeping up, rather than seeing an abrupt
// drop indistinguishable from a network fault. Mirrors the canonical
// coder/websocket chat example. Call from its own goroutine — writing the
// close frame must not block the hub loop.
func (c *Client) closeSlow() {
	c.closeOnce.Do(func() {
		if c.socket != nil {
			// Bound the whole handshake, including partial data frames
			// discarded after the peer receives our close frame.
			_ = c.socket.SetDeadline(time.Now().Add(5 * time.Second))
		}
		if c.conn != nil {
			_ = c.conn.Close(websocket.StatusPolicyViolation, "connection too slow to keep up with messages")
		}
	})
}

// closeNow tears the connection down without waiting for a close handshake.
// Used by the pumps when they unwind for any other reason.
func (c *Client) closeNow() {
	c.closeOnce.Do(func() {
		if c.conn != nil {
			_ = c.conn.CloseNow()
		}
	})
}

type Hub struct {
	mu        sync.RWMutex
	clients   map[*Client]bool
	broadcast chan []byte
	stopping  chan struct{}
	runOnce   sync.Once
	stopOnce  sync.Once
	sessions  sync.WaitGroup
	evictions sync.WaitGroup

	originPatterns []string
}

func NewHub() *Hub {
	return &Hub{
		clients:   make(map[*Client]bool),
		broadcast: make(chan []byte, 256),
		stopping:  make(chan struct{}),
	}
}

func (h *Hub) Run() {
	h.runOnce.Do(func() {
		for {
			select {
			case <-h.stopping:
				return
			case msg := <-h.broadcast:
				h.fanOut(msg)
			}
		}
	})
}

func (h *Hub) fanOut(msg []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for client := range h.clients {
		select {
		case client.send <- msg:
		default:
			delete(h.clients, client)
			// Let the client see why it must reconnect and resync.
			h.evictions.Go(func() {
				client.closeSlow()
				close(client.send)
			})
		}
	}
}

// Stop closes admissions and connections, then joins fan-out and all pumps.
func (h *Hub) Stop() {
	h.stopOnce.Do(func() {
		h.mu.Lock()
		close(h.stopping)
		clients := h.clients
		h.clients = nil
		h.mu.Unlock()
		for client := range clients {
			close(client.send)
			client.closeNow()
		}
		// Join Run, or prevent a future Run if Stop won the race.
		h.runOnce.Do(func() {})
		h.sessions.Wait()
		h.evictions.Wait()
	})
}

func (h *Hub) Register(c *Client) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.clients == nil {
		return false
	}
	h.clients[c] = true
	return true
}

func (h *Hub) Unregister(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.clients[c] {
		delete(h.clients, c)
		close(c.send)
	}
}

// Broadcast queues msg for fan-out to every connected client, waiting for
// room if the buffer is full. Use this for events the UI cannot reconstruct
// by waiting — run completion, item failures, queue changes — since dropping
// one can leave the interface showing a job as running forever.
func (h *Hub) Broadcast(msg []byte) {
	select {
	case <-h.stopping:
		return
	default:
	}
	select {
	case <-h.stopping:
	case h.broadcast <- msg:
	}
}

// BroadcastLossy queues msg only if there is room, discarding it otherwise.
// Use this for high-frequency, self-superseding telemetry (backup/restore
// progress): the next update carries the same information, so dropping one
// costs nothing, while blocking would stall the caller — in practice the
// single backup goroutine, which must never wait on WebSocket bookkeeping
// (issue #256). This mirrors the per-client fan-out in Run, which already
// drops clients that cannot keep up.
func (h *Hub) BroadcastLossy(msg []byte) {
	select {
	case <-h.stopping:
		return
	default:
	}
	select {
	case h.broadcast <- msg:
	default:
	}
}

// SetAllowedOrigins sets the exact scheme://host[:port] origins accepted besides
// same-origin. Call before serving requests.
func (h *Hub) SetAllowedOrigins(origins []string) {
	h.originPatterns = make([]string, len(origins))
	for i, origin := range origins {
		// Patterns are globs; IPv6 hosts carry brackets that must stay literal.
		h.originPatterns[i] = originGlobEscaper.Replace(origin)
	}
}

var originGlobEscaper = strings.NewReplacer(`\`, `\\`, `[`, `\[`, `]`, `\]`, `*`, `\*`, `?`, `\?`)

type socketContextKey struct{}

// WithSocket attaches the HTTP connection for deadlines after a WS upgrade.
func WithSocket(ctx context.Context, socket net.Conn) context.Context {
	return context.WithValue(ctx, socketContextKey{}, socket)
}

func (h *Hub) HandleWS(w http.ResponseWriter, r *http.Request) {
	// Count the upgrade itself before handing its socket to net/http's
	// hijack path. Stop prevents new additions under the same lock.
	h.mu.Lock()
	if h.clients == nil {
		h.mu.Unlock()
		http.Error(w, "server is shutting down", http.StatusServiceUnavailable)
		return
	}
	h.sessions.Add(1)
	h.mu.Unlock()
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// coder/websocket always accepts same-origin; anything else must be an
		// origin the operator listed. A private-network or .local address says
		// nothing about who controls the page (issue #565).
		OriginPatterns: h.originPatterns,
	})
	if err != nil {
		log.Printf("ws accept error: %v", err)
		h.sessions.Done()
		return
	}
	socket, _ := r.Context().Value(socketContextKey{}).(net.Conn)
	client := &Client{hub: h, conn: conn, socket: socket, send: make(chan []byte, 256)}
	if !h.Register(client) {
		client.closeNow()
		h.sessions.Done()
		return
	}

	go func() {
		defer h.sessions.Done()
		var writer sync.WaitGroup
		writer.Go(client.writePump)
		client.readPump()
		writer.Wait()
	}()
}

func (c *Client) writePump() {
	defer c.closeNow()
	for msg := range c.send {
		if c.conn == nil {
			return
		}
		if err := c.conn.Write(context.Background(), websocket.MessageText, msg); err != nil {
			return
		}
	}
}

func (c *Client) readPump() {
	defer func() {
		c.hub.Unregister(c)
		c.closeNow()
	}()
	for {
		if c.conn == nil {
			return
		}
		if _, _, err := c.conn.Read(context.Background()); err != nil {
			return
		}
	}
}
