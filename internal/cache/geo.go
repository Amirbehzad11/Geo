package cache

import (
	"context"
	"errors"
	"strings"

	"github.com/redis/go-redis/v9"
)

// NearbyGeoMember is a Redis GEO member with its resolved coordinate and
// distance from the lookup center.
type NearbyGeoMember struct {
	ID         string
	Lat        float64
	Lng        float64
	DistanceKm float64
}

// FindNearbyGeoMembers searches a Redis GEO index around lat/lng.
func (r *Redis) FindNearbyGeoMembers(ctx context.Context, key string, lat, lng, radiusKm float64, limit int) ([]NearbyGeoMember, error) {
	if r == nil || r.client == nil {
		return nil, errors.New("redis is not configured")
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, errors.New("redis geo key is required")
	}

	locations, err := r.client.GeoSearchLocation(ctx, key, &redis.GeoSearchLocationQuery{
		GeoSearchQuery: redis.GeoSearchQuery{
			Longitude:  lng,
			Latitude:   lat,
			Radius:     radiusKm,
			RadiusUnit: "km",
			Sort:       "ASC",
			Count:      limit,
		},
		WithCoord: true,
		WithDist:  true,
	}).Result()
	if err != nil {
		return nil, err
	}

	out := make([]NearbyGeoMember, 0, len(locations))
	for _, loc := range locations {
		out = append(out, NearbyGeoMember{
			ID:         loc.Name,
			Lat:        loc.Latitude,
			Lng:        loc.Longitude,
			DistanceKm: loc.Dist,
		})
	}
	return out, nil
}
