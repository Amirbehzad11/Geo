package routing

import (
	"math"
	"sort"
	"strconv"
	"strings"
)

// KFastestPaths returns up to k unique fastest paths using Yen's algorithm.
// The first item is always the primary fastest path for the selected profile.
func (g *Graph) KFastestPaths(
	startID, goalID int64,
	k int,
	edgeOK func(*Edge) bool,
	speedFn func(*Edge) float64,
	heuristicSpeedKmH float64,
) ([]*PathResult, error) {
	if k <= 0 {
		k = 1
	}

	first, err := g.aStar(startID, goalID, edgeOK, speedFn, heuristicSpeedKmH, nil, nil)
	if err != nil {
		return nil, err
	}
	accepted := []*PathResult{first}
	if k == 1 || len(first.Nodes) < 3 {
		return accepted, nil
	}

	candidates := make([]*PathResult, 0, k*4)
	seen := map[string]bool{pathSignature(first.Nodes): true}

	for len(accepted) < k {
		base := accepted[len(accepted)-1]

		for spurIdx := 0; spurIdx < len(base.Nodes)-1; spurIdx++ {
			spurID := base.Nodes[spurIdx].ID
			rootNodes := base.Nodes[:spurIdx+1]

			blockedEdges := make(map[EdgeKey]bool, len(accepted))
			for _, path := range accepted {
				if len(path.Nodes) <= spurIdx+1 {
					continue
				}
				if samePrefix(path.Nodes, rootNodes) {
					blockedEdges[EdgeKey{From: path.Nodes[spurIdx].ID, To: path.Nodes[spurIdx+1].ID}] = true
				}
			}

			blockedNodes := make(map[int64]bool, spurIdx)
			for _, n := range rootNodes[:len(rootNodes)-1] {
				blockedNodes[n.ID] = true
			}

			spur, err := g.aStar(spurID, goalID, edgeOK, speedFn, heuristicSpeedKmH, blockedEdges, blockedNodes)
			if err != nil {
				continue
			}

			total, ok := g.combineRootAndSpur(rootNodes, spur.Nodes, edgeOK, speedFn)
			if !ok {
				continue
			}
			sig := pathSignature(total.Nodes)
			if seen[sig] {
				continue
			}
			seen[sig] = true
			candidates = append(candidates, total)
		}

		if len(candidates) == 0 {
			break
		}

		sort.Slice(candidates, func(i, j int) bool {
			if math.Abs(candidates[i].TimeHours-candidates[j].TimeHours) > 1e-9 {
				return candidates[i].TimeHours < candidates[j].TimeHours
			}
			return candidates[i].DistanceKm < candidates[j].DistanceKm
		})

		accepted = append(accepted, candidates[0])
		candidates = candidates[1:]
	}

	return accepted, nil
}

func (g *Graph) combineRootAndSpur(
	rootNodes []*Node,
	spurNodes []*Node,
	edgeOK func(*Edge) bool,
	speedFn func(*Edge) float64,
) (*PathResult, bool) {
	if len(rootNodes) == 0 || len(spurNodes) == 0 {
		return nil, false
	}

	nodes := make([]*Node, 0, len(rootNodes)+len(spurNodes)-1)
	nodes = append(nodes, rootNodes...)
	nodes = append(nodes, spurNodes[1:]...)

	var dist, hours float64
	for i := 0; i < len(nodes)-1; i++ {
		edge, ok := g.edgeBetween(nodes[i].ID, nodes[i+1].ID, edgeOK)
		if !ok {
			return nil, false
		}
		speed := edge.SpeedKmH
		if speedFn != nil {
			speed = speedFn(edge)
		}
		if speed <= 0 {
			speed = 40
		}
		dist += edge.DistanceKm
		hours += edge.DistanceKm / speed
	}

	return &PathResult{
		Nodes:      nodes,
		DistanceKm: dist,
		TimeHours:  hours,
	}, true
}

func (g *Graph) edgeBetween(from, to int64, edgeOK func(*Edge) bool) (*Edge, bool) {
	var best *Edge
	for i := range g.Edges[from] {
		edge := &g.Edges[from][i]
		if edge.To != to {
			continue
		}
		if edgeOK != nil && !edgeOK(edge) {
			continue
		}
		if best == nil || edge.DistanceKm < best.DistanceKm {
			best = edge
		}
	}
	return best, best != nil
}

func samePrefix(nodes, prefix []*Node) bool {
	if len(nodes) < len(prefix) {
		return false
	}
	for i := range prefix {
		if nodes[i].ID != prefix[i].ID {
			return false
		}
	}
	return true
}

func pathSignature(nodes []*Node) string {
	var b strings.Builder
	for i, n := range nodes {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatInt(n.ID, 10))
	}
	return b.String()
}
