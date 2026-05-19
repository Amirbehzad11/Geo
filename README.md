# geo-service

[![CI](https://github.com/Amirbehzad11/geo-service/actions/workflows/ci.yml/badge.svg)](https://github.com/Amirbehzad11/geo-service/actions/workflows/ci.yml)
[![Go Version](https://img.shields.io/badge/Go-1.23-00ADD8?logo=go)](https://go.dev/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/Amirbehzad11/geo-service)](https://goreportcard.com/report/github.com/Amirbehzad11/geo-service)

A production-ready geospatial routing microservice written in Go. It computes optimal routes over a PostGIS-backed OpenStreetMap road graph using a custom A\* + Yen's k-shortest-paths engine, processes live GPS streams with noise smoothing and deviation detection, and broadcasts real-time events to WebSocket clients via Redis Pub/Sub.

---

## Features

- **Custom routing engine** — A\* pathfinding with an admissible Haversine heuristic; Yen's algorithm for up to 3 alternative routes
- **5 transport modes** — `car`, `motorcycle`, `bus`, `walking`, `airplane`
- **Live GPS pipeline** — EMA smoothing, speed computation, cross-track deviation detection
- **WebSocket events** — real-time `location.updated` and `deviation.detected` streams per trip
- **Prometheus metrics** — request latency histograms, route counters, active WebSocket gauge
- **Swagger UI** — interactive API docs at `/docs/index.html`
- **Redis caching** — routes cached for 1 hour; GPS state cached for 24 hours
- **PostGIS persistence** — partitioned GPS location history + route audit records
- **Structured JSON logging** — powered by Go stdlib `slog`
- **Configurable CORS & API key auth**
- **Docker Compose** — one command to run the full stack

---

## Architecture

```
HTTP client / WebSocket client
           |
    geo-service (:8080)
     |        |        |
  Redis    PostGIS    Redis
  live     roads +   Pub/Sub
  state    history   events
                       |
               WebSocket clients
```

The routing engine loads the OSM road graph from PostGIS at startup into memory. All routing decisions are made in-process with no external calls. Redis holds transient per-trip state and acts as the Pub/Sub broker for the WebSocket hub.

---

## Quick Start

### Prerequisites

- Docker & Docker Compose
- An OSM data file (`.osm.pbf`) for your region — download from [Geofabrik](https://download.geofabrik.de/)

### 1. Start the infrastructure

```bash
git clone https://github.com/Amirbehzad11/geo-service.git
cd geo-service
docker compose up -d postgres redis
```

### 2. Import OSM road data

```bash
go build -o osm2postgis ./cmd/osm2postgis

# Example: import a bounding box over Tehran
./osm2postgis \
  -in data/iran-latest.osm.pbf \
  -dsn "host=localhost port=5433 user=geo password=geo_secret dbname=geodb sslmode=disable" \
  -bbox "35.5,51.1,35.9,51.7" \
  -truncate=true
```

Bounding box format: `lat_min,lng_min,lat_max,lng_max`.

### 3. Start the service

```bash
docker compose up --build geo-service
```

The service is available at `http://localhost:8080`.

- Swagger UI: `http://localhost:8080/docs/index.html`
- Prometheus metrics: `http://localhost:8080/metrics`
- Health check: `http://localhost:8080/health`

---

## API Reference

All endpoints return a consistent JSON envelope:

```json
// Success
{ "success": true, "data": { ... } }

// Error
{ "success": false, "error": { "code": "VALIDATION_ERROR", "message": "..." } }
```

For the full interactive docs open `/docs/index.html` after starting the service.

### `POST /route` — Calculate a route

```bash
curl -s -X POST http://localhost:8080/route \
  -H "Content-Type: application/json" \
  -d '{
    "trip_id": 1,
    "start_lat": 35.6892,
    "start_lng": 51.3890,
    "end_lat": 35.8042,
    "end_lng": 51.4307,
    "mode": "car",
    "alternatives": 2
  }' | jq .
```

Returns: distance (km), duration (min), Google Encoded Polyline geometry, and up to 2 alternative routes.

### `POST /gps/update` — Process a GPS update

```bash
curl -s -X POST http://localhost:8080/gps/update \
  -H "Content-Type: application/json" \
  -d '{
    "trip_id": 1,
    "lat": 35.6960,
    "lng": 51.4060,
    "timestamp": 1715000100
  }' | jq .
```

Returns the smoothed location state including computed speed and deviation from the planned route.

### `GET /gps/trip/:id/location` — Get latest trip location

```bash
curl -s http://localhost:8080/gps/trip/1/location | jq .
```

### `GET /ws/trip/:id` — WebSocket live events

Connect with any WebSocket client to receive `location.updated` and `deviation.detected` events in real time:

```js
const ws = new WebSocket("ws://localhost:8080/ws/trip/1");
ws.onmessage = (e) => console.log(JSON.parse(e.data));
```

### `GET /ws/shipments/nearby` - Nearby shipment origins

Connect a WebSocket client, then send the passenger coordinate. The service queries the configured Laravel database directly and returns shipment rows whose origin lat/lng are within the configured radius, defaulting to 10 km.

```js
const ws = new WebSocket("ws://localhost:8080/ws/shipments/nearby");

ws.onopen = () => {
  ws.send(JSON.stringify({
    lat: 35.6892,
    lng: 51.3890
  }));
};

ws.onmessage = (e) => console.log(JSON.parse(e.data));
```

Example response:

```json
{
  "type": "shipment.nearby",
  "timestamp_ms": 1779100000000,
  "query": { "lat": 35.6892, "lng": 51.389, "radius_km": 10, "limit": 100 },
  "count": 1,
  "shipments": [
    {
      "id": 42,
      "origin_lat": "35.6900",
      "origin_lng": "51.3900",
      "distance_km": 0.12
    }
  ]
}
```

You can also pass one lookup in the URL:

```js
new WebSocket("ws://localhost:8080/ws/shipments/nearby?lat=35.6892&lng=51.3890");
```

### `GET /health` — Health check

```bash
curl -s http://localhost:8080/health | jq .
```

---

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `8080` | HTTP listen port |
| `REDIS_ADDR` | `localhost:6379` | Redis address |
| `REDIS_PASSWORD` | — | Redis password |
| `REDIS_DB` | `0` | Redis database index |
| `POSTGRES_DSN` | **required** | PostGIS connection string |
| `ROAD_GRAPH_REGIONS` | — | Comma-separated region table suffixes |
| `ROAD_GRAPH_LOAD_TIMEOUT_SEC` | `180` | Startup graph load timeout (seconds) |
| `AVG_SPEED_KMH` | `40` | Speed used for Haversine fallback routes |
| `GPS_RATE_LIMIT_MS` | `3000` | Minimum milliseconds between GPS updates per trip |
| `DEVIATION_THRESH_KM` | `0.05` | Distance from route (km) that triggers a deviation event |
| `BATCH_SIZE` | `500` | PostGIS GPS insert batch size |
| `BATCH_FLUSH_SEC` | `2` | Batch flush interval (seconds) |
| `SHIPMENT_DB_DRIVER` | `mysql` | Direct shipment DB driver: `mysql`, `mariadb`, `postgres`, `postgresql`, or `pgx` |
| `SHIPMENT_DB_DSN` | — | Laravel database DSN. MySQL example: `user:pass@tcp(host.docker.internal:3306)/laravel_db?parseTime=true` |
| `SHIPMENT_TABLE` | `shipment` | Shipment table name in the Laravel database |
| `SHIPMENT_ORIGIN_LAT_COLUMN` | `origin_lat` | Shipment origin latitude column |
| `SHIPMENT_ORIGIN_LNG_COLUMN` | `origin_lng` | Shipment origin longitude column |
| `SHIPMENT_SEARCH_RADIUS_KM` | `10` | Default nearby shipment search radius |
| `SHIPMENT_SEARCH_LIMIT` | `100` | Default max rows returned over WebSocket; capped at 500 |
| `CORS_ALLOWED_ORIGINS` | `*` | Comma-separated allowed origins for CORS |
| `API_KEY_ENABLED` | `false` | Set to `true` to require `X-API-Key` header |
| `API_KEY` | — | Expected API key value (used when `API_KEY_ENABLED=true`) |
| `GIN_MODE` | `release` | Gin framework mode (`release` or `debug`) |

---

## Algorithm Deep Dive

### Routing Engine

Routes are computed in two phases:

**1. Graph construction** (at startup)

Road segments are loaded from PostGIS (`road_segments` table), which is populated by the `osm2postgis` importer from OpenStreetMap PBF/XML files. Each segment carries mode-specific access flags (`car_allowed`, `foot_allowed`, etc.) and a speed in km/h derived from OSM `highway` type and `maxspeed` tags.

The in-memory graph uses a **spatial grid index** (0.02° cells ≈ 2.2 km) so that snapping a GPS coordinate to the nearest routable node is an O(1) lookup rather than a full graph scan.

**2. Pathfinding**

The primary route is found with **A\*** ([`internal/routing/astar.go`](internal/routing/astar.go)):

```
g(n) = cost from start to n (travel time in minutes)
h(n) = haversine(n, goal) / max_road_speed   ← admissible, never overestimates
f(n) = g(n) + h(n)
```

Using travel time as cost (instead of distance) means the algorithm naturally produces the *fastest* route, not the shortest.

Alternative routes are produced with **Yen's k-shortest paths** ([`internal/routing/yen.go`](internal/routing/yen.go)): for each iteration, spur paths are computed from every node in the previously accepted path, with the overlapping prefix edges removed to force divergence. The top-k candidates are sorted by duration.

If either endpoint is more than 0.3 km from the nearest graph node, the engine falls back to a **Haversine straight-line** estimate at the mode's average speed.

### GPS Processing Pipeline

Each `POST /gps/update` call goes through this pipeline ([`internal/service/gps.go`](internal/service/gps.go)):

```
1. Rate limit    Redis SET NX with TTL = GPS_RATE_LIMIT_MS
2. EMA smooth    lat_s = α·lat_raw + (1−α)·lat_prev   (α = 0.75)
                 lng_s = α·lng_raw + (1−α)·lng_prev
3. Speed         v = haversine(prev, curr) / Δt
4. Deviation     d = min cross-track distance from curr to all segments of the planned route polyline
                 if d > DEVIATION_THRESH_KM → publish deviation.detected event
5. Persist       write to Redis (24 h TTL); async batch insert to PostGIS
6. Broadcast     publish location.updated to Redis Pub/Sub channel geo:events:trip:{id}
```

The **cross-track distance** formula ([`internal/utils/geo.go`](internal/utils/geo.go)) computes the perpendicular distance from a point to a great-circle segment, giving accurate deviation even on curved roads.

---

## Importing OSM Data

The `cmd/osm2postgis` tool reads OSM XML or PBF files and inserts road segments into PostGIS:

```bash
# Build the importer
go build -o osm2postgis ./cmd/osm2postgis

# Import with a bounding box filter
./osm2postgis \
  -in  data/region.osm.pbf \
  -dsn "host=localhost port=5433 user=geo password=geo_secret dbname=geodb sslmode=disable" \
  -bbox "lat_min,lng_min,lat_max,lng_max" \
  -region my_region \     # optional: writes to road_segments_my_region
  -truncate=true
```

The importer respects `highway` type, `maxspeed`, `oneway`, and `access` tags.

---

## Running Tests & Load Test

```bash
# Unit tests
make test

# Load test — 50 concurrent users, 30 seconds, mixed scenario
make loadtest
```

The load test tool ([`cmd/loadtest`](cmd/loadtest)) reports p50/p90/p95/p99 latency percentiles and requests/second.

---

## Project Structure

```
cmd/server/main.go          Entry point and dependency wiring
cmd/osm2postgis/main.go     OSM to PostGIS road importer
cmd/loadtest/main.go        Concurrent load testing tool
config/config.go            Environment-based configuration
internal/cache              Redis client and key helpers
internal/events             Redis Pub/Sub event bus
internal/handler            HTTP and WebSocket handlers
internal/middleware         CORS, structured logging, API key, Prometheus
internal/model              Request and response models
internal/response           Unified JSON response envelope
internal/routing            Graph, A*, Yen's algorithm, polyline encoding
internal/service            Route calculation and GPS processing
internal/storage            PostGIS schema, repositories, batch writer
internal/utils              Haversine, cross-track distance, EMA smoother
internal/ws                 WebSocket hub and per-connection clients
docs/                       Auto-generated Swagger/OpenAPI specs
```

---

## Contributing

1. Fork the repository and create a feature branch.
2. Run `make test` and `make lint` before opening a PR.
3. Open a pull request — all CI checks must pass.

---

## License

[MIT](LICENSE) © 2025 Amirbehzad11
