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
echo "Health check (from host):"
docker exec mrchamedon-gateway-nginx wget -qO- http://mrchamedon-geo-service:8080/health || true

echo ""
echo "Done. Next steps:"
echo "  1. Copy deploy/nginx/geo.nurpa.conf.example into frontend nginx conf.d"
echo "  2. Issue SSL cert for geo.nurpa.ir and reload gateway nginx"
echo "  3. Set NEXT_PUBLIC_GEO_API_URL=https://geo.nurpa.ir and rebuild frontend"
