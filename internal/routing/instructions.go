package routing

import (
	"fmt"
	"math"
	"strings"

	"geo-service/internal/utils"
)

type Instruction struct {
	Index       int     `json:"index"`
	Type        string  `json:"type"`
	Modifier    string  `json:"modifier"`
	Text        string  `json:"text"`
	DistanceKm  float64 `json:"distance_km"`
	DurationMin float64 `json:"duration_min"`
	Location    Point   `json:"location"`
	StreetName  string  `json:"street_name,omitempty"`
}

type maneuver struct {
	nodeIndex int
	typ       string
	modifier  string
	street    string
}

func buildInstructions(path *PathResult, mode TransportMode, start, end Point, fallbackSpeedKmH float64) []Instruction {
	if path == nil || len(path.Nodes) == 0 {
		return []Instruction{
			arrivalInstruction(0, end),
		}
	}

	maneuvers := []maneuver{{
		nodeIndex: 0,
		typ:       "depart",
		modifier:  "straight",
		street:    streetName(path, 0),
	}}

	for i := 1; i < len(path.Nodes)-1; i++ {
		prev := path.Nodes[i-1]
		cur := path.Nodes[i]
		next := path.Nodes[i+1]
		delta := turnAngle(
			bearingDegrees(prev.Lat, prev.Lng, cur.Lat, cur.Lng),
			bearingDegrees(cur.Lat, cur.Lng, next.Lat, next.Lng),
		)
		linkEnd, linkDelta, linkDistance := unnamedLinkChain(path, i)
		if linkEnd > i {
			if math.Abs(linkDelta) >= 150 {
				maneuvers = append(maneuvers, maneuver{
					nodeIndex: i,
					typ:       "uturn",
					modifier:  "uturn",
					street:    streetName(path, linkEnd),
				})
			} else if linkDistance >= 0.08 && math.Abs(linkDelta) >= 55 {
				typ, modifier := maneuverType(linkDelta)
				maneuvers = append(maneuvers, maneuver{
					nodeIndex: i,
					typ:       typ,
					modifier:  modifier,
					street:    streetName(path, linkEnd),
				})
			}
			i = linkEnd - 1
			continue
		}
		prevStreet := streetName(path, i-1)
		nextStreet := streetName(path, i)
		if math.Abs(delta) < 25 && sameStreet(prevStreet, nextStreet) {
			continue
		}
		typ, modifier := maneuverType(delta)
		if typ == "continue" && sameStreet(prevStreet, nextStreet) {
			continue
		}
		maneuvers = append(maneuvers, maneuver{
			nodeIndex: i,
			typ:       typ,
			modifier:  modifier,
			street:    nextStreet,
		})
	}

	lastIdx := len(path.Nodes) - 1
	maneuvers = append(maneuvers, maneuver{
		nodeIndex: lastIdx,
		typ:       "arrive",
		modifier:  "straight",
	})

	out := make([]Instruction, 0, len(maneuvers))
	for i := 0; i < len(maneuvers); i++ {
		m := maneuvers[i]
		node := path.Nodes[m.nodeIndex]
		location := Point{Lat: node.Lat, Lng: node.Lng}
		if i == 0 {
			location = start
		}
		if m.typ == "arrive" {
			location = end
		}

		distanceKm, durationMin := instructionLeg(path, m.nodeIndex, nextManeuverNode(maneuvers, i), mode, fallbackSpeedKmH)
		inst := Instruction{
			Index:       i,
			Type:        m.typ,
			Modifier:    m.modifier,
			Text:        instructionText(m.typ, m.modifier, m.street),
			DistanceKm:  utils.Round(distanceKm, 3),
			DurationMin: utils.Round(durationMin, 2),
			Location:    location,
			StreetName:  m.street,
		}
		out = append(out, inst)
	}

	return out
}

func arrivalInstruction(index int, p Point) Instruction {
	return Instruction{
		Index:    index,
		Type:     "arrive",
		Modifier: "straight",
		Text:     "به مقصد رسیدید",
		Location: p,
	}
}

func nextManeuverNode(maneuvers []maneuver, i int) int {
	if i+1 >= len(maneuvers) {
		return maneuvers[i].nodeIndex
	}
	return maneuvers[i+1].nodeIndex
}

func instructionLeg(path *PathResult, fromNode, toNode int, mode TransportMode, fallbackSpeedKmH float64) (float64, float64) {
	if toNode <= fromNode || len(path.Edges) == 0 {
		return 0, 0
	}
	if toNode > len(path.Edges) {
		toNode = len(path.Edges)
	}
	var distance, hours float64
	speedFn := profileSpeedFn(mode)
	for i := fromNode; i < toNode; i++ {
		edge := &path.Edges[i]
		distance += edge.DistanceKm
		hours += edgeTravelTimeHours(edge, speedFn)
	}
	if hours == 0 && distance > 0 {
		if fallbackSpeedKmH <= 0 {
			fallbackSpeedKmH = 40
		}
		hours = distance / fallbackSpeedKmH
	}
	return distance, hours * 60
}

func profileSpeedFn(mode TransportMode) func(*Edge) float64 {
	if p, ok := profiles[mode]; ok {
		return p.edgeSpeed
	}
	return nil
}

func streetName(path *PathResult, edgeIdx int) string {
	if path == nil || edgeIdx < 0 || edgeIdx >= len(path.Edges) {
		return ""
	}
	return strings.TrimSpace(path.Edges[edgeIdx].Name)
}

func sameStreet(a, b string) bool {
	a = strings.TrimSpace(strings.ToLower(a))
	b = strings.TrimSpace(strings.ToLower(b))
	return a != "" && a == b
}

func maneuverType(delta float64) (string, string) {
	abs := math.Abs(delta)
	if abs >= 155 {
		return "uturn", "uturn"
	}
	side := "right"
	if delta < 0 {
		side = "left"
	}
	switch {
	case abs < 25:
		return "continue", "straight"
	case abs < 55:
		return "turn", "slight_" + side
	case abs < 125:
		return "turn", side
	default:
		return "turn", "sharp_" + side
	}
}

func unnamedLinkChain(path *PathResult, startEdge int) (endEdge int, delta float64, distanceKm float64) {
	if path == nil || startEdge < 0 || startEdge >= len(path.Edges) || startEdge+1 >= len(path.Nodes) {
		return startEdge, 0, 0
	}
	if !isUnnamedLink(path.Edges[startEdge]) {
		return startEdge, 0, 0
	}

	endEdge = startEdge
	for endEdge < len(path.Edges) && isUnnamedLink(path.Edges[endEdge]) {
		distanceKm += path.Edges[endEdge].DistanceKm
		endEdge++
	}
	if endEdge >= len(path.Nodes) {
		endEdge = len(path.Nodes) - 1
	}
	if endEdge <= startEdge {
		return startEdge, 0, distanceKm
	}

	prev := path.Nodes[startEdge-1]
	enter := path.Nodes[startEdge]
	exit := path.Nodes[endEdge]
	if endEdge+1 >= len(path.Nodes) {
		delta = turnAngle(
			bearingDegrees(prev.Lat, prev.Lng, enter.Lat, enter.Lng),
			bearingDegrees(enter.Lat, enter.Lng, exit.Lat, exit.Lng),
		)
		return endEdge, delta, distanceKm
	}
	after := path.Nodes[endEdge+1]
	delta = turnAngle(
		bearingDegrees(prev.Lat, prev.Lng, enter.Lat, enter.Lng),
		bearingDegrees(exit.Lat, exit.Lng, after.Lat, after.Lng),
	)
	return endEdge, delta, distanceKm
}

func isUnnamedLink(edge Edge) bool {
	return strings.TrimSpace(edge.Name) == "" && strings.HasSuffix(strings.TrimSpace(edge.HighwayType), "_link")
}

func instructionText(typ, modifier, street string) string {
	target := ""
	if street != "" {
		target = " به " + street
	}
	switch typ {
	case "depart":
		if street != "" {
			return "حرکت را در " + street + " شروع کنید"
		}
		return "حرکت را شروع کنید"
	case "continue":
		if street != "" {
			return "در " + street + " ادامه دهید"
		}
		return "مستقیم ادامه دهید"
	case "uturn":
		return "دور بزنید" + target
	case "turn":
		return fmt.Sprintf("%s%s", persianTurn(modifier), target)
	case "arrive":
		return "به مقصد رسیدید"
	default:
		return "ادامه دهید" + target
	}
}

func persianTurn(modifier string) string {
	switch modifier {
	case "slight_left":
		return "کمی به چپ بپیچید"
	case "left":
		return "به چپ بپیچید"
	case "sharp_left":
		return "تند به چپ بپیچید"
	case "slight_right":
		return "کمی به راست بپیچید"
	case "right":
		return "به راست بپیچید"
	case "sharp_right":
		return "تند به راست بپیچید"
	default:
		return "مستقیم ادامه دهید"
	}
}

func bearingDegrees(lat1, lng1, lat2, lng2 float64) float64 {
	const toRad = math.Pi / 180
	const toDeg = 180 / math.Pi
	lat1r := lat1 * toRad
	lat2r := lat2 * toRad
	dLng := (lng2 - lng1) * toRad
	y := math.Sin(dLng) * math.Cos(lat2r)
	x := math.Cos(lat1r)*math.Sin(lat2r) - math.Sin(lat1r)*math.Cos(lat2r)*math.Cos(dLng)
	b := math.Atan2(y, x) * toDeg
	if b < 0 {
		b += 360
	}
	return b
}

func turnAngle(fromBearing, toBearing float64) float64 {
	delta := math.Mod(toBearing-fromBearing+540, 360) - 180
	return delta
}
