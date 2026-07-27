package storage

import (
	"strings"
	"testing"
)

func TestBuildUserVehiclesByUserIDQuery(t *testing.T) {
	args := newPlaceholderArgs("postgres")
	query := buildUserVehiclesByUserIDQuery("postgres", args.Add(int64(1)))
	for _, want := range []string{
		`FROM "user_vehicles" AS uv`,
		`LEFT JOIN "vehicle_types" AS vt`,
		`uv."user_id" = $1`,
		`vehicle_type_title`,
		`ORDER BY uv."is_default" DESC, uv."id" DESC`,
	} {
		if !strings.Contains(query, want) {
			t.Fatalf("expected query to contain %q, got:\n%s", want, query)
		}
	}
}
