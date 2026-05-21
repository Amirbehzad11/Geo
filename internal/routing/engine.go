package routing

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"strings"

	"geo-service/internal/utils"
)

// TransportMode determines which roads are accessible and what speeds apply.
type TransportMode string

const (
	ModeCar        TransportMode = "car"
	ModeMotorcycle TransportMode = "motorcycle"
	ModeBus        TransportMode = "bus"
	ModeWalking    TransportMode = "walking"
	ModeAirplane   TransportMode = "airplane"
)

// Point is a geographic coordinate used for polyline encoding.
type Point struct {
	Lat float64
	Lng float64
}

// Route is the result of a route calculation.
type Route struct {
	Distance     float64 // km
	Duration     float64 // minutes
	Points       []Point
	Polyline     string // Google Encoded Polyline
	Instructions []Instruction
}

// Engine wraps the graph router with an automatic Haversine fallback.
type Engine struct {
	graph        *Graph
	avgSpeedKmH  float64
	yenSpurCap   int // max spur positions per Yen's iteration; 0 = unlimited
}

// NewEngineWithGraph creates an Engine backed by g.
// yenSpurCap limits how many spur positions Yen's algorithm explores per
// alternative (0 = unlimited, recommended: 60–80 for production).
func NewEngineWithGraph(avgSpeedKmH float64, g *Graph, yenSpurCap int) *Engine {
	return &Engine{avgSpeedKmH: avgSpeedKmH, graph: g, yenSpurCap: yenSpurCap}
}

// NormalizeMode validates external mode/vehicle strings and applies the
// service default. It accepts a few common aliases from Laravel/mobile clients.
func NormalizeMode(raw string) (TransportMode, error) {
	mode := TransportMode(strings.ToLower(strings.TrimSpace(raw)))
	if mode == "" {
		return ModeCar, nil
	}

	switch mode {
	case ModeCar, ModeMotorcycle, ModeBus, ModeWalking, ModeAirplane:
		return mode, nil
	case "walk", "pedestrian", "foot":
		return ModeWalking, nil
	case "bike", "bicycle":
		return "", fmt.Errorf("unsupported routing mode %q; supported modes: car, motorcycle, bus, walking", raw)
	default:
		return "", fmt.Errorf("unsupported routing mode %q; supported modes: car, motorcycle, bus, walking", raw)
	}
}

// Calculate returns the primary route between two points for the given mode.
func (e *Engine) Calculate(startLat, startLng, endLat, endLng float64, mode TransportMode) *Route {
	routes := e.CalculateAlternatives(startLat, startLng, endLat, endLng, mode, 1)
	if len(routes) == 0 {
		return e.haversineRoute(startLat, startLng, endLat, endLng, ModeCar)
	}
	return routes[0]
}

// CalculateAlternatives returns up to k candidate routes sorted by duration.
// When the graph is unavailable or endpoints cannot be snapped, it returns a
// single Haversine fallback route.
// Uses context.Background() — for timeout control use CalculateAlternativesCtx.
func (e *Engine) CalculateAlternatives(startLat, startLng, endLat, endLng float64, mode TransportMode, k int) []*Route {
	return e.CalculateAlternativesCtx(context.Background(), startLat, startLng, endLat, endLng, mode, k)
}

// CalculateAlternativesCtx is the context-aware variant of CalculateAlternatives.
// When ctx is cancelled or its deadline expires, the A* search returns
// immediately without manufacturing a ground-mode fallback route.
func (e *Engine) CalculateAlternativesCtx(ctx context.Context, startLat, startLng, endLat, endLng float64, mode TransportMode, k int) []*Route {
	if mode == "" {
		mode = ModeCar
	}
	if k <= 0 {
		k = 1
	}

	if mode == ModeAirplane {
		return []*Route{e.airplaneRoute(startLat, startLng, endLat, endLng)}
	}

	if e.graph != nil {
		if routes := e.graphRoutesCtx(ctx, startLat, startLng, endLat, endLng, mode, k); len(routes) > 0 {
			return routes
		}
		if ctx.Err() != nil {
			return nil
		}
	}
	return []*Route{e.haversineRoute(startLat, startLng, endLat, endLng, mode)}
}

func (e *Engine) HasGraph() bool { return e.graph != nil }

const snapThresholdKm = 0.3

// longRouteAltsThresholdKm is the straight-line distance above which the
// engine automatically reduces alternatives to 1. On the Iran graph a 300 km
// route (Tehran → Isfahan) triggers Yen's k=3 with ~60 spur A* calls, each
// taking ~25 ms — totalling ~1.5 s per request. For long-distance routes the
// alternative paths are near-identical (they share 95 %+ of the road network),
// so k=1 returns the same user-visible quality at 1/60 of the compute cost.
// Routes shorter than this threshold keep the caller-requested k unchanged.
const longRouteAltsThresholdKm = 50.0

// ---- mode profiles ----

// modeProfile defines edge access and speed rules for a transport mode.
type modeProfile struct {
	edgeAllowed       func(*Edge) bool
	edgeSpeed         func(*Edge) float64
	fallbackSpeed     float64
	heuristicSpeedKmH float64
}

var profiles = map[TransportMode]modeProfile{
	ModeCar: {
		edgeAllowed:       func(e *Edge) bool { return e.Flags.Has(FlagCar) },
		edgeSpeed:         nil, // use precomputed edge.TimeHours
		fallbackSpeed:     0,   // engine uses avgSpeedKmH
		heuristicSpeedKmH: 130,
	},
	ModeMotorcycle: {
		edgeAllowed:       func(e *Edge) bool { return e.Flags.Has(FlagMotorcycle) },
		edgeSpeed:         motorcycleSpeed,
		fallbackSpeed:     60,
		heuristicSpeedKmH: 150,
	},
	ModeBus: {
		edgeAllowed:       func(e *Edge) bool { return e.Flags&(FlagBus|FlagCar) != 0 },
		edgeSpeed:         busSpeed,
		fallbackSpeed:     65,
		heuristicSpeedKmH: 100,
	},
	ModeWalking: {
		edgeAllowed:       walkingAllowed,
		edgeSpeed:         walkingSpeed,
		fallbackSpeed:     5,
		heuristicSpeedKmH: 6,
	},
}

func minSpeed(a, b float64) float64 {
	if a <= 0 {
		return b
	}
	if b <= 0 {
		return a
	}
	return math.Min(a, b)
}

// ---- speed functions (switch on HighwayKind — no map lookup) ----

func motorcycleSpeed(e *Edge) float64 {
	switch e.Kind {
	case HWMotorway:
		return 120
	case HWMotorwayLink:
		return 90
	case HWTrunk:
		return 100
	case HWTrunkLink:
		return 80
	case HWPrimary:
		return 90
	case HWPrimaryLink:
		return 75
	case HWSecondary:
		return 70
	case HWSecondaryLink:
		return 60
	case HWTertiary:
		return 60
	case HWTertiaryLink:
		return 50
	case HWUnclassified:
		return 50
	case HWResidential:
		return 40
	case HWLivingStreet:
		return 25
	case HWService:
		return 25
	default:
		return float64(e.SpeedKmH) * 1.1
	}
}

func busSpeed(e *Edge) float64 {
	switch e.Kind {
	case HWMotorway:
		return minSpeed(90, float64(e.SpeedKmH))
	case HWMotorwayLink:
		return minSpeed(70, float64(e.SpeedKmH))
	case HWTrunk:
		return minSpeed(80, float64(e.SpeedKmH))
	case HWTrunkLink:
		return minSpeed(70, float64(e.SpeedKmH))
	case HWPrimary:
		return minSpeed(70, float64(e.SpeedKmH))
	case HWPrimaryLink:
		return minSpeed(60, float64(e.SpeedKmH))
	case HWSecondary:
		return minSpeed(55, float64(e.SpeedKmH))
	case HWSecondaryLink:
		return minSpeed(50, float64(e.SpeedKmH))
	case HWTertiary:
		return minSpeed(45, float64(e.SpeedKmH))
	case HWTertiaryLink:
		return minSpeed(40, float64(e.SpeedKmH))
	case HWUnclassified:
		return minSpeed(35, float64(e.SpeedKmH))
	case HWResidential:
		return minSpeed(25, float64(e.SpeedKmH))
	case HWLivingStreet:
		return minSpeed(15, float64(e.SpeedKmH))
	case HWService:
		return minSpeed(15, float64(e.SpeedKmH))
	default:
		return float64(e.SpeedKmH) * 0.8
	}
}

func walkingAllowed(e *Edge) bool {
	return e.Flags.Has(FlagFoot) && !e.Kind.BlocksWalking()
}

func walkingSpeed(e *Edge) float64 {
	switch e.Kind {
	case HWFootway, HWPedestrian, HWPath, HWCorridor, HWCrossing, HWSidewalk, HWPlatform:
		return 5.0
	case HWSteps:
		return 3.0
	case HWLivingStreet:
		return 4.0
	case HWResidential, HWService:
		return 2.2
	case HWUnclassified:
		return 1.8
	case HWTertiary, HWTertiaryLink:
		return 1.5
	case HWSecondary, HWSecondaryLink, HWPrimary, HWPrimaryLink:
		return 1.2
	default:
		return 2.0
	}
}

// ---- route builders ----

// graphRoutes is a context.Background() wrapper kept for backward compatibility.
func (e *Engine) graphRoutes(startLat, startLng, endLat, endLng float64, mode TransportMode, k int) []*Route {
	return e.graphRoutesCtx(context.Background(), startLat, startLng, endLat, endLng, mode, k)
}

// graphRoutesCtx runs A* with the given context so that cancellation / timeout
// propagates all the way into the inner Dijkstra loop.
// Returns nil on A* timeout so callers can avoid a misleading fallback route.
func (e *Engine) graphRoutesCtx(ctx context.Context, startLat, startLng, endLat, endLng float64, mode TransportMode, k int) []*Route {
	if ctx.Err() != nil {
		return nil
	}

	p, ok := profiles[mode]
	if !ok {
		return nil
	}

	startNode := e.graph.NearestRoutableNodeWithin(startLat, startLng, snapThresholdKm, p.edgeAllowed)
	endNode := e.graph.NearestRoutableNodeWithin(endLat, endLng, snapThresholdKm, p.edgeAllowed)
	if startNode == nil || endNode == nil {
		return nil
	}

	// Long-route optimisation: for routes whose straight-line distance exceeds
	// longRouteAltsThresholdKm, the alternative paths are near-identical (they
	// share almost all of the road network), so we limit k to 1. This reduces
	// Yen's spur-A* calls from 60 to 0, cutting per-request time from ~1.5 s to
	// ~25 ms (bidirectional A*) — enabling 100+ concurrent users at < 1 s each.
	effectiveK := k
	if k > 1 {
		straightKm := utils.Haversine(startLat, startLng, endLat, endLng)
		if straightKm > longRouteAltsThresholdKm {
			effectiveK = 1
		}
	}

	paths, err := e.graph.KFastestPaths(ctx, startNode.ID, endNode.ID, effectiveK, e.yenSpurCap, p.edgeAllowed, p.edgeSpeed, p.heuristicSpeedKmH)
	if err != nil {
		if !errors.Is(err, ErrNoPath) && ctx.Err() == nil {
			// Only log unexpected errors; context cancellation is expected behaviour.
			log.Printf("[routing] A* error (%s): %v", mode, err)
		}
		return nil
	}

	routes := make([]*Route, 0, len(paths))
	for _, path := range paths {
		routes = append(routes, e.pathToRoute(path, startLat, startLng, endLat, endLng, mode))
	}
	return routes
}

func (e *Engine) pathToRoute(path *PathResult, startLat, startLng, endLat, endLng float64, mode TransportMode) *Route {
	points := make([]Point, 0, len(path.Nodes)+2)
	appendPoint(&points, Point{Lat: startLat, Lng: startLng})
	for _, n := range path.Nodes {
		appendPoint(&points, Point{Lat: n.Lat, Lng: n.Lng})
	}
	appendPoint(&points, Point{Lat: endLat, Lng: endLng})

	distance := path.DistanceKm
	snapDistance := 0.0
	if len(path.Nodes) > 0 {
		first := path.Nodes[0]
		last := path.Nodes[len(path.Nodes)-1]
		snapDistance += utils.Haversine(startLat, startLng, first.Lat, first.Lng)
		snapDistance += utils.Haversine(endLat, endLng, last.Lat, last.Lng)
		distance += snapDistance
	}

	duration := path.TimeHours * 60
	if snapDistance > 0 {
		duration += (snapDistance / e.fallbackSpeed(mode)) * 60
	}
	if duration == 0 && distance > 0 {
		duration = (distance / e.fallbackSpeed(mode)) * 60
	}

	return &Route{
		Distance: utils.Round(distance, 3),
		Duration: utils.Round(duration, 2),
		Points:   points,
		Polyline: EncodePolyline(points),
		// Pass the graph's name resolver so buildInstructions can look up street
		// names from the pool without storing them redundantly in Edge.
		Instructions: buildInstructions(path, mode,
			Point{Lat: startLat, Lng: startLng},
			Point{Lat: endLat, Lng: endLng},
			e.fallbackSpeed(mode),
			e.graph.NameFor,
		),
	}
}

func appendPoint(points *[]Point, p Point) {
	if len(*points) == 0 {
		*points = append(*points, p)
		return
	}
	last := (*points)[len(*points)-1]
	if math.Abs(last.Lat-p.Lat) < 1e-7 && math.Abs(last.Lng-p.Lng) < 1e-7 {
		return
	}
	*points = append(*points, p)
}

func (e *Engine) haversineRoute(startLat, startLng, endLat, endLng float64, mode TransportMode) *Route {
	dist := utils.Haversine(startLat, startLng, endLat, endLng)
	spd := e.fallbackSpeed(mode)
	duration := (dist / spd) * 60
	start := Point{Lat: startLat, Lng: startLng}
	end := Point{Lat: endLat, Lng: endLng}
	points := []Point{start, end}
	return &Route{
		Distance: utils.Round(dist, 3),
		Duration: utils.Round(duration, 2),
		Points:   points,
		Polyline: EncodePolyline(points),
	}
}

// airplaneRoute: quadratic Bézier arc at cruising speed.
// A control point is placed 25% of the total distance above the geographic
// midpoint (perpendicular to the start→end bearing), producing the classic
// curved flight-path shape visible on airline booking sites.
func (e *Engine) airplaneRoute(startLat, startLng, endLat, endLng float64) *Route {
	const cruiseSpeedKmH = 800.0
	dist := utils.Haversine(startLat, startLng, endLat, endLng)
	// Add fixed 30 min for takeoff + landing regardless of distance.
	duration := (dist/cruiseSpeedKmH)*60 + 30
	start := Point{Lat: startLat, Lng: startLng}
	end := Point{Lat: endLat, Lng: endLng}
	points := flightArc(start, end, 50)
	return &Route{
		Distance: utils.Round(dist, 3),
		Duration: utils.Round(duration, 2),
		Points:   points,
		Polyline: EncodePolyline(points),
	}
}

func (e *Engine) fallbackSpeed(mode TransportMode) float64 {
	if p, ok := profiles[mode]; ok && p.fallbackSpeed > 0 {
		return p.fallbackSpeed
	}
	if e.avgSpeedKmH > 0 {
		return e.avgSpeedKmH
	}
	return 40
}

// flightArc returns n+2 points along a quadratic Bézier arc between start and end.
//
// A control point is placed perpendicular to the start→end bearing at the
// geographic midpoint, offset by 25% of the total distance. The perpendicular
// direction is chosen so the arc always curves toward the nearest pole —
// producing the classic upward-curving flight-path shape.
func flightArc(start, end Point, n int) []Point {
	const toRad = math.Pi / 180
	const earthRadiusKm = 6371.0

	// Geographic midpoint.
	midLat := (start.Lat + end.Lat) / 2
	midLng := (start.Lng + end.Lng) / 2

	// Initial bearing from start to end (radians).
	lat1r := start.Lat * toRad
	lat2r := end.Lat * toRad
	dLng := (end.Lng - start.Lng) * toRad
	y := math.Sin(dLng) * math.Cos(lat2r)
	x := math.Cos(lat1r)*math.Sin(lat2r) - math.Sin(lat1r)*math.Cos(lat2r)*math.Cos(dLng)
	bearing := math.Atan2(y, x)

	// Lift the control point 25% of the total distance from the midpoint.
	dist := utils.Haversine(start.Lat, start.Lng, end.Lat, end.Lng)
	liftKm := dist * 0.25
	d := liftKm / earthRadiusKm

	midLatR := midLat * toRad
	midLngR := midLng * toRad

	// destLat returns the latitude (degrees) reached by moving from the midpoint
	// by distance d in the given bearing.
	destLat := func(perp float64) float64 {
		return math.Asin(math.Sin(midLatR)*math.Cos(d)+math.Cos(midLatR)*math.Sin(d)*math.Cos(perp)) / toRad
	}

	// Of the two perpendicular directions, pick the one whose destination is
	// further from the equator — this always curves toward the nearest pole.
	perp1 := bearing - math.Pi/2
	perp2 := bearing + math.Pi/2
	perpBearing := perp1
	if math.Abs(destLat(perp2)) > math.Abs(destLat(perp1)) {
		perpBearing = perp2
	}

	ctrlLatR := math.Asin(math.Sin(midLatR)*math.Cos(d) +
		math.Cos(midLatR)*math.Sin(d)*math.Cos(perpBearing))
	ctrlLngR := midLngR + math.Atan2(
		math.Sin(perpBearing)*math.Sin(d)*math.Cos(midLatR),
		math.Cos(d)-math.Sin(midLatR)*math.Sin(ctrlLatR),
	)
	ctrl := Point{Lat: ctrlLatR / toRad, Lng: ctrlLngR / toRad}

	// Quadratic Bézier: B(t) = (1-t)²·P0 + 2(1-t)t·P1 + t²·P2
	pts := make([]Point, n+2)
	pts[0] = start
	pts[n+1] = end
	for i := 1; i <= n; i++ {
		t := float64(i) / float64(n+1)
		s := 1 - t
		pts[i] = Point{
			Lat: s*s*start.Lat + 2*s*t*ctrl.Lat + t*t*end.Lat,
			Lng: s*s*start.Lng + 2*s*t*ctrl.Lng + t*t*end.Lng,
		}
	}
	return pts
}

// EncodePolyline implements Google's Encoded Polyline Algorithm Format.
func EncodePolyline(points []Point) string {
	var buf strings.Builder
	prevLat, prevLng := 0, 0
	for _, p := range points {
		lat := int(math.Round(p.Lat * 1e5))
		lng := int(math.Round(p.Lng * 1e5))
		encodeChunk(&buf, lat-prevLat)
		encodeChunk(&buf, lng-prevLng)
		prevLat = lat
		prevLng = lng
	}
	return buf.String()
}

// DecodePolyline decodes a Google Encoded Polyline string into a slice of Points.
func DecodePolyline(encoded string) []Point {
	var points []Point
	index, lat, lng := 0, 0, 0
	for index < len(encoded) {
		dLat, ok := decodeChunk(encoded, &index)
		if !ok {
			return nil
		}
		dLng, ok := decodeChunk(encoded, &index)
		if !ok {
			return nil
		}
		lat += dLat
		lng += dLng
		points = append(points, Point{Lat: float64(lat) / 1e5, Lng: float64(lng) / 1e5})
	}
	return points
}

func decodeChunk(s string, index *int) (int, bool) {
	result, shift := 0, 0
	for {
		if *index >= len(s) {
			return 0, false
		}
		b := int(s[*index]) - 63
		(*index)++
		result |= (b & 0x1f) << shift
		shift += 5
		if b < 0x20 {
			break
		}
		if shift > 30 {
			return 0, false
		}
	}
	if result&1 != 0 {
		return ^(result >> 1), true
	}
	return result >> 1, true
}

func encodeChunk(buf *strings.Builder, value int) {
	value <<= 1
	if value < 0 {
		value = ^value
	}
	for value >= 0x20 {
		buf.WriteByte(byte((0x20 | (value & 0x1f)) + 63))
		value >>= 5
	}
	buf.WriteByte(byte(value + 63))
}

// profileSpeedFn returns the speed function for a transport mode, used by
// instructions to compute per-leg durations.
func profileSpeedFn(mode TransportMode) func(*Edge) float64 {
	if p, ok := profiles[mode]; ok {
		return p.edgeSpeed
	}
	return nil
}
