package ws

import (
	"context"
	"encoding/json"
	"log"
	"strconv"
	"sync"

	"geo-service/internal/events"
)

// Hub manages WebSocket clients grouped by trip ID.
//
// Horizontal scaling: each unique trip with local subscribers gets exactly ONE
// Redis Pub/Sub goroutine. All geo-service instances subscribe independently,
// so every instance forwards updates to its own connected clients.
type Hub struct {
	mu      sync.RWMutex
	clients map[string]map[*Client]struct{} // tripID -> set of local clients
	closers map[string]func()               // tripID -> cancel+close function
	bus     *events.Bus
}

func NewHub(bus *events.Bus) *Hub {
	return &Hub{
		clients: make(map[string]map[*Client]struct{}),
		closers: make(map[string]func()),
		bus:     bus,
	}
}

// Register adds a client to the hub. Starts a Redis subscription for the trip
// if no local client was previously tracking it.
func (h *Hub) Register(tripID string, c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if _, ok := h.clients[tripID]; !ok {
		h.clients[tripID] = make(map[*Client]struct{})
		h.startSubscription(tripID)
	}
	h.clients[tripID][c] = struct{}{}
}

// Unregister removes a client. Stops the Redis subscription when the last
// local client for this trip disconnects.
func (h *Hub) Unregister(tripID string, c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()

	group, ok := h.clients[tripID]
	if !ok {
		return
	}
	delete(group, c)
	if len(group) == 0 {
		if cancel, ok := h.closers[tripID]; ok {
			cancel()
			delete(h.closers, tripID)
		}
		delete(h.clients, tripID)
	}
}

// startSubscription opens a Redis subscription for tripID and starts the
// forwarding goroutine. Must be called with h.mu held.
func (h *Hub) startSubscription(tripID string) {
	ctx, cancel := context.WithCancel(context.Background())

	tid, err := strconv.ParseInt(tripID, 10, 64)
	if err != nil {
		cancel()
		log.Printf("[ws] invalid trip id %q: %v", tripID, err)
		return
	}

	eventCh, closer := h.bus.SubscribeTrip(ctx, tid)
	h.closers[tripID] = func() {
		cancel()
		closer()
	}

	go h.forwardEvents(tripID, eventCh)
}

// forwardEvents reads from the event channel and broadcasts each
// LocationUpdated payload to all local clients for that trip.
func (h *Hub) forwardEvents(tripID string, eventCh <-chan *events.Event) {
	for ev := range eventCh {
		if ev.Type != events.LocationUpdated && ev.Type != events.DeviationDetected {
			continue
		}
		// Wrap in a thin envelope so clients can distinguish event types.
		envelope, err := json.Marshal(ev)
		if err != nil {
			continue
		}
		h.broadcastLocal(tripID, envelope)
	}
}

// broadcastLocal sends payload to every local client for tripID.
// Slow clients are dropped (non-blocking send) rather than blocking the loop.
func (h *Hub) broadcastLocal(tripID string, payload []byte) {
	h.mu.RLock()
	group, ok := h.clients[tripID]
	if !ok {
		h.mu.RUnlock()
		return
	}
	targets := make([]*Client, 0, len(group))
	for c := range group {
		targets = append(targets, c)
	}
	h.mu.RUnlock()

	for _, c := range targets {
		select {
		case c.send <- payload:
		default:
			// Drop message for lagging client; they'll catch the next update.
		}
	}
}
