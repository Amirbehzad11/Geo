package cache

import (
	"fmt"
	"strconv"
)

// routeCacheVersion is embedded in every route cache key.
// Increment this string whenever the cached RouteResponse structure changes
// or when a new routing backend is introduced, so old entries are naturally
// invalidated without a manual FLUSHALL.
const routeCacheVersion = "v15-prod-routing-cache-precision"

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
	return RouteKeyWithPrecision(backend, mode, alternatives, 6, sLat, sLng, eLat, eLng)
}

// RouteKeyWithPrecision builds a Redis route key with configurable coordinate
// precision. Precision 6 preserves the legacy key behavior.
func RouteKeyWithPrecision(backend, mode string, alternatives, precision int, sLat, sLng, eLat, eLng float64) string {
	if backend == "" {
		backend = "internal"
	}
	if mode == "" {
		mode = "car"
	}
	if alternatives <= 0 {
		alternatives = 1
	}
	precision = NormalizeRoutePrecision(precision)
	coordFmt := "%." + strconv.Itoa(precision) + "f"
	return fmt.Sprintf("route:%s:%s:%s:%d:"+coordFmt+":"+coordFmt+":"+coordFmt+":"+coordFmt,
		routeCacheVersion, backend, mode, alternatives, sLat, sLng, eLat, eLng)
}

func NormalizeRoutePrecision(precision int) int {
	if precision < 0 {
		return 0
	}
	if precision > 6 {
		return 6
	}
	return precision
}

func RateLimitKey(tripID int64) string { return fmt.Sprintf("rl:trip:%d", tripID) }
