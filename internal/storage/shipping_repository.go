package storage

import (
	"context"
	"fmt"
	"strings"
)

var excludedShippingStatusLabels = []string{"CANCELED", "DELIVERED"}

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
WHERE sh."shipment_id" IN (%s)
  AND UPPER(ss."label") NOT IN (%s)
ORDER BY sh."shipment_id" ASC, sh."id" DESC`,
			shippingsTable,
			statusesTable,
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
