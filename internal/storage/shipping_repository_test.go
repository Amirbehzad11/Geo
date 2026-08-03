package storage

import (
	"strings"
	"testing"
)

func TestBuildLatestActiveShippingDestinationsQuery(t *testing.T) {
	db := &ShipmentDB{
		dialect: "postgres",
		table:   `"shipments"`,
	}
	args := newPlaceholderArgs(db.dialect)
	query := buildLatestActiveShippingDestinationsQuery(db, []string{args.Add(int64(27)), args.Add(int64(28))})

	for _, want := range []string{
		`DISTINCT ON (t."user_id")`,
		`FROM "shippings" AS sh`,
		`JOIN "shipping_statuses" AS ss`,
		`JOIN "trips" AS t`,
		`JOIN "shipments" AS sm`,
		`LEFT JOIN "cities" AS eci`,
		`sh."trip_id" AS trip_id`,
		`t."vehicle_type_id" AS vehicle_type_id`,
		`COALESCE(vt."image", '') AS vehicle_type_image`,
		`LEFT JOIN "vehicle_types" AS vt`,
		`sm."end_city_id"`,
		`sm."end_address"`,
		`AS destination`,
		`NOT IN ('CANCELED','CANCELLED','DELIVERED')`,
		`ORDER BY t."user_id" ASC, sh."id" DESC`,
	} {
		if !strings.Contains(query, want) {
			t.Fatalf("expected query to contain %q, got:\n%s", want, query)
		}
	}

	for _, deny := range []string{
		`end_lat`,
		`end_lng`,
		`absolute_amount`,
		`payment_type`,
		`shipping_code`,
		`ST_Y`,
		`ST_X`,
	} {
		if strings.Contains(query, deny) {
			t.Fatalf("query must not include %q, got:\n%s", deny, query)
		}
	}
}
