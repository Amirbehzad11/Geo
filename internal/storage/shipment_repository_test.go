package storage

import (
	"strings"
	"testing"
)

// TestShipmentNearbyQueryPostGIS verifies the PostGIS query shape: positional
// args, ST_DWithin filter, ST_Y/ST_X extraction, and LIMIT $4.
func TestShipmentNearbyQueryPostGIS(t *testing.T) {
	db := &ShipmentDB{
		dialect:           "postgres",
		table:             `"public"."shipments"`,
		idColumn:          `"id"`,
		vehicleAllowedCol: `"vehicle_allowed"`,
		visibleOnMapCol:   `"visible_on_map"`,
		shipmentCodeCol:   `"shipment_code"`,
		locationColumn:    `"start_location"`,
	}

	query, args := db.buildNearbyQuery(35.7, 51.4, 2, 50)

	// Table reference
	if !strings.Contains(query, `FROM "public"."shipments" AS s`) {
		t.Fatalf("expected quoted postgres table, got:\n%s", query)
	}
	// Spatial filter
	if !strings.Contains(query, `ST_DWithin`) {
		t.Fatalf("expected ST_DWithin in PostGIS query, got:\n%s", query)
	}
	// Only active/searchable shipments should be returned.
	if !strings.Contains(query, `AND s."last_status_id" = 4`) {
		t.Fatalf("expected last_status_id filter in PostGIS query, got:\n%s", query)
	}
	// Lat extraction (ST_Y = latitude)
	if !strings.Contains(query, `ST_Y("start_location"::geometry)::float8 AS start_lat`) {
		t.Fatalf("expected ST_Y extraction as start_lat, got:\n%s", query)
	}
	// Lng extraction (ST_X = longitude)
	if !strings.Contains(query, `ST_X("start_location"::geometry)::float8 AS start_lng`) {
		t.Fatalf("expected ST_X extraction as start_lng, got:\n%s", query)
	}
	// Distance column
	if !strings.Contains(query, `AS distance_km`) {
		t.Fatalf("expected distance_km alias, got:\n%s", query)
	}
	if !strings.Contains(query, `AS visible_on_map`) {
		t.Fatalf("expected visible_on_map column in PostGIS query, got:\n%s", query)
	}
	if !strings.Contains(query, `AS shipment_code`) {
		t.Fatalf("expected shipment_code column in PostGIS query, got:\n%s", query)
	}
	// LIMIT uses positional placeholder
	if !strings.Contains(query, "LIMIT $4") {
		t.Fatalf("expected LIMIT $4, got:\n%s", query)
	}
	// Should NOT contain MySQL-style ?
	if strings.Contains(query, "?") {
		t.Fatalf("PostGIS query must not contain MySQL placeholders: %s", query)
	}
	// Args: lat, lng, radius_m, limit
	if len(args) != 4 {
		t.Fatalf("expected 4 args [$1=lat $2=lng $3=radius_m $4=limit], got %d: %#v", len(args), args)
	}
	if args[0] != 35.7 || args[1] != 51.4 {
		t.Fatalf("args[0] should be lat=35.7 and args[1] should be lng=51.4, got %#v", args)
	}
	if args[2] != 2000.0 {
		t.Fatalf("args[2] should be radius_m=2000, got %v", args[2])
	}
	if args[3] != 50 {
		t.Fatalf("args[3] should be limit=50, got %v", args[3])
	}
}

// TestShipmentNearbyQueryPostGISWithEndLocation checks that end_lat/end_lng
// columns are added when endLocationColumn is set.
func TestShipmentNearbyQueryPostGISWithEndLocation(t *testing.T) {
	db := &ShipmentDB{
		dialect:           "postgres",
		table:             `"shipments"`,
		idColumn:          `"id"`,
		vehicleAllowedCol: `"vehicle_allowed"`,
		visibleOnMapCol:   `"visible_on_map"`,
		shipmentCodeCol:   `"shipment_code"`,
		locationColumn:    `"start_location"`,
		endLocationColumn: `"end_location"`,
	}

	query, _ := db.buildNearbyQuery(35.7, 51.4, 2, 50)

	if !strings.Contains(query, `AS end_lat`) {
		t.Fatalf("expected end_lat column when endLocationColumn is set, got:\n%s", query)
	}
	if !strings.Contains(query, `AS end_lng`) {
		t.Fatalf("expected end_lng column when endLocationColumn is set, got:\n%s", query)
	}
}

func TestShipmentNearbyQueryPostGISWithImages(t *testing.T) {
	db := &ShipmentDB{
		dialect:              "postgres",
		table:                `"shipments"`,
		idColumn:             `"id"`,
		vehicleAllowedCol:    `"vehicle_allowed"`,
		visibleOnMapCol:      `"visible_on_map"`,
		shipmentCodeCol:        `"shipment_code"`,
		locationColumn:       `"start_location"`,
		shipmentImagesSelect: buildShipmentImagesSelect("postgres", `"shipment_images"`, `"shipment_id"`, `"image"`, `"id"`, `"id"`),
	}

	query, _ := db.buildNearbyQuery(35.7, 51.4, 2, 50)

	if !strings.Contains(query, `AS images`) {
		t.Fatalf("expected images alias in query, got:\n%s", query)
	}
	if !strings.Contains(query, `FROM "shipment_images" AS si WHERE si."shipment_id" = s."id"`) {
		t.Fatalf("expected shipment_images correlated subquery, got:\n%s", query)
	}
	if strings.Contains(query, `SELECT s.*`) {
		t.Fatalf("nearby shipment query must not expose full shipment rows, got:\n%s", query)
	}
}

// TestShipmentNearbyQueryMySQL verifies the legacy Haversine query still
// works correctly for MySQL (separate float lat/lng columns).
func TestShipmentNearbyQueryMySQL(t *testing.T) {
	db := &ShipmentDB{
		dialect:         "mysql",
		table:           "`shipment`",
		idColumn:        "`id`",
		vehicleAllowedCol: "`vehicle_allowed`",
		visibleOnMapCol:   "`visible_on_map`",
		shipmentCodeCol:   "`shipment_code`",
		latColumn:       "`origin_lat`",
		lngColumn:       "`origin_lng`",
	}

	query, args := db.buildNearbyQuery(35.7, 51.4, 10, 25)

	if !strings.Contains(query, "FROM `shipment` AS s") {
		t.Fatalf("expected quoted MySQL table, got:\n%s", query)
	}
	if strings.Contains(query, "$1") {
		t.Fatalf("MySQL query must not contain postgres placeholders: %s", query)
	}
	if !strings.Contains(query, "AND s.`last_status_id` = 4") {
		t.Fatalf("expected last_status_id filter in MySQL query, got:\n%s", query)
	}
	if !strings.Contains(query, "AS visible_on_map") {
		t.Fatalf("expected visible_on_map column in MySQL query, got:\n%s", query)
	}
	if !strings.Contains(query, "AS shipment_code") {
		t.Fatalf("expected shipment_code column in MySQL query, got:\n%s", query)
	}
	if len(args) != 9 {
		t.Fatalf("expected 9 args, got %d", len(args))
	}
	if args[0] != 35.7 || args[2] != 51.4 {
		t.Fatalf("unexpected args: %#v", args)
	}
}

// TestShipmentIdentifierValidation ensures malformed identifiers are rejected.
func TestShipmentIdentifierValidation(t *testing.T) {
	if _, err := quoteQualifiedIdentifier("mysql", "shipment;DROP"); err == nil {
		t.Fatal("expected invalid table identifier to be rejected")
	}
	if got, err := quoteQualifiedIdentifier("postgres", "public.shipments"); err != nil || got != `"public"."shipments"` {
		t.Fatalf("unexpected qualified identifier result got=%q err=%v", got, err)
	}
}

func TestValidateShipmentDriverDSN(t *testing.T) {
	pgDSN := "host=localhost port=5432 user=u password=p dbname=mr_chamedon sslmode=disable"
	if err := validateShipmentDriverDSN("mysql", pgDSN); err == nil {
		t.Fatal("expected mysql driver with postgres DSN to be rejected")
	}
	if err := validateShipmentDriverDSN("postgres", pgDSN); err != nil {
		t.Fatalf("expected postgres driver with postgres DSN to pass, got %v", err)
	}
}

func TestShipmentVehicleTypesQuery(t *testing.T) {
	db := &ShipmentDB{
		dialect:                "postgres",
		vehicleTypesTable:      `"vehicle_types"`,
		vehicleTypeLabelColumn: `"label"`,
		vehicleTypeTitleColumn: `"title"`,
	}

	query, err := db.buildVehicleTypesQuery()
	if err != nil {
		t.Fatalf("buildVehicleTypesQuery returned error: %v", err)
	}
	if !strings.Contains(query, `SELECT vt."id", COALESCE(vt."label", '') AS label`) {
		t.Fatalf("expected label lookup in query, got:\n%s", query)
	}
	if !strings.Contains(query, `COALESCE(vt."title", '') AS title`) {
		t.Fatalf("expected title lookup in query, got:\n%s", query)
	}
	if !strings.Contains(query, `FROM "vehicle_types" AS vt`) {
		t.Fatalf("expected vehicle_types table reference, got:\n%s", query)
	}
	if !strings.Contains(query, `ORDER BY vt."id" ASC`) {
		t.Fatalf("expected ID ordering, got:\n%s", query)
	}
}
