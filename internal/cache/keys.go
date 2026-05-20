package cache

import "fmt"

// routeCacheVersion is embedded in every route cache key.
// Increment this string whenever the cached RouteResponse structure changes
// or when a new routing backend is introduced, so old entries are naturally
// invalidated without a manual FLUSHALL.
const routeCacheVersion = "v12-osrm-ready"

// Key builders — single source of truth for all Redis key patterns.

func TripLocationKey(tripID int64) string   { return fmt.Sprintf("trip:%d:location", tripID) }
func TripLastUpdateKey(tripID int64) string { return fmt.Sprintf("trip:%d:last_update", tripID) }
func TripRouteKey(tripID int64) string      { return fmt.Sprintf("trip:%d:route", tripID) }
func DriverLocationKey(driverID string) string {
	return fmt.Sprintf("driver:%s:location", driverID)
}

// RouteKey builds a Redis key for a computed route.
//
// Components:
//
//	route : <version> : <backend> : <mode> : <alternatives> : <sLat> : <sLng> : <eLat> : <eLng>
//
// Coordinates are encoded at 6 decimal places (≈11 cm precision at the equator),
// which is sufficient for routing — two requests within 11 cm of each other will
// share the same cached route.
//
// The backend name ("internal" or "osrm") is included so that switching
// backends does not serve stale results from the previous backend.
func RouteKey(backend, mode string, alternatives int, sLat, sLng, eLat, eLng float64) string {
	if backend == "" {
		backend = "internal"
	}
	if mode == "" {
		mode = "car"
	}
	if alternatives <= 0 {
		alternatives = 1
	}
	return fmt.Sprintf("route:%s:%s:%s:%d:%.6f:%.6f:%.6f:%.6f",
		routeCacheVersion, backend, mode, alternatives, sLat, sLng, eLat, eLng)
}

func RateLimitKey(tripID int64) string { return fmt.Sprintf("rl:trip:%d", tripID) }
