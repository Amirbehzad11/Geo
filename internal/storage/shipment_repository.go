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
)

const shipmentEarthRadiusKm = 6371.0

var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ShipmentDBConfig describes the external Laravel-owned shipment database.
type ShipmentDBConfig struct {
	Driver    string
	DSN       string
	Table     string
	LatColumn string
	LngColumn string
}

// ShipmentDB is a read-only connection to the Laravel database. It does not
// run migrations and does not depend on Laravel code.
type ShipmentDB struct {
	db        *sql.DB
	dialect   string
	table     string
	latColumn string
	lngColumn string
}

// NewShipmentDB opens a direct DB connection for nearby shipment search. An
// empty DSN disables the feature and returns nil.
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
	latColumn, err := quoteIdentifier(dialect, cfg.LatColumn)
	if err != nil {
		return nil, fmt.Errorf("shipment lat column: %w", err)
	}
	lngColumn, err := quoteIdentifier(dialect, cfg.LngColumn)
	if err != nil {
		return nil, fmt.Errorf("shipment lng column: %w", err)
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

	return &ShipmentDB{
		db:        db,
		dialect:   dialect,
		table:     table,
		latColumn: latColumn,
		lngColumn: lngColumn,
	}, nil
}

// Close releases shipment DB connections.
func (s *ShipmentDB) Close() {
	if s != nil && s.db != nil {
		s.db.Close()
	}
}

// FindNearbyShipments returns rows from the configured shipment table whose
// origin lat/lng are within radiusKm from lat/lng.
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

func (s *ShipmentDB) buildNearbyQuery(lat, lng, radiusKm float64, limit int) (string, []any) {
	args := newPlaceholderArgs(s.dialect)
	latRef := coordinateExpression(s.dialect, "s."+s.latColumn)
	lngRef := coordinateExpression(s.dialect, "s."+s.lngColumn)

	distanceExpr := fmt.Sprintf(
		`%[1]f * 2 * ASIN(LEAST(1, SQRT(POWER(SIN(RADIANS((%[2]s - %[3]s) / 2)), 2) + COS(RADIANS(%[4]s)) * COS(RADIANS(%[2]s)) * POWER(SIN(RADIANS((%[5]s - %[6]s) / 2)), 2))))`,
		shipmentEarthRadiusKm,
		latRef,
		args.Add(lat),
		args.Add(lat),
		lngRef,
		args.Add(lng),
	)

	minLat, maxLat, minLng, maxLng := shipmentBoundingBox(lat, lng, radiusKm)
	query := fmt.Sprintf(`
SELECT *
FROM (
    SELECT s.*, %s AS distance_km
    FROM %s AS s
    WHERE %s IS NOT NULL
      AND %s IS NOT NULL
      AND %s BETWEEN %s AND %s
      AND %s BETWEEN %s AND %s
) AS nearby
WHERE distance_km <= %s
ORDER BY distance_km ASC
LIMIT %s`,
		distanceExpr,
		s.table,
		latRef,
		lngRef,
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
		if column == "distance_km" {
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
