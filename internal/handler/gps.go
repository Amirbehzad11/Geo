package handler

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"geo-service/internal/middleware"
	"geo-service/internal/model"
	"geo-service/internal/response"
	"geo-service/internal/service"
)

// GPSHandler exposes the live GPS update endpoints.
type GPSHandler struct {
	svc *service.GPSService
}

// NewGPSHandler creates a GPSHandler backed by the given service.
func NewGPSHandler(svc *service.GPSService) *GPSHandler {
	return &GPSHandler{svc: svc}
}

// Update handles POST /gps/update
//
//	@Summary		Process a GPS update
//	@Description	Accepts a raw GPS position for an active trip. Applies rate limiting, EMA smoothing (α=0.75), speed computation, and cross-track deviation detection against the planned route. Writes state to Redis and broadcasts events via Redis Pub/Sub.
//	@Tags			gps
//	@Accept			json
//	@Produce		json
//	@Param			update	body		model.GPSUpdate									true	"GPS update payload"
//	@Success		200		{object}	response.Success{data=model.LocationState}		"Processed location state"
//	@Failure		400		{object}	response.Failure								"Malformed JSON body"
//	@Failure		422		{object}	response.Failure								"Validation error"
//	@Failure		429		{object}	response.Failure								"Rate limited — update received too soon"
//	@Failure		500		{object}	response.Failure								"Internal processing error"
//	@Router			/gps/update [post]
func (h *GPSHandler) Update(c *gin.Context) {
	var update model.GPSUpdate
	if err := c.ShouldBindJSON(&update); err != nil {
		response.Fail(c, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}

	if update.TripID <= 0 {
		response.ValidationFail(c, "trip_id must be a positive integer")
		return
	}
	if update.Timestamp <= 0 {
		response.ValidationFail(c, "timestamp must be a positive Unix epoch (seconds)")
		return
	}
	if !validateCoords(c, update.Lat, update.Lng) {
		return
	}

	state, err := h.svc.ProcessUpdate(c.Request.Context(), &update)
	if err != nil {
		status, code, message := mapServiceError(err)
		if status >= 500 {
			slog.Error("gps update failed", "err", err, "trip_id", update.TripID)
		}
		response.Fail(c, status, code, message)
		return
	}

	middleware.GPSUpdateTotal.Inc()
	response.OK(c, state)
}

// GetLocation handles GET /gps/trip/:id/location
//
//	@Summary		Get latest trip location
//	@Description	Returns the most recent GPS state for a trip, read from Redis. State is cached for 24 hours after the last update.
//	@Tags			gps
//	@Produce		json
//	@Param			id	path		int											true	"Trip ID"
//	@Success		200	{object}	response.Success{data=model.LocationState}	"Latest location state"
//	@Failure		400	{object}	response.Failure							"Invalid trip ID"
//	@Failure		404	{object}	response.Failure							"No location found for this trip"
//	@Router			/gps/trip/{id}/location [get]
func (h *GPSHandler) GetLocation(c *gin.Context) {
	tripID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || tripID <= 0 {
		response.Fail(c, http.StatusBadRequest, "INVALID_TRIP_ID", "trip id must be a positive integer")
		return
	}

	state, err := h.svc.GetLocation(c.Request.Context(), tripID)
	if err != nil {
		response.Fail(c, http.StatusNotFound, "NOT_FOUND", "no location found for this trip")
		return
	}

	response.OK(c, state)
}
