package route

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"geo-service/internal/middleware"
	"geo-service/internal/response"
)

type TripWriteAuthorizer interface {
	UserOwnsTrip(ctx context.Context, userID, tripID int64) (bool, error)
}

func authorizeTripWrite(c *gin.Context, authz TripWriteAuthorizer, tripID int64) bool {
	if tripID <= 0 || middleware.AuthenticatedWithAPIKey(c) {
		return true
	}
	userID, ok := middleware.AuthenticatedUserID(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "authenticated user is required")
		return false
	}
	if authz == nil {
		response.Fail(c, http.StatusServiceUnavailable, "AUTHORIZATION_UNAVAILABLE", "trip authorization is not configured")
		return false
	}
	allowed, err := authz.UserOwnsTrip(c.Request.Context(), userID, tripID)
	if err != nil {
		slog.Error("route trip ownership check failed", "err", err, "trip_id", tripID, "user_id", userID)
		response.Fail(c, http.StatusInternalServerError, "INTERNAL_ERROR", "internal error; please retry")
		return false
	}
	if !allowed {
		response.Fail(c, http.StatusForbidden, "FORBIDDEN", "trip access denied")
		return false
	}
	return true
}
