// osm2transit imports bus stops, metro stations, and route relations for
// the supported Iranian cities (Tehran, Mashhad, Isfahan, Shiraz, Tabriz)
// from an OSM PBF export into PostGIS.
//
// Usage:
//
//	osm2transit -in iran-latest.osm.pbf -dsn "$POSTGRES_DSN"
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

// cityBBox is the inclusive lat/lng bounding box for one supported city.
type cityBBox struct {
	name    string
	latMin  float64
	lngMin  float64
	latMax  float64
	lngMax  float64
}

// supportedCities lists every metropolitan area we import. A node or relation
// belongs to a city when its representative coordinate falls inside the city
// bbox. Bboxes are intentionally generous to cover suburbs and BRT terminals.
var supportedCities = []cityBBox{
	{name: "tehran", latMin: 35.55, lngMin: 51.10, latMax: 35.85, lngMax: 51.65},
	{name: "mashhad", latMin: 36.20, lngMin: 59.35, latMax: 36.45, lngMax: 59.80},
	{name: "isfahan", latMin: 32.55, lngMin: 51.55, latMax: 32.80, lngMax: 51.85},
	{name: "shiraz", latMin: 29.50, lngMin: 52.40, latMax: 29.80, lngMax: 52.75},
	{name: "tabriz", latMin: 38.00, lngMin: 46.15, latMax: 38.18, lngMax: 46.45},
}

func cityFor(lat, lng float64) string {
	for _, c := range supportedCities {
		if lat >= c.latMin && lat <= c.latMax && lng >= c.lngMin && lng <= c.lngMax {
			return c.name
		}
	}
	return ""
}

type stopNode struct {
	osmID  int64
	name   string
	nameEn string
	city   string
	lat    float64
	lng    float64
}

type routeRelation struct {
	osmID    int64
	ref      string
	name     string
	operator string
	color    string
	city     string
	stops    []int64 // ordered list of OSM node IDs of stops
}

func main() {
	inFile := flag.String("in", "", "OSM PBF file path")
	dsn := flag.String("dsn", os.Getenv("POSTGRES_DSN"), "Postgres DSN")
	truncate := flag.Bool("truncate", true, "Truncate transit tables before import")
	timeout := flag.Duration("timeout", 1*time.Hour, "Import timeout")
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

	log.Println("pass 1: extracting bus stops + metro stations from PBF...")
	busStops, metroStations, err := extractStops(*inFile)
	if err != nil {
		log.Fatalf("extract stops: %v", err)
	}
	log.Printf("found %d bus stops, %d metro stations across %d cities",
		len(busStops), len(metroStations), len(supportedCities))

	log.Println("pass 2: extracting route relations from PBF...")
	busRoutes, metroRoutes, err := extractRoutes(*inFile, busStops, metroStations)
	if err != nil {
		log.Fatalf("extract routes: %v", err)
	}
	log.Printf("found %d bus routes, %d metro routes", len(busRoutes), len(metroRoutes))

	log.Println("writing to Postgres...")
	if err := writeAll(ctx, pg.Pool(), *truncate, busStops, metroStations, busRoutes, metroRoutes); err != nil {
		log.Fatalf("write: %v", err)
	}

	for _, table := range []string{"bus_stops", "bus_lines", "bus_line_stops", "metro_stations", "metro_lines", "metro_line_stations"} {
		if _, err := pg.Pool().Exec(ctx, "ANALYZE "+pgx.Identifier{table}.Sanitize()); err != nil {
			log.Printf("analyze %s warning: %v", table, err)
		}
	}

	log.Printf("transit import complete")
}

// extractStops walks the PBF once and returns bus stops and metro stations
// keyed by OSM node ID. Nodes outside every supported city bbox are dropped.
func extractStops(path string) (map[int64]stopNode, map[int64]stopNode, error) {
	busStops := make(map[int64]stopNode)
	metroStations := make(map[int64]stopNode)

	err := scanPBF(path, func(v any) {
		node, ok := v.(*osmpbf.Node)
		if !ok {
			return
		}
		city := cityFor(node.Lat, node.Lon)
		if city == "" {
			return
		}
		if isBusStop(node.Tags) {
			busStops[node.ID] = stopNode{
				osmID:  node.ID,
				name:   firstNonEmpty(node.Tags["name:fa"], node.Tags["name"]),
				nameEn: firstNonEmpty(node.Tags["name:en"], node.Tags["int_name"]),
				city:   city,
				lat:    node.Lat,
				lng:    node.Lon,
			}
			return
		}
		if isMetroStation(node.Tags) {
			metroStations[node.ID] = stopNode{
				osmID:  node.ID,
				name:   firstNonEmpty(node.Tags["name:fa"], node.Tags["name"]),
				nameEn: firstNonEmpty(node.Tags["name:en"], node.Tags["int_name"]),
				city:   city,
				lat:    node.Lat,
				lng:    node.Lon,
			}
		}
	})
	if err != nil {
		return nil, nil, err
	}
	return busStops, metroStations, nil
}

// extractRoutes walks the PBF a second time and collects route=bus / route=subway
// relations whose stops fall inside one of the supported cities. The relation's
// city is the city of its first matching stop.
func extractRoutes(path string, busStops, metroStations map[int64]stopNode) ([]routeRelation, []routeRelation, error) {
	var busRoutes, metroRoutes []routeRelation

	err := scanPBF(path, func(v any) {
		rel, ok := v.(*osmpbf.Relation)
		if !ok {
			return
		}
		if rel.Tags["type"] != "route" {
			return
		}
		routeTag := rel.Tags["route"]
		var lookup map[int64]stopNode
		switch routeTag {
		case "bus":
			lookup = busStops
		case "subway", "light_rail":
			lookup = metroStations
		default:
			return
		}

		stops, city := orderedStops(rel.Members, lookup)
		if len(stops) < 2 || city == "" {
			return
		}

		r := routeRelation{
			osmID:    rel.ID,
			ref:      strings.TrimSpace(rel.Tags["ref"]),
			name:     strings.TrimSpace(firstNonEmpty(rel.Tags["name:fa"], rel.Tags["name"])),
			operator: strings.TrimSpace(rel.Tags["operator"]),
			color:    strings.TrimSpace(firstNonEmpty(rel.Tags["colour"], rel.Tags["color"])),
			city:     city,
			stops:    stops,
		}
		if routeTag == "bus" {
			busRoutes = append(busRoutes, r)
		} else {
			metroRoutes = append(metroRoutes, r)
		}
	})
	if err != nil {
		return nil, nil, err
	}
	return busRoutes, metroRoutes, nil
}

// orderedStops walks the relation members in order and keeps every node that
// corresponds to a known stop. Stops belonging to different cities are dropped
// to keep a relation strictly intra-city. Duplicates in a row are skipped.
func orderedStops(members []osmpbf.Member, lookup map[int64]stopNode) ([]int64, string) {
	out := make([]int64, 0, len(members))
	city := ""
	var lastID int64
	for _, m := range members {
		if m.Type != osmpbf.NodeType {
			continue
		}
		stop, ok := lookup[m.ID]
		if !ok {
			continue
		}
		if city == "" {
			city = stop.city
		} else if stop.city != city {
			continue // skip stops from another city
		}
		if m.ID == lastID {
			continue
		}
		out = append(out, m.ID)
		lastID = m.ID
	}
	return out, city
}

func isBusStop(tags map[string]string) bool {
	if tags["highway"] == "bus_stop" {
		return true
	}
	if tags["public_transport"] == "platform" && isTrue(tags["bus"]) {
		return true
	}
	if tags["public_transport"] == "stop_position" && isTrue(tags["bus"]) {
		return true
	}
	return false
}

func isMetroStation(tags map[string]string) bool {
	if tags["railway"] == "subway_entrance" {
		return true
	}
	switch tags["railway"] {
	case "station", "halt", "stop":
		if isTrue(tags["subway"]) || tags["station"] == "subway" {
			return true
		}
		if isTrue(tags["light_rail"]) || tags["station"] == "light_rail" {
			return true
		}
	}
	if tags["public_transport"] == "station" {
		if isTrue(tags["subway"]) || isTrue(tags["light_rail"]) {
			return true
		}
	}
	return false
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

func scanPBF(path string, visit func(any)) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	decoder := osmpbf.NewDecoder(f)
	if err := decoder.Start(runtime.NumCPU()); err != nil {
		return fmt.Errorf("start PBF decoder: %w", err)
	}
	for {
		v, err := decoder.Decode()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("decode PBF: %w", err)
		}
		visit(v)
	}
	return nil
}

// ---- DB writes -------------------------------------------------------------

func writeAll(
	ctx context.Context,
	pool *pgxpool.Pool,
	truncate bool,
	busStops, metroStations map[int64]stopNode,
	busRoutes, metroRoutes []routeRelation,
) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if truncate {
		const truncSQL = `
			TRUNCATE bus_line_stops, bus_lines, bus_stops,
			         metro_line_stations, metro_lines, metro_stations
			RESTART IDENTITY CASCADE`
		if _, err := tx.Exec(ctx, truncSQL); err != nil {
			return fmt.Errorf("truncate: %w", err)
		}
	}

	busStopIDs, err := insertBusStops(ctx, tx, busStops)
	if err != nil {
		return err
	}
	log.Printf("inserted %d bus_stops", len(busStopIDs))

	metroStationIDs, err := insertMetroStations(ctx, tx, metroStations)
	if err != nil {
		return err
	}
	log.Printf("inserted %d metro_stations", len(metroStationIDs))

	busLineCount, busLinkCount, err := insertBusRoutes(ctx, tx, busRoutes, busStopIDs)
	if err != nil {
		return err
	}
	log.Printf("inserted %d bus_lines, %d bus_line_stops", busLineCount, busLinkCount)

	metroLineCount, metroLinkCount, err := insertMetroRoutes(ctx, tx, metroRoutes, metroStationIDs)
	if err != nil {
		return err
	}
	log.Printf("inserted %d metro_lines, %d metro_line_stations", metroLineCount, metroLinkCount)

	return tx.Commit(ctx)
}

// insertBusStops inserts every bus stop and returns a map from OSM node ID to
// the newly-assigned database row ID so that route relations can link to them.
func insertBusStops(ctx context.Context, tx pgx.Tx, stops map[int64]stopNode) (map[int64]int64, error) {
	out := make(map[int64]int64, len(stops))
	const q = `
		INSERT INTO bus_stops (osm_node_id, name, name_en, city, lat, lng, geom)
		VALUES ($1, $2, $3, $4, $5, $6, ST_GeogFromText($7))
		ON CONFLICT (osm_node_id) DO UPDATE SET
			name = EXCLUDED.name,
			name_en = EXCLUDED.name_en,
			city = EXCLUDED.city,
			lat = EXCLUDED.lat,
			lng = EXCLUDED.lng,
			geom = EXCLUDED.geom
		RETURNING id`
	for _, s := range stops {
		wkt := fmt.Sprintf("SRID=4326;POINT(%.7f %.7f)", s.lng, s.lat)
		var id int64
		if err := tx.QueryRow(ctx, q, s.osmID, s.name, s.nameEn, s.city, s.lat, s.lng, wkt).Scan(&id); err != nil {
			return nil, fmt.Errorf("insert bus_stop %d: %w", s.osmID, err)
		}
		out[s.osmID] = id
	}
	return out, nil
}

func insertMetroStations(ctx context.Context, tx pgx.Tx, stations map[int64]stopNode) (map[int64]int64, error) {
	out := make(map[int64]int64, len(stations))
	const q = `
		INSERT INTO metro_stations (osm_node_id, name, name_en, city, lat, lng, geom)
		VALUES ($1, $2, $3, $4, $5, $6, ST_GeogFromText($7))
		ON CONFLICT (osm_node_id) DO UPDATE SET
			name = EXCLUDED.name,
			name_en = EXCLUDED.name_en,
			city = EXCLUDED.city,
			lat = EXCLUDED.lat,
			lng = EXCLUDED.lng,
			geom = EXCLUDED.geom
		RETURNING id`
	for _, s := range stations {
		wkt := fmt.Sprintf("SRID=4326;POINT(%.7f %.7f)", s.lng, s.lat)
		var id int64
		if err := tx.QueryRow(ctx, q, s.osmID, s.name, s.nameEn, s.city, s.lat, s.lng, wkt).Scan(&id); err != nil {
			return nil, fmt.Errorf("insert metro_station %d: %w", s.osmID, err)
		}
		out[s.osmID] = id
	}
	return out, nil
}

func insertBusRoutes(ctx context.Context, tx pgx.Tx, routes []routeRelation, stopIDs map[int64]int64) (int, int, error) {
	const insertLine = `
		INSERT INTO bus_lines (osm_relation_id, ref, name, operator, city, color)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (osm_relation_id) DO UPDATE SET
			ref = EXCLUDED.ref,
			name = EXCLUDED.name,
			operator = EXCLUDED.operator,
			city = EXCLUDED.city,
			color = EXCLUDED.color
		RETURNING id`

	const insertLink = `
		INSERT INTO bus_line_stops (line_id, stop_id, stop_sequence)
		VALUES ($1, $2, $3)
		ON CONFLICT (line_id, stop_sequence) DO UPDATE SET stop_id = EXCLUDED.stop_id`

	lineCount, linkCount := 0, 0
	for _, r := range routes {
		var lineID int64
		if err := tx.QueryRow(ctx, insertLine, r.osmID, r.ref, r.name, r.operator, r.city, r.color).Scan(&lineID); err != nil {
			return lineCount, linkCount, fmt.Errorf("insert bus_line %d: %w", r.osmID, err)
		}
		lineCount++
		// Clear any previous links for this line so re-imports stay clean.
		if _, err := tx.Exec(ctx, `DELETE FROM bus_line_stops WHERE line_id = $1`, lineID); err != nil {
			return lineCount, linkCount, fmt.Errorf("delete bus_line_stops for line %d: %w", lineID, err)
		}
		seq := 0
		for _, osmStopID := range r.stops {
			stopID, ok := stopIDs[osmStopID]
			if !ok {
				continue
			}
			if _, err := tx.Exec(ctx, insertLink, lineID, stopID, seq); err != nil {
				return lineCount, linkCount, fmt.Errorf("insert bus_line_stops line=%d seq=%d: %w", lineID, seq, err)
			}
			seq++
			linkCount++
		}
	}
	return lineCount, linkCount, nil
}

func insertMetroRoutes(ctx context.Context, tx pgx.Tx, routes []routeRelation, stationIDs map[int64]int64) (int, int, error) {
	const insertLine = `
		INSERT INTO metro_lines (osm_relation_id, ref, name, city, color)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (osm_relation_id) DO UPDATE SET
			ref = EXCLUDED.ref,
			name = EXCLUDED.name,
			city = EXCLUDED.city,
			color = EXCLUDED.color
		RETURNING id`

	const insertLink = `
		INSERT INTO metro_line_stations (line_id, station_id, station_sequence)
		VALUES ($1, $2, $3)
		ON CONFLICT (line_id, station_sequence) DO UPDATE SET station_id = EXCLUDED.station_id`

	lineCount, linkCount := 0, 0
	for _, r := range routes {
		var lineID int64
		if err := tx.QueryRow(ctx, insertLine, r.osmID, r.ref, r.name, r.city, r.color).Scan(&lineID); err != nil {
			return lineCount, linkCount, fmt.Errorf("insert metro_line %d: %w", r.osmID, err)
		}
		lineCount++
		if _, err := tx.Exec(ctx, `DELETE FROM metro_line_stations WHERE line_id = $1`, lineID); err != nil {
			return lineCount, linkCount, fmt.Errorf("delete metro_line_stations for line %d: %w", lineID, err)
		}
		seq := 0
		for _, osmStopID := range r.stops {
			stationID, ok := stationIDs[osmStopID]
			if !ok {
				continue
			}
			if _, err := tx.Exec(ctx, insertLink, lineID, stationID, seq); err != nil {
				return lineCount, linkCount, fmt.Errorf("insert metro_line_stations line=%d seq=%d: %w", lineID, seq, err)
			}
			seq++
			linkCount++
		}
	}
	return lineCount, linkCount, nil
}
