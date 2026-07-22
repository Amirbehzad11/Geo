package storage

import (
	"strings"
	"testing"
)

func TestFindTripsByUserIDsQueryMatchesLaravelSchema(t *testing.T) {
	db := &ShipmentDB{dialect: "postgres"}
	args := newPlaceholderArgs(db.dialect)
	query := buildTripsByUserIDsQuery(db, []string{args.Add(int64(1))}, `,
    ST_Y(t."start_location"::geometry)::float8 AS start_lat,
    ST_X(t."start_location"::geometry)::float8 AS start_lng,
    ST_Y(t."end_location"::geometry)::float8 AS end_lat,
    ST_X(t."end_location"::geometry)::float8 AS end_lng`)

	for _, col := range []string{
		`t."release_time"`,
		`t."movement_status"`,
	} {
		if strings.Contains(query, col) {
			t.Fatalf("query must not reference removed Laravel column %s", col)
		}
	}

	for _, col := range []string{
		`t."trip_code"`,
		`t."max_weight"`,
		`t."user_vehicle_id"`,
		`start_location`,
		`end_location`,
	} {
		if !strings.Contains(query, col) {
			t.Fatalf("query must include %s, got:\n%s", col, query)
		}
	}
}
