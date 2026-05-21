package routing

// HighwayKind is a compact encoding of the OSM highway tag.
// Using uint8 instead of string saves ~24 bytes per edge (16-byte string header
// + ~8-byte heap body), totalling ~1.2 GB on a 50M-edge Iran road graph.
type HighwayKind uint8

const (
	HWUnknown       HighwayKind = iota // unknown / empty
	HWMotorway                         // motorway
	HWMotorwayLink                     // motorway_link
	HWTrunk                            // trunk
	HWTrunkLink                        // trunk_link
	HWPrimary                          // primary
	HWPrimaryLink                      // primary_link
	HWSecondary                        // secondary
	HWSecondaryLink                    // secondary_link
	HWTertiary                         // tertiary
	HWTertiaryLink                     // tertiary_link
	HWUnclassified                     // unclassified
	HWResidential                      // residential
	HWLivingStreet                     // living_street
	HWService                          // service
	HWFootway                          // footway
	HWPedestrian                       // pedestrian
	HWPath                             // path
	HWSteps                            // steps
	HWCorridor                         // corridor
	HWCrossing                         // crossing
	HWSidewalk                         // sidewalk
	HWPlatform                         // platform
	hwKindCount                        // sentinel — must stay last
)

// hwKindStr maps each HighwayKind to its canonical OSM highway string.
var hwKindStr = [hwKindCount]string{
	HWUnknown:       "",
	HWMotorway:      "motorway",
	HWMotorwayLink:  "motorway_link",
	HWTrunk:         "trunk",
	HWTrunkLink:     "trunk_link",
	HWPrimary:       "primary",
	HWPrimaryLink:   "primary_link",
	HWSecondary:     "secondary",
	HWSecondaryLink: "secondary_link",
	HWTertiary:      "tertiary",
	HWTertiaryLink:  "tertiary_link",
	HWUnclassified:  "unclassified",
	HWResidential:   "residential",
	HWLivingStreet:  "living_street",
	HWService:       "service",
	HWFootway:       "footway",
	HWPedestrian:    "pedestrian",
	HWPath:          "path",
	HWSteps:         "steps",
	HWCorridor:      "corridor",
	HWCrossing:      "crossing",
	HWSidewalk:      "sidewalk",
	HWPlatform:      "platform",
}

var parseHighwayKindMap map[string]HighwayKind

func init() {
	parseHighwayKindMap = make(map[string]HighwayKind, int(hwKindCount))
	for i, s := range hwKindStr {
		if s != "" {
			parseHighwayKindMap[s] = HighwayKind(i)
		}
	}
}

// ParseHighwayKind converts an OSM highway string to a HighwayKind.
// Unknown strings map to HWUnknown (0).
func ParseHighwayKind(s string) HighwayKind {
	if k, ok := parseHighwayKindMap[s]; ok {
		return k
	}
	return HWUnknown
}

// String returns the canonical OSM highway string for h.
func (h HighwayKind) String() string {
	if int(h) < len(hwKindStr) {
		return hwKindStr[h]
	}
	return ""
}

// IsLink returns true for ramp/slip-road classes (_link suffix).
func (h HighwayKind) IsLink() bool {
	switch h {
	case HWMotorwayLink, HWTrunkLink, HWPrimaryLink, HWSecondaryLink, HWTertiaryLink:
		return true
	}
	return false
}

// IsPedestrian returns true for road classes primarily intended for foot traffic.
func (h HighwayKind) IsPedestrian() bool {
	switch h {
	case HWFootway, HWPedestrian, HWPath, HWSteps, HWCorridor, HWCrossing, HWSidewalk, HWPlatform:
		return true
	}
	return false
}

// BlocksWalking returns true for high-speed carriageways where pedestrian
// access is prohibited by law (motorway, motorway_link, trunk, trunk_link).
func (h HighwayKind) BlocksWalking() bool {
	switch h {
	case HWMotorway, HWMotorwayLink, HWTrunk, HWTrunkLink:
		return true
	}
	return false
}

// AllowsPedestriansAgainstFlow returns true for road classes where pedestrians
// may legally walk against a vehicle one-way restriction.
// Purely-pedestrian infrastructure (footway, steps, etc.) returns false because
// those roads already carry explicit direction data in OSM.
func (h HighwayKind) AllowsPedestriansAgainstFlow() bool {
	return !h.IsPedestrian()
}
