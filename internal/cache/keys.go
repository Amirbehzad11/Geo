package cache

import "fmt"

const routeCacheVersion = "v4-pedestrian-routing"

// Key builders — single source of truth for all Redis key patterns.

func TripLocationKey(tripID int64) string   { return fmt.Sprintf("trip:%d:location", tripID) }
func TripLastUpdateKey(tripID int64) string { return fmt.Sprintf("trip:%d:last_update", tripID) }
func TripRouteKey(tripID int64) string      { return fmt.Sprintf("trip:%d:route", tripID) }

func RouteKey(mode string, alternatives int, sLat, sLng, eLat, eLng float64) string {
	if mode == "" {
		mode = "car"
	}
	if alternatives <= 0 {
		alternatives = 1
	}
	return fmt.Sprintf("route:%s:%s:%d:%.6f:%.6f:%.6f:%.6f", routeCacheVersion, mode, alternatives, sLat, sLng, eLat, eLng)
}

func RateLimitKey(tripID int64) string { return fmt.Sprintf("rl:trip:%d", tripID) }
