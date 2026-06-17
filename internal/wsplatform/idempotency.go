package wsplatform

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// IdempotencyGuard deduplicates retried client messages for a short window.
type IdempotencyGuard struct {
	rdb    *redis.Client
	prefix string
	ttl    time.Duration
}

func NewIdempotencyGuard(rdb *redis.Client, ttl time.Duration) *IdempotencyGuard {
	if ttl <= 0 {
		ttl = 2 * time.Minute
	}
	return &IdempotencyGuard{rdb: rdb, prefix: "ws:idem:", ttl: ttl}
}

// Seen reports whether messageID was already processed for connectionID.
func (g *IdempotencyGuard) Seen(ctx context.Context, connectionID, messageID string) (bool, error) {
	if g == nil || g.rdb == nil || messageID == "" {
		return false, nil
	}
	key := g.prefix + connectionID + ":" + messageID
	ok, err := g.rdb.SetNX(ctx, key, "1", g.ttl).Result()
	if err != nil {
		return false, err
	}
	return !ok, nil
}

// MarkProcessed is an alias for Seen when callers want explicit naming.
func (g *IdempotencyGuard) MarkProcessed(ctx context.Context, connectionID, messageID string) (duplicate bool, err error) {
	return g.Seen(ctx, connectionID, fmt.Sprintf("%s", messageID))
}
