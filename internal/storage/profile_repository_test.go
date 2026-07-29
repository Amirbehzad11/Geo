package storage

import (
	"strings"
	"testing"
)

func TestBuildVisibleOnMapUserIDsQueryPostgres(t *testing.T) {
	args := newPlaceholderArgs("postgres")
	query := buildVisibleOnMapUserIDsQuery("postgres", []string{args.Add(int64(1)), args.Add(int64(2))})

	for _, want := range []string{
		`FROM "profiles" AS p`,
		`p."user_id" IN ($1, $2)`,
		`p."visible_on_map" IS TRUE`,
	} {
		if !strings.Contains(query, want) {
			t.Fatalf("expected query to contain %q, got:\n%s", want, query)
		}
	}
}

func TestBuildVisibleOnMapUserIDsQueryMySQL(t *testing.T) {
	args := newPlaceholderArgs("mysql")
	query := buildVisibleOnMapUserIDsQuery("mysql", []string{args.Add(int64(1))})

	for _, want := range []string{
		`FROM`,
		`profiles`,
		`visible_on_map = 1`,
	} {
		if !strings.Contains(query, want) {
			t.Fatalf("expected query to contain %q, got:\n%s", want, query)
		}
	}
}
