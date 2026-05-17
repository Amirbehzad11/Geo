package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"geo-service/internal/middleware"
	"geo-service/internal/response"
	"geo-service/internal/ws"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	// Allow all origins — origin checks are handled by the CORS middleware.
	CheckOrigin: func(r *http.Request) bool { return true },
}

// WSHandler upgrades HTTP connections to WebSocket for live trip event streaming.
type WSHandler struct {
	hub *ws.Hub
}

// NewWSHandler creates a WSHandler backed by the given hub.
func NewWSHandler(hub *ws.Hub) *WSHandler {
	return &WSHandler{hub: hub}
}

// HandleConnection handles GET /ws/trip/:id
//
//	@Summary		WebSocket — live trip events
//	@Description	Upgrades the connection to WebSocket and streams real-time `location.updated` and `deviation.detected` events for the specified trip via Redis Pub/Sub. Send any message to keep the connection alive.
//	@Tags			websocket
//	@Param			id	path	int	true	"Trip ID"
//	@Success		101	"Switching Protocols — WebSocket upgrade successful"
//	@Failure		400	{object}	response.Failure	"Missing or invalid trip ID"
//	@Router			/ws/trip/{id} [get]
func (h *WSHandler) HandleConnection(c *gin.Context) {
	tripID := c.Param("id")
	if tripID == "" {
		response.Fail(c, http.StatusBadRequest, "INVALID_TRIP_ID", "trip id is required")
		return
	}

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}

	middleware.WSConnections.Inc()
	client := ws.NewClient(h.hub, conn, tripID, func() {
		middleware.WSConnections.Dec()
	})
	h.hub.Register(tripID, client)

	client.SendJSON(map[string]any{
		"type":    "connected",
		"trip_id": tripID,
	})

	go client.WritePump()
	go client.ReadPump()
}
