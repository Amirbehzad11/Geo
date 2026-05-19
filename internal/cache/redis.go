package cache

import (
	"context"
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	locationTTL  = 24 * time.Hour
	routeTTL     = 1 * time.Hour
	rateLimitTTL = 10 * time.Second
)

// Redis wraps go-redis with domain-specific helpers.
type Redis struct {
	client *redis.Client
}

func NewRedis(addr, password string, db int) *Redis {
	return &Redis{
		client: redis.NewClient(&redis.Options{
			Addr:     addr,
			Password: password,
			DB:       db,
		}),
	}
}

func (r *Redis) Ping(ctx context.Context) error {
	return r.client.Ping(ctx).Err()
}

// ---- pub/sub ----

// Publish publishes raw bytes to a Redis channel.
func (r *Redis) Publish(ctx context.Context, channel string, data []byte) error {
	return r.client.Publish(ctx, channel, data).Err()
}

// Subscribe returns a PubSub handle for one or more exact channels.
func (r *Redis) Subscribe(ctx context.Context, channels ...string) *redis.PubSub {
	return r.client.Subscribe(ctx, channels...)
}

// PSubscribe returns a PubSub handle for one or more glob patterns.
func (r *Redis) PSubscribe(ctx context.Context, patterns ...string) *redis.PubSub {
	return r.client.PSubscribe(ctx, patterns...)
}

// ---- route cache ----

func (r *Redis) SetRoute(ctx context.Context, key string, value any) error {
	return r.setJSON(ctx, key, value, routeTTL)
}

func (r *Redis) GetRoute(ctx context.Context, key string, dest any) error {
	return r.getJSON(ctx, key, dest)
}

// ---- trip route polyline (for deviation detection) ----

func (r *Redis) SetTripRoute(ctx context.Context, tripID int64, value any) error {
	return r.setJSON(ctx, TripRouteKey(tripID), value, 24*time.Hour)
}

func (r *Redis) GetTripRoute(ctx context.Context, tripID int64, dest any) error {
	return r.getJSON(ctx, TripRouteKey(tripID), dest)
}

// ---- trip location state ----

func (r *Redis) SetLocation(ctx context.Context, tripID int64, state any) error {
	return r.setJSON(ctx, TripLocationKey(tripID), state, locationTTL)
}

func (r *Redis) GetLocation(ctx context.Context, tripID int64, dest any) error {
	return r.getJSON(ctx, TripLocationKey(tripID), dest)
}

// ---- rate limiting (fixed-window via SET NX) ----

// AcquireRateLimit tries to claim the rate-limit slot for a trip.
// Returns true if the update is allowed, false if rate-limited.
// windowMs is the minimum gap in milliseconds between allowed updates.
func (r *Redis) AcquireRateLimit(ctx context.Context, tripID int64, windowMs int64) (bool, error) {
	ttl := time.Duration(windowMs) * time.Millisecond
	ok, err := r.client.SetNX(ctx, RateLimitKey(tripID), 1, ttl).Result()
	return ok, err
}

// ---- last update timestamp ----

func (r *Redis) SetLastUpdate(ctx context.Context, tripID, ts int64) error {
	return r.client.Set(ctx, TripLastUpdateKey(tripID), ts, locationTTL).Err()
}

// GetLastUpdate returns (0, redis.Nil) when the key does not exist yet.
func (r *Redis) GetLastUpdate(ctx context.Context, tripID int64) (int64, error) {
	return r.client.Get(ctx, TripLastUpdateKey(tripID)).Int64()
}

// ---- internal helpers ----

func (r *Redis) setJSON(ctx context.Context, key string, value any, ttl time.Duration) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return r.client.Set(ctx, key, data, ttl).Err()
}

func (r *Redis) getJSON(ctx context.Context, key string, dest any) error {
	data, err := r.client.Get(ctx, key).Bytes()
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dest)
}
