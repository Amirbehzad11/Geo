package storage

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

var roadRegionNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func RoadSegmentsTableName(region string) (string, error) {
	region = strings.ToLower(strings.TrimSpace(region))
	if region == "" {
		return "road_segments", nil
	}
	region = strings.TrimPrefix(region, "road_segments_")
	if !roadRegionNameRe.MatchString(region) {
		return "", fmt.Errorf("invalid road region %q; use a slug like isfahan or tehran", region)
	}
	return "road_segments_" + region, nil
}

func (p *Postgres) EnsureRoadSegmentsTable(ctx context.Context, table string) error {
	if p == nil {
		return fmt.Errorf("postgres disabled")
	}
	if table == "road_segments" {
		return nil
	}
	if !roadRegionNameRe.MatchString(strings.TrimPrefix(table, "road_segments_")) {
		return fmt.Errorf("invalid road segments table %q", table)
	}

	qTable := quoteIdent(table)
	queries := []string{
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (LIKE road_segments INCLUDING DEFAULTS INCLUDING CONSTRAINTS)`, qTable),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (from_node_id)`, quoteIdent("idx_"+table+"_from_node"), qTable),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (to_node_id)`, quoteIdent("idx_"+table+"_to_node"), qTable),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (osm_way_id, from_node_id, to_node_id)`, quoteIdent("idx_"+table+"_osm_edge"), qTable),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (highway_type)`, quoteIdent("idx_"+table+"_highway"), qTable),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (car_allowed)`, quoteIdent("idx_"+table+"_car"), qTable),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (import_region)`, quoteIdent("idx_"+table+"_region"), qTable),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (from_node_id) WHERE car_allowed`, quoteIdent("idx_"+table+"_car_from"), qTable),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (from_node_id) WHERE motorcycle_allowed`, quoteIdent("idx_"+table+"_motorcycle_from"), qTable),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (from_node_id) WHERE bus_allowed`, quoteIdent("idx_"+table+"_bus_from"), qTable),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (from_node_id) WHERE foot_allowed`, quoteIdent("idx_"+table+"_foot_from"), qTable),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s USING BRIN (imported_at)`, quoteIdent("idx_"+table+"_imported_brin"), qTable),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s USING GIST (geom)`, quoteIdent("idx_"+table+"_geom_gist"), qTable),
	}
	for _, query := range queries {
		if _, err := p.pool.Exec(ctx, query); err != nil {
			return err
		}
	}
	return nil
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
