package wsplatform

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestLocationGuardRejectsFrequentUpdates(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	guard := NewLocationGuard(rdb, LocationGuardConfig{
		MinUpdateInterval: 500 * time.Millisecond,
	})

	ctx := context.Background()
	if err := guard.Validate(ctx, "u:1", ChannelShipmentNearby, 32.0, 51.0); err != nil {
		t.Fatalf("first update: %v", err)
	}
	err = guard.Validate(ctx, "u:1", ChannelShipmentNearby, 32.001, 51.001)
	if err == nil {
		t.Fatal("expected frequent update rejection")
	}
}

func TestRedisConnLimiterAcquireRelease(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	lim := NewRedisConnLimiter(rdb, ConnLimitConfig{MaxGlobal: 2, MaxPerIP: 2, SlotTTL: time.Minute})

	ctx := context.Background()
	slot1 := ConnSlot{ConnectionID: "c1", ClientIP: "1.2.3.4"}
	slot2 := ConnSlot{ConnectionID: "c2", ClientIP: "1.2.3.4"}
	slot3 := ConnSlot{ConnectionID: "c3", ClientIP: "1.2.3.4"}

	if err := lim.Acquire(ctx, slot1); err != nil {
		t.Fatal(err)
	}
	if err := lim.Acquire(ctx, slot2); err != nil {
		t.Fatal(err)
	}
	if err := lim.Acquire(ctx, slot3); err == nil {
		t.Fatal("expected global limit rejection")
	}
	lim.Release(ctx, slot1)
	if err := lim.Acquire(ctx, slot3); err != nil {
		t.Fatalf("expected slot after release: %v", err)
	}
}

func TestLogEventIncludesTraceID(t *testing.T) {
	// smoke: should not panic
	LogEvent(context.Background(), slog.LevelInfo, ChannelShipmentNearby, EventConnect, "cid", "127.0.0.1", 1)
}
