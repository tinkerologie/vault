package ws

import (
	"context"
	"log"
	"net/http"
	"strings"
	"sync"

	"github.com/coder/websocket"
)

type Client struct {
	hub  *Hub
	conn *websocket.Conn
	send chan []byte

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
	clients    map[*Client]bool
	broadcast  chan []byte
	register   chan *Client
	unregister chan *Client
	mu         sync.RWMutex

	originPatterns []string
}

func NewHub() *Hub {
	return &Hub{
		clients:    make(map[*Client]bool),
		broadcast:  make(chan []byte, 256),
		register:   make(chan *Client),
		unregister: make(chan *Client),
	}
}

func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			h.mu.Unlock()
		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
			}
			h.mu.Unlock()
		case msg := <-h.broadcast:
			// Take the write lock — the broadcast branch may
			// delete from h.clients (when a client's send buffer
			// is full and we drop them). RLock would race with
			// the map mutation under -race.
			h.mu.Lock()
			for client := range h.clients {
				select {
				case client.send <- msg:
				default:
					close(client.send)
					delete(h.clients, client)
					// Tell the client why it was dropped. It reconnects and
					// resyncs from /runner/status, so the message discarded
					// here — possibly a terminal event — is recovered.
					go client.closeSlow()
				}
			}
			h.mu.Unlock()
		}
	}
}

func (h *Hub) Register(c *Client) {
	h.register <- c
}

func (h *Hub) Unregister(c *Client) {
	h.unregister <- c
}

// Broadcast queues msg for fan-out to every connected client, waiting for
// room if the buffer is full. Use this for events the UI cannot reconstruct
// by waiting — run completion, item failures, queue changes — since dropping
// one can leave the interface showing a job as running forever.
func (h *Hub) Broadcast(msg []byte) {
	h.broadcast <- msg
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

func (h *Hub) HandleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// coder/websocket always accepts same-origin; anything else must be an
		// origin the operator listed. A private-network or .local address says
		// nothing about who controls the page (issue #565).
		OriginPatterns: h.originPatterns,
	})
	if err != nil {
		log.Printf("ws accept error: %v", err)
		return
	}
	client := &Client{hub: h, conn: conn, send: make(chan []byte, 256)}
	h.Register(client)

	go client.writePump()
	go client.readPump()
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
