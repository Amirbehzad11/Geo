package routing

import (
	"math"

	"geo-service/internal/utils"
)

const gridCellSizeDeg = 0.02 // roughly 2.2 km latitude; used for fast snap lookup.

// Node is a road intersection.
type Node struct {
	ID  int64
	Lat float64
	Lng float64
}

// Edge is a directed road segment between two nodes.
type Edge struct {
	To                int64
	DistanceKm        float64
	SpeedKmH          float64
	TimeHours         float64 // precomputed: DistanceKm / SpeedKmH (car speed)
	HighwayType       string  // e.g. "primary", "residential"; empty for legacy files
	CarAllowed        bool
	MotorcycleAllowed bool
	BusAllowed        bool
	FootAllowed       bool
}

type EdgeKey struct {
	From int64
	To   int64
}

type cellKey struct {
	Lat int
	Lng int
}

type accessMask uint8

const (
	accessCar accessMask = 1 << iota
	accessMotorcycle
	accessBus
	accessFoot
)

// Graph is the in-memory road network (adjacency list).
type Graph struct {
	Nodes      map[int64]*Node
	Edges      map[int64][]Edge
	nodeAccess map[int64]accessMask
	grid       map[cellKey][]*Node
}

func NewGraph() *Graph {
	return &Graph{
		Nodes:      make(map[int64]*Node),
		Edges:      make(map[int64][]Edge),
		nodeAccess: make(map[int64]accessMask),
		grid:       make(map[cellKey][]*Node),
	}
}

func (g *Graph) AddNode(n *Node) {
	g.Nodes[n.ID] = n
	k := cellFor(n.Lat, n.Lng)
	g.grid[k] = append(g.grid[k], n)
}

func (g *Graph) AddEdge(from int64, e Edge) {
	g.Edges[from] = append(g.Edges[from], e)
	mask := maskForEdge(e)
	g.nodeAccess[from] |= mask
	g.nodeAccess[e.To] |= mask
}

func (g *Graph) NodeCount() int { return len(g.Nodes) }

// NearestNode returns the node closest to (lat, lng) by Haversine distance.
// It is exact but O(n); route snapping should prefer NearestNodeWithin.
func (g *Graph) NearestNode(lat, lng float64) *Node {
	var nearest *Node
	minDist := math.MaxFloat64
	for _, n := range g.Nodes {
		if d := utils.Haversine(lat, lng, n.Lat, n.Lng); d < minDist {
			minDist = d
			nearest = n
		}
	}
	return nearest
}

// NearestNodeWithin searches only nearby grid cells and returns nil when no
// node is within maxKm. This avoids scanning a country-scale graph for every
// request while preserving the existing snap-threshold behavior.
func (g *Graph) NearestNodeWithin(lat, lng, maxKm float64) *Node {
	return g.nearestNodeWithin(lat, lng, maxKm, nil)
}

// NearestRoutableNodeWithin returns the closest node that can be used as a
// route endpoint for the selected profile.
func (g *Graph) NearestRoutableNodeWithin(lat, lng, maxKm float64, edgeOK func(*Edge) bool) *Node {
	return g.nearestNodeWithin(lat, lng, maxKm, edgeOK)
}

func (g *Graph) nearestNodeWithin(lat, lng, maxKm float64, edgeOK func(*Edge) bool) *Node {
	if maxKm <= 0 {
		return nil
	}
	if len(g.grid) == 0 {
		n := g.NearestNode(lat, lng)
		if n != nil && g.nodeRoutable(n.ID, edgeOK) && utils.Haversine(lat, lng, n.Lat, n.Lng) <= maxKm {
			return n
		}
		return nil
	}

	center := cellFor(lat, lng)
	radius := int(math.Ceil(maxKm/(111.0*gridCellSizeDeg))) + 1
	if radius < 1 {
		radius = 1
	}

	var nearest *Node
	minDist := math.MaxFloat64
	for dLat := -radius; dLat <= radius; dLat++ {
		for dLng := -radius; dLng <= radius; dLng++ {
			for _, n := range g.grid[cellKey{Lat: center.Lat + dLat, Lng: center.Lng + dLng}] {
				if !g.nodeRoutable(n.ID, edgeOK) {
					continue
				}
				if d := utils.Haversine(lat, lng, n.Lat, n.Lng); d < minDist {
					minDist = d
					nearest = n
				}
			}
		}
	}
	if nearest == nil || minDist > maxKm {
		return nil
	}
	return nearest
}

func (g *Graph) nodeRoutable(id int64, edgeOK func(*Edge) bool) bool {
	if edgeOK == nil {
		return true
	}
	mask := g.nodeAccess[id]
	if mask == 0 {
		return false
	}
	// O(1) pre-filter: the nodeAccess mask is the union of access flags from
	// every edge connected to this node (outgoing and incoming via AddEdge).
	// If the synthetic union-edge fails edgeOK, no real edge will pass — bail out.
	synth := edgeFromMask(mask)
	if !edgeOK(&synth) {
		return false
	}
	// Full edge scan for correctness: walking mode also checks HighwayType
	// (e.g. walkingBlockedHW), which the bitmask does not capture.
	edges := g.Edges[id]
	for i := range edges {
		if edgeOK(&edges[i]) {
			return true
		}
	}
	// No outgoing edges (destination-only node whose access was recorded from
	// incoming edges). The mask already passed edgeOK above — trust it.
	return len(edges) == 0
}

func maskForEdge(e Edge) accessMask {
	var mask accessMask
	if e.CarAllowed {
		mask |= accessCar
	}
	if e.MotorcycleAllowed {
		mask |= accessMotorcycle
	}
	if e.BusAllowed {
		mask |= accessBus
	}
	if e.FootAllowed {
		mask |= accessFoot
	}
	return mask
}

func edgeFromMask(mask accessMask) Edge {
	return Edge{
		CarAllowed:        mask&accessCar != 0,
		MotorcycleAllowed: mask&accessMotorcycle != 0,
		BusAllowed:        mask&accessBus != 0,
		FootAllowed:       mask&accessFoot != 0,
	}
}

func cellFor(lat, lng float64) cellKey {
	return cellKey{
		Lat: int(math.Floor(lat / gridCellSizeDeg)),
		Lng: int(math.Floor(lng / gridCellSizeDeg)),
	}
}
