package gps

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"geo-service/internal/middleware"
	"geo-service/internal/response"
)

// GPSHandler exposes the live GPS update endpoints.
type GPSHandler struct {
	svc   *GPSService
	authz TripAuthorizer
}

// NewGPSHandler creates a GPSHandler backed by the given service.
func NewGPSHandler(svc *GPSService, authorizers ...TripAuthorizer) *GPSHandler {
	h := &GPSHandler{svc: svc}
	if len(authorizers) > 0 {
		h.authz = authorizers[0]
	}
	return h
}

type TripAuthorizer interface {
	UserOwnsTrip(ctx context.Context, userID, tripID int64) (bool, error)
	UserCanAccessTrip(ctx context.Context, userID, tripID int64) (bool, error)
}

// Update handles POST /gps/update
//
//	@Summary		Process a GPS update
//	@Description	Accepts a raw GPS position for an active trip. Applies rate limiting, EMA smoothing (α=0.75), speed computation, and cross-track deviation detection against the planned route. Writes state to Redis and broadcasts events via Redis Pub/Sub.
//	@Tags			gps
//	@Accept			json
//	@Produce		json
//	@Param			update	body		GPSUpdate									true	"GPS update payload"
//	@Success		200		{object}	response.Success{data=LocationState}		"Processed location state"
//	@Failure		400		{object}	response.Failure								"Malformed JSON body"
//	@Failure		422		{object}	response.Failure								"Validation error"
//	@Failure		429		{object}	response.Failure								"Rate limited — update received too soon"
//	@Failure		500		{object}	response.Failure								"Internal processing error"
//	@Router			/gps/update [post]
func (h *GPSHandler) Update(c *gin.Context) {
	var update GPSUpdate
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
	if !h.authorizeTripWrite(c, update.TripID) {
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
//	@Success		200	{object}	response.Success{data=LocationState}	"Latest location state"
//	@Failure		400	{object}	response.Failure							"Invalid trip ID"
//	@Failure		404	{object}	response.Failure							"No location found for this trip"
//	@Router			/gps/trip/{id}/location [get]
func (h *GPSHandler) GetLocation(c *gin.Context) {
	tripID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || tripID <= 0 {
		response.Fail(c, http.StatusBadRequest, "INVALID_TRIP_ID", "trip id must be a positive integer")
		return
	}
	if !h.authorizeTripRead(c, tripID) {
		return
	}

	state, err := h.svc.GetLocation(c.Request.Context(), tripID)
	if err != nil {
		response.Fail(c, http.StatusNotFound, "NOT_FOUND", "no location found for this trip")
		return
	}

	response.OK(c, state)
}

func (h *GPSHandler) authorizeTripWrite(c *gin.Context, tripID int64) bool {
	if middleware.AuthenticatedWithAPIKey(c) {
		return true
	}
	userID, ok := middleware.AuthenticatedUserID(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "authenticated user is required")
		return false
	}
	if h.authz == nil {
		response.Fail(c, http.StatusServiceUnavailable, "AUTHORIZATION_UNAVAILABLE", "trip authorization is not configured")
		return false
	}
	allowed, err := h.authz.UserOwnsTrip(c.Request.Context(), userID, tripID)
	if err != nil {
		slog.Error("trip ownership check failed", "err", err, "trip_id", tripID, "user_id", userID)
		response.Fail(c, http.StatusInternalServerError, "INTERNAL_ERROR", "internal error; please retry")
		return false
	}
	if !allowed {
		response.Fail(c, http.StatusForbidden, "FORBIDDEN", "trip access denied")
		return false
	}
	return true
}

func (h *GPSHandler) authorizeTripRead(c *gin.Context, tripID int64) bool {
	if middleware.AuthenticatedWithAPIKey(c) {
		return true
	}
	userID, ok := middleware.AuthenticatedUserID(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "authenticated user is required")
		return false
	}
	if h.authz == nil {
		response.Fail(c, http.StatusServiceUnavailable, "AUTHORIZATION_UNAVAILABLE", "trip authorization is not configured")
		return false
	}
	allowed, err := h.authz.UserCanAccessTrip(c.Request.Context(), userID, tripID)
	if err != nil {
		slog.Error("trip access check failed", "err", err, "trip_id", tripID, "user_id", userID)
		response.Fail(c, http.StatusInternalServerError, "INTERNAL_ERROR", "internal error; please retry")
		return false
	}
	if !allowed {
		response.Fail(c, http.StatusForbidden, "FORBIDDEN", "trip access denied")
		return false
	}
	return true
}
