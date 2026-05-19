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
			continue
		}
		state.DistanceKm = member.DistanceKm
		out = append(out, state)
	}
	return out, nil
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
