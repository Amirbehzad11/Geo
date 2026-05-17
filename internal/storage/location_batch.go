package storage

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"geo-service/internal/events"
	"geo-service/internal/model"
)

// LocationBatchWriter subscribes to LocationUpdated events and bulk-inserts
// into trip_locations in configurable batches to minimise DB round-trips.
type LocationBatchWriter struct {
	pg         *Postgres
	bus        *events.Bus
	batchSize  int
	flushEvery time.Duration
}

func NewLocationBatchWriter(pg *Postgres, bus *events.Bus, batchSize int, flushSec int64) *LocationBatchWriter {
	if flushSec <= 0 {
		flushSec = 5
	}
	if batchSize <= 0 {
		batchSize = 500
	}
	return &LocationBatchWriter{
		pg:         pg,
		bus:        bus,
		batchSize:  batchSize,
		flushEvery: time.Duration(flushSec) * time.Second,
	}
}

// Run blocks until ctx is cancelled. Must be started in a goroutine.
func (w *LocationBatchWriter) Run(ctx context.Context) {
	eventCh, closer := w.bus.SubscribeAll(ctx)
	defer closer()

	ticker := time.NewTicker(w.flushEvery)
	defer ticker.Stop()

	batch := make([]*model.LocationState, 0, w.batchSize)

	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := w.insertBatch(ctx, batch); err != nil {
			log.Printf("[storage] batch insert error: %v", err)
		}
		batch = batch[:0]
	}

	for {
		select {
		case ev, ok := <-eventCh:
			if !ok {
				flush()
				return
			}
			if ev.Type != events.LocationUpdated {
				continue
			}
			var state model.LocationState
			if err := json.Unmarshal(ev.Payload, &state); err != nil {
				continue
			}
			batch = append(batch, &state)
			if len(batch) >= w.batchSize {
				flush()
			}

		case <-ticker.C:
			flush()

		case <-ctx.Done():
			// Drain remaining items with a short deadline.
			flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if len(batch) > 0 {
				_ = w.insertBatch(flushCtx, batch)
			}
			cancel()
			return
		}
	}
}

// insertBatch performs one set-based insert for all locations.
// Uses ST_MakePoint(lng, lat) because PostGIS expects longitude first.
func (w *LocationBatchWriter) insertBatch(ctx context.Context, locs []*model.LocationState) error {
	const q = `
		INSERT INTO trip_locations (trip_id, location, speed_kmh, timestamp)
		SELECT
			trip_id,
			ST_MakePoint(lng, lat)::geography,
			speed_kmh,
			ts
		FROM unnest(
			$1::bigint[],
			$2::double precision[],
			$3::double precision[],
			$4::double precision[],
			$5::bigint[]
		) AS rows(trip_id, lat, lng, speed_kmh, ts)`

	tripIDs := make([]int64, 0, len(locs))
	lats := make([]float64, 0, len(locs))
	lngs := make([]float64, 0, len(locs))
	speeds := make([]float64, 0, len(locs))
	timestamps := make([]int64, 0, len(locs))

	for _, l := range locs {
		tripIDs = append(tripIDs, l.TripID)
		lats = append(lats, l.Lat)
		lngs = append(lngs, l.Lng)
		speeds = append(speeds, l.SpeedKmH)
		timestamps = append(timestamps, l.Timestamp)
	}

	_, err := w.pg.pool.Exec(ctx, q, tripIDs, lats, lngs, speeds, timestamps)
	return err
}
