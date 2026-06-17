// sim-drivers simulates live driver locations in Redis GEO.
//
// Default: 500 drivers inside Isfahan city. Updates Redis only when a driver
// moved at least -min-move-m meters since the last publish.
//
// Usage:
//
//	go run ./cmd/sim-drivers -redis localhost:6379 -count 500 -city isfahan -reset-geo
//	docker compose --profile sim up -d --build sim-drivers
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand/v2"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"geo-service/internal/cache"
	"geo-service/internal/utils"
)

const driverLocationTTL = 2 * time.Minute

type region struct {
	name               string
	centerLat, centerLng float64
	spreadKm           float64
	minLat, maxLat     float64
	minLng, maxLng     float64
}

var regions = map[string]region{
	"isfahan": {
		name: "isfahan", centerLat: 32.6539, centerLng: 51.6660, spreadKm: 12,
		minLat: 32.58, maxLat: 32.72, minLng: 51.58, maxLng: 51.75,
	},
}

type driver struct {
	id       int
	lat      float64
	lng      float64
	sentLat  float64
	sentLng  float64
	lastSent time.Time
	rng      *rand.Rand
}

type config struct {
	redisAddr    string
	redisPass    string
	redisDB      int
	geoKey       string
	region       region
	count        int
	idStart      int
	workers      int
	batchSize    int
	seedBatch    int
	minMoveM     float64
	tick         time.Duration
	activeRatio  float64
	moveMinM     float64
	moveMaxM     float64
	seedOnly     bool
	forceSeed    bool
	resetGeo     bool
	keepaliveSec int
	anchorLat    float64
	anchorLng    float64
}

func main() {
	cfg := parseFlags()
	log.Printf("sim-drivers: city=%s count=%d id_start=%d workers=%d tick=%s active=%.0f%%",
		cfg.region.name, cfg.count, cfg.idStart, cfg.workers, cfg.tick, cfg.activeRatio*100)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := redis.NewClient(&redis.Options{
		Addr:     cfg.redisAddr,
		Password: cfg.redisPass,
		DB:       cfg.redisDB,
	})
	defer client.Close()

	if err := client.Ping(ctx).Err(); err != nil {
		log.Fatalf("redis ping failed: %v", err)
	}

	if cfg.resetGeo {
		if err := resetDriverStore(ctx, client, cfg.geoKey); err != nil {
			log.Fatalf("reset geo: %v", err)
		}
	}

	drivers := buildDrivers(cfg)
	log.Printf("generated %d drivers in %s", len(drivers), cfg.region.name)

	skipSeed, err := shouldSkipSeed(ctx, client, cfg)
	if err != nil {
		log.Fatalf("seed check failed: %v", err)
	}

	switch {
	case skipSeed:
		log.Printf("resuming %d driver positions from Redis", len(drivers))
		if err := loadDriversFromRedis(ctx, client, cfg, drivers); err != nil {
			log.Fatalf("load existing drivers failed: %v", err)
		}
	case cfg.seedOnly:
		if err := seedDrivers(ctx, client, cfg, drivers); err != nil {
			log.Fatalf("seed failed: %v", err)
		}
		log.Printf("seed-only complete")
		return
	default:
		if err := seedDrivers(ctx, client, cfg, drivers); err != nil {
			log.Fatalf("seed failed: %v", err)
		}
	}

	log.Printf("live update loop started")
	runSimulation(ctx, client, cfg, drivers)
}

func parseFlags() config {
	redisAddr := flag.String("redis", envOr("REDIS_ADDR", "localhost:6379"), "Redis address")
	redisPass := flag.String("redis-pass", envOr("REDIS_PASSWORD", ""), "Redis password")
	redisDB := flag.Int("redis-db", envIntOr("REDIS_DB", 0), "Redis DB index")
	geoKey := flag.String("geo-key", envOr("DRIVER_GEO_KEY", "drivers:geo"), "Redis GEO key")
	city := flag.String("city", envOr("SIM_DRIVER_CITY", "isfahan"), "City region (isfahan)")
	count := flag.Int("count", envIntOr("SIM_DRIVER_COUNT", 500), "Number of simulated drivers")
	idStart := flag.Int("id-start", envIntOr("SIM_DRIVER_ID_START", 1), "First numeric driver id")
	workers := flag.Int("workers", envIntOr("SIM_DRIVER_WORKERS", 4), "Simulation worker goroutines")
	batchSize := flag.Int("batch-size", 100, "Redis pipeline batch size for updates")
	seedBatch := flag.Int("seed-batch", 200, "Redis pipeline batch size for initial seed")
	minMoveM := flag.Float64("min-move-m", 10, "Min movement in meters before Redis publish")
	tick := flag.Duration("tick", 5*time.Second, "Simulation tick interval")
	activeRatio := flag.Float64("active-ratio", 0.1, "Fraction of drivers that move each tick")
	moveMinM := flag.Float64("move-min-m", 3, "Min random movement per active tick (meters)")
	moveMaxM := flag.Float64("move-max-m", 10, "Max random movement per active tick (meters)")
	seedOnly := flag.Bool("seed-only", false, "Only seed, do not simulate")
	forceSeed := flag.Bool("force-seed", false, "Always seed even when Redis already has drivers")
	resetGeo := flag.Bool("reset-geo", false, "Delete geo key and driver location hashes before seeding")
	keepaliveSec := flag.Int("keepalive-sec", 90, "Re-publish idle drivers before TTL (0=off)")
	anchorLat := flag.Float64("anchor-lat", 0, "Optional fixed lat for the first simulated driver (0=random)")
	anchorLng := flag.Float64("anchor-lng", 0, "Optional fixed lng for the first simulated driver (0=random)")
	flag.Parse()

	reg, ok := regions[strings.ToLower(strings.TrimSpace(*city))]
	if !ok {
		log.Fatalf("unknown city %q (available: isfahan)", *city)
	}
	if *count <= 0 {
		log.Fatal("count must be positive")
	}
	if *idStart <= 0 {
		log.Fatal("id-start must be positive")
	}
	if *workers <= 0 {
		log.Fatal("workers must be positive")
	}
	if *activeRatio < 0 || *activeRatio > 1 {
		log.Fatal("active-ratio must be between 0 and 1")
	}

	return config{
		redisAddr:    *redisAddr,
		redisPass:    *redisPass,
		redisDB:      *redisDB,
		geoKey:       *geoKey,
		region:       reg,
		count:        *count,
		idStart:      *idStart,
		workers:      *workers,
		batchSize:    *batchSize,
		seedBatch:    *seedBatch,
		minMoveM:     *minMoveM,
		tick:         *tick,
		activeRatio:  *activeRatio,
		moveMinM:     *moveMinM,
		moveMaxM:     *moveMaxM,
		seedOnly:     *seedOnly,
		forceSeed:    *forceSeed,
		resetGeo:     *resetGeo,
		keepaliveSec: *keepaliveSec,
		anchorLat:    *anchorLat,
		anchorLng:    *anchorLng,
	}
}

func buildDrivers(cfg config) []driver {
	drivers := make([]driver, cfg.count)
	for i := 0; i < cfg.count; i++ {
		rng := rand.New(rand.NewPCG(uint64(i+1), uint64(i+1)*131))
		var lat, lng float64
		if i == 0 && cfg.anchorLat != 0 && cfg.anchorLng != 0 {
			lat, lng = cfg.anchorLat, cfg.anchorLng
		} else {
			lat, lng = randomCoordInRegion(rng, cfg.region)
		}
		drivers[i] = driver{
			id: i + 1, lat: lat, lng: lng,
			sentLat: lat, sentLng: lng,
			lastSent: time.Now(), rng: rng,
		}
	}
	return drivers
}

func randomCoordInRegion(rng *rand.Rand, reg region) (float64, float64) {
	angle := rng.Float64() * 2 * math.Pi
	radiusKm := rng.Float64() * reg.spreadKm
	if rng.Float64() < 0.7 {
		radiusKm *= rng.Float64()
	}
	lat, lng := offsetMeters(reg.centerLat, reg.centerLng, angle, radiusKm*1000)
	return clamp(lat, reg.minLat, reg.maxLat), clamp(lng, reg.minLng, reg.maxLng)
}

func resetDriverStore(ctx context.Context, client *redis.Client, geoKey string) error {
	log.Printf("resetting driver store (geo key + location hashes)")
	if err := client.Del(ctx, geoKey).Err(); err != nil {
		return err
	}
	var cursor uint64
	removed := 0
	for {
		keys, next, err := client.Scan(ctx, cursor, "driver:*:location", 200).Result()
		if err != nil {
			return err
		}
		if len(keys) > 0 {
			if err := client.Del(ctx, keys...).Err(); err != nil {
				return err
			}
			removed += len(keys)
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	log.Printf("removed %d stale driver location hashes", removed)
	return nil
}

func seedDrivers(ctx context.Context, client *redis.Client, cfg config, drivers []driver) error {
	start := time.Now()
	now := time.Now().UnixMilli()

	for offset := 0; offset < len(drivers); offset += cfg.seedBatch {
		end := min(offset+cfg.seedBatch, len(drivers))
		if err := writeDriverBatch(ctx, client, cfg, drivers[offset:end], now); err != nil {
			return fmt.Errorf("seed batch %d-%d: %w", offset, end, err)
		}
		if end == len(drivers) || end%cfg.seedBatch == 0 {
			log.Printf("seeded %d/%d drivers", end, len(drivers))
		}
	}

	card, _ := client.ZCard(ctx, cfg.geoKey).Result()
	log.Printf("redis geo %q size=%d seed_time=%s", cfg.geoKey, card, time.Since(start).Round(time.Millisecond))
	return nil
}

func writeDriverBatch(ctx context.Context, client *redis.Client, cfg config, batch []driver, ts int64) error {
	pipe := client.Pipeline()
	for i := range batch {
		d := &batch[i]
		id := driverRedisID(cfg, d.id)
		key := cache.DriverLocationKey(id)
		pipe.GeoAdd(ctx, cfg.geoKey, &redis.GeoLocation{Name: id, Longitude: d.lng, Latitude: d.lat})
		pipe.HSet(ctx, key, map[string]any{
			"id": id, "driver_id": id, "lat": d.lat, "lng": d.lng, "timestamp_ms": ts,
		})
		pipe.Expire(ctx, key, driverLocationTTL)
	}
	_, err := pipe.Exec(ctx)
	return err
}

func shouldSkipSeed(ctx context.Context, client *redis.Client, cfg config) (bool, error) {
	if cfg.forceSeed || cfg.resetGeo || cfg.seedOnly {
		return false, nil
	}
	card, err := client.ZCard(ctx, cfg.geoKey).Result()
	if err != nil {
		return false, err
	}
	if card > int64(cfg.count*2) {
		log.Printf("warning: geo key has %d members but target is %d — run with -reset-geo to free Redis memory", card, cfg.count)
	}
	threshold := int64(cfg.count * 9 / 10)
	if card >= threshold && card <= int64(cfg.count*2) {
		log.Printf("geo key has %d drivers, skipping seed", card)
		return true, nil
	}
	return false, nil
}

func loadDriversFromRedis(ctx context.Context, client *redis.Client, cfg config, drivers []driver) error {
	const batch = 100
	loaded := 0
	for offset := 0; offset < len(drivers); offset += batch {
		end := min(offset+batch, len(drivers))
		pipe := client.Pipeline()
		cmds := make([]*redis.MapStringStringCmd, end-offset)
		for i := offset; i < end; i++ {
			id := driverRedisID(cfg, drivers[i].id)
			cmds[i-offset] = pipe.HGetAll(ctx, cache.DriverLocationKey(id))
		}
		if _, err := pipe.Exec(ctx); err != nil {
			return err
		}
		for i := offset; i < end; i++ {
			values, _ := cmds[i-offset].Result()
			if len(values) == 0 {
				continue
			}
			lat, err1 := strconv.ParseFloat(values["lat"], 64)
			lng, err2 := strconv.ParseFloat(values["lng"], 64)
			if err1 != nil || err2 != nil {
				continue
			}
			d := &drivers[i]
			d.lat, d.lng, d.sentLat, d.sentLng = lat, lng, lat, lng
			if ts, _ := strconv.ParseInt(values["timestamp_ms"], 10, 64); ts > 0 {
				d.lastSent = time.UnixMilli(ts)
			} else {
				d.lastSent = time.Now()
			}
			loaded++
		}
	}
	log.Printf("loaded %d/%d drivers from Redis", loaded, len(drivers))
	return nil
}

func runSimulation(ctx context.Context, client *redis.Client, cfg config, drivers []driver) {
	var tickCount, updates atomic.Int64
	ticker := time.NewTicker(cfg.tick)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Printf("stopped: ticks=%d updates=%d", tickCount.Load(), updates.Load())
			return
		case <-ticker.C:
			tick := tickCount.Add(1)
			var wg sync.WaitGroup
			var tickUpdates atomic.Int64
			chunk := (len(drivers) + cfg.workers - 1) / cfg.workers
			for w := 0; w < cfg.workers; w++ {
				start, end := w*chunk, min((w+1)*chunk, len(drivers))
				if start >= end {
					continue
				}
				wg.Add(1)
				go func(s, e int) {
					defer wg.Done()
					tickUpdates.Add(int64(simulateChunk(ctx, client, cfg, drivers[s:e])))
				}(start, end)
			}
			wg.Wait()
			n := tickUpdates.Load()
			updates.Add(n)
			if tick == 1 || tick%12 == 0 {
				log.Printf("tick=%d updated=%d total=%d", tick, n, updates.Load())
			}
		}
	}
}

func simulateChunk(ctx context.Context, client *redis.Client, cfg config, slice []driver) int {
	now := time.Now()
	nowMs := now.UnixMilli()
	minMoveKm := cfg.minMoveM / 1000
	updated := 0

	for offset := 0; offset < len(slice); offset += cfg.batchSize {
		end := min(offset+cfg.batchSize, len(slice))
		pending := make([]driver, 0, end-offset)

		for i := offset; i < end; i++ {
			d := &slice[i]
			if d.rng.Float64() < cfg.activeRatio {
				distM := cfg.moveMinM + d.rng.Float64()*(cfg.moveMaxM-cfg.moveMinM)
				bearing := d.rng.Float64() * 2 * math.Pi
				d.lat, d.lng = offsetMeters(d.lat, d.lng, bearing, distM)
				d.lat = clamp(d.lat, cfg.region.minLat, cfg.region.maxLat)
				d.lng = clamp(d.lng, cfg.region.minLng, cfg.region.maxLng)
			}
			movedKm := utils.Haversine(d.sentLat, d.sentLng, d.lat, d.lng)
			keepalive := cfg.keepaliveSec > 0 && now.Sub(d.lastSent) >= time.Duration(cfg.keepaliveSec)*time.Second
			if movedKm >= minMoveKm || keepalive {
				d.sentLat, d.sentLng, d.lastSent = d.lat, d.lng, now
				pending = append(pending, *d)
			}
		}
		if len(pending) > 0 {
			if err := writeDriverBatch(ctx, client, cfg, pending, nowMs); err != nil {
				log.Printf("redis update failed: %v", err)
			} else {
				updated += len(pending)
			}
		}
	}
	return updated
}

func driverRedisID(cfg config, slot int) string {
	if slot <= 0 {
		slot = 1
	}
	return strconv.Itoa(cfg.idStart + slot - 1)
}

func offsetMeters(lat, lng, bearingRad, distanceM float64) (float64, float64) {
	if distanceM == 0 {
		return lat, lng
	}
	δ := distanceM / 6371000
	φ1 := lat * math.Pi / 180
	λ1 := lng * math.Pi / 180
	φ2 := math.Asin(math.Sin(φ1)*math.Cos(δ) + math.Cos(φ1)*math.Sin(δ)*math.Cos(bearingRad))
	λ2 := λ1 + math.Atan2(math.Sin(bearingRad)*math.Sin(δ)*math.Cos(φ1), math.Cos(δ)-math.Sin(φ1)*math.Sin(φ2))
	return φ2 * 180 / math.Pi, λ2 * 180 / math.Pi
}

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envIntOr(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
