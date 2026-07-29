package storage

import (
	"context"
	"fmt"
	"strings"
)

// FindVisibleOnMapUserIDs returns the subset of userIDs whose profile has
// visible_on_map = true. Users without a profile (or with false) are omitted.
func (s *ShipmentDB) FindVisibleOnMapUserIDs(ctx context.Context, userIDs []int64) (map[int64]struct{}, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("shipment db is not configured")
	}
	if len(userIDs) == 0 {
		return map[int64]struct{}{}, nil
	}

	args := newPlaceholderArgs(s.dialect)
	placeholders := make([]string, 0, len(userIDs))
	for _, id := range userIDs {
		placeholders = append(placeholders, args.Add(id))
	}

	query := buildVisibleOnMapUserIDsQuery(s.dialect, placeholders)
	rows, err := s.db.QueryContext(ctx, query, args.Values()...)
	if err != nil {
		return nil, fmt.Errorf("visible_on_map profiles by user_ids: %w", err)
	}
	defer rows.Close()

	items, err := scanShipmentRows(rows)
	if err != nil {
		return nil, err
	}

	out := make(map[int64]struct{}, len(items))
	for _, item := range items {
		userID := anyToInt64(item["user_id"])
		if userID <= 0 {
			continue
		}
		out[userID] = struct{}{}
	}
	return out, nil
}

func buildVisibleOnMapUserIDsQuery(dialect string, placeholders []string) string {
	profiles := quotedTable(dialect, "profiles")
	inList := strings.Join(placeholders, ", ")
	if dialect == "postgres" {
		return fmt.Sprintf(`
SELECT p."user_id" AS user_id
FROM %s AS p
WHERE p."user_id" IN (%s)
  AND p."visible_on_map" IS TRUE`,
			profiles,
			inList,
		)
	}
	return fmt.Sprintf(`
SELECT p.user_id AS user_id
FROM %s AS p
WHERE p.user_id IN (%s)
  AND p.visible_on_map = 1`,
		profiles,
		inList,
	)
}
