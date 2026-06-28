#!/usr/bin/env bash
# install.sh — Cadence setup script
#
# What this does:
#   1. Copies example configs to config/
#   2. Asks interactive questions and patches config files
#   3. Builds Docker images locally (cadence, cadence_icecast2, cadence_liquidsoap)
#   4. Prints copy-paste instructions for Portainer
#
# What this does NOT do:
#   - Run docker compose up (you deploy via Portainer)
#   - Require the repo to be on the server at runtime

set -euo pipefail

COLOR_GREEN='\033[0;32m'
COLOR_YELLOW='\033[1;33m'
COLOR_RED='\033[0;31m'
COLOR_CYAN='\033[0;36m'
COLOR_RESET='\033[0m'

info()  { echo -e "${COLOR_GREEN}[cadence]${COLOR_RESET} $*"; }
step()  { echo -e "${COLOR_CYAN}[cadence]${COLOR_RESET} $*"; }
warn()  { echo -e "${COLOR_YELLOW}[warn]${COLOR_RESET}   $*"; }
err()   { echo -e "${COLOR_RED}[error]${COLOR_RESET}  $*" >&2; }

info "Cadence Setup"
echo

# -----------------------------------------------------------------------
# Dependency checks
# -----------------------------------------------------------------------
command -v docker >/dev/null 2>&1 || { err "Docker not found. Install Docker first."; exit 1; }

# -----------------------------------------------------------------------
# Bootstrap config files from examples
# -----------------------------------------------------------------------
for f in cadence.env icecast.xml liquidsoap.liq Caddyfile; do
  if [ ! -f "config/$f" ]; then
    cp "config/$f.example" "config/$f"
    info "Created config/$f from example."
  else
    info "config/$f already exists, skipping."
  fi
done

# -----------------------------------------------------------------------
# Interactive setup
# -----------------------------------------------------------------------
echo
info "=== Configuration ==="
echo

# HOST_MUSIC_DIR  — path on the Docker host, bind-mounted into containers as /music.
# Containers always see music at /music; this is only used for the volume mount.
read -rp "Music directory on this host [/srv/data/music]: " HOST_MUSIC_DIR
HOST_MUSIC_DIR=${HOST_MUSIC_DIR:-/srv/data/music}

if [ ! -d "$HOST_MUSIC_DIR" ]; then
  warn "'$HOST_MUSIC_DIR' does not exist. Create it before starting containers."
fi

echo
echo "Database backend:"
echo "  1) sqlite   - lightweight, single file, recommended for homelab"
echo "  2) postgres - fuzzy search via levenshtein, requires more RAM"
read -rp "Choice [1]: " DB_CHOICE
DB_CHOICE=${DB_CHOICE:-1}
case "$DB_CHOICE" in
  2) DB_BACKEND=postgres ;;
  *) DB_BACKEND=sqlite   ;;
esac

PG_PASS=""
if [ "$DB_BACKEND" = "postgres" ]; then
  read -rsp "Postgres password: " PG_PASS; echo
  [ -z "$PG_PASS" ] && { err "Postgres password cannot be empty."; exit 1; }
fi

echo
read -rp "Enable Redis rate limiting? (y/N): " USE_REDIS
USE_REDIS=${USE_REDIS:-n}
REDIS_PASS=""
if [[ "$USE_REDIS" =~ ^[Yy]$ ]]; then
  read -rsp "Redis password (leave blank for none): " REDIS_PASS; echo
fi

echo
read -rp "Public stream URL (e.g. https://radio.lan.example.com/cadence1) [leave blank]: " PUBLIC_STREAM
PUBLIC_STREAM=${PUBLIC_STREAM:-}

echo
read -rp "Log level (debug/info/warn/error) [info]: " LOG_LEVEL
LOG_LEVEL=${LOG_LEVEL:-info}

# -----------------------------------------------------------------------
# Patch config files
# CSERVER_MUSIC_DIR and liquidsoap.liq always get /music (container path).
# HOST_MUSIC_DIR only goes into docker-compose.yml as the volume source.
# -----------------------------------------------------------------------
CONTAINER_MUSIC="/music"

sed_inplace() {
  sed -i.bak "$1" "$2" && rm -f "$2.bak"
}

sed_inplace "s|^#\{0,1\}\s*CSERVER_MUSIC_DIR=.*|CSERVER_MUSIC_DIR=${CONTAINER_MUSIC}|"  config/cadence.env
sed_inplace "s|^#\{0,1\}\s*CSERVER_DB_BACKEND=.*|CSERVER_DB_BACKEND=${DB_BACKEND}|"     config/cadence.env
sed_inplace "s|^#\{0,1\}\s*CSERVER_LOGLEVEL=.*|CSERVER_LOGLEVEL=${LOG_LEVEL}|"           config/cadence.env

[ -n "$PG_PASS" ]      && sed_inplace "s|^#\{0,1\}\s*POSTGRES_PASSWORD=.*|POSTGRES_PASSWORD=${PG_PASS}|"                     config/cadence.env
[ -n "$REDIS_PASS" ]   && sed_inplace "s|^#\{0,1\}\s*CSERVER_REDISPASSWORD=.*|CSERVER_REDISPASSWORD=${REDIS_PASS}|"           config/cadence.env
[ -n "$PUBLIC_STREAM" ] && sed_inplace "s|^#\{0,1\}\s*CSERVER_PUBLIC_STREAM_URL=.*|CSERVER_PUBLIC_STREAM_URL=${PUBLIC_STREAM}|" config/cadence.env

# Liquidsoap always references /music inside the container
sed_inplace "s|CADENCE_PATH_EXAMPLE|${CONTAINER_MUSIC}|g" config/liquidsoap.liq
info "Patched config/liquidsoap.liq → music path = ${CONTAINER_MUSIC}"

# Patch HOST_MUSIC_DIR placeholder in docker-compose.yml
# The compose file ships with a placeholder so this script can fill it in.
sed_inplace "s|\${HOST_MUSIC_DIR}|${HOST_MUSIC_DIR}|g" docker-compose.yml
info "Patched docker-compose.yml → HOST_MUSIC_DIR = ${HOST_MUSIC_DIR}"

warn "Edit config/icecast.xml and config/liquidsoap.liq: replace CADENCE_PASS_EXAMPLE with real passwords."

# -----------------------------------------------------------------------
# Build Docker images locally
# These image names are referenced by docker-compose.yml (no build: block).
# -----------------------------------------------------------------------
echo
step "Building Docker images..."

docker build \
  -f src/cadence.Dockerfile \
  -t cadence:latest \
  ./src
info "Built: cadence:latest"

docker build \
  -f src/icecast2.Dockerfile \
  -t cadence_icecast2:latest \
  ./src
info "Built: cadence_icecast2:latest"

docker build \
  -f src/liquidsoap.Dockerfile \
  -t cadence_liquidsoap:latest \
  ./src
info "Built: cadence_liquidsoap:latest"

# -----------------------------------------------------------------------
# Done — print Portainer instructions
# -----------------------------------------------------------------------
echo
info "===== Build complete ====="
echo
step "Next steps:"
echo
echo "  1. Copy docker-compose.yml to Portainer:"
echo "       Portainer → Stacks → Add stack → paste docker-compose.yml"
echo
echo "  2. Set Portainer stack environment variable:"
echo "       HOST_MUSIC_DIR = ${HOST_MUSIC_DIR}"
echo
echo "  3. Ensure config files are at the paths in docker-compose.yml:"
echo "       /srv/ssd/HOMELAB/cadence/config/cadence.env"
echo "       /srv/ssd/HOMELAB/cadence/config/icecast.xml"
echo "       /srv/ssd/HOMELAB/cadence/config/liquidsoap.liq"
echo "       /srv/ssd/HOMELAB/cadence/custom.css  (empty file is fine)"
echo
echo "  4. Create data directories if needed:"
echo "       mkdir -p /srv/data/hdd_01/HOMELAB/cadence/data"
echo
warn "Icecast passwords: edit config/icecast.xml and config/liquidsoap.liq (CADENCE_PASS_EXAMPLE)."
echo
info "Caddy snippet: see config/Caddyfile.example"
info "Logs after deploy: Portainer → cadence container → Logs"
