// osm2postgis imports an OpenStreetMap XML export into PostGIS road_segments.
//
// Usage:
//
//	osm2postgis -in isfahan.osm -dsn "host=localhost port=5432 user=geo password=geo_secret dbname=geodb sslmode=disable"
package main

import (
	"context"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/qedus/osmpbf"

	"geo-service/internal/storage"
)

type osmRoot struct {
	Nodes []osmNode `xml:"node"`
	Ways  []osmWay  `xml:"way"`
}

type osmNode struct {
	ID  int64   `xml:"id,attr"`
	Lat float64 `xml:"lat,attr"`
	Lon float64 `xml:"lon,attr"`
}

type osmWay struct {
	ID   int64     `xml:"id,attr"`
	Refs []nodeRef `xml:"nd"`
	Tags []osmTag  `xml:"tag"`
}

type nodeRef struct {
	Ref int64 `xml:"ref,attr"`
}

type osmTag struct {
	K string `xml:"k,attr"`
	V string `xml:"v,attr"`
}

type roadSegment struct {
	osmWayID                       int64
	fromNodeID, toNodeID           int64
	highwayType, name              string
	speedKmH, distanceKm           float64
	bidirectional                  bool
	car, motorcycle, bus, foot     bool
	fromLat, fromLng, toLat, toLng float64
}

type geoPoint struct {
	Lat float64
	Lon float64
}

type wayCandidate struct {
	id            int64
	refs          []int64
	highway       string
	name          string
	speedKmH      float64
	bidirectional bool
	access        accessFlags
}

// invalidDSNFlagValue returns true when dsn looks like an unexpanded shell
// variable or a flag name rather than a real connection string.
func invalidDSNFlagValue(dsn string) bool {
	trimmed := strings.TrimSpace(dsn)
	if trimmed == "" {
		return true
	}
	if strings.HasPrefix(trimmed, "-") {
		return true
	}
	if strings.HasPrefix(trimmed, "$") || strings.HasPrefix(trimmed, "%") {
		return true
	}
	return false
}

var railwaySpeed = map[string]float64{
	"rail":         120,
	"subway":       60,
	"light_rail":   50,
	"narrow_gauge": 40,
	"tram":         30,
	"monorail":     60,
}

type railCandidate struct {
	id            int64
	refs          []int64
	railway       string
	name          string
	speedKmH      float64
	bidirectional bool
}

func railCandidateFromWay(id int64, refs []int64, tags map[string]string) (railCandidate, bool) {
	railway := tags["railway"]
	if _, ok := railwaySpeed[railway]; !ok || len(refs) < 2 {
		return railCandidate{}, false
	}

	bidirectional := true
	if strings.TrimSpace(tags["oneway"]) == "-1" {
		bidirectional = false
		refs = reverseIDs(refs)
	} else if isOneway(tags) {
		bidirectional = false
	}

	name := tags["name"]
	if name == "" {
		name = tags["ref"]
	}

	speed := railwaySpeed[railway]
	if ms, ok := tags["maxspeed"]; ok {
		match := leadingNumber.FindStringSubmatch(ms)
		if len(match) == 2 {
			if v, err := strconv.ParseFloat(match[1], 64); err == nil && v > 0 {
				speed = v
			}
		}
	}

	return railCandidate{
		id:            id,
		refs:          refs,
		railway:       railway,
		name:          name,
		speedKmH:      speed,
		bidirectional: bidirectional,
	}, true
}

var highwaySpeed = map[string]float64{
	"motorway":       110,
	"motorway_link":  80,
	"trunk":          90,
	"trunk_link":     70,
	"primary":        80,
	"primary_link":   60,
	"secondary":      60,
	"secondary_link": 50,
	"tertiary":       50,
	"tertiary_link":  40,
	"unclassified":   40,
	"residential":    30,
	"living_street":  20,
	"service":        20,
	"track":          15,
	"pedestrian":     5,
	"footway":        5,
	"path":           5,
	"steps":          3,
	"corridor":       5,
	"platform":       5,
	"sidewalk":       5,
	"crossing":       5,
}

func main() {
	inFile := flag.String("in", "", "OSM XML file path")
	dsn := flag.String("dsn", os.Getenv("POSTGRES_DSN"), "Postgres DSN")
	bboxStr := flag.String("bbox", "", "Bounding box filter: lat_min,lng_min,lat_max,lng_max")
	region := flag.String("region", "", "Import region label, e.g. isfahan_province")
	tableRegion := flag.String("table-region", "", "Optional slug for a separate road_segments_<slug> table")
	truncate := flag.Bool("truncate", true, "Truncate road_segments before import")
	batchSize := flag.Int("batch-size", 20000, "Rows per COPY staging batch")
	analyze := flag.Bool("analyze", true, "Run ANALYZE on the target table after import")
	timeout := flag.Duration("timeout", 4*time.Hour, "Import timeout")
	flag.Parse()

	if *inFile == "" || *dsn == "" {
		flag.Usage()
		os.Exit(1)
	}

	var bbox *[4]float64
	if *bboxStr != "" {
		parts := strings.Split(*bboxStr, ",")
		if len(parts) != 4 {
			log.Fatal("bbox must be: lat_min,lng_min,lat_max,lng_max")
		}
		var b [4]float64
		for i, p := range parts {
			v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
			if err != nil {
				log.Fatalf("bbox parse error: %v", err)
			}
			b[i] = v
		}
		bbox = &b
	}

	if *batchSize <= 0 {
		*batchSize = 20000
	}
	if *region == "" && *tableRegion != "" {
		*region = *tableRegion
	}

	targetTable, err := storage.RoadSegmentsTableName(*tableRegion)
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	pg, err := storage.NewPostgres(ctx, *dsn)
	if err != nil {
		log.Fatalf("postgres: %v", err)
	}
	defer pg.Close()

	if err := pg.EnsureRoadSegmentsTable(ctx, targetTable); err != nil {
		log.Fatalf("ensure %s: %v", targetTable, err)
	}

	if *truncate {
		if _, err := pg.Pool().Exec(ctx, "TRUNCATE "+pgx.Identifier{targetTable}.Sanitize()+" RESTART IDENTITY"); err != nil {
			log.Fatalf("truncate %s: %v", targetTable, err)
		}
	}

	count, err := importSegments(ctx, pg, targetTable, *inFile, bbox, *region, *batchSize)
	if err != nil {
		log.Fatal(err)
	}
	if count == 0 {
		log.Fatal("no routable road segments found")
	}

	if *analyze {
		if _, err := pg.Pool().Exec(ctx, "ANALYZE "+pgx.Identifier{targetTable}.Sanitize()); err != nil {
			log.Printf("analyze %s warning: %v", targetTable, err)
		}
	}

	log.Printf("imported %d road segments into PostGIS table %s", count, targetTable)
}

func importSegments(
	ctx context.Context,
	pg *storage.Postgres,
	table string,
	path string,
	bbox *[4]float64,
	region string,
	batchSize int,
) (int, error) {
	if strings.HasSuffix(strings.ToLower(path), ".pbf") {
		return importSegmentsPBF(ctx, pg, table, path, bbox, region, batchSize)
	}
	return importSegmentsXML(ctx, pg, table, path, bbox, region, batchSize)
}

func importSegmentsXML(
	ctx context.Context,
	pg *storage.Postgres,
	table string,
	path string,
	bbox *[4]float64,
	region string,
	batchSize int,
) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	log.Println("parsing OSM XML...")
	var osm osmRoot
	dec := xml.NewDecoder(f)
	if err := dec.Decode(&osm); err != nil && err != io.EOF {
		return 0, fmt.Errorf("decode OSM: %w", err)
	}
	log.Printf("parsed %d nodes, %d ways", len(osm.Nodes), len(osm.Ways))

	nodeByID := make(map[int64]geoPoint, len(osm.Nodes))
	for i := range osm.Nodes {
		n := &osm.Nodes[i]
		if bbox == nil || inBBox(n.Lat, n.Lon, bbox) {
			nodeByID[n.ID] = geoPoint{Lat: n.Lat, Lon: n.Lon}
		}
	}

	candidates := make([]wayCandidate, 0)
	for _, way := range osm.Ways {
		tags := tagsMap(way.Tags)
		candidate, ok := candidateFromWay(way.ID, nodeRefsToIDs(way.Refs), tags)
		if !ok {
			continue
		}
		candidates = append(candidates, candidate)
	}
	return importCandidateSegments(ctx, pg, table, candidates, nodeByID, region, batchSize)
}

func importSegmentsPBF(
	ctx context.Context,
	pg *storage.Postgres,
	table string,
	path string,
	bbox *[4]float64,
	region string,
	batchSize int,
) (int, error) {
	candidates := make([]wayCandidate, 0)
	requiredNodes := make(map[int64]struct{})

	log.Println("scanning OSM PBF ways...")
	if err := scanPBF(path, func(v any) {
		way, ok := v.(*osmpbf.Way)
		if !ok {
			return
		}
		candidate, ok := candidateFromWay(way.ID, append([]int64(nil), way.NodeIDs...), way.Tags)
		if !ok {
			return
		}
		candidates = append(candidates, candidate)
		for _, nodeID := range candidate.refs {
			requiredNodes[nodeID] = struct{}{}
		}
	}); err != nil {
		return 0, err
	}
	log.Printf("found %d routable ways; collecting referenced nodes...", len(candidates))

	nodeByID := make(map[int64]geoPoint, len(requiredNodes))
	if err := scanPBF(path, func(v any) {
		node, ok := v.(*osmpbf.Node)
		if !ok {
			return
		}
		if _, ok := requiredNodes[node.ID]; !ok {
			return
		}
		if bbox != nil && !inBBox(node.Lat, node.Lon, bbox) {
			return
		}
		nodeByID[node.ID] = geoPoint{Lat: node.Lat, Lon: node.Lon}
	}); err != nil {
		return 0, err
	}
	log.Printf("collected %d referenced nodes inside bbox", len(nodeByID))

	return importCandidateSegments(ctx, pg, table, candidates, nodeByID, region, batchSize)
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
		value, err := decoder.Decode()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("decode PBF: %w", err)
		}
		visit(value)
	}
	return nil
}

func candidateFromWay(id int64, refs []int64, tags map[string]string) (wayCandidate, bool) {
	highway := tags["highway"]
	if !routable(highway) || len(refs) < 2 {
		return wayCandidate{}, false
	}

	bidirectional := true
	switch {
	case isOneway(tags):
		bidirectional = false
	case strings.TrimSpace(tags["oneway"]) == "-1":
		bidirectional = false
		refs = reverseIDs(refs)
	}

	access := accessForWay(tags, highway)
	if !access.car && !access.motorcycle && !access.bus && !access.foot {
		return wayCandidate{}, false
	}

	return wayCandidate{
		id:            id,
		refs:          refs,
		highway:       highway,
		name:          tags["name"],
		speedKmH:      speedForWay(tags, highway),
		bidirectional: bidirectional,
		access:        access,
	}, true
}

func importCandidateSegments(
	ctx context.Context,
	pg *storage.Postgres,
	table string,
	candidates []wayCandidate,
	nodeByID map[int64]geoPoint,
	region string,
	batchSize int,
) (int, error) {
	importer, err := newRoadSegmentImporter(ctx, pg, table, region, batchSize)
	if err != nil {
		return 0, err
	}
	defer importer.Release()

	for _, candidate := range candidates {
		for i := 0; i < len(candidate.refs)-1; i++ {
			fromID := candidate.refs[i]
			toID := candidate.refs[i+1]
			fromNode, ok1 := nodeByID[fromID]
			toNode, ok2 := nodeByID[toID]
			if !ok1 || !ok2 {
				continue
			}

			dist := haversine(fromNode.Lat, fromNode.Lon, toNode.Lat, toNode.Lon)
			if dist < 0.001 {
				continue
			}

			if err := importer.Add(ctx, roadSegment{
				osmWayID:      candidate.id,
				fromNodeID:    fromID,
				toNodeID:      toID,
				highwayType:   candidate.highway,
				name:          candidate.name,
				speedKmH:      candidate.speedKmH,
				distanceKm:    roundF(dist, 4),
				bidirectional: candidate.bidirectional,
				car:           candidate.access.car,
				motorcycle:    candidate.access.motorcycle,
				bus:           candidate.access.bus,
				foot:          candidate.access.foot,
				fromLat:       fromNode.Lat,
				fromLng:       fromNode.Lon,
				toLat:         toNode.Lat,
				toLng:         toNode.Lon,
			}); err != nil {
				return importer.Total(), err
			}
		}
	}
	if err := importer.Flush(ctx); err != nil {
		return importer.Total(), err
	}
	return importer.Total(), nil
}

type roadSegmentImporter struct {
	conn      *pgxpool.Conn
	table     string
	region    string
	batchSize int
	rows      [][]any
	total     int
}

var roadSegmentStageColumns = []string{
	"osm_way_id",
	"from_node_id",
	"to_node_id",
	"highway_type",
	"name",
	"speed_kmh",
	"distance_km",
	"bidirectional",
	"car_allowed",
	"motorcycle_allowed",
	"bus_allowed",
	"foot_allowed",
	"from_lat",
	"from_lng",
	"to_lat",
	"to_lng",
	"geom_wkt",
	"import_region",
}

func newRoadSegmentImporter(
	ctx context.Context,
	pg *storage.Postgres,
	table string,
	region string,
	batchSize int,
) (*roadSegmentImporter, error) {
	conn, err := pg.Pool().Acquire(ctx)
	if err != nil {
		return nil, err
	}

	const stageTable = `
		CREATE TEMP TABLE IF NOT EXISTS road_segment_import_stage (
			osm_way_id BIGINT NOT NULL,
			from_node_id BIGINT NOT NULL,
			to_node_id BIGINT NOT NULL,
			highway_type TEXT NOT NULL,
			name TEXT NOT NULL,
			speed_kmh DOUBLE PRECISION NOT NULL,
			distance_km DOUBLE PRECISION NOT NULL,
			bidirectional BOOLEAN NOT NULL,
			car_allowed BOOLEAN NOT NULL,
			motorcycle_allowed BOOLEAN NOT NULL,
			bus_allowed BOOLEAN NOT NULL,
			foot_allowed BOOLEAN NOT NULL,
			from_lat DOUBLE PRECISION NOT NULL,
			from_lng DOUBLE PRECISION NOT NULL,
			to_lat DOUBLE PRECISION NOT NULL,
			to_lng DOUBLE PRECISION NOT NULL,
			geom_wkt TEXT NOT NULL,
			import_region TEXT NOT NULL
		) ON COMMIT PRESERVE ROWS`
	if _, err := conn.Exec(ctx, stageTable); err != nil {
		conn.Release()
		return nil, err
	}
	if _, err := conn.Exec(ctx, "TRUNCATE pg_temp.road_segment_import_stage"); err != nil {
		conn.Release()
		return nil, err
	}

	return &roadSegmentImporter{
		conn:      conn,
		table:     table,
		region:    region,
		batchSize: batchSize,
		rows:      make([][]any, 0, batchSize),
	}, nil
}

func (i *roadSegmentImporter) Add(ctx context.Context, s roadSegment) error {
	i.rows = append(i.rows, roadSegmentRow(s, i.region))
	if len(i.rows) < i.batchSize {
		return nil
	}
	return i.Flush(ctx)
}

func (i *roadSegmentImporter) Flush(ctx context.Context) error {
	if len(i.rows) == 0 {
		return nil
	}

	copied, err := i.conn.CopyFrom(
		ctx,
		pgx.Identifier{"pg_temp", "road_segment_import_stage"},
		roadSegmentStageColumns,
		pgx.CopyFromRows(i.rows),
	)
	if err != nil {
		return fmt.Errorf("copy stage: %w", err)
	}
	if int(copied) != len(i.rows) {
		return fmt.Errorf("copy stage: copied %d rows, expected %d", copied, len(i.rows))
	}

	insertSQL := fmt.Sprintf(`
		INSERT INTO %s (
			osm_way_id, from_node_id, to_node_id,
			highway_type, name, speed_kmh, distance_km, bidirectional,
			car_allowed, motorcycle_allowed, bus_allowed, foot_allowed,
			from_lat, from_lng, to_lat, to_lng, geom, import_region
		)
		SELECT
			osm_way_id, from_node_id, to_node_id,
			highway_type, name, speed_kmh, distance_km, bidirectional,
			car_allowed, motorcycle_allowed, bus_allowed, foot_allowed,
			from_lat, from_lng, to_lat, to_lng,
			ST_GeogFromText(geom_wkt)::geography,
			import_region
		FROM pg_temp.road_segment_import_stage`,
		pgx.Identifier{i.table}.Sanitize(),
	)
	if _, err := i.conn.Exec(ctx, insertSQL); err != nil {
		return fmt.Errorf("insert road segments: %w", err)
	}
	if _, err := i.conn.Exec(ctx, "TRUNCATE pg_temp.road_segment_import_stage"); err != nil {
		return fmt.Errorf("truncate stage: %w", err)
	}

	i.total += len(i.rows)
	log.Printf("inserted %d road segments", i.total)
	i.rows = i.rows[:0]
	return nil
}

func (i *roadSegmentImporter) Total() int {
	if i == nil {
		return 0
	}
	return i.total
}

func (i *roadSegmentImporter) Release() {
	if i != nil && i.conn != nil {
		i.conn.Release()
	}
}

func roadSegmentRow(s roadSegment, region string) []any {
	return []any{
		s.osmWayID,
		s.fromNodeID,
		s.toNodeID,
		s.highwayType,
		s.name,
		s.speedKmH,
		s.distanceKm,
		s.bidirectional,
		s.car,
		s.motorcycle,
		s.bus,
		s.foot,
		s.fromLat,
		s.fromLng,
		s.toLat,
		s.toLng,
		fmt.Sprintf("SRID=4326;LINESTRING(%.7f %.7f,%.7f %.7f)", s.fromLng, s.fromLat, s.toLng, s.toLat),
		region,
	}
}

func tagsMap(tags []osmTag) map[string]string {
	m := make(map[string]string, len(tags))
	for _, t := range tags {
		m[t.K] = t.V
	}
	return m
}

func routable(highway string) bool {
	_, ok := highwaySpeed[highway]
	return ok
}

func isOneway(tags map[string]string) bool {
	oneway := strings.ToLower(strings.TrimSpace(tags["oneway"]))
	return oneway == "yes" || oneway == "1" || oneway == "true" || tags["junction"] == "roundabout"
}

func nodeRefsToIDs(refs []nodeRef) []int64 {
	ids := make([]int64, len(refs))
	for i := range refs {
		ids[i] = refs[i].Ref
	}
	return ids
}

func reverseIDs(ids []int64) []int64 {
	out := make([]int64, len(ids))
	for i := range ids {
		out[len(ids)-1-i] = ids[i]
	}
	return out
}

var leadingNumber = regexp.MustCompile(`^\s*([0-9]+(?:\.[0-9]+)?)`)

func speedForWay(tags map[string]string, highway string) float64 {
	if ms, ok := tags["maxspeed"]; ok {
		match := leadingNumber.FindStringSubmatch(ms)
		if len(match) == 2 {
			v, err := strconv.ParseFloat(match[1], 64)
			if err == nil && v > 0 {
				return v
			}
		}
	}
	if s, ok := highwaySpeed[highway]; ok {
		return s
	}
	return 40
}

type accessFlags struct {
	car        bool
	motorcycle bool
	bus        bool
	foot       bool
}

func accessForWay(tags map[string]string, highway string) accessFlags {
	pedestrianOnly := highway == "footway" || highway == "pedestrian" ||
		highway == "path" || highway == "steps" ||
		highway == "corridor" || highway == "platform" ||
		highway == "sidewalk" || highway == "crossing"
	flags := accessFlags{
		car:        !pedestrianOnly,
		motorcycle: !pedestrianOnly,
		bus: highway == "motorway" || highway == "motorway_link" ||
			highway == "trunk" || highway == "trunk_link" ||
			highway == "primary" || highway == "primary_link" ||
			highway == "secondary" || highway == "secondary_link" ||
			highway == "tertiary" || highway == "tertiary_link" ||
			highway == "unclassified" || highway == "residential" ||
			highway == "living_street" || highway == "service",
		foot: highway != "motorway" && highway != "motorway_link" &&
			highway != "trunk" && highway != "trunk_link",
	}

	if isNo(tags["access"]) {
		flags.car = false
		flags.motorcycle = false
		flags.bus = false
		flags.foot = false
	}
	if isNo(tags["vehicle"]) {
		flags.car = false
		flags.motorcycle = false
		flags.bus = false
	}
	if isNo(tags["motor_vehicle"]) {
		flags.car = false
		flags.motorcycle = false
		flags.bus = false
	}
	if isYes(tags["motor_vehicle"]) {
		flags.car = true
		flags.motorcycle = true
	}
	if isNo(tags["motorcar"]) {
		flags.car = false
	}
	if isNo(tags["motorcycle"]) {
		flags.motorcycle = false
	}
	if isNo(tags["bus"]) {
		flags.bus = false
	}
	if isYes(tags["bus"]) {
		flags.bus = true
	}
	if isNo(tags["foot"]) || isNo(tags["sidewalk"]) {
		flags.foot = false
	}
	if isYes(tags["foot"]) || isYes(tags["sidewalk"]) {
		flags.foot = true
	}
	return flags
}

func isNo(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "no", "private", "customers":
		return true
	default:
		return false
	}
}

func isYes(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "yes", "designated", "permissive", "destination":
		return true
	default:
		return false
	}
}

func inBBox(lat, lng float64, b *[4]float64) bool {
	return lat >= b[0] && lng >= b[1] && lat <= b[2] && lng <= b[3]
}

func haversine(lat1, lng1, lat2, lng2 float64) float64 {
	const r = 6371.0
	dLat := (lat2 - lat1) * math.Pi / 180
	dLng := (lng2 - lng1) * math.Pi / 180
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*math.Pi/180)*math.Cos(lat2*math.Pi/180)*
			math.Sin(dLng/2)*math.Sin(dLng/2)
	return r * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

func roundF(v float64, prec int) float64 {
	p := math.Pow(10, float64(prec))
	return math.Round(v*p) / p
}
