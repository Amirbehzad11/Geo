# راهنمای Deploy — geo-service روی سرور MrchamedonBeta

این راهنما برای سروری است که **Laravel** و **Next.js** از قبل روی Docker اجرا می‌شوند:

```
/var/www/mrchamedonBeta          → Laravel + Postgres + Redis
/var/www/mrchamedonBetaFront/... → Frontend + Gateway Nginx
```

geo-service به همان شبکه Docker (`mrchamedon-network`) وصل می‌شود.

---

## پیش‌نیاز

- Docker + Docker Compose روی سرور
- شبکه `mrchamedon-network` (معمولاً از قبل وجود دارد)
- دسترسی SSH به سرور
- دامنه پیشنهادی: `geo.nurpa.ir`

---

## مرحله ۱ — کلون پروژه

```bash
cd /var/www
git clone https://github.com/farbodsheikhbahaei-oss/GeoMap.git geo-service
cd geo-service
```

---

## مرحله ۲ — فایل `.env` production

```bash
cp deploy/.env.production.example .env
nano .env
```

### مقادیر حیاتی (از `.env` لارavel کپی کن)

| متغیر | منبع |
|--------|------|
| `JWT_SECRET` | همان `JWT_SECRET` لارavel |
| `SHIPMENT_DB_DSN` | user/password/dbname از Laravel |
| `CORS_ALLOWED_ORIGINS` | `https://nurpa.ir,https://www.nurpa.ir` |

نمونه `SHIPMENT_DB_DSN`:

```env
SHIPMENT_DB_DSN=host=mrchamedon-postgres port=5432 user=mr_chamedon_db_pgs password=YOUR_PASS dbname=mr_chamedon sslmode=disable
```

> hostname داخل Docker: **`mrchamedon-postgres`** (نه `localhost`)

---

## مرحله ۳ — بالا آوردن سرویس

```bash
bash deploy/deploy.sh
```

یا دستی:

```bash
docker compose -f deploy/docker-compose.prod.yml --env-file .env up -d --build
```

### چک سلامت

```bash
docker ps | grep geo
docker logs mrchamedon-geo-service --tail 50
docker exec mrchamedon-gateway-nginx wget -qO- http://mrchamedon-geo-service:8080/health
```

خروجی مورد انتظار: JSON با `"status":"ok"`

---

## مرحله ۴ — Nginx + SSL (Gateway)

### ۴.۱ DNS

رکورد **A** بساز:

```
geo.nurpa.ir  →  IP سرور
```

### ۴.۲ فایل nginx

```bash
cp deploy/nginx/geo.nurpa.conf.example \
  /var/www/mrchamedonBetaFront/MrChamedonBetaFrontend/nginx/conf.d/geo.conf
```

### ۴.۳ SSL (certbot — مثل بقیه دامنه‌ها)

```bash
cd /var/www/mrchamedonBetaFront/MrChamedonBetaFrontend
docker compose run --rm certbot certonly --webroot \
  -w /var/www/certbot \
  -d geo.nurpa.ir
```

### ۴.۴ Reload nginx

```bash
docker exec mrchamedon-gateway-nginx nginx -t
docker exec mrchamedon-gateway-nginx nginx -s reload
```

### ۴.۵ تست از بیرون

```bash
curl -s https://geo.nurpa.ir/health
```

---

## مرحله ۵ — اتصال فرانت

در `.env` فرانت روی سرور:

```env
NEXT_PUBLIC_GEO_API_URL=https://geo.nurpa.ir
```

Rebuild (چون `NEXT_PUBLIC_*` موقع build bake می‌شود):

```bash
cd /var/www/mrchamedonBetaFront/MrChamedonBetaFrontend
docker compose up -d --build web
docker exec mrchamedon-gateway-nginx nginx -s reload
```

---

## مرحله ۶ (اختیاری) — OSRM برای مسیریابی production

بدون OSRM، مسیریابی دقیق جاده‌ای در دسترس نیست (GPS، WebSocket، nearby shipments کار می‌کنند).

### ۶.۱ دانلود و preprocess (یک‌بار — زمان‌بر)

```bash
cd /var/www/geo-service
mkdir -p data
wget -O data/map.osm.pbf https://download.geofabrik.de/asia/iran-latest.osm.pbf

docker run --rm -v "$(pwd)/data:/data" osrm/osrm-backend:latest \
  osrm-extract -p /opt/car.lua /data/map.osm.pbf

docker run --rm -v "$(pwd)/data:/data" osrm/osrm-backend:latest \
  osrm-partition /data/map.osrm

docker run --rm -v "$(pwd)/data:/data" osrm/osrm-backend:latest \
  osrm-customize /data/map.osrm
```

### ۶.۲ فعال‌سازی در `.env`

```env
ROUTING_BACKEND=osrm
INTERNAL_GRAPH_ENABLED=false
INTERNAL_GRAPH_REQUIRED=false
```

### ۶.۳ بالا آوردن OSRM

```bash
COMPOSE_PROFILES=osrm docker compose -f deploy/docker-compose.prod.yml --env-file .env up -d osrm
docker compose -f deploy/docker-compose.prod.yml --env-file .env up -d --force-recreate geo-service
```

---

## عملیات روزمره

| کار | دستور |
|-----|--------|
| لاگ | `docker logs -f mrchamedon-geo-service` |
| آپدیت کد | `git pull && bash deploy/deploy.sh` |
| ری‌استارت | `docker compose -f deploy/docker-compose.prod.yml --env-file .env restart geo-service` |
| وضعیت | `docker ps \| grep geo` |

---

## عیب‌یابی

| مشکل | راه‌حل |
|------|--------|
| `401` روی API/WS | `JWT_SECRET` باید دقیقاً مثل Laravel باشد |
| `SHIPMENT_SEARCH_DISABLED` | `SHIPMENT_DB_DSN` را چک کن |
| WebSocket بسته می‌شود | `CORS_ALLOWED_ORIGINS` و SSL (`wss://`) |
| `driver.nearby` خالی | راننده باید `POST /driver-location` بفرستد |
| مسیریابی `503` | OSRM را فعال کن یا road graph import کن |

---

## معماری نهایی

```
Internet
   │
   ▼
mrchamedon-gateway-nginx (443)
   ├── nurpa.ir              → mrchamedon-front-web:3000
   ├── testmrchamedon.ir     → mrchamedon-back-nginx:80
   └── geo.nurpa.ir          → mrchamedon-geo-service:8080

mrchamedon-network
   ├── mrchamedon-postgres   (Laravel DB — geo فقط read)
   ├── mrchamedon-redis      (Laravel — جدا نگه دار)
   ├── mrchamedon-geo-redis  (GPS / driver GEO)
   ├── mrchamedon-geo-service
   └── mrchamedon-osrm       (اختیاری)
```

---

## امنیت

- پورت `8080` geo را به اینترنت **باز نکن** — فقط از gateway
- Redis لارavel را برای geo share **نکن**
- `.env` را commit نکن
- در production: `WS_SHIPMENT_REQUIRE_TLS=true`
