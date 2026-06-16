package storage

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// FindTripsByUserIDs loads trips for the given Laravel user IDs (driver_id).
// Results are grouped by user_id. Each trip row includes start/end coordinates
// extracted from PostGIS geography columns when available.
func (s *ShipmentDB) FindTripsByUserIDs(ctx context.Context, userIDs []int64) (map[int64][]map[string]any, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("shipment db is not configured")
	}
	if len(userIDs) == 0 {
		return map[int64][]map[string]any{}, nil
	}

	tripsTable, err := quoteQualifiedIdentifier(s.dialect, "trips")
	if err != nil {
		return nil, err
	}

	args := newPlaceholderArgs(s.dialect)
	placeholders := make([]string, 0, len(userIDs))
	for _, id := range userIDs {
		placeholders = append(placeholders, args.Add(id))
	}

	startCoords := ""
	if s.dialect == "postgres" {
		startCoords = `,
    ST_Y(t."start_location"::geometry)::float8 AS start_lat,
    ST_X(t."start_location"::geometry)::float8 AS start_lng,
    ST_Y(t."end_location"::geometry)::float8 AS end_lat,
    ST_X(t."end_location"::geometry)::float8 AS end_lng`
	}

	query := fmt.Sprintf(`
SELECT
    t."id" AS id,
    t."user_id" AS user_id,
    t."shipping_type_id" AS shipping_type_id,
    COALESCE(st."title", '') AS shipping_type_title,
    t."start_country_id" AS start_country_id,
    COALESCE(sc."title", '') AS start_country_title,
    t."end_country_id" AS end_country_id,
    COALESCE(ec."title", '') AS end_country_title,
    t."start_state_id" AS start_state_id,
    COALESCE(ss."title", '') AS start_state_title,
    t."start_city_id" AS start_city_id,
    COALESCE(sci."title", '') AS start_city_title,
    t."start_address" AS start_address,
    t."end_state_id" AS end_state_id,
    COALESCE(es."title", '') AS end_state_title,
    t."end_city_id" AS end_city_id,
    COALESCE(eci."title", '') AS end_city_title,
    t."end_address" AS end_address%s,
    t."release_time" AS release_time,
    t."start_time" AS start_time,
    t."end_time" AS end_time,
    t."vehicle_type_id" AS vehicle_type_id,
    COALESCE(vt."title", '') AS vehicle_type_title,
    t."ticket_number" AS ticket_number,
    t."movement_status" AS movement_status,
    t."last_status_id" AS last_status_id,
    COALESCE(ts."title", '') AS last_status_title,
    t."last_status_description" AS last_status_description,
    t."auto_generated" AS auto_generated
FROM %s AS t
LEFT JOIN %s AS st ON st."id" = t."shipping_type_id"
LEFT JOIN %s AS sc ON sc."id" = t."start_country_id"
LEFT JOIN %s AS ec ON ec."id" = t."end_country_id"
LEFT JOIN %s AS ss ON ss."id" = t."start_state_id"
LEFT JOIN %s AS sci ON sci."id" = t."start_city_id"
LEFT JOIN %s AS es ON es."id" = t."end_state_id"
LEFT JOIN %s AS eci ON eci."id" = t."end_city_id"
LEFT JOIN %s AS vt ON vt."id" = t."vehicle_type_id"
LEFT JOIN %s AS ts ON ts."id" = t."last_status_id"
WHERE t."user_id" IN (%s)
ORDER BY t."user_id" ASC, t."id" DESC`,
		startCoords,
		tripsTable,
		quotedTable(s.dialect, "shipping_types"),
		quotedTable(s.dialect, "countries"),
		quotedTable(s.dialect, "countries"),
		quotedTable(s.dialect, "states"),
		quotedTable(s.dialect, "cities"),
		quotedTable(s.dialect, "states"),
		quotedTable(s.dialect, "cities"),
		quotedTable(s.dialect, "vehicle_types"),
		quotedTable(s.dialect, "trip_statuses"),
		strings.Join(placeholders, ", "),
	)

	rows, err := s.db.QueryContext(ctx, query, args.Values()...)
	if err != nil {
		return nil, fmt.Errorf("trips by user_ids: %w", err)
	}
	defer rows.Close()

	items, err := scanShipmentRows(rows)
	if err != nil {
		return nil, err
	}

	out := make(map[int64][]map[string]any, len(userIDs))
	for _, item := range items {
		userID := anyToInt64(item["user_id"])
		if userID <= 0 {
			continue
		}
		item["start_location"] = locationObject(item["start_lat"], item["start_lng"])
		item["end_location"] = locationObject(item["end_lat"], item["end_lng"])
		delete(item, "start_lat")
		delete(item, "start_lng")
		delete(item, "end_lat")
		delete(item, "end_lng")
		out[userID] = append(out[userID], item)
	}
	for _, id := range userIDs {
		if _, ok := out[id]; !ok {
			out[id] = []map[string]any{}
		}
	}
	return out, nil
}

func quotedTable(dialect, table string) string {
	q, err := quoteQualifiedIdentifier(dialect, table)
	if err != nil {
		return `"` + table + `"`
	}
	return q
}

func locationObject(lat, lng any) map[string]any {
	return map[string]any{
		"lat": anyToFloat64(lat),
		"lng": anyToFloat64(lng),
	}
}

func anyToInt64(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int32:
		return int64(x)
	case int:
		return int64(x)
	case float64:
		return int64(x)
	case []byte:
		n, _ := strconv.ParseInt(string(x), 10, 64)
		return n
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		return n
	default:
		return 0
	}
}
