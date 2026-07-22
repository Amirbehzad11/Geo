#!/usr/bin/env bash
set -euo pipefail

# Run on the production server from the geo-service repo root.
# Example:
#   cd /var/www/geo-service
#   bash deploy/deploy.sh

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

if [[ ! -f .env ]]; then
  echo "Missing .env — copy deploy/.env.production.example to .env and edit it first."
  exit 1
fi

if ! docker network inspect mrchamedon-network >/dev/null 2>&1; then
  echo "Creating Docker network mrchamedon-network..."
  docker network create mrchamedon-network
fi

echo "Building and starting geo-service stack..."
docker compose -f deploy/docker-compose.prod.yml --env-file .env up -d --build

echo ""
echo "Waiting for geo-service to start..."
for i in 1 2 3 4 5 6 7 8 9 10; do
  if docker exec mrchamedon-geo-service wget -qO- http://127.0.0.1:8080/health >/dev/null 2>&1; then
    echo "Health check OK (attempt $i)"
    docker exec mrchamedon-geo-service wget -qO- http://127.0.0.1:8080/health || true
    break
  fi
  if [[ $i -eq 10 ]]; then
    echo "Health check failed after 10 attempts — showing logs:"
    docker logs mrchamedon-geo-service --tail 40
    echo ""
    echo "Common fixes:"
    echo "  - Set JWT_SECRET in .env (must match Laravel, not empty/CHANGE_ME)"
    echo "  - Set SHIPMENT_DB_DSN with mrchamedon-postgres hostname"
    exit 1
  fi
  sleep 2
done

echo ""
echo "Gateway health check (from nginx container):"
docker exec mrchamedon-gateway-nginx wget -qO- http://mrchamedon-geo-service:8080/health || true

echo ""
echo "Done. Next steps:"
echo "  1. Copy deploy/nginx/geo.nurpa.conf.example into frontend nginx conf.d"
echo "  2. Issue SSL cert for geo.nurpa.ir and reload gateway nginx"
echo "  3. Set NEXT_PUBLIC_GEO_API_URL=https://geo.nurpa.ir and rebuild frontend"
