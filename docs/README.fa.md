# geo-service — مستندات فارسی

> نسخه انگلیسی: [README.md](../README.md)

**geo-service** یک میکروسرویس آمادهٔ production برای پلتفرم‌های لجستیک است. مسیریابی، ردیابی GPS زنده، رویدادهای WebSocket، جستجوی بسته‌های نزدیک و موقعیت راننده را در یک باینری Go ارائه می‌دهد.

در پروژه **MrchamedonBeta**، این سرویس لایهٔ GIS است: Laravel احراز هویت و منطق کسب‌وکار را مدیریت می‌کند؛ geo-service نقشه، مسیریابی، ردیابی و کشف زنده را انجام می‌دهد.

---

## فهرست مطالب

- [قابلیت‌ها](#قابلیت‌ها)
- [معماری](#معماری)
- [شروع سریع](#شروع-سریع)
- [ادغام با Laravel](#ادغام-با-laravel-mrchamedonbeta)
- [احراز هویت و دسترسی](#احراز-هویت-و-دسترسی)
- [مرجع API](#مرجع-api)
- [راهنمای WebSocket](#راهنمای-websocket)
- [تنظیمات محیطی](#تنظیمات-محیطی)
- [موتورهای مسیریابی](#موتورهای-مسیریابی)
- [حالت‌های حمل‌ونقل](#حالت‌های-حمل‌ونقل)
- [وارد کردن داده OSM](#وارد-کردن-داده-osm)
- [توسعه و تست](#توسعه-و-تست)
- [ساختار پروژه](#ساختار-پروژه)

---

## قابلیت‌ها

| بخش | توضیح |
|-----|--------|
| **مسیریابی** | OSRM (MLD) به‌عنوان موتور اصلی + fallback خودکار به A\* و Yen داخل پروسه |
| **حالت حمل** | `car`, `motorcycle`, `bus`, `walking`, `train`, `public_transport`, `airplane` |
| **چند مقصد** | `POST /route/waypoints` با مرتب‌سازی نزدیک‌ترین همسایه |
| **GPS** | هموارسازی EMA، سرعت، تشخیص انحراف از مسیر، Redis، تاریخچه PostGIS |
| **WebSocket** | رویدادهای زنده سفر؛ بسته‌های نزدیک؛ جستجوی راننده |
| **دیتابیس Laravel** | کوئری read-only روی `shipments` با PostGIS، تصاویر، نوع وسیله |
| **امنیت** | JWT مشترک با Laravel، API key اختیاری، CORS، rate limit |
| **دسترسی سطح شیء** | مالکیت trip و دسترسی فرستنده/گیرنده shipment |
| **مانیتورینگ** | لاگ JSON، Prometheus، Swagger |

---

## معماری

```
                    ┌─────────────────────────────────────────┐
  موبایل / وب       │           geo-service (:8080)           │
  Laravel API  ────►│  Gin HTTP  +  WebSocket  +  Middleware  │
                    └──────┬──────────┬──────────┬────────────┘
                           │          │          │
              ┌────────────┘          │          └──────────────┐
              ▼                       ▼                         ▼
        ┌──────────┐           ┌──────────┐            ┌──────────────┐
        │  Redis   │           │ PostGIS  │            │ دیتابیس      │
        │  کش      │           │ گراف     │            │ Laravel      │
        │  Pub/Sub │           │ جاده +   │            │ (فقط خواندن) │
        │  GEO     │           │ GPS      │            │ shipments    │
        └──────────┘           └──────────┘            └──────────────┘
              │                       │
              │                       ▼
              │                ┌──────────┐
              └───────────────►│   OSRM   │  (اختیاری — production)
                               │  :5000   │
                               └──────────┘
```

### جریان درخواست مسیریابی

کلاینت → middleware JWT → handler → OSRM یا موتور داخلی → کش Redis → پاسخ JSON

### جریام GPS

کلاینت → بررسی مالکیت trip → پردازش GPS → Redis → Pub/Sub → WebSocket + ذخیره PostGIS

### جستجوی بسته نزدیک

WebSocket + JWT → `ST_DWithin` روی `shipments.start_location` → enrich با vehicles، images، content_type

---

## شروع سریع

### پیش‌نیاز

- Docker و Docker Compose
- (اختیاری) فایل OSM ایران از [Geofabrik](https://download.geofabrik.de/asia/iran-latest.osm.pbf)

### ۱. کلون و تنظیم env

```bash
git clone https://github.com/Amirbehzad11/geo-service.git
cd geo-service
cp .env.example .env
```

مقادیر مهم در `.env`:

- `JWT_SECRET` — باید **دقیقاً** همان `JWT_SECRET` لاراول باشد
- `SHIPMENT_DB_DSN` — اتصال read-only به PostgreSQL لاراول
- `CORS_ALLOWED_ORIGINS` — origin فرانت (مثلاً `http://localhost:5173`)

### ۲. بالا آوردن زیرساخت

```bash
docker compose up -d postgres redis
```

PostGIS روی پورت **5433** هاست در دسترس است (`geo` / `geo_secret` / `geodb`).

### ۳. import گراف جاده (مسیریابی داخلی)

```bash
go build -o osm2postgis ./cmd/osm2postgis

./osm2postgis \
  -in data/iran-latest.osm.pbf \
  -dsn "host=localhost port=5433 user=geo password=geo_secret dbname=geodb sslmode=disable" \
  -bbox "35.5,51.1,35.9,51.7" \
  -truncate=true
```

فرمت bbox: `lat_min,lng_min,lat_max,lng_max`

### ۴. اجرای سرویس

```bash
docker compose up --build geo-service
```

| آدرس | کاربرد |
|------|--------|
| `http://localhost:8080` | API |
| `http://localhost:8080/docs/index.html` | Swagger |
| `http://localhost:8080/health` | سلامت سرویس |
| `http://localhost:8080/metrics` | Prometheus |

### ۵. حالت OSRM (پیشنهادی برای production ایران)

```bash
COMPOSE_PROFILES=osrm ROUTING_BACKEND=osrm INTERNAL_GRAPH_ENABLED=false \
  docker compose up -d osrm geo-service
```

جزئیات preprocess OSRM در کامنت‌های `docker-compose.yml` آمده است.

---

## ادغام با Laravel (MrchamedonBeta)

geo-service به‌صورت **فقط خواندن** به همان PostgreSQL لاراول وصل می‌شود.

### کاربردها

1. **جستجوی بسته نزدیک** — `GET /ws/shipments/nearby`
2. **اعتبارسنجی trip** — بررسی `trips.user_id` و ارتباط shipping
3. **غنی‌سازی پاسخ** — vehicle types، تصاویر بسته، content image

### جداول و ستون‌های مورد انتظار

| جدول / ستون | کاربرد |
|-------------|--------|
| `shipments.start_location` | PostGIS — مبدأ جستجوی شعاعی |
| `shipments.end_location` | PostGIS — مقصد در پاسخ |
| `trips.id`, `trips.user_id` | مالکیت trip برای عملیات write |
| `shippings.trip_id`, `shippings.shipment_id` | دسترسی فرستنده/گیرنده به WebSocket سفر |
| `vehicle_types` | آرایه `vehicles[]` در پاسخ |
| `shipment_images` | آرایه `images[]` (اختیاری) |
| `content_types` | فیلد `content_image` (اختیاری) |

### اتصال از Docker به لاراول

```env
SHIPMENT_DB_DRIVER=postgres
SHIPMENT_DB_DSN=host=host.docker.internal port=5432 user=USER password=PASS dbname=mr_chamedon sslmode=disable
JWT_SECRET=<همان مقدار Laravel>
CORS_ALLOWED_ORIGINS=http://localhost,http://localhost:5173
```

اگر هر دو سرویس در یک `docker-compose` یا شبکه مشترک هستند، به‌جای `host.docker.internal` می‌توانید hostname سرویس postgres لاراول را بگذارید.

### الگوی احراز هویت در فرانت

**HTTP:**

```http
Authorization: Bearer <laravel_jwt_token>
```

**WebSocket** (مرورگر نمی‌تواند `Authorization` روی upgrade بفرستد):

```js
const token = localStorage.getItem('access_token');
const ws = new WebSocket('ws://localhost:8080/ws/trip/42', ['bearer', token]);
```

سرور توکن را از `Sec-WebSocket-Protocol: bearer, <token>` می‌خواند.

### نکته امنیتی

- `JWT_SECRET` در Go و Laravel **باید یکی باشد** (`tymon/jwt-auth`)
- در production از `CORS_ALLOWED_ORIGINS=*` استفاده نکنید
- `JWT_AUTH_ENABLED=true` را خاموش نگذارید مگر در محیط dev

---

## احراز هویت و دسترسی

### روش‌های ورود

| روش | نحوه ارسال | کاربرد |
|-----|------------|--------|
| **JWT** (پیش‌فرض) | `Authorization: Bearer` یا subprotocol WebSocket | اپ موبایل، فرانت لاراول |
| **API key** | `X-API-Key` | سرویس به سرویس (بدون چک per-user) |

### کنترل دسترسی سطح شیء

وقتی `SHIPMENT_DB_DSN` تنظیم شده باشد:

| Endpoint | قانون |
|----------|-------|
| `POST /route` با `trip_id` | کاربر JWT باید مالک trip باشد |
| `POST /route/waypoints` با `trip_id` | همان |
| `POST /gps/update` | مالک trip |
| `GET /gps/trip/:id/location` | مالک یا فرستنده/گیرنده shipment مرتبط |
| `GET /ws/trip/:id` | مالک یا فرستنده/گیرنده shipment مرتبط |
| `POST /driver-location` | `user_id` توکن باید برابر `driver_id` باشد |

درخواست با API key از چک per-user عبور می‌کند (فقط برای backendهای مورد اعتماد).

### محافظت لبه

- Rate limit بر اساس IP (`RATE_LIMIT_PER_MINUTE`، پیش‌فرض ۳۰۰)
- سقف اندازه body (`REQUEST_BODY_LIMIT_BYTES`، پیش‌فرض ۱ مگابایت)
- WebSocket: read limit ۶۴ کیلوبایت، حداکثر ۳۰ پیام در دقیقه
- بررسی `Origin` برای WebSocket (جدا از CORS HTTP)

---

## مرجع API

همه endpointهای JSON پاسخ یکسان برمی‌گردانند:

```json
{ "success": true, "data": { } }
{ "success": false, "error": { "code": "VALIDATION_ERROR", "message": "..." } }
```

مستندات تعاملی: `/docs/index.html`

### `POST /route` — محاسبه مسیر

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

| فیلد | توضیح |
|------|--------|
| `mode` / `vehicle_type` | حالت حمل (alias پشتیبانی می‌شود) |
| `trip_id` | اختیاری؛ با JWT مالکیت بررسی می‌شود |
| `alternatives` | مسیرهای جایگزین (محدود به `ROUTING_MAX_ALTERNATIVES`) |

خروجی: فاصله (km)، زمان (دقیقه)، polyline، دستورالعمل گام‌به‌گام، `legs[]` برای چندوجهی.

### `POST /route/waypoints` — مسیر چند مقصد

```bash
curl -s -X POST http://localhost:8080/route/waypoints \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "trip_id": 1,
    "mode": "car",
    "waypoints": [
      { "lat": 35.6892, "lng": 51.3890, "label": "تهران" },
      { "lat": 32.6539, "lng": 51.6660, "label": "اصفهان" }
    ]
  }'
```

### `POST /gps/update` — ارسال موقعیت GPS

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

خط لوله: rate limit → EMA (α=0.75) → سرعت → انحراف → Redis → broadcast

### `GET /gps/trip/:id/location` — آخرین موقعیت

### `POST /driver-location` — به‌روزرسانی موقعیت راننده

```bash
curl -s -X POST http://localhost:8080/driver-location \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{ "driver_id": "16", "lat": 35.70, "lng": 51.40 }'
```

در Redis GEO ذخیره می‌شود؛ برای جستجوی مسافر نزدیک در حالت `sender` استفاده می‌شود.

### `GET /health` و `GET /metrics`

---

## راهنمای WebSocket

### `GET /ws/trip/:id` — رویدادهای زنده سفر

```js
const ws = new WebSocket(`ws://localhost:8080/ws/trip/${tripId}`, ['bearer', token]);

ws.onmessage = (e) => {
  const msg = JSON.parse(e.data);
  // type: "connected" | "location.updated" | "deviation.detected"
};
```

نیاز به JWT دارد. کاربر باید مالک trip یا طرف shipping مرتبط باشد.

### `GET /ws/shipments/nearby` — بسته‌های نزدیک / رانندگان

**حالت مسافر (passenger)** — جستجوی shipment نزدیک:

```js
const ws = new WebSocket('ws://localhost:8080/ws/shipments/nearby', ['bearer', token]);

ws.onopen = () => {
  ws.send(JSON.stringify({
    type: 'passenger',       // یا خالی / shipment.nearby
    lat: 35.6892,
    lng: 51.3890,
    radius_km: 2,
    limit: 50,
    filter_vehicle_types: [1, 2, 3]
  }));
};
```

**حالت فرستنده (sender)** — جستجوی راننده نزدیک در Redis:

```js
ws.send(JSON.stringify({
  type: 'sender',
  lat: 35.6892,
  lng: 51.3890,
  radius_km: 20
}));
```

**جستجو با query string** (یک‌بار هنگام اتصال):

```
/ws/shipments/nearby?type=passenger&lat=35.6892&lng=51.3890&radius_km=2
```

**نمونه پاسخ:**

```json
{
  "type": "shipment.nearby",
  "timestamp_ms": 1779100000000,
  "query": { "lat": 35.6892, "lng": 51.389, "radius_km": 2, "limit": 100 },
  "count": 1,
  "shipments": [
    {
      "id": 42,
      "start_lat": 35.69,
      "start_lng": 51.39,
      "destination_lat": 35.80,
      "destination_lng": 51.43,
      "distance_km": 0.12,
      "content_image": "https://...",
      "images": ["path/to/image1.jpg"],
      "vehicles": [{ "id": 1, "label": "car", "title": "سواری" }]
    }
  ]
}
```

> فقط ستون‌های allowlist از جدول shipment خوانده می‌شود — `SELECT *` استفاده نمی‌شود.

---

## تنظیمات محیطی

الگوی کامل در [`.env.example`](../.env.example).

### هسته

| متغیر | پیش‌فرض | توضیح |
|-------|---------|--------|
| `PORT` | `8080` | پورت HTTP |
| `POSTGRES_DSN` | — | PostGIS برای گراف جاده + تاریخچه GPS |
| `REDIS_ADDR` | `localhost:6379` | آدرس Redis |
| `AVG_SPEED_KMH` | `40` | سرعت fallback خط مستقیم |

### مسیریابی

| متغیر | پیش‌فرض | توضیح |
|-------|---------|--------|
| `ROUTING_BACKEND` | `internal` | `internal` یا `osrm` |
| `OSRM_BASE_URL` | `http://osrm:5000` | آدرس OSRM |
| `ROUTING_TIMEOUT_MS` | `30000` | مهلت هر درخواست |
| `INTERNAL_GRAPH_ENABLED` | `true` | فعال‌سازی گراف داخلی |
| `ROUTING_MAX_ALTERNATIVES` | `1` | سقف مسیر جایگزین |
| `ROAD_GRAPH_REGIONS` | — | پسوندهای جدول منطقه (مثلاً `tehran,mashhad`) |

### Laravel / shipment

| متغیر | پیش‌فرض | توضیح |
|-------|---------|--------|
| `SHIPMENT_DB_DRIVER` | `mysql` | `postgres` / `pgx` / `mysql` |
| `SHIPMENT_DB_DSN` | — | اتصال read-only به DB لاراول |
| `SHIPMENT_TABLE` | `shipments` | نام جدول |
| `SHIPMENT_ORIGIN_LOCATION_COLUMN` | `start_location` | ستون PostGIS مبدأ |
| `SHIPMENT_END_LOCATION_COLUMN` | `end_location` | ستون PostGIS مقصد |
| `SHIPMENT_SEARCH_RADIUS_KM` | `2` | شعاع پیش‌فرض (حداکثر ۵۰) |
| `SHIPMENT_SEARCH_LIMIT` | `100` | سقف ردیف (حداکثر ۵۰۰) |
| `SHIPMENT_IMAGES_TABLE` | — | مثلاً `shipment_images` |
| `VEHICLE_TYPES_TABLE` | `vehicle_types` | enrich وسیله |

### امنیت

| متغیر | پیش‌فرض | توضیح |
|-------|---------|--------|
| `JWT_AUTH_ENABLED` | `true` | الزام JWT یا API key |
| `JWT_SECRET` | — | باید با Laravel یکی باشد |
| `JWT_ALGO` | `HS256` | `HS256` / `HS384` / `HS512` |
| `API_KEY_ENABLED` | `false` | احراز با `X-API-Key` |
| `CORS_ALLOWED_ORIGINS` | `*` | originهای مجاز |
| `RATE_LIMIT_PER_MINUTE` | `300` | محدودیت درخواست per-IP |
| `REQUEST_BODY_LIMIT_BYTES` | `1048576` | حداکثر body (۱ مگ) |

### GPS و راننده

| متغیر | پیش‌فرض | توضیح |
|-------|---------|--------|
| `GPS_RATE_LIMIT_MS` | `3000` | فاصله حداقل بین آپدیت‌های هر trip |
| `DEVIATION_THRESH_KM` | `0.05` | آستانه هشدار انحراف از مسیر |
| `DRIVER_GEO_KEY` | `drivers:geo` | کلید Redis GEO |
| `DRIVER_SEARCH_RADIUS_KM` | `20` | شعاع جستجوی راننده |

---

## موتورهای مسیریابی

### موتور داخلی (`ROUTING_BACKEND=internal`)

1. **استارتاپ** — بارگذاری `road_segments` از PostGIS به حافظه با ایندکس grid (~۲.۲ km)
2. **مسیر** — A\* با هزینه زمان سفر؛ Yen برای مسیرهای جایگزین
3. **Fallback** — Haversine اگر نقطه بیش از ۳۰۰ متر از گراف فاصله داشته باشد

گراف‌های اختیاری:

| گراف | جدول | حالت |
|------|------|------|
| جاده | `road_segments` | car, motorcycle, bus, walking |
| ریل | `rail_stations` | train |
| ترانزیت | overlay اتوبوس/مترو | public_transport |

### OSRM (`ROUTING_BACKEND=osrm`)

OSRM MLD موتور اصلی است. در صورت timeout یا خطا، fallback به موتور داخلی انجام می‌شود.

---

## حالت‌های حمل‌ونقل

| حالت | نام‌های جایگزین | توضیح |
|------|-----------------|--------|
| `car` | (پیش‌فرض) | جاده معمولی |
| `motorcycle` | — | پروفایل موتور |
| `bus` | — | جاده مجاز اتوبوس |
| `walking` | `walk`, `foot` | پیاده |
| `train` | — | نیاز به `osm2stations` |
| `public_transport` | — | نیاز به `osm2transit` |
| `airplane` | — | خط دایره‌ای بزرگ |

---

## وارد کردن داده OSM

### گراف جاده — `cmd/osm2postgis`

```bash
go build -o osm2postgis ./cmd/osm2postgis
./osm2postgis -in data/region.osm.pbf -dsn "$POSTGRES_DSN" \
  -bbox "lat_min,lng_min,lat_max,lng_max" -region tehran -truncate=true
```

### ایستگاه ریل — `cmd/osm2stations`

```bash
go build -o osm2stations ./cmd/osm2stations
./osm2stations -in data/iran-latest.osm.pbf -dsn "$POSTGRES_DSN"
```

### ترانزیت (شهرهای ایران) — `cmd/osm2transit`

تهران، مشهد، اصفهان، شیراز، تبریز:

```bash
go build -o osm2transit ./cmd/osm2transit
./osm2transit -in data/iran-latest.osm.pbf -dsn "$POSTGRES_DSN"
```

---

## توسعه و تست

```bash
make run        # اجرای محلی
make build      # باینری release
make test       # تست واحد + race detector
make lint       # golangci-lint
make swag       # بازتولید Swagger
make loadtest   # تست بار ۵۰ کاربر همزمان
```

### خط لوله GPS (جزئیات)

```
1. Rate limit     Redis SET NX
2. EMA            lat_s = 0.75·raw + 0.25·prev
3. سرعت           haversine / Δt
4. انحراف         فاصله عمودی تا polyline مسیر
5. ذخیره          Redis + batch PostGIS
6. broadcast      Pub/Sub → WebSocket
```

### مسیرهای مهم کد

| بخش | مسیر |
|-----|------|
| A\* | `internal/routing/astar.go` |
| Yen | `internal/routing/yen.go` |
| GPS | `internal/gps/service.go` |
| Route HTTP | `internal/route/` |
| Shipment | `internal/storage/shipment_repository.go` |
| JWT | `internal/middleware/auth.go` |

---

## ساختار پروژه

```
cmd/
  server/           نقطه ورود HTTP
  osm2postgis/      OSM → road_segments
  osm2stations/     OSM → rail_stations
  osm2transit/      OSM → overlay ترانزیت
  loadtest/         تست بار

config/             تنظیمات از env

internal/
  cache/            Redis
  events/           Pub/Sub
  gps/              GPS handler + service
  handler/          health، driver، WebSocket
  middleware/       CORS، JWT، rate limit، metrics
  route/            لایه HTTP مسیریابی، OSRM، authz
  routing/          گراف، A*، Yen، multimodal
  service/          shipment search، driver
  storage/          PostGIS، shipment DB، batch writer
  utils/            haversine، polyline، EMA
  ws/               hub WebSocket

docs/               Swagger + این فایل فارسی
```

---

## عیب‌یابی رایج

| مشکل | علت احتمالی | راه‌حل |
|------|-------------|--------|
| `401 UNAUTHORIZED` | JWT نامعتبر یا secret متفاوت | `JWT_SECRET` را با Laravel هم‌سان کنید |
| `403 FORBIDDEN` trip | کاربر مالک trip نیست | `trip_id` و `user_id` توکن را بررسی کنید |
| `SHIPMENT_SEARCH_DISABLED` | `SHIPMENT_DB_DSN` خالی | DSN و driver را در `.env` تنظیم کنید |
| WebSocket قطع می‌شود | Origin مجاز نیست | `CORS_ALLOWED_ORIGINS` را اصلاح کنید |
| مسیریابی `503` | گراف بارگذاری نشده | `osm2postgis` اجرا کنید یا OSRM را بالا بیاورید |
| بسته‌ها بدون `images` | جدول تنظیم نشده | `SHIPMENT_IMAGES_TABLE=shipment_images` |

---

## مجوز

[MIT](../LICENSE) © 2025 Amirbehzad11
