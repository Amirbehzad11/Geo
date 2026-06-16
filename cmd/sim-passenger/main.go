// sim-passenger posts GPS updates to geo-service for a single trip, moving
// along a list of waypoints (start → pickup → destination).
//
// Usage:
//
//	go run ./cmd/sim-passenger -base http://localhost:8080 -trip 4 -api-key SECRET
//	docker compose --profile sim up -d --build sim-passenger
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"geo-service/internal/utils"
)

type waypoint struct {
	name string
	lat  float64
	lng  float64
}

type config struct {
	baseURL   string
	tripID    int64
	apiKey    string
	jwt       string
	speedKmH  float64
	tick      time.Duration
	pause     time.Duration
	loop      bool
	waypoints []waypoint
}

func main() {
	cfg := parseFlags()
	log.Printf("sim-passenger: trip=%d base=%s speed=%.0f km/h tick=%s waypoints=%d",
		cfg.tripID, cfg.baseURL, cfg.speedKmH, cfg.tick, len(cfg.waypoints))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := &http.Client{Timeout: 10 * time.Second}

	for {
		for leg := 0; leg < len(cfg.waypoints)-1; leg++ {
			from := cfg.waypoints[leg]
			to := cfg.waypoints[leg+1]
			log.Printf("leg %d: %s → %s", leg+1, from.name, to.name)

			for {
				err := walkLeg(ctx, client, cfg, from.lat, from.lng, to.lat, to.lng)
				if err == nil {
					break
				}
				if ctx.Err() != nil {
					log.Println("stopped")
					return
				}
				log.Printf("leg %d error (retry in 5s): %v", leg+1, err)
				select {
				case <-ctx.Done():
					log.Println("stopped")
					return
				case <-time.After(5 * time.Second):
				}
			}

			if cfg.pause > 0 && leg < len(cfg.waypoints)-2 {
				log.Printf("pause %s at %s", cfg.pause, to.name)
				select {
				case <-ctx.Done():
					log.Println("stopped")
					return
				case <-time.After(cfg.pause):
				}
			}
		}

		if !cfg.loop {
			log.Printf("arrived at %s — done", cfg.waypoints[len(cfg.waypoints)-1].name)
			return
		}
		log.Println("looping route from start")
	}
}

func walkLeg(ctx context.Context, client *http.Client, cfg config, fromLat, fromLng, toLat, toLng float64) error {
	lat, lng := fromLat, fromLng
	legKm := utils.Haversine(fromLat, fromLng, toLat, toLng)
	if legKm < 0.0001 {
		return postUpdate(ctx, client, cfg, lat, lng)
	}

	stepKm := cfg.speedKmH * (cfg.tick.Seconds() / 3600.0)
	if stepKm <= 0 {
		return fmt.Errorf("invalid step distance")
	}
	bearing := bearingRad(fromLat, fromLng, toLat, toLng)
	steps := int(math.Ceil(legKm / stepKm))
	if steps < 1 {
		steps = 1
	}

	for i := 0; i < steps; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		frac := float64(i+1) / float64(steps)
		if i == steps-1 {
			lat, lng = toLat, toLng
		} else {
			travelKm := stepKm * float64(i+1)
			lat, lng = destinationPoint(fromLat, fromLng, bearing, travelKm)
		}

		if err := postUpdate(ctx, client, cfg, lat, lng); err != nil {
			return err
		}
		log.Printf("  %.6f, %.6f (%.0f%% of leg)", lat, lng, frac*100)

		if i < steps-1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(cfg.tick):
			}
		}
	}
	return nil
}

func postUpdate(ctx context.Context, client *http.Client, cfg config, lat, lng float64) error {
	body, err := json.Marshal(map[string]any{
		"trip_id":   cfg.tripID,
		"lat":       utils.Round(lat, 6),
		"lng":       utils.Round(lng, 6),
		"timestamp": time.Now().Unix(),
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(cfg.baseURL, "/")+"/gps/update", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.apiKey != "" {
		req.Header.Set("X-API-Key", cfg.apiKey)
	}
	if cfg.jwt != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.jwt)
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode == http.StatusTooManyRequests {
		log.Printf("rate limited — waiting %s", cfg.tick)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(cfg.tick):
		}
		return postUpdate(ctx, client, cfg, lat, lng)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("gps/update %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return nil
}

func parseFlags() config {
	baseURL := flag.String("base", "http://localhost:8080", "geo-service base URL")
	tripID := flag.Int64("trip", 4, "Laravel trip id")
	apiKey := flag.String("api-key", "", "X-API-Key (bypasses trip ownership in dev)")
	jwt := flag.String("jwt", "", "Bearer JWT for trip owner")
	speed := flag.Float64("speed-kmh", 45, "simulated travel speed")
	tick := flag.Duration("tick", 4*time.Second, "interval between GPS posts (>= GPS_RATE_LIMIT_MS)")
	pause := flag.Duration("pause", 3*time.Second, "pause at intermediate waypoints")
	loop := flag.Bool("loop", true, "restart route after reaching destination")

	startLat := flag.Float64("start-lat", 32.673130, "start latitude")
	startLng := flag.Float64("start-lng", 51.633027, "start longitude")
	pickupLat := flag.Float64("pickup-lat", 32.562882, "package pickup latitude")
	pickupLng := flag.Float64("pickup-lng", 51.753255, "package pickup longitude")
	destLat := flag.Float64("dest-lat", 32.796752, "destination latitude")
	destLng := flag.Float64("dest-lng", 51.749825, "destination longitude")

	flag.Parse()

	return config{
		baseURL:  strings.TrimSpace(*baseURL),
		tripID:   *tripID,
		apiKey:   strings.TrimSpace(*apiKey),
		jwt:      strings.TrimSpace(*jwt),
		speedKmH: *speed,
		tick:     *tick,
		pause:    *pause,
		loop:     *loop,
		waypoints: []waypoint{
			{name: "start", lat: *startLat, lng: *startLng},
			{name: "pickup", lat: *pickupLat, lng: *pickupLng},
			{name: "destination", lat: *destLat, lng: *destLng},
		},
	}
}

func bearingRad(lat1, lng1, lat2, lng2 float64) float64 {
	φ1 := lat1 * math.Pi / 180
	φ2 := lat2 * math.Pi / 180
	Δλ := (lng2 - lng1) * math.Pi / 180
	y := math.Sin(Δλ) * math.Cos(φ2)
	x := math.Cos(φ1)*math.Sin(φ2) - math.Sin(φ1)*math.Cos(φ2)*math.Cos(Δλ)
	return math.Atan2(y, x)
}

func destinationPoint(lat, lng, bearingRad, distanceKm float64) (float64, float64) {
	const r = 6371.0
	δ := distanceKm / r
	φ1 := lat * math.Pi / 180
	λ1 := lng * math.Pi / 180

	φ2 := math.Asin(math.Sin(φ1)*math.Cos(δ) + math.Cos(φ1)*math.Sin(δ)*math.Cos(bearingRad))
	λ2 := λ1 + math.Atan2(
		math.Sin(bearingRad)*math.Sin(δ)*math.Cos(φ1),
		math.Cos(δ)-math.Sin(φ1)*math.Sin(φ2),
	)

	return φ2 * 180 / math.Pi, λ2 * 180 / math.Pi
}
