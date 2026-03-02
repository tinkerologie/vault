package ws

import (
	"context"
	"log"
	"net/http"
	"sync"

	"github.com/coder/websocket"
)

type Client struct {
	hub  *Hub
	conn *websocket.Conn
	send chan []byte
}

type Hub struct {
	clients    map[*Client]bool
	broadcast  chan []byte
	register   chan *Client
	unregister chan *Client
	mu         sync.RWMutex
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
			h.mu.RLock()
			for client := range h.clients {
				select {
				case client.send <- msg:
				default:
					close(client.send)
					delete(h.clients, client)
				}
			}
			h.mu.RUnlock()
		}
	}
}

func (h *Hub) Register(c *Client) {
	h.register <- c
}

func (h *Hub) Unregister(c *Client) {
	h.unregister <- c
}

func (h *Hub) Broadcast(msg []byte) {
	h.broadcast <- msg
}

func (h *Hub) HandleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Validate origin: allow same-origin and local network connections.
		// The coder/websocket library rejects cross-origin by default when
		// InsecureSkipVerify is false, but we use OriginPatterns to allow
		// common local access patterns.
		OriginPatterns: []string{
			"localhost:*",
			"127.0.0.1:*",
			"*.local:*",
			"192.168.*.*:*",
			"10.*.*.*:*",
			"172.16.*.*:*",
		},
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
	defer func() {
		if c.conn != nil {
			_ = c.conn.CloseNow()
		}
	}()
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
		if c.conn != nil {
			_ = c.conn.CloseNow()
		}
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
