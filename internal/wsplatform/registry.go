package wsplatform

import (
	"context"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Closer is implemented by active WebSocket sessions for graceful shutdown.
type Closer interface {
	CloseWithReason(reason string)
}

// Registry tracks live connections for coordinated drain on shutdown.
type Registry struct {
	mu    sync.Mutex
	conns map[string]Closer
}

func NewRegistry() *Registry {
	return &Registry{conns: make(map[string]Closer)}
}

func (r *Registry) Register(id string, c Closer) {
	r.mu.Lock()
	r.conns[id] = c
	r.mu.Unlock()
}

func (r *Registry) Unregister(id string) {
	r.mu.Lock()
	delete(r.conns, id)
	r.mu.Unlock()
}

func (r *Registry) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.conns)
}

// Drain closes all connections with a normal close frame and waits up to timeout.
func (r *Registry) Drain(ctx context.Context, reason string) {
	r.mu.Lock()
	targets := make([]Closer, 0, len(r.conns))
	for _, c := range r.conns {
		targets = append(targets, c)
	}
	r.mu.Unlock()

	done := make(chan struct{})
	go func() {
		for _, c := range targets {
			c.CloseWithReason(reason)
		}
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
	}
}

// CloseGracefully sends a WebSocket close frame before tearing down the TCP conn.
func CloseGracefully(conn *websocket.Conn, code int, reason string) {
	if conn == nil {
		return
	}
	msg := websocket.FormatCloseMessage(code, reason)
	_ = conn.WriteControl(websocket.CloseMessage, msg, time.Now().Add(2*time.Second))
	_ = conn.Close()
}
