package storage

import (
	"context"
	"fmt"
	"strings"

	"geo-service/internal/model"
)

var excludedShippingStatusLabels = []string{"CANCELED", "CANCELLED", "DELIVERED"}

// FindLatestActiveShippingDestinationsByUserIDs returns destination, trip_id,
// vehicle_type_id, and vehicle_type_image of the latest in-progress shipping
// for each driver (trips.user_id).
// Active = shipping_statuses.label NOT IN (CANCELED, DELIVERED).
// Name prefers shipment end city title, then end_address.
func (s *ShipmentDB) FindLatestActiveShippingDestinationsByUserIDs(ctx context.Context, userIDs []int64) (map[int64]model.DriverActiveJob, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("shipment db is not configured")
	}
	if len(userIDs) == 0 {
		return map[int64]model.DriverActiveJob{}, nil
	}

	args := newPlaceholderArgs(s.dialect)
	placeholders := make([]string, 0, len(userIDs))
	for _, id := range userIDs {
		placeholders = append(placeholders, args.Add(id))
	}

	query := buildLatestActiveShippingDestinationsQuery(s, placeholders)
	rows, err := s.db.QueryContext(ctx, query, args.Values()...)
	if err != nil {
		return nil, fmt.Errorf("active shipping destinations by user_ids: %w", err)
	}
	defer rows.Close()

	items, err := scanShipmentRows(rows)
	if err != nil {
		return nil, err
	}

	out := make(map[int64]model.DriverActiveJob, len(items))
	for _, item := range items {
		userID := anyToInt64(item["user_id"])
		tripID := anyToInt64(item["trip_id"])
		if userID <= 0 || tripID <= 0 {
			continue
		}
		name := strings.TrimSpace(anyToString(item["destination"]))
		if name == "" {
			continue
		}
		out[userID] = model.DriverActiveJob{
			Destination:      name,
			TripID:           tripID,
			VehicleTypeID:    anyToInt64(item["vehicle_type_id"]),
			VehicleTypeImage: ResolvePublicMediaURL(s.mediaPublicBaseURL, anyToString(item["vehicle_type_image"])),
		}
	}
	return out, nil
}

func buildLatestActiveShippingDestinationsQuery(s *ShipmentDB, placeholders []string) string {
	excluded := "'" + strings.Join(excludedShippingStatusLabels, "','") + "'"
	shippingsTable := quotedTable(s.dialect, "shippings")
	statusesTable := quotedTable(s.dialect, "shipping_statuses")
	tripsTable := quotedTable(s.dialect, "trips")
	citiesTable := quotedTable(s.dialect, "cities")
	vehicleTypesTable := quotedTable(s.dialect, "vehicle_types")
	shipmentsTable := s.table
	if strings.TrimSpace(shipmentsTable) == "" {
		shipmentsTable = quotedTable(s.dialect, "shipments")
	}
	inList := strings.Join(placeholders, ", ")

	if s.dialect == "postgres" {
		return fmt.Sprintf(`
SELECT DISTINCT ON (t."user_id")
    t."user_id" AS user_id,
    sh."trip_id" AS trip_id,
    t."vehicle_type_id" AS vehicle_type_id,
    COALESCE(vt."image", '') AS vehicle_type_image,
    COALESCE(
        NULLIF(TRIM(eci."title"), ''),
        NULLIF(TRIM(sm."end_address"), ''),
        ''
    ) AS destination
FROM %s AS sh
JOIN %s AS ss ON ss."id" = sh."last_status_id"
JOIN %s AS t ON t."id" = sh."trip_id"
JOIN %s AS sm ON sm."id" = sh."shipment_id"
LEFT JOIN %s AS eci ON eci."id" = sm."end_city_id"
LEFT JOIN %s AS vt ON vt."id" = t."vehicle_type_id"
WHERE t."user_id" IN (%s)
  AND UPPER(ss."label") NOT IN (%s)
ORDER BY t."user_id" ASC, sh."id" DESC`,
			shippingsTable,
			statusesTable,
			tripsTable,
			shipmentsTable,
			citiesTable,
			vehicleTypesTable,
			inList,
			excluded,
		)
	}

	return fmt.Sprintf(`
SELECT
    t.user_id AS user_id,
    sh.trip_id AS trip_id,
    t.vehicle_type_id AS vehicle_type_id,
    COALESCE(vt.image, '') AS vehicle_type_image,
    COALESCE(
        NULLIF(TRIM(eci.title), ''),
        NULLIF(TRIM(sm.end_address), ''),
        ''
    ) AS destination
FROM %s AS sh
JOIN %s AS ss ON ss.id = sh.last_status_id
JOIN %s AS t ON t.id = sh.trip_id
JOIN %s AS sm ON sm.id = sh.shipment_id
LEFT JOIN %s AS eci ON eci.id = sm.end_city_id
LEFT JOIN %s AS vt ON vt.id = t.vehicle_type_id
WHERE t.user_id IN (%s)
  AND UPPER(ss.label) NOT IN (%s)
  AND sh.id = (
      SELECT MAX(sh2.id)
      FROM %s AS sh2
      JOIN %s AS ss2 ON ss2.id = sh2.last_status_id
      JOIN %s AS t2 ON t2.id = sh2.trip_id
      WHERE t2.user_id = t.user_id
        AND UPPER(ss2.label) NOT IN (%s)
  )`,
		shippingsTable,
		statusesTable,
		tripsTable,
		shipmentsTable,
		citiesTable,
		vehicleTypesTable,
		inList,
		excluded,
		shippingsTable,
		statusesTable,
		tripsTable,
		excluded,
	)
}

// FindLatestTripDestinationsByUserIDs returns the latest trip (by id DESC) for
// each driver user_id. Only drivers whose user_id is in userIDs are returned.
// The caller is expected to call this only for drivers who have no active shipping.
func (s *ShipmentDB) FindLatestTripDestinationsByUserIDs(ctx context.Context, userIDs []int64) (map[int64]model.DriverLatestTrip, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("shipment db is not configured")
	}
	if len(userIDs) == 0 {
		return map[int64]model.DriverLatestTrip{}, nil
	}

	args := newPlaceholderArgs(s.dialect)
	placeholders := make([]string, 0, len(userIDs))
	for _, id := range userIDs {
		placeholders = append(placeholders, args.Add(id))
	}

	tripsTable := quotedTable(s.dialect, "trips")
	citiesTable := quotedTable(s.dialect, "cities")
	inList := strings.Join(placeholders, ", ")

	var query string
	if s.dialect == "postgres" {
		query = fmt.Sprintf(`
SELECT DISTINCT ON (t."user_id")
    t."user_id"  AS user_id,
    t."id"       AS trip_id,
    COALESCE(
        NULLIF(TRIM(eci."title"), ''),
        NULLIF(TRIM(t."end_address"), ''),
        ''
    ) AS destination
FROM %s AS t
LEFT JOIN %s AS eci ON eci."id" = t."end_city_id"
WHERE t."user_id" IN (%s)
ORDER BY t."user_id" ASC, t."id" DESC`,
			tripsTable, citiesTable, inList)
	} else {
		query = fmt.Sprintf(`
SELECT t.user_id AS user_id, t.id AS trip_id,
    COALESCE(
        NULLIF(TRIM(eci.title), ''),
        NULLIF(TRIM(t.end_address), ''),
        ''
    ) AS destination
FROM %s AS t
LEFT JOIN %s AS eci ON eci.id = t.end_city_id
WHERE t.user_id IN (%s)
  AND t.id = (SELECT MAX(t2.id) FROM %s AS t2 WHERE t2.user_id = t.user_id)`,
			tripsTable, citiesTable, inList, tripsTable)
	}

	rows, err := s.db.QueryContext(ctx, query, args.Values()...)
	if err != nil {
		return nil, fmt.Errorf("latest trip destinations by user_ids: %w", err)
	}
	defer rows.Close()

	items, err := scanShipmentRows(rows)
	if err != nil {
		return nil, err
	}

	out := make(map[int64]model.DriverLatestTrip, len(items))
	for _, item := range items {
		userID := anyToInt64(item["user_id"])
		tripID := anyToInt64(item["trip_id"])
		if userID <= 0 || tripID <= 0 {
			continue
		}
		dest := strings.TrimSpace(anyToString(item["destination"]))
		out[userID] = model.DriverLatestTrip{
			TripID:      tripID,
			Destination: dest,
		}
	}
	return out, nil
}

// LoadShippingsByShipmentIDs returns the latest active shipping per shipment.
// Shippings whose shipping_statuses.label is CANCELED or DELIVERED are omitted.
func (s *ShipmentDB) LoadShippingsByShipmentIDs(ctx context.Context, shipmentIDs []int64) (map[int64]map[string]any, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("shipment db is not configured")
	}
	if len(shipmentIDs) == 0 {
		return map[int64]map[string]any{}, nil
	}

	shippingsTable, err := quoteQualifiedIdentifier(s.dialect, "shippings")
	if err != nil {
		return nil, err
	}
	statusesTable, err := quoteQualifiedIdentifier(s.dialect, "shipping_statuses")
	if err != nil {
		return nil, err
	}
	tripsTable, err := quoteQualifiedIdentifier(s.dialect, "trips")
	if err != nil {
		return nil, err
	}

	args := newPlaceholderArgs(s.dialect)
	placeholders := make([]string, 0, len(shipmentIDs))
	for _, id := range shipmentIDs {
		placeholders = append(placeholders, args.Add(id))
	}

	excluded := "'" + strings.Join(excludedShippingStatusLabels, "','") + "'"

	var query string
	if s.dialect == "postgres" {
		query = fmt.Sprintf(`
SELECT DISTINCT ON (sh."shipment_id")
    sh."id" AS id,
    sh."shipping_code" AS shipping_code,
    sh."trip_id" AS trip_id,
    sh."shipment_id" AS shipment_id,
    t."user_id" AS passenger_user_id,
    sh."payment_type" AS payment_type,
    sh."currency" AS currency,
    sh."absolute_amount" AS absolute_amount,
    sh."suggested_amount" AS suggested_amount,
    sh."insurance_amount" AS insurance_amount,
    sh."commission_amount" AS commission_amount,
    sh."tax_amount" AS tax_amount,
    sh."commission_percent" AS commission_percent,
    sh."insurance_percent" AS insurance_percent,
    sh."tax_percent" AS tax_percent,
    sh."last_status_id" AS last_status_id,
    sh."last_status_description" AS last_status_description,
    COALESCE(ss."title", '') AS last_status_title,
    COALESCE(ss."label", '') AS last_status_label
FROM %s AS sh
JOIN %s AS ss ON ss."id" = sh."last_status_id"
JOIN %s AS t ON t."id" = sh."trip_id"
WHERE sh."shipment_id" IN (%s)
  AND UPPER(ss."label") NOT IN (%s)
ORDER BY sh."shipment_id" ASC, sh."id" DESC`,
			shippingsTable,
			statusesTable,
			tripsTable,
			strings.Join(placeholders, ", "),
			excluded,
		)
	} else {
		query = fmt.Sprintf(`
SELECT
    sh.id AS id,
    sh.shipping_code AS shipping_code,
    sh.trip_id AS trip_id,
    sh.shipment_id AS shipment_id,
    t.user_id AS passenger_user_id,
    sh.payment_type AS payment_type,
    sh.currency AS currency,
    sh.absolute_amount AS absolute_amount,
    sh.suggested_amount AS suggested_amount,
    sh.insurance_amount AS insurance_amount,
    sh.commission_amount AS commission_amount,
    sh.tax_amount AS tax_amount,
    sh.commission_percent AS commission_percent,
    sh.insurance_percent AS insurance_percent,
    sh.tax_percent AS tax_percent,
    sh.last_status_id AS last_status_id,
    sh.last_status_description AS last_status_description,
    COALESCE(ss.title, '') AS last_status_title,
    COALESCE(ss.label, '') AS last_status_label
FROM %s AS sh
JOIN %s AS ss ON ss.id = sh.last_status_id
JOIN %s AS t ON t.id = sh.trip_id
WHERE sh.shipment_id IN (%s)
  AND UPPER(ss.label) NOT IN (%s)
  AND sh.id = (
      SELECT MAX(sh2.id) FROM %s AS sh2
      JOIN %s AS ss2 ON ss2.id = sh2.last_status_id
      WHERE sh2.shipment_id = sh.shipment_id
        AND UPPER(ss2.label) NOT IN (%s)
  )`,
			shippingsTable,
			statusesTable,
			tripsTable,
			strings.Join(placeholders, ", "),
			excluded,
			shippingsTable,
			statusesTable,
			excluded,
		)
	}

	rows, err := s.db.QueryContext(ctx, query, args.Values()...)
	if err != nil {
		return nil, fmt.Errorf("shippings by shipment_ids: %w", err)
	}
	defer rows.Close()

	items, err := scanShipmentRows(rows)
	if err != nil {
		return nil, err
	}

	out := make(map[int64]map[string]any, len(items))
	for _, item := range items {
		shipmentID := anyToInt64(item["shipment_id"])
		if shipmentID <= 0 {
			continue
		}
		out[shipmentID] = item
	}
	return out, nil
}
