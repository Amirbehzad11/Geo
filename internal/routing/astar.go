package routing

import (
	"container/heap"
	"errors"
	"math"

	"geo-service/internal/utils"
)

// ErrNoPath is returned when A* cannot find a route.
var ErrNoPath = errors.New("no path found between the given nodes")

// PathResult is the output of a successful A* search.
type PathResult struct {
	Nodes      []*Node
	DistanceKm float64
	TimeHours  float64
}

// ---- priority queue ----

type pqItem struct {
	nodeID int64
	gCost  float64 // actual cost from start, in hours
	fCost  float64 // g + heuristic
	index  int
}

type priorityQueue []*pqItem

func (pq priorityQueue) Len() int           { return len(pq) }
func (pq priorityQueue) Less(i, j int) bool { return pq[i].fCost < pq[j].fCost }
func (pq priorityQueue) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
	pq[i].index = i
	pq[j].index = j
}
func (pq *priorityQueue) Push(x any) {
	item := x.(*pqItem)
	item.index = len(*pq)
	*pq = append(*pq, item)
}
func (pq *priorityQueue) Pop() any {
	old := *pq
	n := len(old)
	item := old[n-1]
	old[n-1] = nil
	*pq = old[:n-1]
	return item
}

// ---- A* ----

// AStar finds the fastest path from startID to goalID using A*.
//
// edgeOK returns false to skip an edge for a transport profile.
// speedFn returns the effective speed in km/h for the edge.
func (g *Graph) AStar(startID, goalID int64, edgeOK func(*Edge) bool, speedFn func(*Edge) float64) (*PathResult, error) {
	return g.aStar(startID, goalID, edgeOK, speedFn, 130, nil, nil)
}

func (g *Graph) aStar(
	startID, goalID int64,
	edgeOK func(*Edge) bool,
	speedFn func(*Edge) float64,
	heuristicSpeedKmH float64,
	blockedEdges map[EdgeKey]bool,
	blockedNodes map[int64]bool,
) (*PathResult, error) {
	startNode, ok := g.Nodes[startID]
	if !ok {
		return nil, errors.New("start node not in graph")
	}
	goalNode, ok := g.Nodes[goalID]
	if !ok {
		return nil, errors.New("goal node not in graph")
	}

	if startID == goalID {
		return &PathResult{
			Nodes:      []*Node{startNode},
			DistanceKm: 0,
			TimeHours:  0,
		}, nil
	}

	if heuristicSpeedKmH <= 0 {
		heuristicSpeedKmH = 130
	}

	timeScore := map[int64]float64{startID: 0}
	distScore := map[int64]float64{startID: 0}
	cameFrom := map[int64]int64{}

	pq := &priorityQueue{}
	heap.Init(pq)
	heap.Push(pq, &pqItem{
		nodeID: startID,
		gCost:  0,
		fCost:  utils.Haversine(startNode.Lat, startNode.Lng, goalNode.Lat, goalNode.Lng) / heuristicSpeedKmH,
	})

	for pq.Len() > 0 {
		cur := heap.Pop(pq).(*pqItem)

		if cur.nodeID == goalID {
			return buildPath(g, cameFrom, goalID, distScore[goalID], timeScore[goalID]), nil
		}

		if cur.gCost > timeScore[cur.nodeID]+1e-9 {
			continue
		}

		for _, edge := range g.Edges[cur.nodeID] {
			if blockedNodes != nil && blockedNodes[edge.To] {
				continue
			}
			if blockedEdges != nil && blockedEdges[EdgeKey{From: cur.nodeID, To: edge.To}] {
				continue
			}
			if edgeOK != nil && !edgeOK(&edge) {
				continue
			}
			neighbor, exists := g.Nodes[edge.To]
			if !exists {
				continue
			}

			speed := edge.SpeedKmH
			if speedFn != nil {
				speed = speedFn(&edge)
			}
			if speed <= 0 || math.IsNaN(speed) || math.IsInf(speed, 0) {
				speed = 40
			}

			edgeTime := edge.DistanceKm / speed
			tentTime := timeScore[cur.nodeID] + edgeTime
			prev, seen := timeScore[edge.To]
			if !seen || tentTime < prev {
				timeScore[edge.To] = tentTime
				distScore[edge.To] = distScore[cur.nodeID] + edge.DistanceKm
				cameFrom[edge.To] = cur.nodeID
				h := utils.Haversine(neighbor.Lat, neighbor.Lng, goalNode.Lat, goalNode.Lng) / heuristicSpeedKmH
				heap.Push(pq, &pqItem{
					nodeID: edge.To,
					gCost:  tentTime,
					fCost:  tentTime + h,
				})
			}
		}
	}

	return nil, ErrNoPath
}

func buildPath(g *Graph, cameFrom map[int64]int64, goalID int64, dist, time float64) *PathResult {
	ids := []int64{goalID}
	for cur := goalID; ; {
		prev, ok := cameFrom[cur]
		if !ok {
			break
		}
		ids = append(ids, prev)
		cur = prev
	}

	nodes := make([]*Node, len(ids))
	for i := range ids {
		nodes[len(ids)-1-i] = g.Nodes[ids[i]]
	}
	return &PathResult{Nodes: nodes, DistanceKm: dist, TimeHours: time}
}
