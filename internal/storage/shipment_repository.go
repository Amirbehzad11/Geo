package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"

	"geo-service/internal/model"
)

const shipmentEarthRadiusKm = 6371.0
const nearbyShipmentStatusID = 4

var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ShipmentDBConfig describes the external Laravel-owned shipment database.
type ShipmentDBConfig struct {
	Driver string
	DSN    string
	Table  string

	// PostGIS geometry/geography column for origin (e.g. "start_location").
	// When set, ST_DWithin + ST_Distance are used; lat/lng are extracted via
	// ST_Y / ST_X and returned as start_lat / start_lng in every result row.
	LocationColumn string

	// Optional PostGIS geometry column for the destination (e.g. "end_location").
	// When set, destination_lat / destination_lng are added to each result row.
	EndLocationColumn string

	// Legacy flat float columns (MySQL / tables without spatial types).
	// Used only when LocationColumn is empty.
	LatColumn string
	LngColumn string

	// Vehicle box sizes + types (optional).
	// When VehicleBoxSizesTable is set, LoadVehicleBoxSizes JOINs vehicle_types
	// to attach a human-readable title to each vehicle entry.
	VehicleBoxSizesTable   string // e.g. "vehicle_box_sizes"
	VehicleIDColumn        string // FK column in vehicle_box_sizes; default "vehicle_id"
	VehicleWeightColumn    string // max_weight column in vehicle_box_sizes; default "max_weight"
	VehicleTypesTable      string // e.g. "vehicle_types"; default "vehicle_types"
	VehicleTypeLabelColumn string // label column in vehicle_types; default "label"
	VehicleTypeTitleColumn string // title column in vehicle_types; default "title"

	// Content types (optional).
	// When ContentTypesTable is non-empty, FindNearbyShipments LEFT JOINs the
	// table and adds a content_image column to every result row.
	ContentTypesTable      string // e.g. "content_types"; empty = disabled
	ContentTypeIDColumn    string // FK column in shipments; default "content_type_id"
	ContentTypeImageColumn string // image column in content_types; default "image"
}

// ShipmentDB is a read-only connection to the Laravel database.
type ShipmentDB struct {
	db                *sql.DB
	dialect           string
	table             string
	idColumn          string // quoted shipment primary key
	vehicleAllowedCol string // quoted vehicle_allowed column used for filtering/enrichment
	locationColumn    string // quoted PostGIS geometry column
	endLocationColumn string // quoted destination geometry column (optional)
	latColumn         string // quoted flat lat column (legacy / MySQL)
	lngColumn         string // quoted flat lng column (legacy / MySQL)

	// vehicle box sizes + types (optional — empty vehicleBoxSizesTable = disabled)
	vehicleBoxSizesTable   string // quoted
	vehicleIDColumn        string // quoted FK column in vehicle_box_sizes
	vehicleWeightColumn    string // quoted max_weight column
	vehicleTypesTable      string // quoted vehicle_types table
	vehicleTypeLabelColumn string // quoted label column in vehicle_types
	vehicleTypeTitleColumn string // quoted title column in vehicle_types

	// prebuilt SQL snippets for the content_types LEFT JOIN (empty = disabled)
	contentJoin        string // e.g. "LEFT JOIN \"content_types\" AS ct ON ct.\"id\" = s.\"content_type_id\""
	contentImageSelect string // e.g. ",\n    COALESCE(ct.\"image\", '') AS content_image"
}

// NewShipmentDB opens a direct DB connection for nearby shipment search.
// An empty DSN disables the feature and returns nil.
func NewShipmentDB(ctx context.Context, cfg ShipmentDBConfig) (*ShipmentDB, error) {
	if strings.TrimSpace(cfg.DSN) == "" {
		return nil, nil
	}

	driverName, dialect, err := normalizeShipmentDriver(cfg.Driver)
	if err != nil {
		return nil, err
	}

	table, err := quoteQualifiedIdentifier(dialect, cfg.Table)
	if err != nil {
		return nil, fmt.Errorf("shipment table: %w", err)
	}

	var locationColumn, endLocationColumn, latColumn, lngColumn string

	if strings.TrimSpace(cfg.LocationColumn) != "" {
		// PostGIS path — a single geometry/geography column stores both coords.
		locationColumn, err = quoteIdentifier(dialect, cfg.LocationColumn)
		if err != nil {
			return nil, fmt.Errorf("shipment location column: %w", err)
		}
		if strings.TrimSpace(cfg.EndLocationColumn) != "" {
			endLocationColumn, err = quoteIdentifier(dialect, cfg.EndLocationColumn)
			if err != nil {
				return nil, fmt.Errorf("shipment end location column: %w", err)
			}
		}
	} else {
		// Legacy path — separate float lat/lng columns (MySQL etc.).
		latColumn, err = quoteIdentifier(dialect, cfg.LatColumn)
		if err != nil {
			return nil, fmt.Errorf("shipment lat column: %w", err)
		}
		lngColumn, err = quoteIdentifier(dialect, cfg.LngColumn)
		if err != nil {
			return nil, fmt.Errorf("shipment lng column: %w", err)
		}
	}

	// ---- vehicle types (optional) ----
	var vehicleTypesTable, vehicleTypeLabelColumn, vehicleTypeTitleColumn string
	vtTable := cfg.VehicleTypesTable
	if vtTable == "" {
		vtTable = "vehicle_types"
	}
	vehicleTypesTable, err = quoteQualifiedIdentifier(dialect, vtTable)
	if err != nil {
		return nil, fmt.Errorf("vehicle_types table: %w", err)
	}
	vtLabel := cfg.VehicleTypeLabelColumn
	if vtLabel == "" {
		vtLabel = "label"
	}
	vehicleTypeLabelColumn, err = quoteIdentifier(dialect, vtLabel)
	if err != nil {
		return nil, fmt.Errorf("vehicle_types label column: %w", err)
	}

	vtTitle := cfg.VehicleTypeTitleColumn
	if vtTitle == "" {
		vtTitle = "title"
	}
	vehicleTypeTitleColumn, err = quoteIdentifier(dialect, vtTitle)
	if err != nil {
		return nil, fmt.Errorf("vehicle_types title column: %w", err)
	}

	// ---- vehicle box sizes (legacy optional) ----
	var vehicleBoxSizesTable, vehicleIDColumn, vehicleWeightColumn string
	if strings.TrimSpace(cfg.VehicleBoxSizesTable) != "" {
		vehicleBoxSizesTable, err = quoteQualifiedIdentifier(dialect, cfg.VehicleBoxSizesTable)
		if err != nil {
			return nil, fmt.Errorf("vehicle_box_sizes table: %w", err)
		}
		vidCol := cfg.VehicleIDColumn
		if vidCol == "" {
			vidCol = "vehicle_id"
		}
		vehicleIDColumn, err = quoteIdentifier(dialect, vidCol)
		if err != nil {
			return nil, fmt.Errorf("vehicle_id column: %w", err)
		}
		vwCol := cfg.VehicleWeightColumn
		if vwCol == "" {
			vwCol = "max_weight"
		}
		vehicleWeightColumn, err = quoteIdentifier(dialect, vwCol)
		if err != nil {
			return nil, fmt.Errorf("vehicle_weight column: %w", err)
		}
	}

	// ---- content types (optional) ----
	var contentJoin, contentImageSelect string
	if strings.TrimSpace(cfg.ContentTypesTable) != "" {
		ctTable, err := quoteQualifiedIdentifier(dialect, cfg.ContentTypesTable)
		if err != nil {
			return nil, fmt.Errorf("content_types table: %w", err)
		}
		ctIDFKCol := cfg.ContentTypeIDColumn
		if ctIDFKCol == "" {
			ctIDFKCol = "content_type_id"
		}
		ctIDFK, err := quoteIdentifier(dialect, ctIDFKCol)
		if err != nil {
			return nil, fmt.Errorf("content_type_id column: %w", err)
		}
		ctImgCol := cfg.ContentTypeImageColumn
		if ctImgCol == "" {
			ctImgCol = "image"
		}
		ctImg, err := quoteIdentifier(dialect, ctImgCol)
		if err != nil {
			return nil, fmt.Errorf("content_types image column: %w", err)
		}
		ctPK, _ := quoteIdentifier(dialect, "id")
		contentJoin = fmt.Sprintf("LEFT JOIN %s AS ct ON ct.%s = s.%s", ctTable, ctPK, ctIDFK)
		contentImageSelect = fmt.Sprintf(",\n    COALESCE(ct.%s, '') AS content_image", ctImg)
	}

	db, err := sql.Open(driverName, cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("shipment db: open: %w", err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	db.SetConnMaxIdleTime(5 * time.Minute)

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("shipment db: ping: %w", err)
	}

	idColumn, err := quoteIdentifier(dialect, "id")
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("shipment id column: %w", err)
	}
	vehicleAllowedCol, err := quoteIdentifier(dialect, "vehicle_allowed")
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("shipment vehicle_allowed column: %w", err)
	}

	return &ShipmentDB{
		db:                     db,
		dialect:                dialect,
		table:                  table,
		idColumn:               idColumn,
		vehicleAllowedCol:      vehicleAllowedCol,
		locationColumn:         locationColumn,
		endLocationColumn:      endLocationColumn,
		latColumn:              latColumn,
		lngColumn:              lngColumn,
		vehicleBoxSizesTable:   vehicleBoxSizesTable,
		vehicleIDColumn:        vehicleIDColumn,
		vehicleWeightColumn:    vehicleWeightColumn,
		vehicleTypesTable:      vehicleTypesTable,
		vehicleTypeLabelColumn: vehicleTypeLabelColumn,
		vehicleTypeTitleColumn: vehicleTypeTitleColumn,
		contentJoin:            contentJoin,
		contentImageSelect:     contentImageSelect,
	}, nil
}

// Close releases shipment DB connections.
func (s *ShipmentDB) Close() {
	if s != nil && s.db != nil {
		s.db.Close()
	}
}

// FindNearbyShipments returns rows whose origin is within radiusKm of lat/lng.
func (s *ShipmentDB) FindNearbyShipments(ctx context.Context, lat, lng, radiusKm float64, limit int) ([]map[string]any, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("shipment db is not configured")
	}
	if radiusKm <= 0 {
		return nil, errors.New("radius_km must be positive")
	}
	if limit <= 0 {
		return nil, errors.New("limit must be positive")
	}

	query, args := s.buildNearbyQuery(lat, lng, radiusKm, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("shipment db: nearby query: %w", err)
	}
	defer rows.Close()

	return scanShipmentRows(rows)
}

// UserOwnsTrip reports whether trips.id belongs directly to the authenticated
// Laravel user. This is used for write operations such as live GPS updates.
func (s *ShipmentDB) UserOwnsTrip(ctx context.Context, userID, tripID int64) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("shipment db is not configured")
	}
	tripsTable, err := quoteQualifiedIdentifier(s.dialect, "trips")
	if err != nil {
		return false, err
	}
	idCol, _ := quoteIdentifier(s.dialect, "id")
	userIDCol, _ := quoteIdentifier(s.dialect, "user_id")
	args := newPlaceholderArgs(s.dialect)
	query := fmt.Sprintf(
		`SELECT 1 FROM %s WHERE %s = %s AND %s = %s LIMIT 1`,
		tripsTable,
		idCol,
		args.Add(tripID),
		userIDCol,
		args.Add(userID),
	)
	return s.exists(ctx, query, args.Values()...)
}

// UserCanAccessTrip allows trip owners and users tied to a shipment assigned to
// the trip (sender or receiver) to read/subscribe to trip location.
func (s *ShipmentDB) UserCanAccessTrip(ctx context.Context, userID, tripID int64) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("shipment db is not configured")
	}
	tripsTable, err := quoteQualifiedIdentifier(s.dialect, "trips")
	if err != nil {
		return false, err
	}
	shippingsTable, err := quoteQualifiedIdentifier(s.dialect, "shippings")
	if err != nil {
		return false, err
	}
	shipmentsTable := s.table
	idCol, _ := quoteIdentifier(s.dialect, "id")
	userIDCol, _ := quoteIdentifier(s.dialect, "user_id")
	receiverIDCol, _ := quoteIdentifier(s.dialect, "receiver_id")
	tripIDCol, _ := quoteIdentifier(s.dialect, "trip_id")
	shipmentIDCol, _ := quoteIdentifier(s.dialect, "shipment_id")

	args := newPlaceholderArgs(s.dialect)
	ownerTripArg := args.Add(tripID)
	ownerUserArg := args.Add(userID)
	linkedTripArg := args.Add(tripID)
	linkedUserArg := args.Add(userID)
	query := fmt.Sprintf(`
SELECT 1
WHERE EXISTS (
    SELECT 1 FROM %[1]s AS t
    WHERE t.%[4]s = %[8]s AND t.%[5]s = %[9]s
) OR EXISTS (
    SELECT 1
    FROM %[2]s AS sh
    JOIN %[3]s AS sp ON sp.%[4]s = sh.%[7]s
    WHERE sh.%[6]s = %[11]s
      AND (sp.%[5]s = %[12]s OR sp.%[10]s = %[12]s)
)
LIMIT 1`,
		tripsTable,
		shippingsTable,
		shipmentsTable,
		idCol,
		userIDCol,
		tripIDCol,
		shipmentIDCol,
		ownerTripArg,
		ownerUserArg,
		receiverIDCol,
		linkedTripArg,
		linkedUserArg,
	)
	return s.exists(ctx, query, args.Values()...)
}

func (s *ShipmentDB) exists(ctx context.Context, query string, args ...any) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// LoadVehicleBoxSizes loads vehicle_box_sizes LEFT JOIN vehicle_types so each
// entry carries an ID, max_weight, and a human-readable title.
//
// Query (PostgreSQL example):
//
//	SELECT vbs."vehicle_id", vbs."max_weight",
//	       COALESCE(vt."title", '') AS title
//	FROM   vehicle_box_sizes AS vbs
//	LEFT JOIN vehicle_types  AS vt ON vt.id = vbs."vehicle_id"
//	WHERE  vbs."max_weight" IS NOT NULL
//	ORDER BY vbs."max_weight" ASC
//
// Returns nil, nil when the feature is not configured.
func (s *ShipmentDB) LoadVehicleBoxSizes(ctx context.Context) ([]model.VehicleBoxSize, error) {
	if s == nil || s.db == nil || s.vehicleBoxSizesTable == "" {
		return nil, nil
	}

	// Quote the vehicle_types primary key column using the active dialect.
	vtID, err := quoteIdentifier(s.dialect, "id")
	if err != nil {
		return nil, fmt.Errorf("vehicle_types id column: %w", err)
	}

	query := fmt.Sprintf(
		`SELECT vbs.%s, vbs.%s, COALESCE(vt.%s, '') AS title`+
			` FROM %s AS vbs`+
			` LEFT JOIN %s AS vt ON vt.%s = vbs.%s`+
			` WHERE vbs.%s IS NOT NULL`+
			` ORDER BY vbs.%s ASC`,
		s.vehicleIDColumn, s.vehicleWeightColumn, s.vehicleTypeTitleColumn,
		s.vehicleBoxSizesTable,
		s.vehicleTypesTable, vtID, s.vehicleIDColumn,
		s.vehicleWeightColumn,
		s.vehicleWeightColumn,
	)

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("vehicle_box_sizes query: %w", err)
	}
	defer rows.Close()

	var out []model.VehicleBoxSize
	for rows.Next() {
		var vidRaw, wRaw any
		var label string
		if err := rows.Scan(&vidRaw, &wRaw, &label); err != nil {
			return nil, fmt.Errorf("vehicle_box_sizes scan: %w", err)
		}
		w := anyToFloat64(wRaw)
		if w <= 0 {
			continue
		}
		out = append(out, model.VehicleBoxSize{
			ID:        normalizeSQLValue("vehicle_id", vidRaw),
			MaxWeight: w,
			Title:     label,
		})
	}
	return out, rows.Err()
}

// LoadVehicleTypes loads vehicle_types rows so each entry carries an ID plus
// human-readable label/title values.
//
// Returns nil, nil when the feature is not configured.
func (s *ShipmentDB) LoadVehicleTypes(ctx context.Context) ([]model.VehicleType, error) {
	if s == nil || s.db == nil || s.vehicleTypesTable == "" {
		return nil, nil
	}

	query, err := s.buildVehicleTypesQuery()
	if err != nil {
		return nil, err
	}

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("vehicle_types query: %w", err)
	}
	defer rows.Close()

	var out []model.VehicleType
	for rows.Next() {
		var idRaw, labelRaw, titleRaw any

		if err := rows.Scan(&idRaw, &labelRaw, &titleRaw); err != nil {
			return nil, fmt.Errorf("vehicle_types scan: %w", err)
		}

		label := ""
		if v := normalizeSQLValue("vehicle_type_label", labelRaw); v != nil {
			label = fmt.Sprint(v)
		}

		title := ""
		if v := normalizeSQLValue("vehicle_type_title", titleRaw); v != nil {
			title = fmt.Sprint(v)
		}

		out = append(out, model.VehicleType{
			ID:    normalizeSQLValue("id", idRaw),
			Label: label,
			Title: title,
		})
	}

	return out, rows.Err()
}

func (s *ShipmentDB) buildVehicleTypesQuery() (string, error) {
	idCol, err := quoteIdentifier(s.dialect, "id")
	if err != nil {
		return "", fmt.Errorf("vehicle_types id column: %w", err)
	}

	return fmt.Sprintf(
		`SELECT vt.%s, COALESCE(vt.%s, '') AS label, COALESCE(vt.%s, '') AS title`+
			` FROM %s AS vt`+
			` ORDER BY vt.%s ASC`,
		idCol,
		s.vehicleTypeLabelColumn,
		s.vehicleTypeTitleColumn,
		s.vehicleTypesTable,
		idCol,
	), nil
}

// anyToFloat64 converts common DB value types to float64 (returns 0 on failure).
func anyToFloat64(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case float32:
		return float64(x)
	case int64:
		return float64(x)
	case int32:
		return float64(x)
	case int:
		return float64(x)
	case []byte:
		if f, err := strconv.ParseFloat(string(x), 64); err == nil {
			return f
		}
	case string:
		if f, err := strconv.ParseFloat(x, 64); err == nil {
			return f
		}
	}
	return 0
}

func (s *ShipmentDB) buildNearbyQuery(lat, lng, radiusKm float64, limit int) (string, []any) {
	if s.locationColumn != "" {
		return s.buildNearbyQueryPostGIS(lat, lng, radiusKm, limit)
	}
	return s.buildNearbyQueryHaversine(lat, lng, radiusKm, limit)
}

// buildNearbyQueryPostGIS uses PostGIS spatial functions for accurate, index-
// backed proximity search.
//
// Fixed positional args: $1=lat  $2=lng  $3=radius_m  $4=limit
//
// The SELECT adds plain float8 columns so the frontend receives usable numbers:
//   - start_lat / start_lng  extracted from start_location geometry
//   - end_lat   / end_lng    extracted from end_location geometry (if configured)
//   - distance_km
func (s *ShipmentDB) buildNearbyQueryPostGIS(lat, lng, radiusKm float64, limit int) (string, []any) {
	args := []any{lat, lng, radiusKm * 1000.0, limit} // $1 $2 $3 $4

	locCol := s.locationColumn // e.g. "start_location" (already quoted)
	lastStatusCol := shipmentLastStatusColumn(s.dialect)

	endCols := ""
	if s.endLocationColumn != "" {
		endCols = fmt.Sprintf(
			",\n    ST_Y(%[1]s::geometry)::float8 AS end_lat,\n    ST_X(%[1]s::geometry)::float8 AS end_lng",
			s.endLocationColumn,
		)
	}

	contentJoin := ""
	if s.contentJoin != "" {
		contentJoin = "\n" + s.contentJoin
	}

	query := fmt.Sprintf(`
SELECT s.%[8]s AS id,
    s.%[9]s AS vehicle_allowed,
    ST_Y(%[1]s::geometry)::float8 AS start_lat,
    ST_X(%[1]s::geometry)::float8 AS start_lng%[2]s,
    ST_Distance(%[1]s::geography, ST_MakePoint($2, $1)::geography) / 1000.0 AS distance_km%[4]s
FROM %[3]s AS s%[5]s
WHERE %[1]s IS NOT NULL
  AND %[6]s = %[7]d
  AND ST_DWithin(%[1]s::geography, ST_MakePoint($2, $1)::geography, $3)
ORDER BY distance_km ASC
LIMIT $4`,
		locCol,
		endCols,
		s.table,
		s.contentImageSelect, // [4]: ",\n    COALESCE(ct.image, '') AS content_image" or ""
		contentJoin,          // [5]: "\nLEFT JOIN content_types AS ct ON ..." or ""
		lastStatusCol,
		nearbyShipmentStatusID,
		s.idColumn,
		s.vehicleAllowedCol,
	)

	return query, args
}

// buildNearbyQueryHaversine is the legacy approach for separate float lat/lng
// columns (MySQL without spatial extensions).
func (s *ShipmentDB) buildNearbyQueryHaversine(lat, lng, radiusKm float64, limit int) (string, []any) {
	args := newPlaceholderArgs(s.dialect)
	latRef := coordinateExpression(s.dialect, "s."+s.latColumn)
	lngRef := coordinateExpression(s.dialect, "s."+s.lngColumn)
	lastStatusCol := shipmentLastStatusColumn(s.dialect)

	distanceExpr := fmt.Sprintf(
		`%[1]f * 2 * ASIN(LEAST(1, SQRT(POWER(SIN(RADIANS((%[2]s - %[3]s) / 2)), 2) + COS(RADIANS(%[4]s)) * COS(RADIANS(%[2]s)) * POWER(SIN(RADIANS((%[5]s - %[6]s) / 2)), 2))))`,
		shipmentEarthRadiusKm,
		latRef,
		args.Add(lat),
		args.Add(lat),
		lngRef,
		args.Add(lng),
	)

	contentJoin := ""
	if s.contentJoin != "" {
		contentJoin = "\n    " + s.contentJoin
	}

	minLat, maxLat, minLng, maxLng := shipmentBoundingBox(lat, lng, radiusKm)
	query := fmt.Sprintf(`
SELECT *
FROM (
    SELECT s.%s AS id,
        s.%s AS vehicle_allowed,
        %s AS start_lat,
        %s AS start_lng,
        %s AS distance_km%s
    FROM %s AS s%s
    WHERE %s IS NOT NULL
      AND %s IS NOT NULL
      AND %s = %d
      AND %s BETWEEN %s AND %s
      AND %s BETWEEN %s AND %s
) AS nearby
WHERE distance_km <= %s
ORDER BY distance_km ASC
LIMIT %s`,
		s.idColumn,
		s.vehicleAllowedCol,
		latRef,
		lngRef,
		distanceExpr,
		s.contentImageSelect, // ",\n    COALESCE(ct.image, '') AS content_image" or ""
		s.table,
		contentJoin, // "\n    LEFT JOIN content_types AS ct ON ..." or ""
		latRef,
		lngRef,
		lastStatusCol,
		nearbyShipmentStatusID,
		latRef,
		args.Add(minLat),
		args.Add(maxLat),
		lngRef,
		args.Add(minLng),
		args.Add(maxLng),
		args.Add(radiusKm),
		args.Add(limit),
	)

	return query, args.Values()
}

func shipmentLastStatusColumn(dialect string) string {
	if dialect == "postgres" {
		return `s."last_status_id"`
	}
	return "s.`last_status_id`"
}

func shipmentBoundingBox(lat, lng, radiusKm float64) (minLat, maxLat, minLng, maxLng float64) {
	latDelta := radiusKm / 111.32
	cosLat := math.Cos(lat * math.Pi / 180)
	lngDelta := 180.0
	if math.Abs(cosLat) > 0.000001 {
		lngDelta = radiusKm / (111.32 * math.Abs(cosLat))
	}
	minLat = math.Max(-90, lat-latDelta)
	maxLat = math.Min(90, lat+latDelta)
	minLng = math.Max(-180, lng-lngDelta)
	maxLng = math.Min(180, lng+lngDelta)
	return minLat, maxLat, minLng, maxLng
}

func coordinateExpression(dialect, columnRef string) string {
	if dialect == "postgres" {
		return fmt.Sprintf(
			`CASE WHEN %s::text ~ '^-?[0-9]+(\.[0-9]+)?$' THEN %s::double precision END`,
			columnRef,
			columnRef,
		)
	}
	return fmt.Sprintf(`CAST(NULLIF(%s, '') AS DECIMAL(18, 10))`, columnRef)
}

// ── placeholder helpers ───────────────────────────────────────────────────────

type placeholderArgs struct {
	dialect string
	values  []any
}

func newPlaceholderArgs(dialect string) *placeholderArgs {
	return &placeholderArgs{dialect: dialect}
}

func (p *placeholderArgs) Add(v any) string {
	p.values = append(p.values, v)
	if p.dialect == "postgres" {
		return fmt.Sprintf("$%d", len(p.values))
	}
	return "?"
}

func (p *placeholderArgs) Values() []any {
	return p.values
}

// ── row scanning ─────────────────────────────────────────────────────────────

func scanShipmentRows(rows *sql.Rows) ([]map[string]any, error) {
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	out := make([]map[string]any, 0)
	for rows.Next() {
		values := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		item := make(map[string]any, len(columns))
		for i, col := range columns {
			item[col] = normalizeSQLValue(col, values[i])
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func normalizeSQLValue(column string, v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case []byte:
		if column == "distance_km" || column == "start_lat" || column == "start_lng" ||
			column == "end_lat" || column == "end_lng" || column == "package_weight" {
			if f, err := strconv.ParseFloat(string(x), 64); err == nil {
				return f
			}
		}
		return string(x)
	case time.Time:
		return x.Format(time.RFC3339Nano)
	default:
		return x
	}
}

// ── driver / identifier helpers ───────────────────────────────────────────────

func normalizeShipmentDriver(raw string) (driverName, dialect string, err error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "mysql", "mariadb":
		return "mysql", "mysql", nil
	case "postgres", "postgresql", "pgx":
		return "pgx", "postgres", nil
	default:
		return "", "", fmt.Errorf("unsupported shipment db driver %q", raw)
	}
}

func quoteQualifiedIdentifier(dialect, ident string) (string, error) {
	parts := strings.Split(strings.TrimSpace(ident), ".")
	if len(parts) == 0 {
		return "", errors.New("identifier is required")
	}
	for i, part := range parts {
		quoted, err := quoteIdentifier(dialect, part)
		if err != nil {
			return "", err
		}
		parts[i] = quoted
	}
	return strings.Join(parts, "."), nil
}

func quoteIdentifier(dialect, ident string) (string, error) {
	ident = strings.TrimSpace(ident)
	if !identifierPattern.MatchString(ident) {
		return "", fmt.Errorf("invalid identifier %q", ident)
	}
	if dialect == "postgres" {
		return `"` + ident + `"`, nil
	}
	return "`" + ident + "`", nil
}
