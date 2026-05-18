package routing

import (
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
	Distance float64 // km
	Duration float64 // minutes
	Points   []Point
	Polyline string // Google Encoded Polyline
}

// Engine wraps the graph router with an automatic Haversine fallback.
type Engine struct {
	graph       *Graph
	avgSpeedKmH float64
}

func NewEngineWithGraph(avgSpeedKmH float64, g *Graph) *Engine {
	return &Engine{avgSpeedKmH: avgSpeedKmH, graph: g}
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
func (e *Engine) CalculateAlternatives(startLat, startLng, endLat, endLng float64, mode TransportMode, k int) []*Route {
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
		if routes := e.graphRoutes(startLat, startLng, endLat, endLng, mode, k); len(routes) > 0 {
			return routes
		}
	}
	return []*Route{e.haversineRoute(startLat, startLng, endLat, endLng, mode)}
}

func (e *Engine) HasGraph() bool { return e.graph != nil }

const snapThresholdKm = 0.3

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
		edgeAllowed:       func(e *Edge) bool { return e.CarAllowed },
		edgeSpeed:         func(e *Edge) float64 { return e.SpeedKmH },
		fallbackSpeed:     0, // engine uses avgSpeedKmH
		heuristicSpeedKmH: 130,
	},
	ModeMotorcycle: {
		edgeAllowed: func(e *Edge) bool { return e.MotorcycleAllowed },
		edgeSpeed: func(e *Edge) float64 {
			if v, ok := motorcycleSpeeds[e.HighwayType]; ok {
				return v
			}
			return e.SpeedKmH * 1.1
		},
		fallbackSpeed:     60,
		heuristicSpeedKmH: 130,
	},
	ModeBus: {
		edgeAllowed: func(e *Edge) bool { return e.BusAllowed || e.CarAllowed },
		edgeSpeed: func(e *Edge) float64 {
			if v, ok := busSpeeds[e.HighwayType]; ok {
				return minSpeed(v, e.SpeedKmH)
			}
			return e.SpeedKmH * 0.8
		},
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

// ---- speed/access tables ----

var motorcycleSpeeds = map[string]float64{
	"motorway": 120, "motorway_link": 90,
	"trunk": 100, "trunk_link": 80,
	"primary": 90, "primary_link": 75,
	"secondary": 70, "secondary_link": 60,
	"tertiary": 60, "tertiary_link": 50,
	"unclassified": 50, "residential": 40,
	"living_street": 25, "service": 25,
}

var busSpeeds = map[string]float64{
	"motorway": 90, "motorway_link": 70,
	"trunk": 80, "trunk_link": 70,
	"primary": 70, "primary_link": 60,
	"secondary": 55, "secondary_link": 50,
	"tertiary": 45, "tertiary_link": 40,
	"unclassified": 35, "residential": 25,
	"living_street": 15, "service": 15,
}

var busAllowedHW = map[string]bool{
	"motorway": true, "motorway_link": true,
	"trunk": true, "trunk_link": true,
	"primary": true, "primary_link": true,
	"secondary": true, "secondary_link": true,
	"tertiary": true, "tertiary_link": true,
	"unclassified": true, "residential": true,
	"living_street": true, "service": true,
}

var walkingBlockedHW = map[string]bool{
	"motorway": true, "motorway_link": true,
	"trunk": true, "trunk_link": true,
}

var pedestrianHW = map[string]bool{
	"footway": true, "pedestrian": true,
	"path": true, "steps": true,
	"corridor": true, "crossing": true,
	"sidewalk": true, "platform": true,
}

func walkingAllowed(e *Edge) bool {
	return e.FootAllowed && !walkingBlockedHW[e.HighwayType]
}

func walkingSpeed(e *Edge) float64 {
	if pedestrianHW[e.HighwayType] {
		if e.HighwayType == "steps" {
			return 3.0
		}
		return 5.0
	}

	switch e.HighwayType {
	case "living_street":
		return 4.0
	case "residential", "service":
		return 2.2
	case "unclassified":
		return 1.8
	case "tertiary", "tertiary_link":
		return 1.5
	case "secondary", "secondary_link", "primary", "primary_link":
		return 1.2
	default:
		return 2.0
	}
}

// ---- route builders ----

// longDistanceThresholdKm is the straight-line distance above which graphRoutes
// switches to a highway-only edge filter before trying the full road network.
// Routing Tehran→Isfahan (~340 km) on the full 14 M-node graph can take 10-15 s;
// restricting to motorway/trunk/primary/secondary cuts the search space by ~90 %
// and brings latency under 200 ms for most inter-city pairs.
const longDistanceThresholdKm = 25.0

func (e *Engine) graphRoutes(startLat, startLng, endLat, endLng float64, mode TransportMode, k int) []*Route {
	p, ok := profiles[mode]
	if !ok {
		return nil
	}

	// For long-distance vehicle routes, try a highway-only filter first.
	// Walking always needs the full network (footpaths, pedestrian zones, etc.).
	if mode != ModeWalking {
		dist := utils.Haversine(startLat, startLng, endLat, endLng)
		if dist >= longDistanceThresholdKm {
			hwAllowed := highwayOnlyFilter(p.edgeAllowed)
			if routes := e.runAstar(startLat, startLng, endLat, endLng, k, p, hwAllowed); len(routes) > 0 {
				return routes
			}
			// Highway-only failed (endpoints outside major-road coverage):
			// fall through to full-network search below.
		}
	}

	return e.runAstar(startLat, startLng, endLat, endLng, k, p, p.edgeAllowed)
}

// highwayOnlyFilter wraps edgeAllowed to additionally require the edge to be
// on a major road class. This excludes residential, service, footway and other
// minor OSM ways from A* exploration, reducing the effective graph by ~90 %.
func highwayOnlyFilter(edgeAllowed func(*Edge) bool) func(*Edge) bool {
	return func(e *Edge) bool {
		if !edgeAllowed(e) {
			return false
		}
		switch e.HighwayType {
		case "motorway", "motorway_link",
			"trunk", "trunk_link",
			"primary", "primary_link",
			"secondary", "secondary_link",
			"tertiary", "tertiary_link":
			return true
		}
		return false
	}
}

func (e *Engine) runAstar(startLat, startLng, endLat, endLng float64, k int, p modeProfile, edgeAllowed func(*Edge) bool) []*Route {
	startNode := e.graph.NearestRoutableNodeWithin(startLat, startLng, snapThresholdKm, edgeAllowed)
	endNode := e.graph.NearestRoutableNodeWithin(endLat, endLng, snapThresholdKm, edgeAllowed)
	if startNode == nil || endNode == nil {
		return nil
	}

	paths, err := e.graph.KFastestPaths(startNode.ID, endNode.ID, k, edgeAllowed, p.edgeSpeed, p.heuristicSpeedKmH)
	if err != nil {
		if !errors.Is(err, ErrNoPath) {
			log.Printf("[routing] A* error: %v", err)
		}
		return nil
	}

	routes := make([]*Route, 0, len(paths))
	for _, path := range paths {
		routes = append(routes, e.pathToRoute(path, startLat, startLng, endLat, endLng, p))
	}
	return routes
}

func (e *Engine) pathToRoute(path *PathResult, startLat, startLng, endLat, endLng float64, p modeProfile) *Route {
	points := make([]Point, 0, len(path.Nodes))
	for _, n := range path.Nodes {
		appendPoint(&points, Point{Lat: n.Lat, Lng: n.Lng})
	}

	distance := path.DistanceKm
	if len(path.Nodes) > 0 {
		first := path.Nodes[0]
		last := path.Nodes[len(path.Nodes)-1]
		distance += utils.Haversine(startLat, startLng, first.Lat, first.Lng)
		distance += utils.Haversine(endLat, endLng, last.Lat, last.Lng)
	}

	duration := path.TimeHours * 60
	if duration == 0 && distance > 0 {
		spd := p.fallbackSpeed
		if spd <= 0 {
			spd = e.avgSpeedKmH
		}
		if spd <= 0 {
			spd = 40
		}
		duration = (distance / spd) * 60
	}

	return &Route{
		Distance: utils.Round(distance, 3),
		Duration: utils.Round(duration, 2),
		Points:   points,
		Polyline: EncodePolyline(points),
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
	points := flightArc(start, end, 12)
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
