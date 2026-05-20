// Package osrmclient provides a minimal HTTP client for a self-hosted OSRM instance.
// It maps routing requests to the OSRM Route API and converts the response,
// including OSRM maneuver steps, into the service's model types with Persian text.
package osrmclient

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"geo-service/internal/model"
	"geo-service/internal/routing"
	"geo-service/internal/utils"
)

// Client calls a self-hosted OSRM routing engine over HTTP.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// New returns a Client pointing at the given OSRM base URL (e.g. "http://osrm:5000").
// timeout is applied to every HTTP call; zero falls back to 10 s.
func New(baseURL string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{Timeout: timeout},
	}
}

// osrmProfile maps a TransportMode to an OSRM profile name.
// Returns ("", false) for modes that OSRM cannot handle.
func osrmProfile(mode routing.TransportMode) (string, bool) {
	switch mode {
	case routing.ModeCar, routing.ModeMotorcycle, routing.ModeBus:
		return "driving", true
	case routing.ModeWalking:
		return "foot", true
	default:
		return "", false
	}
}

// Route calls the OSRM Route API and returns a RouteResponse.
// An error is returned when:
//   - mode is not supported by OSRM (e.g. airplane → caller should use internal fallback)
//   - the HTTP request fails or times out (ctx deadline respected)
//   - OSRM returns a non-Ok code or no routes
func (c *Client) Route(
	ctx context.Context,
	mode routing.TransportMode,
	startLat, startLng, endLat, endLng float64,
) (*model.RouteResponse, error) {
	profile, ok := osrmProfile(mode)
	if !ok {
		return nil, fmt.Errorf("osrm: mode %q is not supported; use internal engine for this mode", mode)
	}

	// OSRM coordinate order is lng,lat (GeoJSON convention).
	url := fmt.Sprintf(
		"%s/route/v1/%s/%.6f,%.6f;%.6f,%.6f?overview=full&geometries=polyline&steps=true&alternatives=false",
		c.baseURL, profile,
		startLng, startLat,
		endLng, endLat,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("osrm: build request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("osrm: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("osrm: HTTP %d", resp.StatusCode)
	}

	var parsed osrmResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("osrm: decode response: %w", err)
	}
	if parsed.Code != "Ok" {
		return nil, fmt.Errorf("osrm: routing code %q", parsed.Code)
	}
	if len(parsed.Routes) == 0 {
		return nil, fmt.Errorf("osrm: no routes in response")
	}

	return convertRoute(mode, &parsed.Routes[0]), nil
}

// ---- OSRM JSON types --------------------------------------------------------

type osrmResponse struct {
	Code   string      `json:"code"`
	Routes []osrmRoute `json:"routes"`
}

type osrmRoute struct {
	Distance float64   `json:"distance"` // metres
	Duration float64   `json:"duration"` // seconds
	Geometry string    `json:"geometry"` // Google-encoded polyline
	Legs     []osrmLeg `json:"legs"`
}

type osrmLeg struct {
	Steps []osrmStep `json:"steps"`
}

type osrmStep struct {
	Distance float64      `json:"distance"` // metres
	Duration float64      `json:"duration"` // seconds
	Name     string       `json:"name"`     // street name (may be empty or an OSM class)
	Maneuver osrmManeuver `json:"maneuver"`
}

type osrmManeuver struct {
	Type     string     `json:"type"`
	Modifier string     `json:"modifier"`
	Location [2]float64 `json:"location"` // [lng, lat]
}

// ---- conversion -------------------------------------------------------------

func convertRoute(mode routing.TransportMode, r *osrmRoute) *model.RouteResponse {
	distKm := utils.Round(r.Distance/1000.0, 3)
	durMin := utils.Round(r.Duration/60.0, 2)
	instructions := convertSteps(r.Legs)

	primary := model.RouteOption{
		ID:           1,
		Mode:         string(mode),
		IsPrimary:    true,
		DistanceKm:   distKm,
		DurationMin:  durMin,
		Polyline:     r.Geometry,
		Instructions: instructions,
	}
	return &model.RouteResponse{
		Distance: distKm,
		Duration: durMin,
		Polyline: r.Geometry,
		Mode:     string(mode),
		Primary:  primary,
		Routes:   []model.RouteOption{primary},
	}
}

func convertSteps(legs []osrmLeg) []model.RouteInstruction {
	var out []model.RouteInstruction
	idx := 0
	for _, leg := range legs {
		for i, step := range leg.Steps {
			typ := step.Maneuver.Type
			mod := normalizeModifier(step.Maneuver.Modifier)
			name := cleanStreetName(step.Name)
			distKm := step.Distance / 1000.0
			durMin := step.Duration / 60.0

			// Peek at the next step to build "after X metres, turn …" text.
			var nextTyp, nextMod, nextName string
			if i+1 < len(leg.Steps) {
				ns := leg.Steps[i+1]
				nextTyp = ns.Maneuver.Type
				nextMod = normalizeModifier(ns.Maneuver.Modifier)
				nextName = cleanStreetName(ns.Name)
			}

			out = append(out, model.RouteInstruction{
				Index:       idx,
				Type:        typ,
				Modifier:    mod,
				Text:        stepText(typ, mod, name, distKm, nextTyp, nextMod, nextName),
				DistanceKm:  utils.Round(distKm, 3),
				DurationMin: utils.Round(durMin, 2),
				Location: model.RoutePoint{
					Lat: step.Maneuver.Location[1],
					Lng: step.Maneuver.Location[0],
				},
				StreetName: name,
			})
			idx++
		}
	}
	return out
}

// normalizeModifier converts OSRM space-separated modifiers to underscore form
// so they match the internal engine convention ("slight left" → "slight_left").
func normalizeModifier(m string) string {
	return strings.ReplaceAll(strings.TrimSpace(m), " ", "_")
}

// cleanStreetName removes raw OSM highway-class strings that OSRM sometimes
// emits as step names when no actual name is tagged.
var osmHighwayClasses = map[string]bool{
	"motorway": true, "motorway_link": true,
	"trunk": true, "trunk_link": true,
	"primary": true, "primary_link": true,
	"secondary": true, "secondary_link": true,
	"tertiary": true, "tertiary_link": true,
	"unclassified": true, "residential": true,
	"service": true, "living_street": true,
	"footway": true, "pedestrian": true,
	"path": true, "steps": true,
	"track": true, "cycleway": true,
}

func cleanStreetName(name string) string {
	name = strings.TrimSpace(name)
	// OSRM emits OSM highway-class values in lowercase when no real street
	// name is tagged (e.g. "residential", "primary_link"). Capitalized names
	// like "Footway Lane" are real street names and must be preserved.
	if osmHighwayClasses[name] {
		return ""
	}
	return name
}

// ---- Persian instruction text -----------------------------------------------

func stepText(typ, modifier, street string, distKm float64, nextTyp, nextMod, nextName string) string {
	dest := ""
	if street != "" {
		dest = " به " + street
	}
	switch typ {
	case "depart":
		return leadText("حرکت را شروع کنید", distKm, nextTyp, nextMod, nextName)
	case "arrive":
		return "به مقصد رسیدید"
	case "continue", "new name":
		return leadText("مستقیم ادامه دهید", distKm, nextTyp, nextMod, nextName)
	case "turn", "end of road":
		return persianTurn(modifier) + dest
	case "fork":
		if strings.Contains(modifier, "right") {
			return "در دوراهی به راست بروید" + dest
		}
		if strings.Contains(modifier, "left") {
			return "در دوراهی به چپ بروید" + dest
		}
		return "مستقیم ادامه دهید" + dest
	case "merge":
		return "وارد جاده شوید" + dest
	case "on ramp":
		return "به آزادراه وارد شوید" + dest
	case "off ramp":
		return "از آزادراه خارج شوید" + dest
	case "roundabout", "rotary":
		return "وارد میدان شوید" + dest
	case "exit roundabout", "exit rotary":
		return "از میدان خارج شوید" + dest
	case "use lane":
		return "مسیر صحیح را انتخاب کنید" + dest
	default:
		if modifier == "uturn" {
			return "دور بزنید" + dest
		}
		return "ادامه دهید" + dest
	}
}

// leadText builds "prefix; پس از X متر/کیلومتر nextAction".
func leadText(prefix string, distKm float64, nextTyp, nextMod, nextName string) string {
	action := nextActionText(nextTyp, nextMod, nextName)
	if action == "" {
		return prefix
	}
	return fmt.Sprintf("%s؛ پس از %s %s", prefix, fmtDist(distKm), action)
}

func nextActionText(typ, modifier, name string) string {
	dest := ""
	if name != "" {
		dest = " به " + name
	}
	switch typ {
	case "turn", "end of road":
		return persianTurn(modifier) + dest
	case "arrive":
		return "به مقصد می‌رسید"
	case "roundabout", "rotary":
		return "وارد میدان شوید" + dest
	case "fork":
		if strings.Contains(modifier, "right") {
			return "در دوراهی به راست بروید" + dest
		}
		return "در دوراهی به چپ بروید" + dest
	default:
		return ""
	}
}

func persianTurn(modifier string) string {
	switch modifier {
	case "slight_left", "slight left":
		return "کمی به چپ بپیچید"
	case "left":
		return "به چپ بپیچید"
	case "sharp_left", "sharp left":
		return "تند به چپ بپیچید"
	case "slight_right", "slight right":
		return "کمی به راست بپیچید"
	case "right":
		return "به راست بپیچید"
	case "sharp_right", "sharp right":
		return "تند به راست بپیچید"
	case "uturn":
		return "دور بزنید"
	default:
		return "مستقیم ادامه دهید"
	}
}

func fmtDist(distKm float64) string {
	if distKm < 1.0 {
		m := int(math.Round(distKm * 1000))
		if m < 1 {
			m = 1
		}
		return fmt.Sprintf("%d متر", m)
	}
	if distKm < 10.0 {
		return fmt.Sprintf("%.1f کیلومتر", utils.Round(distKm, 1))
	}
	return fmt.Sprintf("%.0f کیلومتر", math.Round(distKm))
}
