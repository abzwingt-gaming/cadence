#!/usr/bin/env bash
set -euo pipefail

COLOR_GREEN='\033[0;32m'
COLOR_YELLOW='\033[1;33m'
COLOR_RED='\033[0;31m'
COLOR_RESET='\033[0m'

info()  { echo -e "${COLOR_GREEN}[cadence]${COLOR_RESET} $*"; }
warn()  { echo -e "${COLOR_YELLOW}[warn]${COLOR_RESET}   $*"; }
err()   { echo -e "${COLOR_RED}[error]${COLOR_RESET}  $*" >&2; }

info "Cadence Homelab Installer"
echo

# --- Checks ---
command -v docker  >/dev/null 2>&1 || { err "Docker not found. Install Docker first."; exit 1; }
command -v docker-compose >/dev/null 2>&1 || docker compose version >/dev/null 2>&1 || \
  { err "Docker Compose not found."; exit 1; }

# --- Config files ---
if [ ! -f config/cadence.env ]; then
  cp config/cadence.env.example config/cadence.env
  info "Created config/cadence.env from example."
else
  info "config/cadence.env already exists, skipping."
fi

for f in icecast.xml liquidsoap.liq; do
  if [ ! -f "config/$f" ]; then
    cp "config/$f.example" "config/$f"
    info "Created config/$f from example."
  fi
done

if [ ! -f config/Caddyfile ]; then
  cp config/Caddyfile.example config/Caddyfile
  info "Created config/Caddyfile from example."
fi

# Ensure data dir exists for SQLite
mkdir -p data

# --- Interactive setup ---
echo
info "=== Quick Setup ==="

read -rp "Music directory path [/music]: " MUSIC_DIR
MUSIC_DIR=${MUSIC_DIR:-/music}

echo
echo "Database backend:"
echo "  1) sqlite  - lightweight, no extra container (recommended for homelab)"
echo "  2) postgres - full fuzzy search, requires more RAM"
read -rp "Choice [1]: " DB_CHOICE
DB_CHOICE=${DB_CHOICE:-1}

case "$DB_CHOICE" in
  2) DB_BACKEND=postgres ;;
  *) DB_BACKEND=sqlite   ;;
esac

PROFILES=""
PG_PASS=""
if [ "$DB_BACKEND" = "postgres" ]; then
  read -rsp "Postgres password: " PG_PASS; echo
  PROFILES="--profile postgres"
fi

echo
read -rp "Enable Redis rate limiting? (y/N): " USE_REDIS
USE_REDIS=${USE_REDIS:-n}
if [[ "$USE_REDIS" =~ ^[Yy]$ ]]; then
  read -rsp "Redis password (leave blank for none): " REDIS_PASS; echo
  PROFILES="$PROFILES --profile redis"
  # Write REDIS_PASSWORD to env if set
  if [ -n "${REDIS_PASS:-}" ]; then
    echo "REDIS_PASSWORD=${REDIS_PASS}" >> config/cadence.env
    sed -i "s|# CSERVER_REDISPASSWORD=|CSERVER_REDISPASSWORD=${REDIS_PASS}|" config/cadence.env
  fi
fi

echo
read -rp "Public stream URL (e.g. https://radio.example.com/cadence1) [leave blank to skip]: " PUBLIC_STREAM
PUBLIC_STREAM=${PUBLIC_STREAM:-}

# --- Write config ---
sed -i "s|CSERVER_MUSIC_DIR=.*|CSERVER_MUSIC_DIR=${MUSIC_DIR}|"     config/cadence.env
sed -i "s|CSERVER_DB_BACKEND=.*|CSERVER_DB_BACKEND=${DB_BACKEND}|" config/cadence.env
echo "MUSIC_DIR=${MUSIC_DIR}" >> config/cadence.env

if [ -n "$PG_PASS" ]; then
  sed -i "s|# POSTGRES_PASSWORD=.*|POSTGRES_PASSWORD=${PG_PASS}|"   config/cadence.env
fi
if [ -n "$PUBLIC_STREAM" ]; then
  sed -i "s|CSERVER_PUBLIC_STREAM_URL=.*|CSERVER_PUBLIC_STREAM_URL=${PUBLIC_STREAM}|" config/cadence.env
fi

# --- Build & start ---
echo
info "Building images..."
docker compose build

info "Starting Cadence ($DB_BACKEND${PROFILES:+ + ${PROFILES// --profile /+}})..."
# shellcheck disable=SC2086
docker compose $PROFILES up -d

echo
info "Done! Cadence should be available at http://localhost:8080"
if command -v curl >/dev/null 2>&1; then
  sleep 3
  if curl -sf http://localhost:8080/healthz | grep -q 'ok'; then
    info "Health check passed."
  else
    warn "Health check returned degraded. Check: docker compose logs cadence"
  fi
fi
echo
info "Config:     config/cadence.env"
info "Caddy:      config/Caddyfile.example"
info "Custom CSS: src/server/public/css/custom.css (mounted, edit without rebuild)"
info "Logs:       docker compose logs -f cadence"
