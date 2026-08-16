package cache

import (
	"context"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const driverLocationTTL = 2 * time.Minute

type DriverLocationState struct {
	ID          string
	Lat         float64
	Lng         float64
	TimestampMs int64
	DistanceKm  float64
}

func (r *Redis) SetDriverLocation(ctx context.Context, geoKey, streamKey string, state DriverLocationState) error {
	key := DriverLocationKey(state.ID)
	pipe := r.client.TxPipeline()
	pipe.GeoAdd(ctx, geoKey, &redis.GeoLocation{
		Name:      state.ID,
		Longitude: state.Lng,
		Latitude:  state.Lat,
	})
	pipe.HSet(ctx, key, map[string]any{
		"id":           state.ID,
		"driver_id":    state.ID,
		"lat":          state.Lat,
		"lng":          state.Lng,
		"timestamp_ms": state.TimestampMs,
	})
	pipe.Expire(ctx, key, driverLocationTTL)
	if streamKey != "" {
		pipe.XAdd(ctx, &redis.XAddArgs{
			Stream: streamKey,
			MaxLen: 10000,
			Approx: true,
			Values: map[string]any{
				"driver_id":    state.ID,
				"lat":          state.Lat,
				"lng":          state.Lng,
				"timestamp_ms": state.TimestampMs,
			},
		})
	}
	_, err := pipe.Exec(ctx)
	return err
}

func (r *Redis) FindNearbyDrivers(ctx context.Context, geoKey string, lat, lng, radiusKm float64, limit int) ([]DriverLocationState, error) {
	members, err := r.FindNearbyGeoMembers(ctx, geoKey, lat, lng, radiusKm, limit)
	if err != nil {
		return nil, err
	}

	out := make([]DriverLocationState, 0, len(members))
	for _, member := range members {
		state, ok := r.getDriverLocationState(ctx, member.ID)
		if !ok {
			// The location hash already expired (driver stopped broadcasting)
			// but the GEO member outlives it since GEOADD carries no TTL.
			// Self-heal by dropping it here so the geo set doesn't grow
			// unbounded with stale entries.
			r.client.ZRem(ctx, geoKey, member.ID)
			continue
		}
		state.DistanceKm = member.DistanceKm
		out = append(out, state)
	}
	return out, nil
}

// RemoveDriverLocation immediately removes a driver from the geo index and
// drops its location hash, instead of waiting for driverLocationTTL to
// expire. Used when a passenger explicitly leaves the map (navigates away,
// hides the tab, or closes it) so senders stop seeing them right away.
func (r *Redis) RemoveDriverLocation(ctx context.Context, geoKey, driverID string) error {
	pipe := r.client.TxPipeline()
	pipe.ZRem(ctx, geoKey, driverID)
	pipe.Del(ctx, DriverLocationKey(driverID))
	_, err := pipe.Exec(ctx)
	return err
}

// GetDriverLocation returns the live position a driver/user last reported, as
// long as it is still cached (see driverLocationTTL).
func (r *Redis) GetDriverLocation(ctx context.Context, driverID string) (DriverLocationState, bool) {
	return r.getDriverLocationState(ctx, driverID)
}

func (r *Redis) getDriverLocationState(ctx context.Context, driverID string) (DriverLocationState, bool) {
	values, err := r.client.HGetAll(ctx, DriverLocationKey(driverID)).Result()
	if err != nil || len(values) == 0 {
		return DriverLocationState{}, false
	}

	lat, err := strconv.ParseFloat(values["lat"], 64)
	if err != nil {
		return DriverLocationState{}, false
	}
	lng, err := strconv.ParseFloat(values["lng"], 64)
	if err != nil {
		return DriverLocationState{}, false
	}
	timestampMs, _ := strconv.ParseInt(values["timestamp_ms"], 10, 64)

	return DriverLocationState{
		ID:          driverID,
		Lat:         lat,
		Lng:         lng,
		TimestampMs: timestampMs,
	}, true
}
