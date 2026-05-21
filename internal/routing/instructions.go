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

// buildInstructions generates turn-by-turn maneuvers for path.
//
// nameFn resolves an Edge.NameIdx to a human-readable street name.
// Pass Graph.NameFor when a graph is available; nil is safe (produces empty
// street names, which suppresses name references in instruction text).
func buildInstructions(path *PathResult, mode TransportMode, start, end Point, fallbackSpeedKmH float64, nameFn func(uint32) string) []Instruction {
	if path == nil || len(path.Nodes) == 0 {
		return []Instruction{
			arrivalInstruction(0, end),
		}
	}

	maneuvers := []maneuver{{
		nodeIndex: 0,
		typ:       "depart",
		modifier:  "straight",
		street:    streetName(path, 0, nameFn),
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
					street:    streetName(path, linkEnd, nameFn),
				})
			} else if linkDistance >= 0.08 && math.Abs(linkDelta) >= 55 {
				typ, modifier := maneuverType(linkDelta)
				maneuvers = append(maneuvers, maneuver{
					nodeIndex: i,
					typ:       typ,
					modifier:  modifier,
					street:    streetName(path, linkEnd, nameFn),
				})
			}
			i = linkEnd - 1
			continue
		}
		prevStreet := streetName(path, i-1, nameFn)
		nextStreet := streetName(path, i, nameFn)
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
	maneuvers = collapseContinues(maneuvers)

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
		next := nextInstructionManeuver(maneuvers, i)
		inst := Instruction{
			Index:       i,
			Type:        m.typ,
			Modifier:    m.modifier,
			Text:        instructionText(m.typ, m.modifier, m.street, distanceKm, next),
			DistanceKm:  utils.Round(distanceKm, 3),
			DurationMin: utils.Round(durationMin, 2),
			Location:    location,
			StreetName:  m.street,
		}
		out = append(out, inst)
	}

	return out
}

func nextInstructionManeuver(maneuvers []maneuver, i int) maneuver {
	if i+1 >= len(maneuvers) {
		return maneuver{}
	}
	return maneuvers[i+1]
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

func collapseContinues(in []maneuver) []maneuver {
	if len(in) == 0 {
		return nil
	}
	out := make([]maneuver, 0, len(in))
	for _, m := range in {
		if m.typ == "continue" && len(out) > 0 && out[len(out)-1].typ == "continue" {
			continue
		}
		out = append(out, m)
	}
	return out
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

// streetName resolves the name of the edge at edgeIdx using nameFn.
// Returns the trimmed name, or empty string when nameFn is nil or NameIdx is 0.
func streetName(path *PathResult, edgeIdx int, nameFn func(uint32) string) string {
	if path == nil || edgeIdx < 0 || edgeIdx >= len(path.Edges) || nameFn == nil {
		return ""
	}
	return strings.TrimSpace(nameFn(path.Edges[edgeIdx].NameIdx))
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

// unnamedLinkChain detects a sequence of consecutive unnamed link edges
// starting at startEdge, used to suppress ramp/slip-road noise in instructions.
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

// isUnnamedLink returns true for edges that are unnamed slip-road / ramp
// segments (_link highway classes with no street name assigned).
// These edges are collapsed silently in instruction generation.
func isUnnamedLink(edge Edge) bool {
	return edge.NameIdx == 0 && edge.Kind.IsLink()
}

func instructionText(typ, modifier, street string, distanceKm float64, next maneuver) string {
	target := ""
	if street != "" {
		target = " به " + street
	}
	switch typ {
	case "depart":
		return leadInstructionText("حرکت را شروع کنید", distanceKm, next)
	case "continue":
		return leadInstructionText("مستقیم ادامه دهید", distanceKm, next)
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

func leadInstructionText(prefix string, distanceKm float64, next maneuver) string {
	nextAction := nextActionText(next)
	if nextAction == "" {
		return prefix
	}
	return fmt.Sprintf("%s؛ پس از %s %s", prefix, formatInstructionDistance(distanceKm), nextAction)
}

func nextActionText(next maneuver) string {
	target := ""
	if next.street != "" {
		target = " به " + next.street
	}
	switch next.typ {
	case "turn":
		return persianTurn(next.modifier) + target
	case "uturn":
		return "دور بزنید" + target
	case "arrive":
		return "به مقصد می‌رسید"
	default:
		return ""
	}
}

func formatInstructionDistance(distanceKm float64) string {
	if distanceKm < 1 {
		meters := int(math.Round(distanceKm * 1000))
		if meters < 1 {
			meters = 1
		}
		return fmt.Sprintf("%d متر", meters)
	}
	if distanceKm < 10 {
		return fmt.Sprintf("%.1f کیلومتر", utils.Round(distanceKm, 1))
	}
	return fmt.Sprintf("%.0f کیلومتر", math.Round(distanceKm))
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
