package storage

import (
	"context"
	"fmt"
	"strings"

	"geo-service/internal/model"
)

// LoadUserVehiclesByUserID returns registered vehicles for a Laravel user.
func (s *ShipmentDB) LoadUserVehiclesByUserID(ctx context.Context, userID int64) ([]model.UserVehicle, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("shipment db is not configured")
	}
	if userID <= 0 {
		return []model.UserVehicle{}, nil
	}

	args := newPlaceholderArgs(s.dialect)
	userPlaceholder := args.Add(userID)
	query := buildUserVehiclesByUserIDQuery(s.dialect, userPlaceholder)

	rows, err := s.db.QueryContext(ctx, query, args.Values()...)
	if err != nil {
		return nil, fmt.Errorf("user vehicles by user_id: %w", err)
	}
	defer rows.Close()

	items, err := scanShipmentRows(rows)
	if err != nil {
		return nil, err
	}

	out := make([]model.UserVehicle, 0, len(items))
	for _, item := range items {
		out = append(out, model.UserVehicle{
			ID:                anyToInt64(item["id"]),
			UserID:            anyToInt64(item["user_id"]),
			VehicleTypeID:    anyToInt64(item["vehicle_type_id"]),
			VehicleTypeTitle: strings.TrimSpace(anyToString(item["vehicle_type_title"])),
			Title:             strings.TrimSpace(anyToString(item["title"])),
			VIN:               strings.TrimSpace(anyToString(item["vin"])),
			Color:             strings.TrimSpace(anyToString(item["color"])),
			Plaque1:           strings.TrimSpace(anyToString(item["plaque1"])),
			PlaqueAlphabet:    strings.TrimSpace(anyToString(item["plaque_alphabet"])),
			Plaque2:           strings.TrimSpace(anyToString(item["plaque2"])),
			Plaque3:           strings.TrimSpace(anyToString(item["plaque3"])),
			MotorPlaque1:      strings.TrimSpace(anyToString(item["motor_plaque1"])),
			MotorPlaque2:      strings.TrimSpace(anyToString(item["motor_plaque2"])),
			CardImage:         ResolvePublicMediaURL(s.mediaPublicBaseURL, anyToString(item["card_image"])),
			IsDefault:         anyToBool(item["is_default"]),
			Status:            strings.TrimSpace(anyToString(item["status"])),
			StatusDescription: strings.TrimSpace(anyToString(item["status_description"])),
		})
	}
	return out, nil
}

func buildUserVehiclesByUserIDQuery(dialect, userPlaceholder string) string {
	uv := quotedTable(dialect, "user_vehicles")
	vt := quotedTable(dialect, "vehicle_types")
	if dialect == "postgres" {
		return fmt.Sprintf(`
SELECT
    uv."id" AS id,
    uv."user_id" AS user_id,
    uv."vehicle_type_id" AS vehicle_type_id,
    COALESCE(vt."title", '') AS vehicle_type_title,
    uv."title" AS title,
    COALESCE(uv."vin", '') AS vin,
    COALESCE(uv."color", '') AS color,
    COALESCE(uv."plaque1", '') AS plaque1,
    COALESCE(uv."plaque_alphabet", '') AS plaque_alphabet,
    COALESCE(uv."plaque2", '') AS plaque2,
    COALESCE(uv."plaque3", '') AS plaque3,
    COALESCE(uv."motor_plaque1", '') AS motor_plaque1,
    COALESCE(uv."motor_plaque2", '') AS motor_plaque2,
    COALESCE(uv."card_image", '') AS card_image,
    uv."is_default" AS is_default,
    uv."status" AS status,
    COALESCE(uv."status_description", '') AS status_description
FROM %s AS uv
LEFT JOIN %s AS vt ON vt."id" = uv."vehicle_type_id"
WHERE uv."user_id" = %s
ORDER BY uv."is_default" DESC, uv."id" DESC`,
			uv,
			vt,
			userPlaceholder,
		)
	}
	return fmt.Sprintf(`
SELECT
    uv.id AS id,
    uv.user_id AS user_id,
    uv.vehicle_type_id AS vehicle_type_id,
    COALESCE(vt.title, '') AS vehicle_type_title,
    uv.title AS title,
    COALESCE(uv.vin, '') AS vin,
    COALESCE(uv.color, '') AS color,
    COALESCE(uv.plaque1, '') AS plaque1,
    COALESCE(uv.plaque_alphabet, '') AS plaque_alphabet,
    COALESCE(uv.plaque2, '') AS plaque2,
    COALESCE(uv.plaque3, '') AS plaque3,
    COALESCE(uv.motor_plaque1, '') AS motor_plaque1,
    COALESCE(uv.motor_plaque2, '') AS motor_plaque2,
    COALESCE(uv.card_image, '') AS card_image,
    uv.is_default AS is_default,
    uv.status AS status,
    COALESCE(uv.status_description, '') AS status_description
FROM %s AS uv
LEFT JOIN %s AS vt ON vt.id = uv.vehicle_type_id
WHERE uv.user_id = %s
ORDER BY uv.is_default DESC, uv.id DESC`,
		uv,
		vt,
		userPlaceholder,
	)
}

func anyToBool(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case int64:
		return x != 0
	case int32:
		return x != 0
	case int:
		return x != 0
	case float64:
		return x != 0
	case []byte:
		s := strings.TrimSpace(strings.ToLower(string(x)))
		return s == "t" || s == "true" || s == "1"
	case string:
		s := strings.TrimSpace(strings.ToLower(x))
		return s == "t" || s == "true" || s == "1"
	default:
		return false
	}
}
