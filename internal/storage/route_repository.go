package storage

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"geo-service/internal/model"
	"geo-service/internal/routing"
)

// SaveRouteCalculation stores a route request and every returned option in
// PostGIS. Coordinates use ST_MakePoint(lng, lat), because PostGIS expects X/Y.
func (p *Postgres) SaveRouteCalculation(ctx context.Context, req *model.RouteRequest, resp *model.RouteResponse) error {
	if p == nil || resp == nil || len(resp.Routes) == 0 {
		return nil
	}

	var calculationID int64
	const insertCalc = `
		INSERT INTO route_calculations (
			trip_id, mode, start_location, end_location,
			primary_distance_km, primary_duration_min, route_count
		)
		VALUES (
			$1, $2,
			ST_MakePoint($4, $3)::geography,
			ST_MakePoint($6, $5)::geography,
			$7, $8, $9
		)
		RETURNING id`

	err := p.pool.QueryRow(ctx, insertCalc,
		nullableTripID(req.TripID),
		resp.Mode,
		req.StartLat, req.StartLng,
		req.EndLat, req.EndLng,
		resp.Distance, resp.Duration, len(resp.Routes),
	).Scan(&calculationID)
	if err != nil {
		return fmt.Errorf("route calculation insert: %w", err)
	}

	b := &pgx.Batch{}
	const insertOption = `
		INSERT INTO route_options (
			calculation_id, rank, is_primary, distance_km,
			duration_min, polyline, path
		)
		VALUES (
			$1, $2, $3, $4, $5, $6,
			CASE WHEN $7 = '' THEN NULL ELSE ST_GeogFromText($7)::geography END
		)`

	for _, opt := range resp.Routes {
		b.Queue(insertOption,
			calculationID,
			opt.ID,
			opt.IsPrimary,
			opt.DistanceKm,
			opt.DurationMin,
			opt.Polyline,
			lineStringWKT(opt.Polyline),
		)
	}

	results := p.pool.SendBatch(ctx, b)
	defer results.Close()
	for range resp.Routes {
		if _, err := results.Exec(); err != nil {
			return fmt.Errorf("route option insert: %w", err)
		}
	}
	return nil
}

func nullableTripID(tripID int64) any {
	if tripID <= 0 {
		return nil
	}
	return tripID
}

func lineStringWKT(polyline string) string {
	points := routing.DecodePolyline(polyline)
	if len(points) < 2 {
		return ""
	}

	var b strings.Builder
	b.WriteString("SRID=4326;LINESTRING(")
	for i, p := range points {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(fmt.Sprintf("%.7f %.7f", p.Lng, p.Lat))
	}
	b.WriteByte(')')
	return b.String()
}
