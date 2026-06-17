package wsplatform

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"geo-service/internal/utils"
)

// LocationGuardConfig controls anti-spoofing checks for WS location updates.
type LocationGuardConfig struct {
	MinUpdateInterval time.Duration
	KeyPrefix         string
	StateTTL          time.Duration
}

func DefaultLocationGuardConfig() LocationGuardConfig {
	return LocationGuardConfig{
		MinUpdateInterval: 1 * time.Second,
		KeyPrefix:         "ws:loc:",
		StateTTL:          10 * time.Minute,
	}
}

type locationState struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
	TS  int64   `json:"ts_ms"`
}

// LocationGuard rejects invalid coordinates and overly frequent location spam.
type LocationGuard struct {
	rdb *redis.Client
	cfg LocationGuardConfig
}

func NewLocationGuard(rdb *redis.Client, cfg LocationGuardConfig) *LocationGuard {
	if cfg.KeyPrefix == "" {
		cfg.KeyPrefix = "ws:loc:"
	}
	if cfg.MinUpdateInterval <= 0 {
		cfg.MinUpdateInterval = time.Second
	}
	if cfg.StateTTL <= 0 {
		cfg.StateTTL = 10 * time.Minute
	}
	return &LocationGuard{ rdb: rdb, cfg: cfg }
}

// Validate checks coordinates and minimum update interval for subject (user or connection).
func (g *LocationGuard) Validate(ctx context.Context, subject, channel string, lat, lng float64) error {
	if !utils.ValidCoords(lat, lng) {
		LocationRejected(channel, "invalid_coords")
		return fmt.Errorf("coordinates out of valid range")
	}
	if g.rdb == nil {
		return nil
	}

	key := g.cfg.KeyPrefix + subject
	now := time.Now()

	raw, err := g.rdb.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return g.store(ctx, key, lat, lng, now)
	}
	if err != nil {
		return nil // fail-open on Redis errors to avoid total outage
	}

	var prev locationState
	if err := json.Unmarshal(raw, &prev); err != nil {
		return g.store(ctx, key, lat, lng, now)
	}

	elapsed := now.Sub(time.UnixMilli(prev.TS))
	if elapsed < g.cfg.MinUpdateInterval {
		LocationRejected(channel, "update_too_frequent")
		return fmt.Errorf("location updates are too frequent")
	}

	return g.store(ctx, key, lat, lng, now)
}

func (g *LocationGuard) store(ctx context.Context, key string, lat, lng float64, now time.Time) error {
	state, err := json.Marshal(locationState{Lat: lat, Lng: lng, TS: now.UnixMilli()})
	if err != nil {
		return err
	}
	return g.rdb.Set(ctx, key, state, g.cfg.StateTTL).Err()
}
