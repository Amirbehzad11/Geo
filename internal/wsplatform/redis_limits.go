package wsplatform

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// ConnLimitConfig holds distributed WebSocket connection caps.
type ConnLimitConfig struct {
	MaxGlobal int
	MaxPerIP  int
	MaxPerUser int
	KeyPrefix string
	SlotTTL   time.Duration
}

func DefaultConnLimitConfig() ConnLimitConfig {
	return ConnLimitConfig{
		MaxGlobal:  2000,
		MaxPerIP:   10,
		MaxPerUser: 5,
		KeyPrefix:  "ws:conn:",
		SlotTTL:    120 * time.Second,
	}
}

// RedisConnLimiter enforces connection limits across horizontally scaled nodes.
// Active slots are tracked in Redis sorted sets scored by last heartbeat unix time.
type RedisConnLimiter struct {
	rdb *redis.Client
	cfg ConnLimitConfig
}

func NewRedisConnLimiter(rdb *redis.Client, cfg ConnLimitConfig) *RedisConnLimiter {
	if cfg.KeyPrefix == "" {
		cfg.KeyPrefix = "ws:conn:"
	}
	if cfg.SlotTTL <= 0 {
		cfg.SlotTTL = 120 * time.Second
	}
	return &RedisConnLimiter{rdb: rdb, cfg: cfg}
}

type ConnSlot struct {
	ConnectionID string
	ClientIP     string
	UserID       int64
}

// Acquire registers a connection slot after pruning expired entries.
func (l *RedisConnLimiter) Acquire(ctx context.Context, slot ConnSlot) error {
	if l.rdb == nil {
		return nil
	}
	now := float64(time.Now().Unix())
	cutoff := now - l.cfg.SlotTTL.Seconds()

	if err := l.pruneAndCheck(ctx, l.cfg.KeyPrefix+"global", cutoff, l.cfg.MaxGlobal); err != nil {
		return err
	}
	if err := l.pruneAndCheck(ctx, l.cfg.KeyPrefix+"ip:"+slot.ClientIP, cutoff, l.cfg.MaxPerIP); err != nil {
		return err
	}
	if slot.UserID > 0 && l.cfg.MaxPerUser > 0 {
		userKey := fmt.Sprintf("%suser:%d", l.cfg.KeyPrefix, slot.UserID)
		if err := l.pruneAndCheck(ctx, userKey, cutoff, l.cfg.MaxPerUser); err != nil {
			return err
		}
		if err := l.rdb.ZAdd(ctx, userKey, redis.Z{Score: now, Member: slot.ConnectionID}).Err(); err != nil {
			return err
		}
	}

	pipe := l.rdb.Pipeline()
	pipe.ZAdd(ctx, l.cfg.KeyPrefix+"global", redis.Z{Score: now, Member: slot.ConnectionID})
	pipe.ZAdd(ctx, l.cfg.KeyPrefix+"ip:"+slot.ClientIP, redis.Z{Score: now, Member: slot.ConnectionID})
	_, err := pipe.Exec(ctx)
	return err
}

// Release removes a connection from all limit sets.
func (l *RedisConnLimiter) Release(ctx context.Context, slot ConnSlot) {
	if l.rdb == nil {
		return
	}
	pipe := l.rdb.Pipeline()
	pipe.ZRem(ctx, l.cfg.KeyPrefix+"global", slot.ConnectionID)
	pipe.ZRem(ctx, l.cfg.KeyPrefix+"ip:"+slot.ClientIP, slot.ConnectionID)
	if slot.UserID > 0 {
		pipe.ZRem(ctx, fmt.Sprintf("%suser:%d", l.cfg.KeyPrefix, slot.UserID), slot.ConnectionID)
	}
	_, _ = pipe.Exec(ctx)
}

// Heartbeat refreshes slot scores to prevent zombie accumulation.
func (l *RedisConnLimiter) Heartbeat(ctx context.Context, slot ConnSlot) {
	if l.rdb == nil {
		return
	}
	now := float64(time.Now().Unix())
	pipe := l.rdb.Pipeline()
	pipe.ZAdd(ctx, l.cfg.KeyPrefix+"global", redis.Z{Score: now, Member: slot.ConnectionID})
	pipe.ZAdd(ctx, l.cfg.KeyPrefix+"ip:"+slot.ClientIP, redis.Z{Score: now, Member: slot.ConnectionID})
	if slot.UserID > 0 {
		pipe.ZAdd(ctx, fmt.Sprintf("%suser:%d", l.cfg.KeyPrefix, slot.UserID), redis.Z{Score: now, Member: slot.ConnectionID})
	}
	_, _ = pipe.Exec(ctx)
}

func (l *RedisConnLimiter) pruneAndCheck(ctx context.Context, key string, cutoff float64, max int) error {
	if max <= 0 {
		return nil
	}
	pipe := l.rdb.Pipeline()
	pipe.ZRemRangeByScore(ctx, key, "-inf", fmt.Sprintf("%f", cutoff))
	countCmd := pipe.ZCard(ctx, key)
	if _, err := pipe.Exec(ctx); err != nil {
		return err
	}
	if countCmd.Val() >= int64(max) {
		RateLimited(ChannelShipmentNearby, "connection")
		return fmt.Errorf("connection limit exceeded")
	}
	return nil
}
