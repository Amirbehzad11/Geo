package storage

import (
	"context"
	"fmt"
	"log"
	"time"
)

const schema = `
CREATE EXTENSION IF NOT EXISTS postgis;

CREATE TABLE IF NOT EXISTS trip_locations (
    id         BIGSERIAL NOT NULL,
    trip_id    BIGINT    NOT NULL,
    location   GEOGRAPHY(POINT, 4326) NOT NULL,
    speed_kmh  FLOAT     NOT NULL DEFAULT 0,
    timestamp  BIGINT    NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id, timestamp)
) PARTITION BY RANGE (timestamp);

CREATE INDEX IF NOT EXISTS idx_trip_locations_trip_id
    ON trip_locations (trip_id);

CREATE INDEX IF NOT EXISTS idx_trip_locations_gist
    ON trip_locations USING GIST (location);

CREATE INDEX IF NOT EXISTS idx_trip_locations_ts
    ON trip_locations (trip_id, timestamp DESC);

CREATE INDEX IF NOT EXISTS idx_trip_locations_timestamp_brin
    ON trip_locations USING BRIN (timestamp);

CREATE INDEX IF NOT EXISTS idx_trip_locations_created_brin
    ON trip_locations USING BRIN (created_at);

CREATE TABLE IF NOT EXISTS route_calculations (
    id                      BIGSERIAL PRIMARY KEY,
    trip_id                 BIGINT,
    mode                    TEXT      NOT NULL,
    start_location          GEOGRAPHY(POINT, 4326) NOT NULL,
    end_location            GEOGRAPHY(POINT, 4326) NOT NULL,
    primary_distance_km     DOUBLE PRECISION NOT NULL,
    primary_duration_min    DOUBLE PRECISION NOT NULL,
    route_count             INT       NOT NULL,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_route_calculations_trip_id
    ON route_calculations (trip_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_route_calculations_created_brin
    ON route_calculations USING BRIN (created_at);

CREATE INDEX IF NOT EXISTS idx_route_calculations_start_gist
    ON route_calculations USING GIST (start_location);

CREATE INDEX IF NOT EXISTS idx_route_calculations_end_gist
    ON route_calculations USING GIST (end_location);

CREATE TABLE IF NOT EXISTS route_options (
    id              BIGSERIAL PRIMARY KEY,
    calculation_id  BIGINT NOT NULL REFERENCES route_calculations(id) ON DELETE CASCADE,
    rank            INT    NOT NULL,
    is_primary      BOOLEAN NOT NULL DEFAULT FALSE,
    distance_km     DOUBLE PRECISION NOT NULL,
    duration_min    DOUBLE PRECISION NOT NULL,
    polyline        TEXT NOT NULL,
    path            GEOGRAPHY(LINESTRING, 4326),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_route_options_calculation
    ON route_options (calculation_id, rank);

CREATE INDEX IF NOT EXISTS idx_route_options_path_gist
    ON route_options USING GIST (path);

CREATE TABLE IF NOT EXISTS road_segments (
    id                  BIGSERIAL PRIMARY KEY,
    osm_way_id          BIGINT NOT NULL,
    from_node_id        BIGINT NOT NULL,
    to_node_id          BIGINT NOT NULL,
    highway_type        TEXT NOT NULL,
    name                TEXT NOT NULL DEFAULT '',
    speed_kmh           DOUBLE PRECISION NOT NULL,
    distance_km         DOUBLE PRECISION NOT NULL,
    bidirectional       BOOLEAN NOT NULL DEFAULT TRUE,
    car_allowed         BOOLEAN NOT NULL DEFAULT FALSE,
    motorcycle_allowed  BOOLEAN NOT NULL DEFAULT FALSE,
    bus_allowed         BOOLEAN NOT NULL DEFAULT FALSE,
    foot_allowed        BOOLEAN NOT NULL DEFAULT FALSE,
    from_lat            DOUBLE PRECISION NOT NULL,
    from_lng            DOUBLE PRECISION NOT NULL,
    to_lat              DOUBLE PRECISION NOT NULL,
    to_lng              DOUBLE PRECISION NOT NULL,
    geom                GEOGRAPHY(LINESTRING, 4326) NOT NULL,
    import_region       TEXT NOT NULL DEFAULT '',
    imported_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE road_segments
    ADD COLUMN IF NOT EXISTS import_region TEXT NOT NULL DEFAULT '';

ALTER TABLE road_segments
    ADD COLUMN IF NOT EXISTS name TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_road_segments_from_node
    ON road_segments (from_node_id);

CREATE INDEX IF NOT EXISTS idx_road_segments_to_node
    ON road_segments (to_node_id);

CREATE INDEX IF NOT EXISTS idx_road_segments_osm_edge
    ON road_segments (osm_way_id, from_node_id, to_node_id);

CREATE INDEX IF NOT EXISTS idx_road_segments_highway
    ON road_segments (highway_type);

CREATE INDEX IF NOT EXISTS idx_road_segments_car
    ON road_segments (car_allowed);

CREATE INDEX IF NOT EXISTS idx_road_segments_region
    ON road_segments (import_region);

CREATE INDEX IF NOT EXISTS idx_road_segments_car_from
    ON road_segments (from_node_id) WHERE car_allowed;

CREATE INDEX IF NOT EXISTS idx_road_segments_motorcycle_from
    ON road_segments (from_node_id) WHERE motorcycle_allowed;

CREATE INDEX IF NOT EXISTS idx_road_segments_bus_from
    ON road_segments (from_node_id) WHERE bus_allowed;

CREATE INDEX IF NOT EXISTS idx_road_segments_foot_from
    ON road_segments (from_node_id) WHERE foot_allowed;

CREATE INDEX IF NOT EXISTS idx_road_segments_imported_brin
    ON road_segments USING BRIN (imported_at);

CREATE INDEX IF NOT EXISTS idx_road_segments_geom_gist
    ON road_segments USING GIST (geom);

CREATE TABLE IF NOT EXISTS rail_segments (
    id             BIGSERIAL PRIMARY KEY,
    osm_way_id     BIGINT NOT NULL,
    from_node_id   BIGINT NOT NULL,
    to_node_id     BIGINT NOT NULL,
    railway_type   TEXT NOT NULL,
    name           TEXT NOT NULL DEFAULT '',
    speed_kmh      DOUBLE PRECISION NOT NULL,
    distance_km    DOUBLE PRECISION NOT NULL,
    bidirectional  BOOLEAN NOT NULL DEFAULT TRUE,
    from_lat       DOUBLE PRECISION NOT NULL,
    from_lng       DOUBLE PRECISION NOT NULL,
    to_lat         DOUBLE PRECISION NOT NULL,
    to_lng         DOUBLE PRECISION NOT NULL,
    geom           GEOGRAPHY(LINESTRING, 4326) NOT NULL,
    import_region  TEXT NOT NULL DEFAULT '',
    imported_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_rail_segments_from_node
    ON rail_segments (from_node_id);

CREATE INDEX IF NOT EXISTS idx_rail_segments_to_node
    ON rail_segments (to_node_id);

CREATE INDEX IF NOT EXISTS idx_rail_segments_geom_gist
    ON rail_segments USING GIST (geom);

CREATE INDEX IF NOT EXISTS idx_rail_segments_region
    ON rail_segments (import_region);

CREATE TABLE IF NOT EXISTS rail_stations (
    id            BIGSERIAL PRIMARY KEY,
    osm_node_id   BIGINT NOT NULL,
    name          TEXT NOT NULL DEFAULT '',
    name_en       TEXT NOT NULL DEFAULT '',
    station_type  TEXT NOT NULL,
    lat           DOUBLE PRECISION NOT NULL,
    lng           DOUBLE PRECISION NOT NULL,
    geom          GEOGRAPHY(POINT, 4326) NOT NULL,
    import_region TEXT NOT NULL DEFAULT '',
    imported_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (osm_node_id)
);

CREATE INDEX IF NOT EXISTS idx_rail_stations_geom_gist
    ON rail_stations USING GIST (geom);

CREATE INDEX IF NOT EXISTS idx_rail_stations_type
    ON rail_stations (station_type);

CREATE INDEX IF NOT EXISTS idx_rail_stations_region
    ON rail_stations (import_region);

CREATE TABLE IF NOT EXISTS bus_stops (
    id            BIGSERIAL PRIMARY KEY,
    osm_node_id   BIGINT NOT NULL,
    name          TEXT NOT NULL DEFAULT '',
    name_en       TEXT NOT NULL DEFAULT '',
    city          TEXT NOT NULL,
    lat           DOUBLE PRECISION NOT NULL,
    lng           DOUBLE PRECISION NOT NULL,
    geom          GEOGRAPHY(POINT, 4326) NOT NULL,
    imported_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (osm_node_id)
);

CREATE INDEX IF NOT EXISTS idx_bus_stops_geom_gist
    ON bus_stops USING GIST (geom);

CREATE INDEX IF NOT EXISTS idx_bus_stops_city
    ON bus_stops (city);

CREATE TABLE IF NOT EXISTS bus_lines (
    id              BIGSERIAL PRIMARY KEY,
    osm_relation_id BIGINT NOT NULL,
    ref             TEXT NOT NULL DEFAULT '',
    name            TEXT NOT NULL DEFAULT '',
    operator        TEXT NOT NULL DEFAULT '',
    city            TEXT NOT NULL,
    color           TEXT NOT NULL DEFAULT '',
    imported_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (osm_relation_id)
);

CREATE INDEX IF NOT EXISTS idx_bus_lines_city
    ON bus_lines (city);

CREATE INDEX IF NOT EXISTS idx_bus_lines_ref
    ON bus_lines (city, ref);

CREATE TABLE IF NOT EXISTS bus_line_stops (
    line_id        BIGINT NOT NULL REFERENCES bus_lines(id) ON DELETE CASCADE,
    stop_id        BIGINT NOT NULL REFERENCES bus_stops(id) ON DELETE CASCADE,
    stop_sequence  INT NOT NULL,
    PRIMARY KEY (line_id, stop_sequence)
);

CREATE INDEX IF NOT EXISTS idx_bus_line_stops_stop
    ON bus_line_stops (stop_id);

CREATE TABLE IF NOT EXISTS metro_stations (
    id            BIGSERIAL PRIMARY KEY,
    osm_node_id   BIGINT NOT NULL,
    name          TEXT NOT NULL DEFAULT '',
    name_en       TEXT NOT NULL DEFAULT '',
    city          TEXT NOT NULL,
    lat           DOUBLE PRECISION NOT NULL,
    lng           DOUBLE PRECISION NOT NULL,
    geom          GEOGRAPHY(POINT, 4326) NOT NULL,
    imported_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (osm_node_id)
);

CREATE INDEX IF NOT EXISTS idx_metro_stations_geom_gist
    ON metro_stations USING GIST (geom);

CREATE INDEX IF NOT EXISTS idx_metro_stations_city
    ON metro_stations (city);

CREATE TABLE IF NOT EXISTS metro_lines (
    id              BIGSERIAL PRIMARY KEY,
    osm_relation_id BIGINT NOT NULL,
    ref             TEXT NOT NULL DEFAULT '',
    name            TEXT NOT NULL DEFAULT '',
    city            TEXT NOT NULL,
    color           TEXT NOT NULL DEFAULT '',
    imported_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (osm_relation_id)
);

CREATE INDEX IF NOT EXISTS idx_metro_lines_city
    ON metro_lines (city);

CREATE INDEX IF NOT EXISTS idx_metro_lines_ref
    ON metro_lines (city, ref);

CREATE TABLE IF NOT EXISTS metro_line_stations (
    line_id          BIGINT NOT NULL REFERENCES metro_lines(id) ON DELETE CASCADE,
    station_id       BIGINT NOT NULL REFERENCES metro_stations(id) ON DELETE CASCADE,
    station_sequence INT NOT NULL,
    PRIMARY KEY (line_id, station_sequence)
);

CREATE INDEX IF NOT EXISTS idx_metro_line_stations_station
    ON metro_line_stations (station_id);
`

func (p *Postgres) migrate(ctx context.Context) error {
	if _, err := p.pool.Exec(ctx, schema); err != nil {
		return err
	}
	return p.ensureTripLocationPartitions(ctx)
}

func (p *Postgres) ensureTripLocationPartitions(ctx context.Context) error {
	var partitioned bool
	const q = `
		SELECT EXISTS (
			SELECT 1
			FROM pg_partitioned_table pt
			JOIN pg_class c ON c.oid = pt.partrelid
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = current_schema()
			  AND c.relname = 'trip_locations'
		)`
	if err := p.pool.QueryRow(ctx, q).Scan(&partitioned); err != nil {
		return err
	}
	if !partitioned {
		return nil
	}

	month := time.Now().UTC()
	monthStart := time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, time.UTC)
	for i := -1; i <= 3; i++ {
		start := monthStart.AddDate(0, i, 0)
		if err := p.createTripLocationPartition(ctx, start); err != nil {
			log.Printf("[storage] trip_locations partition warning (%s): %v", start.Format("2006-01"), err)
		}
	}

	_, err := p.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS trip_locations_default
		PARTITION OF trip_locations DEFAULT`)
	return err
}

func (p *Postgres) createTripLocationPartition(ctx context.Context, start time.Time) error {
	end := start.AddDate(0, 1, 0)
	table := fmt.Sprintf("trip_locations_%04d_%02d", start.Year(), start.Month())
	query := fmt.Sprintf(
		`CREATE TABLE IF NOT EXISTS %s PARTITION OF trip_locations FOR VALUES FROM (%d) TO (%d)`,
		quoteIdent(table),
		start.Unix(),
		end.Unix(),
	)
	_, err := p.pool.Exec(ctx, query)
	return err
}
