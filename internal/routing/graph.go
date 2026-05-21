package routing

import (
	"math"

	"geo-service/internal/utils"
)

const gridCellSizeDeg = 0.02 // roughly 2.2 km latitude; used for fast snap lookup.

// AccessFlags is a bitmask of transport modes that can traverse an edge.
// Using uint8 instead of four bool fields saves 3 bytes per edge and lets the
// nodeRoutable pre-filter use a single bitwise AND instead of four comparisons.
type AccessFlags uint8

const (
	FlagCar        AccessFlags = 1 << 0
	FlagMotorcycle AccessFlags = 1 << 1
	FlagBus        AccessFlags = 1 << 2
	FlagFoot       AccessFlags = 1 << 3
)

// Has returns true when all bits in flag are set in f.
func (f AccessFlags) Has(flag AccessFlags) bool { return f&flag != 0 }

// Node is a road intersection.
//
// Flags holds the union of AccessFlags from all edges incident to this node
// (both incoming and outgoing). It drives an O(1) pre-filter in nodeRoutable,
// replacing the old separate nodeAccess map[int64]accessMask (which consumed
// ~194 MB for the Iran graph).
type Node struct {
	ID    int64
	Lat   float64
	Lng   float64
	Flags AccessFlags // union of all incident edge access flags
}

// Edge is a directed road segment between two nodes.
//
// Memory layout (32 bytes, down from 72 bytes):
//
//	To         int64       8 B  offset  0
//	DistanceKm float64     8 B  offset  8   (kept float64 for test-exact sums)
//	SpeedKmH   float32     4 B  offset 16
//	TimeHours  float32     4 B  offset 20
//	Kind       HighwayKind 1 B  offset 24   (was string = 16 B header + heap)
//	Flags      AccessFlags 1 B  offset 25   (was 4 bools = 4 B)
//	_          [2]byte     2 B  offset 26   (explicit pad for NameIdx alignment)
//	NameIdx    uint32      4 B  offset 28   (was string = 16 B header + heap)
//	                            --------
//	                            32 B total  (was 72 B, 56 % smaller)
type Edge struct {
	To         int64
	DistanceKm float64
	SpeedKmH   float32
	TimeHours  float32
	Kind       HighwayKind
	Flags      AccessFlags
	_          [2]byte
	NameIdx    uint32
}

// EdgeKey uniquely identifies a directed edge for Yen's blocked-edge sets.
type EdgeKey struct {
	From int64
	To   int64
}

type cellKey struct {
	Lat int
	Lng int
}

// Graph is the in-memory road network (adjacency list).
//
// Name interning: street names are stored once in namePool (index 0 = ""),
// and each Edge.NameIdx is an index into that pool. This eliminates one heap
// allocation per named edge (~560 MB on the Iran graph).
//
// RevAdj is the reverse adjacency list used by bidirectional A*.
// RevAdj[toID] = list of fromIDs that have at least one edge pointing to toID.
// It is populated by AddEdge and enables the backward search to traverse the
// graph in reverse without a separate copy of all edges.
// Memory overhead: ~1.2 GB on the Iran 14.4 M-node graph (57.6 M int64 values
// plus Go map/slice overhead).
type Graph struct {
	Nodes  map[int64]*Node
	Edges  map[int64][]Edge
	RevAdj map[int64][]int64 // toID → []fromID  (reverse adjacency for bidir A*)
	grid   map[cellKey][]*Node

	// Name pool — index 0 is always the empty string.
	namePool  []string
	nameIndex map[string]uint32
}

func NewGraph() *Graph {
	return &Graph{
		Nodes:     make(map[int64]*Node),
		Edges:     make(map[int64][]Edge),
		RevAdj:    make(map[int64][]int64),
		grid:      make(map[cellKey][]*Node),
		namePool:  []string{""},          // slot 0 = empty name
		nameIndex: make(map[string]uint32),
	}
}

// InternName returns the pool index for name, inserting it when not present.
// The empty string always maps to index 0.
func (g *Graph) InternName(name string) uint32 {
	if name == "" {
		return 0
	}
	if idx, ok := g.nameIndex[name]; ok {
		return idx
	}
	idx := uint32(len(g.namePool))
	g.namePool = append(g.namePool, name)
	g.nameIndex[name] = idx
	return idx
}

// NameFor resolves a pool index back to a street name.
// Index 0 and any out-of-range index returns the empty string.
func (g *Graph) NameFor(idx uint32) string {
	if int(idx) < len(g.namePool) {
		return g.namePool[idx]
	}
	return ""
}

// SetEdgeName sets the name for an already-added edge by pool-interning name.
// Primarily used in tests to assign names after calling AddEdge.
func (g *Graph) SetEdgeName(fromID int64, edgeIdx int, name string) {
	if edges, ok := g.Edges[fromID]; ok && edgeIdx < len(edges) {
		g.Edges[fromID][edgeIdx].NameIdx = g.InternName(name)
	}
}

func (g *Graph) AddNode(n *Node) {
	g.Nodes[n.ID] = n
	k := cellFor(n.Lat, n.Lng)
	g.grid[k] = append(g.grid[k], n)
}

// AddEdge appends e to the adjacency list of from and updates the access-flag
// union on both endpoint nodes. It also records the reverse link in RevAdj so
// that bidirectional A* can traverse edges in the reverse direction.
func (g *Graph) AddEdge(from int64, e Edge) {
	g.Edges[from] = append(g.Edges[from], e)
	// Propagate access flags to both endpoints so nodeRoutable can do an O(1)
	// pre-filter using a bitmask instead of scanning all outgoing edges.
	if n, ok := g.Nodes[from]; ok {
		n.Flags |= e.Flags
	}
	if n, ok := g.Nodes[e.To]; ok {
		n.Flags |= e.Flags
	}
	// Reverse adjacency: record that `from` has an edge pointing to `e.To`.
	// biAStar uses this to traverse the graph backward without a separate copy.
	if g.RevAdj != nil {
		g.RevAdj[e.To] = append(g.RevAdj[e.To], from)
	}
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
// request while preserving the existing snap-threshold behaviour.
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

// nodeRoutable returns true when node id has at least one edge that passes
// edgeOK, or when id is a destination-only node whose access flags indicate
// it can be reached by the selected profile.
//
// Two-phase check:
//  1. O(1) bitmask pre-filter using Node.Flags (union of all incident edges).
//     If the profile flag isn't set at all, the node can't be routable.
//  2. Full edge scan for outgoing edges: validates the HighwayKind constraint
//     (e.g. walking blocks motorway despite FootAllowed=true).
func (g *Graph) nodeRoutable(id int64, edgeOK func(*Edge) bool) bool {
	if edgeOK == nil {
		return true
	}
	n, ok := g.Nodes[id]
	if !ok || n.Flags == 0 {
		return false
	}
	// Pre-filter: synthesise a minimal edge carrying only the node's combined
	// access flags (Kind=HWUnknown so highway-level blocks are not applied yet).
	synth := Edge{Flags: n.Flags}
	outgoing := g.Edges[id]
	if !edgeOK(&synth) && len(outgoing) == 0 {
		// Destination-only node whose flags don't pass the profile filter.
		return false
	}
	// Full scan — catches mode-specific highway-type restrictions.
	for i := range outgoing {
		if edgeOK(&outgoing[i]) {
			return true
		}
	}
	// Destination-only node: no outgoing edges but the pre-filter passed,
	// meaning at least one incoming edge brings the right access type.
	return len(outgoing) == 0
}

func cellFor(lat, lng float64) cellKey {
	return cellKey{
		Lat: int(math.Floor(lat / gridCellSizeDeg)),
		Lng: int(math.Floor(lng / gridCellSizeDeg)),
	}
}
