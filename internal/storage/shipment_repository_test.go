package storage

import (
	"strings"
	"testing"
)

func TestShipmentNearbyQueryMySQL(t *testing.T) {
	db := &ShipmentDB{
		dialect:   "mysql",
		table:     "`shipment`",
		latColumn: "`origin_lat`",
		lngColumn: "`origin_lng`",
	}

	query, args := db.buildNearbyQuery(35.7, 51.4, 10, 25)

	if !strings.Contains(query, "FROM `shipment` AS s") {
		t.Fatalf("expected quoted MySQL table, got query=%s", query)
	}
	if !strings.Contains(query, "CAST(NULLIF(s.`origin_lat`, '') AS DECIMAL(18, 10)) BETWEEN ? AND ?") {
		t.Fatalf("expected MySQL placeholders for latitude bbox, got query=%s", query)
	}
	if strings.Contains(query, "$1") {
		t.Fatalf("MySQL query should not contain postgres placeholders: %s", query)
	}
	if len(args) != 9 {
		t.Fatalf("expected 9 args, got %d", len(args))
	}
	if args[0] != 35.7 || args[1] != 35.7 || args[2] != 51.4 || args[7] != 10.0 || args[8] != 25 {
		t.Fatalf("unexpected args: %#v", args)
	}
}

func TestShipmentNearbyQueryPostgres(t *testing.T) {
	db := &ShipmentDB{
		dialect:   "postgres",
		table:     `"public"."shipments"`,
		latColumn: `"pickup_lat"`,
		lngColumn: `"pickup_lng"`,
	}

	query, args := db.buildNearbyQuery(35.7, 51.4, 10, 25)

	if !strings.Contains(query, `FROM "public"."shipments" AS s`) {
		t.Fatalf("expected quoted postgres table, got query=%s", query)
	}
	if !strings.Contains(query, `s."pickup_lat"::double precision END BETWEEN $4 AND $5`) {
		t.Fatalf("expected postgres placeholders for latitude bbox, got query=%s", query)
	}
	if !strings.Contains(query, "LIMIT $9") {
		t.Fatalf("expected postgres limit placeholder, got query=%s", query)
	}
	if len(args) != 9 {
		t.Fatalf("expected 9 args, got %d", len(args))
	}
}

func TestShipmentIdentifierValidation(t *testing.T) {
	if _, err := quoteQualifiedIdentifier("mysql", "shipment;DROP"); err == nil {
		t.Fatal("expected invalid table identifier to be rejected")
	}
	if got, err := quoteQualifiedIdentifier("postgres", "public.shipments"); err != nil || got != `"public"."shipments"` {
		t.Fatalf("unexpected qualified identifier result got=%q err=%v", got, err)
	}
}
