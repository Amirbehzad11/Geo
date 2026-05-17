package ws

import (
	"encoding/json"
	"sync"

	"github.com/gorilla/websocket"
)

// Client represents a single WebSocket connection subscribed to a trip.
type Client struct {
	hub     *Hub
	conn    *websocket.Conn
	tripID  string
	send    chan []byte
	once    sync.Once
	onClose func() // called exactly once when the connection closes; may be nil
}

// NewClient allocates a Client attached to hub for the given tripID.
// onClose is an optional callback invoked when the connection is torn down (e.g. to decrement a metrics gauge).
func NewClient(hub *Hub, conn *websocket.Conn, tripID string, onClose func()) *Client {
	return &Client{
		hub:     hub,
		conn:    conn,
		tripID:  tripID,
		send:    make(chan []byte, 256),
		onClose: onClose,
	}
}

// SendJSON queues a JSON payload on the client's send channel (non-blocking).
func (c *Client) SendJSON(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	select {
	case c.send <- data:
	default:
	}
}

// WritePump drains c.send and writes frames to the WebSocket.
// Must run in its own goroutine.
func (c *Client) WritePump() {
	defer c.close()
	for msg := range c.send {
		if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
			return
		}
	}
}

// ReadPump reads from the connection so that gorilla/websocket can process
// control frames (ping/pong/close). Exits on any read error (including normal close).
// Must run in its own goroutine.
func (c *Client) ReadPump() {
	defer c.close()
	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			return
		}
	}
}

// close tears down a client exactly once regardless of which pump exits first.
func (c *Client) close() {
	c.once.Do(func() {
		if c.onClose != nil {
			c.onClose()
		}
		c.hub.Unregister(c.tripID, c)
		close(c.send)
		c.conn.Close()
	})
}
