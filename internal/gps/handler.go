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
	svc         *GPSService
	authz       TripAuthorizer
	presence    PresenceUpdater
	requireAuth bool
}

// NewGPSHandler creates a GPSHandler backed by the given service.
func NewGPSHandler(svc *GPSService, requireAuth bool, authorizers ...TripAuthorizer) *GPSHandler {
	h := &GPSHandler{svc: svc, requireAuth: requireAuth}
	if len(authorizers) > 0 {
		h.authz = authorizers[0]
	}
	return h
}

// WithPresence enables user map-presence updates when trip_id is omitted.
func (h *GPSHandler) WithPresence(updater PresenceUpdater) *GPSHandler {
	h.presence = updater
	return h
}

type TripAuthorizer interface {
	UserOwnsTrip(ctx context.Context, userID, tripID int64) (bool, error)
	UserCanAccessTrip(ctx context.Context, userID, tripID int64) (bool, error)
}

// PresenceUpdater stores the authenticated user's live position for nearby/map visibility.
type PresenceUpdater interface {
	UpdatePresence(ctx context.Context, userID string, lat, lng float64, timestampMs int64) (any, error)
}

// Update handles POST /gps/update
//
//	@Summary		Process a GPS update
//	@Description	With trip_id: trip tracking (EMA, speed, deviation). Without trip_id: updates the Bearer user's live map presence (same as POST /driver-location).
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

	if !validateCoords(c, update.Lat, update.Lng) {
		return
	}

	// No trip_id → update the authenticated user's live map position.
	if update.TripID <= 0 {
		h.updatePresence(c, update)
		return
	}

	if update.Timestamp <= 0 {
		response.ValidationFail(c, "timestamp must be a positive Unix epoch (seconds)")
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

func (h *GPSHandler) updatePresence(c *gin.Context, update GPSUpdate) {
	if h.presence == nil {
		response.Fail(c, http.StatusServiceUnavailable, "PRESENCE_DISABLED", "user location updates are not configured")
		return
	}

	userID, ok := resolvePresenceUserID(c)
	if !ok {
		return
	}

	timestampMs := int64(0)
	if update.Timestamp > 0 {
		timestampMs = update.Timestamp * 1000
	}

	result, err := h.presence.UpdatePresence(c.Request.Context(), userID, update.Lat, update.Lng, timestampMs)
	if err != nil {
		slog.Error("user presence update failed", "err", err, "user_id", userID)
		response.Fail(c, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to update user location")
		return
	}

	response.OK(c, result)
}

func resolvePresenceUserID(c *gin.Context) (string, bool) {
	if middleware.AuthenticatedWithAPIKey(c) {
		response.ValidationFail(c, "trip_id is required for API-key clients; JWT Bearer clients omit trip_id to update user presence")
		return "", false
	}

	userID, ok := middleware.AuthenticatedUserID(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "authenticated user is required")
		return "", false
	}
	return strconv.FormatInt(userID, 10), true
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
	if !h.requireAuth {
		return true
	}
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
	if !h.requireAuth {
		return true
	}
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
