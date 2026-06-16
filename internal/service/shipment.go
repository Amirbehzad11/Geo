package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"geo-service/internal/model"
)

var ErrShipmentSearchDisabled = errors.New("shipment search database is not configured; set SHIPMENT_DB_DSN, SHIPMENT_DB_DRIVER, SHIPMENT_TABLE, SHIPMENT_ORIGIN_LOCATION_COLUMN")

const (
	defaultShipmentRadiusKm = 2.0
	defaultShipmentLimit    = 100
	maxShipmentLimit        = 500
	maxNearbySearchRadiusKm = 50.0

	vehicleCacheTTL = 2 * time.Minute
)

// ShipmentRepository is the read-only storage contract used by ShipmentService.
type ShipmentRepository interface {
	FindNearbyShipments(ctx context.Context, lat, lng, radiusKm float64, limit int) ([]map[string]any, error)
}

// ShippingRepository optionally enriches shipments with active shipping rows.
type ShippingRepository interface {
	LoadShippingsByShipmentIDs(ctx context.Context, shipmentIDs []int64) (map[int64]map[string]any, error)
}

// VehicleTypeRepository optionally enriches shipments with vehicle labels/titles.
type VehicleTypeRepository interface {
	LoadVehicleTypes(ctx context.Context) ([]model.VehicleType, error)
}

// ShipmentService normalises nearby shipment search requests.
type ShipmentService struct {
	repo            ShipmentRepository
	defaultRadiusKm float64
	defaultLimit    int

	vtMu     sync.RWMutex
	vtData   map[int64]vehicleTypeInfo
	vtLoadAt time.Time
}

// NewShipmentService creates a ShipmentService.
func NewShipmentService(repo ShipmentRepository, defaultRadiusKm float64, defaultLimit int) *ShipmentService {
	if defaultRadiusKm <= 0 {
		defaultRadiusKm = defaultShipmentRadiusKm
	}
	if defaultLimit <= 0 {
		defaultLimit = defaultShipmentLimit
	}
	if defaultLimit > maxShipmentLimit {
		defaultLimit = maxShipmentLimit
	}
	return &ShipmentService{
		repo:            repo,
		defaultRadiusKm: defaultRadiusKm,
		defaultLimit:    defaultLimit,
	}
}

// SearchNearby returns shipments near the passenger.
//
// Each shipment gains a "vehicles" field populated from shipment.vehicle_allowed
// and the configured vehicle_types label/title lookup.
func (s *ShipmentService) SearchNearby(ctx context.Context, req model.NearbyShipmentRequest) (*model.NearbyShipmentResponse, error) {
	if s == nil || s.repo == nil {
		return nil, ErrShipmentSearchDisabled
	}

	radiusKm := req.RadiusKm
	if radiusKm <= 0 {
		radiusKm = s.defaultRadiusKm
	}
	if radiusKm > maxNearbySearchRadiusKm {
		radiusKm = maxNearbySearchRadiusKm
	}
	limit := req.Limit
	if limit <= 0 {
		limit = s.defaultLimit
	}
	if limit > maxShipmentLimit {
		limit = maxShipmentLimit
	}

	rows, err := s.repo.FindNearbyShipments(ctx, req.Lat, req.Lng, radiusKm, limit)
	if err != nil {
		return nil, err
	}

	wantIDs := vehicleTypeSet(req.FilterVehicleTypes)
	var vehicleTypes map[int64]vehicleTypeInfo
	if len(rows) > 0 {
		vehicleTypes = s.cachedVehicleTypes(ctx)
	}

	out := rows[:0]
	for _, row := range rows {
		allowedIDs := shipmentAllowedVehicleIDs(row["vehicle_allowed"])
		if len(wantIDs) > 0 && !hasAnyVehicleType(allowedIDs, wantIDs) {
			continue
		}
		row["vehicles"] = buildShipmentVehicles(allowedIDs, vehicleTypes)
		out = append(out, row)
	}
	s.attachShippings(ctx, out)

	return &model.NearbyShipmentResponse{
		Type:      "shipment.nearby",
		Timestamp: time.Now().UnixMilli(),
		Query: model.NearbyShipmentQuery{
			Lat:      req.Lat,
			Lng:      req.Lng,
			RadiusKm: radiusKm,
			Limit:    limit,
		},
		Count:     len(out),
		Shipments: out,
	}, nil
}

func (s *ShipmentService) attachShippings(ctx context.Context, rows []map[string]any) {
	sr, ok := s.repo.(ShippingRepository)
	if !ok || len(rows) == 0 {
		return
	}

	seen := make(map[int64]struct{}, len(rows))
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		id, ok := toInt64(row["id"])
		if !ok || id <= 0 {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return
	}

	byShipment, err := sr.LoadShippingsByShipmentIDs(ctx, ids)
	if err != nil {
		slog.Warn("nearby shipment shipping enrichment failed", "err", err)
		return
	}
	for _, row := range rows {
		id, ok := toInt64(row["id"])
		if !ok {
			row["shipping"] = nil
			continue
		}
		if shipping, ok := byShipment[id]; ok {
			row["shipping"] = shipping
		} else {
			row["shipping"] = nil
		}
	}
}

type vehicleTypeInfo struct {
	Label string
	Title string
}

func (s *ShipmentService) cachedVehicleTypes(ctx context.Context) map[int64]vehicleTypeInfo {
	vr, ok := s.repo.(VehicleTypeRepository)
	if !ok {
		return nil
	}

	s.vtMu.RLock()
	if !s.vtLoadAt.IsZero() && time.Since(s.vtLoadAt) < vehicleCacheTTL {
		data := s.vtData
		s.vtMu.RUnlock()
		return data
	}
	s.vtMu.RUnlock()

	s.vtMu.Lock()
	defer s.vtMu.Unlock()
	if !s.vtLoadAt.IsZero() && time.Since(s.vtLoadAt) < vehicleCacheTTL {
		return s.vtData
	}

	types, err := vr.LoadVehicleTypes(ctx)
	if err == nil {
		s.vtData = vehicleTypeMap(types)
		s.vtLoadAt = time.Now()
	}
	return s.vtData
}

func vehicleTypeSet(ids []int64) map[int64]struct{} {
	if len(ids) == 0 {
		return nil
	}
	want := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		want[id] = struct{}{}
	}
	return want
}

func hasAnyVehicleType(allowedIDs []int64, want map[int64]struct{}) bool {
	for _, id := range allowedIDs {
		if _, ok := want[id]; ok {
			return true
		}
	}
	return false
}

func buildShipmentVehicles(allowedIDs []int64, vehicleTypes map[int64]vehicleTypeInfo) []model.ShipmentVehicle {
	out := make([]model.ShipmentVehicle, 0, len(allowedIDs))
	seen := make(map[int64]struct{}, len(allowedIDs))
	for _, id := range allowedIDs {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}

		info := vehicleTypes[id]
		label := info.Label
		if label == "" {
			label = strconv.FormatInt(id, 10)
		}
		out = append(out, model.ShipmentVehicle{
			VehicleTypeID: id,
			Label:         label,
			Title:         info.Title,
		})
	}
	return out
}

func vehicleTypeMap(types []model.VehicleType) map[int64]vehicleTypeInfo {
	if len(types) == 0 {
		return nil
	}

	out := make(map[int64]vehicleTypeInfo, len(types))
	for _, vt := range types {
		id, ok := toInt64(vt.ID)
		if !ok {
			continue
		}
		label := strings.TrimSpace(vt.Label)
		title := strings.TrimSpace(vt.Title)
		if label == "" && title == "" {
			continue
		}
		out[id] = vehicleTypeInfo{
			Label: label,
			Title: title,
		}
	}
	return out
}

func shipmentAllowedVehicleIDs(raw any) []int64 {
	switch v := raw.(type) {
	case nil:
		return nil
	case []int64:
		return append([]int64(nil), v...)
	case []int32:
		out := make([]int64, 0, len(v))
		for _, n := range v {
			out = append(out, int64(n))
		}
		return out
	case []int:
		out := make([]int64, 0, len(v))
		for _, n := range v {
			out = append(out, int64(n))
		}
		return out
	case []uint64:
		out := make([]int64, 0, len(v))
		for _, n := range v {
			out = append(out, int64(n))
		}
		return out
	case []uint32:
		out := make([]int64, 0, len(v))
		for _, n := range v {
			out = append(out, int64(n))
		}
		return out
	case []float64:
		out := make([]int64, 0, len(v))
		for _, n := range v {
			out = append(out, int64(n))
		}
		return out
	case []float32:
		out := make([]int64, 0, len(v))
		for _, n := range v {
			out = append(out, int64(n))
		}
		return out
	case []any:
		out := make([]int64, 0, len(v))
		for _, item := range v {
			if id, ok := toInt64(item); ok {
				out = append(out, id)
			}
		}
		return out
	case []byte:
		return shipmentAllowedVehicleIDsFromString(string(v))
	case string:
		return shipmentAllowedVehicleIDsFromString(v)
	case json.Number:
		if id, err := v.Int64(); err == nil {
			return []int64{id}
		}
		if f, err := v.Float64(); err == nil {
			return []int64{int64(f)}
		}
		return nil
	default:
		if id, ok := toInt64(v); ok {
			return []int64{id}
		}
	}
	return nil
}

func shipmentAllowedVehicleIDsFromString(raw string) []int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.EqualFold(raw, "null") {
		return nil
	}

	if strings.HasPrefix(raw, "[") {
		if ids, ok := parseJSONInt64Slice(raw); ok {
			return ids
		}
	}

	if strings.HasPrefix(raw, "{") && strings.HasSuffix(raw, "}") {
		raw = strings.TrimSpace(raw[1 : len(raw)-1])
	}

	if strings.Contains(raw, ",") {
		return parseCommaSeparatedInt64s(raw)
	}

	if id, err := strconv.ParseInt(strings.Trim(raw, `"'`), 10, 64); err == nil {
		return []int64{id}
	}
	return nil
}

func parseJSONInt64Slice(raw string) ([]int64, bool) {
	var out []int64
	if err := json.Unmarshal([]byte(raw), &out); err == nil {
		return dedupeInt64s(out), true
	}

	var items []any
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil, false
	}
	out = make([]int64, 0, len(items))
	for _, item := range items {
		if id, ok := toInt64(item); ok {
			out = append(out, id)
		}
	}
	return dedupeInt64s(out), true
}

func parseCommaSeparatedInt64s(raw string) []int64 {
	parts := strings.Split(raw, ",")
	out := make([]int64, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(strings.Trim(part, `"'{}[]`))
		if part == "" || strings.EqualFold(part, "null") {
			continue
		}
		if id, err := strconv.ParseInt(part, 10, 64); err == nil {
			out = append(out, id)
		}
	}
	return dedupeInt64s(out)
}

func dedupeInt64s(ids []int64) []int64 {
	if len(ids) == 0 {
		return nil
	}
	out := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// toInt64 normalises any integer-like DB value to int64.
func toInt64(v any) (int64, bool) {
	switch x := v.(type) {
	case int64:
		return x, true
	case float64:
		return int64(x), true
	case int32:
		return int64(x), true
	case int:
		return int64(x), true
	case uint64:
		return int64(x), true
	case uint32:
		return int64(x), true
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return n, true
		}
		if f, err := x.Float64(); err == nil {
			return int64(f), true
		}
	case string:
		if n, err := strconv.ParseInt(x, 10, 64); err == nil {
			return n, true
		}
	case []byte:
		if n, err := strconv.ParseInt(string(x), 10, 64); err == nil {
			return n, true
		}
	}
	return 0, false
}

// rowFloat64 reads a numeric value from a shipment map row.
// Kept for compatibility with mixed driver output elsewhere in the package.
func rowFloat64(row map[string]any, column string) float64 {
	v, ok := row[column]
	if !ok || v == nil {
		return 0
	}
	switch x := v.(type) {
	case float64:
		return x
	case float32:
		return float64(x)
	case int64:
		return float64(x)
	case int32:
		return float64(x)
	case int:
		return float64(x)
	case string:
		if f, err := strconv.ParseFloat(x, 64); err == nil {
			return f
		}
	}
	return 0
}
