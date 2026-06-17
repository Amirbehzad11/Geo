# geo-service

[![CI](https://github.com/Amirbehzad11/geo-service/actions/workflows/ci.yml/badge.svg)](https://github.com/Amirbehzad11/geo-service/actions/workflows/ci.yml)
[![Go Version](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go)](https://go.dev/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

**geo-service** is a production-ready geospatial microservice for logistics platforms. It provides route calculation, live GPS tracking, real-time WebSocket events, nearby shipment discovery against a Laravel database, and driver location search — all in a single Go binary.

In the **MrchamedonBeta** stack, this service acts as the GIS layer: Laravel handles auth and business logic; geo-service handles maps, routing, tracking, and live discovery.

> **مستندات فارسی:** [docs/README.fa.md](docs/README.fa.md)

---

## Table of Contents

- [Features](#features)
- [Architecture](#architecture)
- [Tech Stack](#tech-stack)
- [Quick Start](#quick-start)
- [Laravel Integration](#laravel-integration-mrchamedonbeta)
- [Authentication & Authorization](#authentication--authorization)
- [API Reference](#api-reference)
- [WebSocket Guide](#websocket-guide)
- [Configuration](#configuration)
- [Local Simulators](#local-simulators)
- [Routing Backends](#routing-backends)
- [Transport Modes](#transport-modes)
- [OSM Data Import](#osm-data-import)
- [Development](#development)
- [Project Structure](#project-structure)
- [License](#license)

---

## Features

| Area | Capabilities |
|------|-------------|
| **Routing** | OSRM (MLD) primary backend with automatic fallback to in-process **A\*** + **Yen's k-shortest paths** |
| **Transport modes** | `car`, `motorcycle`, `bus`, `walking`, `train`, `public_transport`, `airplane` |
| **Multi-waypoint** | `POST /route/waypoints` with greedy nearest-neighbor ordering |
| **GPS pipeline** | EMA smoothing, speed, cross-track deviation detection, Redis state, PostGIS batch history |
| **WebSocket** | Live trip events; nearby shipments (passenger); nearby drivers (sender); structured + legacy JSON |
| **Laravel DB** | Read-only PostGIS queries on `shipments` (origin/destination geometry, images, vehicle types) |
| **Security** | JWT (Laravel `tymon/jwt-auth`), optional API key, CORS/Origin checks, rate limits, body size cap |
| **Object-level auth** | Trip ownership and shipment-linked access enforced against Laravel schema |
| **Observability** | Structured JSON logs (`slog`), Prometheus `/metrics`, Swagger UI at `/docs/index.html` |

---

## Architecture

```
                    ┌─────────────────────────────────────────┐
  Mobile / Web      │           geo-service (:8080)           │
  Laravel API  ────►│  Gin HTTP  +  WebSocket  +  Middleware  │
                    └──────┬──────────┬──────────┬────────────┘
                           │          │          │
              ┌────────────┘          │          └──────────────┐
              ▼                       ▼                         ▼
        ┌──────────┐           ┌──────────┐            ┌──────────────┐
        │  Redis   │           │ PostGIS  │            │ Laravel DB   │
        │  cache   │           │ road     │            │ (read-only)  │
        │  Pub/Sub │           │ graph +  │            │ shipments,   │
        │  GEO     │           │ GPS hist │            │ trips, authz │
        └──────────┘           └──────────┘            └──────────────┘
              │                       │
              │                       ▼
              │                ┌──────────┐
              └───────────────►│   OSRM   │  (optional primary backend)
                               │  :5000   │
                               └──────────┘
```

**Request flow (routing):** Client → JWT middleware → route handler → OSRM or internal engine → Redis cache → JSON response.

**Request flow (GPS):** Client → ownership check → GPS service → Redis state → Redis Pub/Sub → WebSocket subscribers + async PostGIS batch writer.

**Shipment nearby:** WebSocket client → JWT upgrade → PostGIS `ST_DWithin` on Laravel `shipments.start_location` → enriched rows (vehicles, images, content type).

---

## Tech Stack

- **Language:** Go 1.25
- **HTTP:** Gin
- **WebSocket:** gorilla/websocket
- **Cache / events:** Redis 7
- **Spatial DB:** PostGIS 16
- **Routing:** Custom A\* / Yen + optional OSRM MLD
- **Auth:** `github.com/golang-jwt/jwt/v5` (HMAC, Laravel-compatible claims)
- **Docs:** Swaggo OpenAPI

---

## Quick Start

### Prerequisites

- Docker & Docker Compose
- (Optional) Iran OSM extract from [Geofabrik](https://download.geofabrik.de/asia/iran-latest.osm.pbf) for routing data

### 1. Clone and configure

```bash
git clone https://github.com/Amirbehzad11/geo-service.git
cd geo-service
cp .env.example .env
# Edit JWT_SECRET, SHIPMENT_DB_DSN, CORS_ALLOWED_ORIGINS
```

### 2. Start infrastructure

```bash
docker compose up -d postgres redis
```

PostGIS is exposed on host port **5433** (`geo` / `geo_secret` / `geodb`).

### 3. Import road data (internal routing)

```bash
go build -o osm2postgis ./cmd/osm2postgis

./osm2postgis \
  -in data/iran-latest.osm.pbf \
  -dsn "host=localhost port=5433 user=geo password=geo_secret dbname=geodb sslmode=disable" \
  -bbox "35.5,51.1,35.9,51.7" \
  -truncate=true
```

Bounding box format: `lat_min,lng_min,lat_max,lng_max`.

### 4. Start geo-service

```bash
docker compose up -d --build geo-service
```

| Endpoint | URL |
|----------|-----|
| API base | `http://localhost:8080` |
| Swagger UI | `http://localhost:8080/docs/index.html` |
| Health | `http://localhost:8080/health` |
| Prometheus | `http://localhost:8080/metrics` |

### 5. (Optional) Start local simulators

For development without real mobile clients:

```bash
# Driver in Redis GEO (sender search) + passenger GPS along a trip
docker compose --profile sim up -d --build sim-drivers sim-passenger
```

See [Local Simulators](#local-simulators) for flags and troubleshooting.

### 6. OSRM mode (production, large graphs)

```bash
# One-time OSRM preprocessing — see docker-compose.yml comments
mkdir -p ./data
# wget -O ./data/map.osm.pbf https://download.geofabrik.de/asia/iran-latest.osm.pbf
# docker run ... osrm-extract / osrm-partition / osrm-customize

COMPOSE_PROFILES=osrm ROUTING_BACKEND=osrm INTERNAL_GRAPH_ENABLED=false \
  docker compose up -d osrm geo-service
```

---

## Laravel Integration (MrchamedonBeta)

geo-service connects **read-only** to the same PostgreSQL database as Laravel for:

1. **Nearby shipments** — `GET /ws/shipments/nearby`
2. **Trip authorization** — validates `trips.user_id` and shipment sender/receiver links
3. **Enrichment** — vehicle types, content images, `shipment_images`

### Required Laravel schema assumptions

| Table / column | Usage |
|----------------|-------|
| `shipments.start_location` | PostGIS geometry — origin for radius search |
| `shipments.end_location` | PostGIS geometry — destination coordinates in response |
| `trips.id`, `trips.user_id` | Trip ownership for write operations |
| `shippings.trip_id`, `shippings.shipment_id` | Linked parties can subscribe to trip WebSocket |
| `vehicle_types` | Labels/titles for `vehicles[]` in responses |
| `shipment_images` | Optional `images[]` array per shipment |

### Docker networking

When Laravel runs in Docker on the same host:

```env
SHIPMENT_DB_DRIVER=postgres
SHIPMENT_DB_DSN=host=host.docker.internal port=5432 user=YOUR_USER password=YOUR_PASS dbname=mr_chamedon sslmode=disable
JWT_SECRET=<same value as Laravel JWT_SECRET>
CORS_ALLOWED_ORIGINS=http://localhost,http://localhost:5173
```

`JWT_SECRET` **must** match Laravel's `tymon/jwt-auth` secret. Tokens carry `user_id` or `sub` claim.

### Frontend auth pattern

**HTTP:**

```http
Authorization: Bearer <laravel_jwt_token>
```

**WebSocket** (browsers cannot set `Authorization` on upgrade):

```js
const token = localStorage.getItem('access_token');
const ws = new WebSocket('ws://localhost:8080/ws/trip/42', ['bearer', token]);
```

The server reads `Sec-WebSocket-Protocol: bearer, <token>`.

---

## Authentication & Authorization

### Credential modes

| Method | Header / protocol | Use case |
|--------|-------------------|----------|
| **JWT** (default) | `Authorization: Bearer …` or WS subprotocol | Mobile app, Laravel frontend |
| **API key** | `X-API-Key: …` | Internal service-to-service (bypasses user-level checks) |

Enable via `JWT_AUTH_ENABLED=true` and/or `API_KEY_ENABLED=true`. At least one should be enabled in production.

> **Note:** Laravel **Reverb/Pusher** (`ws://host:8084/app/...`) is a separate service. geo-service WebSockets live on port **8080** at `/ws/trip/:id` and `/ws/shipments/nearby`.

### Object-level access control

When `SHIPMENT_DB_DSN` is set, geo-service enforces Laravel ownership:

| Endpoint | Rule |
|----------|------|
| `POST /route`, `POST /route/waypoints` (with `trip_id`) | JWT user must own the trip |
| `POST /gps/update` | JWT user must own the trip |
| `GET /gps/trip/:id/location` | Owner or linked shipment sender/receiver |
| `GET /ws/trip/:id` | Owner or linked shipment sender/receiver |
| `POST /driver-location` | JWT `user_id` must equal `driver_id` |

API-key requests skip per-user checks (intended for trusted backends only).

### Edge protection

- Per-IP rate limit (`RATE_LIMIT_PER_MINUTE`, default 300)
- Request body cap (`REQUEST_BODY_LIMIT_BYTES`, default 1 MiB)
- WebSocket read limit (64 KiB) and message rate limit (30/min)
- CORS + explicit WebSocket `Origin` validation

---

## API Reference

All JSON endpoints return a unified envelope:

```json
{ "success": true, "data": { } }
{ "success": false, "error": { "code": "VALIDATION_ERROR", "message": "..." } }
```

Full interactive docs: `/docs/index.html`.

### `POST /route` — Calculate route

```bash
curl -s -X POST http://localhost:8080/route \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "trip_id": 1,
    "start_lat": 35.6892,
    "start_lng": 51.3890,
    "end_lat": 35.8042,
    "end_lng": 51.4307,
    "mode": "car",
    "alternatives": 1
  }'
```

| Field | Description |
|-------|-------------|
| `mode` / `vehicle_type` | Transport mode (aliases supported) |
| `trip_id` | Optional; when set with JWT, ownership is verified |
| `alternatives` | Extra routes (capped by `ROUTING_MAX_ALTERNATIVES`) |

Response includes distance (km), duration (min), Google encoded polyline, turn-by-turn instructions, and optional `legs[]` for multi-modal routes.

### `POST /route/waypoints` — Multi-stop route

```bash
curl -s -X POST http://localhost:8080/route/waypoints \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "trip_id": 1,
    "mode": "car",
    "waypoints": [
      { "lat": 35.6892, "lng": 51.3890, "label": "Tehran" },
      { "lat": 32.6539, "lng": 51.6660, "label": "Isfahan" },
      { "lat": 36.2605, "lng": 59.6168, "label": "Mashhad" }
    ]
  }'
```

Waypoints after the first are reordered by nearest-neighbor before routing.

### `POST /gps/update` — Process GPS fix

```bash
curl -s -X POST http://localhost:8080/gps/update \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "trip_id": 1,
    "lat": 35.6960,
    "lng": 51.4060,
    "timestamp": 1715000100
  }'
```

Pipeline: rate limit → EMA smooth (α=0.75) → speed → deviation check → Redis → Pub/Sub broadcast.

### `GET /gps/trip/:id/location` — Latest position

```bash
curl -s http://localhost:8080/gps/trip/1/location \
  -H "Authorization: Bearer $TOKEN"
```

### `POST /driver-location` — Update driver position

```bash
curl -s -X POST http://localhost:8080/driver-location \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{ "driver_id": "16", "lat": 35.70, "lng": 51.40 }'
```

Stored in Redis GEO (`DRIVER_GEO_KEY`) for sender-side nearby passenger search.

### `GET /health` — Health check

```bash
curl -s http://localhost:8080/health
```

### `GET /metrics` — Prometheus metrics

Request latency histograms, route counters, active WebSocket gauge.

---

## WebSocket Guide

geo-service exposes two WebSocket endpoints on the same port as HTTP (`8080`).

| Endpoint | Purpose | Auth (default) |
|----------|---------|----------------|
| `GET /ws/trip/:id` | Live `location.updated` / `deviation.detected` for one trip | Off (`WEBSOCKET_AUTH_ENABLED=false`) |
| `GET /ws/shipments/nearby` | Nearby shipments (passenger) or drivers (sender) | On (`WS_SHIPMENT_AUTH_REQUIRED=true`) |

### Development-friendly auth (no frontend changes)

For local testing against `ws://192.168.x.x:8080` without JWT:

```env
JWT_AUTH_ENABLED=false
WEBSOCKET_AUTH_ENABLED=false
WS_SHIPMENT_AUTH_REQUIRED=false
WS_SHIPMENT_LEGACY_FORMAT=true
WS_SHIPMENT_REQUIRE_TLS=false
CORS_ALLOWED_ORIGINS=*
```

Restart `geo-service` after editing `.env`.

### Token delivery (production)

Browsers cannot set `Authorization` on the WebSocket handshake. Use one of:

- `Sec-WebSocket-Protocol: bearer, <jwt>`
- Query string: `?token=<jwt>`
- Header `Authorization: Bearer <jwt>` (mobile / non-browser clients)

```js
const token = localStorage.getItem('access_token');
const ws = new WebSocket('ws://localhost:8080/ws/trip/42', ['bearer', token]);
```

---

### `GET /ws/trip/:id` — Live trip events

```js
const ws = new WebSocket(`ws://localhost:8080/ws/trip/${tripId}`, ['bearer', token]);

ws.onmessage = (e) => {
  const msg = JSON.parse(e.data);
  // msg.type: "connected" | "location.updated" | "deviation.detected"
};
```

When `WEBSOCKET_AUTH_ENABLED=true`, the user must own the trip or be linked via an active `shippings` row.

Passenger GPS updates are posted to `POST /gps/update` (see sim-passenger below).

---

### `GET /ws/shipments/nearby` — Nearby shipments / drivers

On connect the server sends:

```json
{
  "type": "connected",
  "channel": "shipment.nearby",
  "protocol": {
    "version": 1,
    "messages": ["SUBSCRIBE_LOCATION", "PING"]
  }
}
```

#### Message formats

**Legacy JSON** (enabled when `WS_SHIPMENT_LEGACY_FORMAT=true`) — flat object, no envelope:

```js
// Passenger — find shipments
ws.send(JSON.stringify({
  type: 'passenger',
  lat: 32.646625,
  lng: 51.664761,
  radius_km: 2,
  limit: 50,
  filter_vehicle_types: [1, 2, 3]
}));

// Sender — find nearby drivers (Redis GEO)
ws.send(JSON.stringify({
  type: 'sender',
  lat: 32.646625,
  lng: 51.664761,
  radius_km: 20,
  limit: 100
}));
```

**Structured JSON** (recommended for new clients):

```js
ws.send(JSON.stringify({
  type: 'SUBSCRIBE_LOCATION',
  data: {
    lat: 32.646625,
    lng: 51.664761,
    role: 'sender',       // "passenger" | "sender"
    radius_km: 20,
    limit: 100
  }
}));

// Keep-alive
ws.send(JSON.stringify({ type: 'PING' }));
// → { "type": "PONG", "timestamp_ms": ... }
```

**Query-string bootstrap** (single lookup on connect):

```
/ws/shipments/nearby?type=sender&lat=32.646625&lng=51.664761&radius_km=20
```

#### Responses

**`shipment.nearby`** (passenger):

```json
{
  "type": "shipment.nearby",
  "timestamp_ms": 1779100000000,
  "query": { "lat": 32.646625, "lng": 51.664761, "radius_km": 2, "limit": 100 },
  "count": 1,
  "shipments": [
    {
      "id": 42,
      "start_lat": 32.65,
      "start_lng": 51.67,
      "destination_lat": 32.80,
      "destination_lng": 51.75,
      "distance_km": 0.12,
      "content_image": "https://...",
      "images": ["path/to/image1.jpg"],
      "vehicles": [{ "id": 1, "label": "car", "title": "سواری" }]
    }
  ]
}
```

**`driver.nearby`** (sender):

```json
{
  "type": "driver.nearby",
  "timestamp_ms": 1781681527304,
  "query": { "lat": 32.646625, "lng": 51.664761, "radius_km": 20, "limit": 100 },
  "count": 1,
  "drivers": [
    {
      "id": "27",
      "driver_id": 27,
      "lat": 32.646625,
      "lng": 51.664761,
      "timestamp_ms": 1781681806750,
      "distance_km": 0.0001,
      "trips": []
    }
  ]
}
```

Driver positions come from Redis GEO (`DRIVER_GEO_KEY`). Each driver hash expires after **2 minutes** unless refreshed by `POST /driver-location` or `sim-drivers`.

#### WebSocket errors

```json
{
  "type": "error",
  "code": "VALIDATION_ERROR",
  "message": "...",
  "timestamp_ms": 1779100000000
}
```

Common codes: `UNAUTHORIZED`, `RATE_LIMITED`, `PAYLOAD_TOO_LARGE`, `DRIVER_LOCATION_DISABLED`, `SHIPMENT_SEARCH_DISABLED`.

Only an allowlisted set of shipment columns is selected — no `SELECT *`.

---

## Configuration

See [`.env.example`](.env.example) for a copy-paste template.

### Core

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `8080` | HTTP listen port |
| `POSTGRES_DSN` | — | PostGIS DSN for road graph + GPS history |
| `REDIS_ADDR` | `localhost:6379` | Redis address |
| `AVG_SPEED_KMH` | `40` | Haversine fallback speed |

### Routing

| Variable | Default | Description |
|----------|---------|-------------|
| `ROUTING_BACKEND` | `internal` | `internal` or `osrm` |
| `OSRM_BASE_URL` | `http://osrm:5000` | OSRM HTTP endpoint |
| `ROUTING_TIMEOUT_MS` | `30000` | Per-request backend deadline |
| `ROUTING_MAX_IN_FLIGHT` | `100` | Total concurrent route calculations |
| `INTERNAL_GRAPH_ENABLED` | `true` | Enable in-process graph |
| `INTERNAL_GRAPH_LAZY_LOAD` | `false` | Load graph on first internal request |
| `ROUTING_YEN_SPUR_CAP` | `60` | Yen's spur node cap per iteration |
| `ROUTING_MAX_ALTERNATIVES` | `1` | Server-side alternatives cap |
| `ROAD_GRAPH_REGIONS` | — | Comma-separated region table suffixes |

### Laravel / shipments

| Variable | Default | Description |
|----------|---------|-------------|
| `SHIPMENT_DB_DRIVER` | `mysql` | `postgres`, `pgx`, `mysql`, `mariadb` |
| `SHIPMENT_DB_DSN` | — | Read-only Laravel DB connection |
| `SHIPMENT_TABLE` | `shipments` | Shipment table name |
| `SHIPMENT_ORIGIN_LOCATION_COLUMN` | `start_location` | PostGIS origin geometry |
| `SHIPMENT_END_LOCATION_COLUMN` | `end_location` | PostGIS destination geometry |
| `SHIPMENT_SEARCH_RADIUS_KM` | `2` | Default search radius (max 50) |
| `SHIPMENT_SEARCH_LIMIT` | `100` | Default row cap (max 500) |
| `SHIPMENT_IMAGES_TABLE` | — | Enable `images[]` (e.g. `shipment_images`) |
| `VEHICLE_TYPES_TABLE` | `vehicle_types` | Vehicle label enrichment |

### Security

| Variable | Default | Description |
|----------|---------|-------------|
| `JWT_AUTH_ENABLED` | `true` | Require JWT or API key |
| `JWT_SECRET` | — | Must match Laravel `JWT_SECRET` |
| `JWT_ALGO` | `HS256` | `HS256`, `HS384`, or `HS512` |
| `API_KEY_ENABLED` | `false` | Enable `X-API-Key` auth |
| `CORS_ALLOWED_ORIGINS` | `*` | Comma-separated origins |
| `RATE_LIMIT_PER_MINUTE` | `300` | Per-IP HTTP rate limit |
| `REQUEST_BODY_LIMIT_BYTES` | `1048576` | Max request body (1 MiB) |

### GPS & drivers

| Variable | Default | Description |
|----------|---------|-------------|
| `GPS_RATE_LIMIT_MS` | `3000` | Min interval between updates per trip |
| `DEVIATION_THRESH_KM` | `0.05` | Cross-track deviation alert threshold |
| `DRIVER_GEO_KEY` | `drivers:geo` | Redis GEO key for drivers |
| `DRIVER_SEARCH_RADIUS_KM` | `20` | Sender-mode driver search radius |

### WebSocket security (`/ws/shipments/nearby`)

| Variable | Default | Description |
|----------|---------|-------------|
| `WS_SHIPMENT_AUTH_REQUIRED` | `true` | Require JWT/API key on upgrade |
| `WS_SHIPMENT_REQUIRE_TLS` | `false` | Reject non-WSS handshakes |
| `WS_SHIPMENT_LEGACY_FORMAT` | `true` | Accept flat `{type,lat,lng}` messages |
| `WS_SHIPMENT_MAX_PER_IP` | `10` | Max concurrent connections per client IP |
| `WS_SHIPMENT_MESSAGES_PER_SEC` | `2` | Per-connection message rate limit |
| `WS_SHIPMENT_MESSAGE_BURST` | `5` | Token-bucket burst size |
| `WS_SHIPMENT_IDLE_TIMEOUT_SEC` | `90` | Disconnect idle clients |
| `WS_SHIPMENT_PING_INTERVAL_SEC` | `30` | Server WebSocket ping interval |
| `WEBSOCKET_AUTH_ENABLED` | `false` | Require auth on `/ws/trip/:id` |

---

## Local Simulators

Docker Compose profile `sim` provides two CLI tools for local end-to-end testing.

### Start

```bash
docker compose --profile sim up -d --build sim-drivers sim-passenger
```

| Service | What it does |
|---------|----------------|
| **sim-drivers** | Seeds and updates driver positions in Redis GEO (`drivers:geo`) |
| **sim-passenger** | Posts GPS fixes to `POST /gps/update` along a fixed Isfahan route |

### `sim-drivers`

Updates Redis only when a driver moves at least `-min-move-m` meters (default 10 m). Re-publishes idle drivers every `-keepalive-sec` (default 90 s) so the **2-minute** location hash TTL does not expire.

Default in `docker-compose.yml`:

| Flag | Value | Meaning |
|------|-------|---------|
| `-count` | `1` | One simulated driver |
| `-id-start` | `27` | Redis member / driver id `27` |
| `-anchor-lat` / `-anchor-lng` | Isfahan test point | Initial position for first driver |
| `-reset-geo` | on | Clear GEO key before seeding |

```bash
# Run outside Docker
go run ./cmd/sim-drivers -redis localhost:6379 -count 1 -id-start=27 \
  -anchor-lat=32.646625 -anchor-lng=51.664761 -reset-geo
```

### `sim-passenger`

Moves along **start → pickup → destination** waypoints and loops forever.

| Flag | Default | Meaning |
|------|---------|---------|
| `-trip` | `4` | Laravel `trips.id` |
| `-tick` | `4s` | Interval between GPS posts (≥ `GPS_RATE_LIMIT_MS`) |
| `-speed-kmh` | `45` | Simulated speed |

Requires `JWT_AUTH_ENABLED=false` **or** a valid `-api-key` / `-jwt` when auth is enabled.

```bash
go run ./cmd/sim-passenger -base http://localhost:8080 -trip 4
```

### Quick WebSocket test (sender / drivers)

```bash
python tools/test_nearby_ws.py 20 100
```

Or connect manually and send:

```json
{"type":"sender","lat":32.646625,"lng":51.664761,"radius_km":20}
```

---

## Routing Backends

### Internal engine (`ROUTING_BACKEND=internal`)

1. **Startup** — Load `road_segments` from PostGIS into an in-memory graph with a spatial grid index (~2.2 km cells).
2. **Pathfinding** — A\* with travel-time cost and Haversine heuristic; Yen's algorithm for alternatives.
3. **Fallback** — Straight-line Haversine when endpoints are >300 m from the nearest graph node.

Optional graphs (loaded if present):

| Graph | Source table | Enables |
|-------|-------------|---------|
| Road | `road_segments[_region]` | car, motorcycle, bus, walking |
| Rail | `rail_stations` + segments | `train` mode |
| Transit | bus/metro overlay | `public_transport` mode |

### OSRM backend (`ROUTING_BACKEND=osrm`)

OSRM MLD serves as the primary engine. On timeout or connection failure, geo-service automatically falls back to the internal engine. Recommended for full-country Iran deployments:

```bash
COMPOSE_PROFILES=osrm ROUTING_BACKEND=osrm INTERNAL_GRAPH_ENABLED=false docker compose up -d
```

---

## Transport Modes

| Mode | Aliases | Notes |
|------|---------|-------|
| `car` | (default) | Standard road routing |
| `motorcycle` | — | Road graph with motorcycle profile |
| `bus` | — | Bus-accessible roads |
| `walking` | `walk`, `foot`, `pedestrian` | Foot network |
| `train` | — | Requires rail graph (`osm2stations`) |
| `public_transport` | — | Requires transit graph (`osm2transit`) |
| `airplane` | — | Great-circle estimate |

---

## OSM Data Import

### Road network — `cmd/osm2postgis`

```bash
go build -o osm2postgis ./cmd/osm2postgis

./osm2postgis \
  -in data/region.osm.pbf \
  -dsn "$POSTGRES_DSN" \
  -bbox "lat_min,lng_min,lat_max,lng_max" \
  -region tehran \
  -truncate=true
```

Respects OSM `highway`, `maxspeed`, `oneway`, and `access` tags.

### Rail stations — `cmd/osm2stations`

```bash
go build -o osm2stations ./cmd/osm2stations
./osm2stations -in data/iran-latest.osm.pbf -dsn "$POSTGRES_DSN" -region iran
```

### Transit overlay — `cmd/osm2transit`

Imports bus stops, metro stations, and route relations for supported Iranian cities (Tehran, Mashhad, Isfahan, Shiraz, Tabriz):

```bash
go build -o osm2transit ./cmd/osm2transit
./osm2transit -in data/iran-latest.osm.pbf -dsn "$POSTGRES_DSN"
```

---

## Development

```bash
# Run locally
make run

# Build release binary
make build

# Unit tests (race detector)
make test

# Lint
make lint

# Regenerate Swagger after handler changes
make swag

# Load test (50 concurrent users, 30 s)
make loadtest
```

### Algorithm references

| Component | Package |
|-----------|---------|
| A\* pathfinding | [`internal/routing/astar.go`](internal/routing/astar.go) |
| Yen's k-shortest | [`internal/routing/yen.go`](internal/routing/yen.go) |
| Multi-modal routing | [`internal/routing/multimodal.go`](internal/routing/multimodal.go) |
| GPS pipeline | [`internal/gps/service.go`](internal/gps/service.go) |
| Route HTTP layer | [`internal/route/`](internal/route/) |
| Shipment queries | [`internal/storage/shipment_repository.go`](internal/storage/shipment_repository.go) |
| JWT middleware | [`internal/middleware/auth.go`](internal/middleware/auth.go) |

### GPS pipeline detail

```
1. Rate limit     Redis SET NX (GPS_RATE_LIMIT_MS)
2. EMA smooth     lat_s = 0.75·lat_raw + 0.25·lat_prev
3. Speed          v = haversine(prev, curr) / Δt
4. Deviation      cross-track distance to cached route polyline
                  → publish deviation.detected if > DEVIATION_THRESH_KM
5. Persist        Redis (24 h TTL) + async PostGIS batch insert
6. Broadcast      Redis Pub/Sub → WebSocket hub
```

---

## Project Structure

```
cmd/
  server/           HTTP entry point, dependency wiring
  sim-drivers/      Redis GEO driver location simulator
  sim-passenger/    Trip GPS update simulator (/gps/update)
  osm2postgis/      OSM PBF/XML → PostGIS road_segments
  osm2stations/     OSM → rail_stations
  osm2transit/      OSM → transit overlay (Iranian cities)
  loadtest/         Concurrent load testing tool

config/             Environment-based configuration

internal/
  cache/            Redis client, GEO helpers, key conventions
  events/           Redis Pub/Sub event bus
  gps/              GPS HTTP handlers + processing service
  handler/          Health, driver, WebSocket upgrade handlers
  middleware/       CORS, JWT/API key auth, rate limit, metrics, logging
  model/            Shared request/response models
  response/         Unified JSON envelope
  route/            Route HTTP layer, OSRM client, caching, trip authz
  routing/          In-memory graph, A*, Yen, multi-modal engine
  service/          Shipment search, driver location
  storage/          PostGIS repos, shipment DB, batch writer, migrations
  utils/            Haversine, polyline, cross-track distance, EMA
  ws/               Trip WebSocket hub and per-connection clients
  wsnearby/         Nearby shipment WebSocket protocol + session
  wsplatform/       Shared WS metrics, logging helpers, location guard

tools/              test_nearby_ws.py and other dev utilities
docs/               Persian guide (README.fa.md) + Swagger (make swag)
```

---

## Troubleshooting

| Symptom | Likely cause | Fix |
|---------|--------------|-----|
| `401 UNAUTHORIZED` on HTTP/WS | JWT invalid or `JWT_SECRET` mismatch | Match Laravel `JWT_SECRET`; or disable auth in dev (see WebSocket Guide) |
| `driver.nearby` → `count: 0` | No live drivers in Redis GEO | Run `docker compose --profile sim up -d sim-drivers` |
| `count: 0` but GEO has members | Driver hash TTL expired (2 min) | Keep `sim-drivers` running; it refreshes via keepalive |
| `SHIPMENT_SEARCH_DISABLED` | `SHIPMENT_DB_DSN` empty | Set Laravel read-only DSN in `.env` |
| WebSocket closes immediately | Origin not allowed | Set `CORS_ALLOWED_ORIGINS` to your frontend URL |
| `gps/update` fails with 401 | Auth enabled without token | `JWT_AUTH_ENABLED=false` in dev or pass `-api-key` to sim-passenger |
| Routing `503` | Road graph not loaded | Run `osm2postgis` or start OSRM profile |
| Reverb/Pusher URL does not work | Wrong service | Laravel Reverb ≠ geo-service; use port **8080** and paths above |

---

## Contributing

1. Fork the repository and create a feature branch.
2. Run `make test` and `make lint` before opening a PR.
3. Update Swagger (`make swag`) when changing API handlers.
4. All CI checks must pass.

---

## License

[MIT](LICENSE) © 2025 Amirbehzad11
