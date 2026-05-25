// osm2stations imports railway/metro/tram stations from an OSM PBF/XML export
// into PostGIS rail_stations.
//
// Usage:
//
//	osm2stations -in iran-latest.osm.pbf -dsn "$POSTGRES_DSN" -region iran
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/qedus/osmpbf"

	"geo-service/internal/storage"
)

type station struct {
	osmNodeID   int64
	name        string
	nameEn      string
	stationType string
	lat         float64
	lng         float64
}

func main() {
	inFile := flag.String("in", "", "OSM PBF/XML file path")
	dsn := flag.String("dsn", os.Getenv("POSTGRES_DSN"), "Postgres DSN")
	region := flag.String("region", "", "Import region label, e.g. iran")
	truncate := flag.Bool("truncate", true, "Truncate rail_stations before import")
	batchSize := flag.Int("batch-size", 5000, "Rows per COPY staging batch")
	timeout := flag.Duration("timeout", 30*time.Minute, "Import timeout")
	flag.Parse()

	if *inFile == "" || *dsn == "" {
		flag.Usage()
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	pg, err := storage.NewPostgres(ctx, *dsn)
	if err != nil {
		log.Fatalf("postgres: %v", err)
	}
	defer pg.Close()

	if *truncate {
		if _, err := pg.Pool().Exec(ctx, "TRUNCATE rail_stations RESTART IDENTITY"); err != nil {
			log.Fatalf("truncate rail_stations: %v", err)
		}
	}

	stations, err := extractStations(*inFile)
	if err != nil {
		log.Fatalf("extract stations: %v", err)
	}
	log.Printf("found %d candidate stations", len(stations))

	if len(stations) == 0 {
		log.Fatal("no stations found in OSM file")
	}

	inserted, err := insertStations(ctx, pg.Pool(), stations, *region, *batchSize)
	if err != nil {
		log.Fatalf("insert stations: %v", err)
	}

	if _, err := pg.Pool().Exec(ctx, "ANALYZE rail_stations"); err != nil {
		log.Printf("analyze rail_stations warning: %v", err)
	}
	log.Printf("imported %d rail_stations into PostGIS (region=%q)", inserted, *region)
}

// extractStations walks an OSM PBF and returns every node that represents a
// passenger boarding point on the rail network.
func extractStations(path string) ([]station, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	decoder := osmpbf.NewDecoder(f)
	if err := decoder.Start(runtime.NumCPU()); err != nil {
		return nil, fmt.Errorf("start PBF decoder: %w", err)
	}

	var stations []station
	for {
		v, err := decoder.Decode()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("decode PBF: %w", err)
		}
		node, ok := v.(*osmpbf.Node)
		if !ok {
			continue
		}
		s, ok := stationFromNode(node.ID, node.Lat, node.Lon, node.Tags)
		if !ok {
			continue
		}
		stations = append(stations, s)
	}
	return stations, nil
}

// stationFromNode returns a station record when tags indicate a passenger stop.
// Recognized tags:
//
//	railway=station | halt | tram_stop | stop | subway_entrance
//	public_transport=station | stop_position (when railway tag is absent)
func stationFromNode(id int64, lat, lng float64, tags map[string]string) (station, bool) {
	railway := strings.TrimSpace(tags["railway"])
	pt := strings.TrimSpace(tags["public_transport"])
	stationType, ok := classifyStation(railway, pt, tags)
	if !ok {
		return station{}, false
	}
	if lat == 0 && lng == 0 {
		return station{}, false
	}
	return station{
		osmNodeID:   id,
		name:        firstNonEmpty(tags["name:fa"], tags["name"]),
		nameEn:      firstNonEmpty(tags["name:en"], tags["int_name"]),
		stationType: stationType,
		lat:         lat,
		lng:         lng,
	}, true
}

func classifyStation(railway, publicTransport string, tags map[string]string) (string, bool) {
	switch railway {
	case "station", "halt", "stop":
		return stationSubtype(tags, "train"), true
	case "tram_stop":
		return "tram", true
	case "subway_entrance":
		return "metro", true
	}
	if publicTransport == "station" || publicTransport == "stop_position" {
		// Require some rail/transit qualifier to avoid bus stops, ferries, etc.
		if isTrue(tags["train"]) {
			return "train", true
		}
		if isTrue(tags["subway"]) {
			return "metro", true
		}
		if isTrue(tags["light_rail"]) {
			return "light_rail", true
		}
		if isTrue(tags["tram"]) {
			return "tram", true
		}
	}
	return "", false
}

// stationSubtype refines a railway=station/halt/stop into a more specific kind
// when sub-tags say so (subway, light_rail, monorail, tram).
func stationSubtype(tags map[string]string, fallback string) string {
	switch strings.ToLower(strings.TrimSpace(tags["station"])) {
	case "subway":
		return "metro"
	case "light_rail":
		return "light_rail"
	case "monorail":
		return "monorail"
	}
	if isTrue(tags["subway"]) {
		return "metro"
	}
	if isTrue(tags["light_rail"]) {
		return "light_rail"
	}
	if isTrue(tags["monorail"]) {
		return "monorail"
	}
	if isTrue(tags["tram"]) {
		return "tram"
	}
	return fallback
}

func isTrue(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "yes", "designated", "permissive", "1", "true":
		return true
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func insertStations(ctx context.Context, pool *pgxpool.Pool, stations []station, region string, batchSize int) (int, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Release()

	const stageSQL = `
		CREATE TEMP TABLE rail_station_import_stage (
			osm_node_id   BIGINT NOT NULL,
			name          TEXT NOT NULL,
			name_en       TEXT NOT NULL,
			station_type  TEXT NOT NULL,
			lat           DOUBLE PRECISION NOT NULL,
			lng           DOUBLE PRECISION NOT NULL,
			geom_wkt      TEXT NOT NULL,
			import_region TEXT NOT NULL
		) ON COMMIT DROP`
	tx, err := conn.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, stageSQL); err != nil {
		return 0, fmt.Errorf("create stage: %w", err)
	}

	cols := []string{"osm_node_id", "name", "name_en", "station_type", "lat", "lng", "geom_wkt", "import_region"}
	rows := make([][]any, 0, batchSize)
	flush := func() error {
		if len(rows) == 0 {
			return nil
		}
		_, err := tx.CopyFrom(ctx, pgx.Identifier{"rail_station_import_stage"}, cols, pgx.CopyFromRows(rows))
		if err != nil {
			return fmt.Errorf("copy stage: %w", err)
		}
		rows = rows[:0]
		return nil
	}

	for _, s := range stations {
		rows = append(rows, []any{
			s.osmNodeID,
			s.name,
			s.nameEn,
			s.stationType,
			s.lat,
			s.lng,
			fmt.Sprintf("SRID=4326;POINT(%.7f %.7f)", s.lng, s.lat),
			region,
		})
		if len(rows) >= batchSize {
			if err := flush(); err != nil {
				return 0, err
			}
		}
	}
	if err := flush(); err != nil {
		return 0, err
	}

	const insertSQL = `
		INSERT INTO rail_stations
		    (osm_node_id, name, name_en, station_type, lat, lng, geom, import_region)
		SELECT
		    osm_node_id, name, name_en, station_type, lat, lng,
		    ST_GeogFromText(geom_wkt)::geography,
		    import_region
		FROM rail_station_import_stage
		ON CONFLICT (osm_node_id) DO UPDATE
		    SET name          = EXCLUDED.name,
		        name_en       = EXCLUDED.name_en,
		        station_type  = EXCLUDED.station_type,
		        lat           = EXCLUDED.lat,
		        lng           = EXCLUDED.lng,
		        geom          = EXCLUDED.geom,
		        import_region = EXCLUDED.import_region`
	tag, err := tx.Exec(ctx, insertSQL)
	if err != nil {
		return 0, fmt.Errorf("insert into rail_stations: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}
