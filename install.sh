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
read -rp "Music directory on this host [/srv/data/music]: " HOST_MUSIC_DIR
HOST_MUSIC_DIR=${HOST_MUSIC_DIR:-/srv/data/music}

if [ ! -d "$HOST_MUSIC_DIR" ]; then
  warn "'$HOST_MUSIC_DIR' does not exist. Create it before starting containers."
fi

# -----------------------------------------------------------------------
# Database backend
# -----------------------------------------------------------------------
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

# -----------------------------------------------------------------------
# Postgres — deploy new vs. connect to existing
# -----------------------------------------------------------------------
PG_DEPLOY_NEW=false
PG_HOST="cadence-postgres"
PG_PORT="5432"
PG_USER="postgres"
PG_PASS=""
PG_DB="cadence"
PG_TABLE="metadata"
PG_SSL="disable"

if [ "$DB_BACKEND" = "postgres" ]; then
  echo
  echo "Postgres setup:"
  echo "  1) Deploy a new Postgres container (managed by this stack)"
  echo "  2) Connect to an existing Postgres instance"
  read -rp "Choice [1]: " PG_SETUP_CHOICE
  PG_SETUP_CHOICE=${PG_SETUP_CHOICE:-1}

  if [ "$PG_SETUP_CHOICE" = "2" ]; then
    # ---- existing postgres ----
    echo
    info "Connecting to existing Postgres."
    echo
    read -rp  "Host (IP or hostname) [localhost]: " PG_HOST
    PG_HOST=${PG_HOST:-localhost}

    read -rp  "Port [5432]: " PG_PORT
    PG_PORT=${PG_PORT:-5432}

    read -rp  "Database name [cadence]: " PG_DB
    PG_DB=${PG_DB:-cadence}

    read -rp  "Username [cadence_user]: " PG_USER
    PG_USER=${PG_USER:-cadence_user}

    read -rsp "Password: " PG_PASS; echo
    [ -z "$PG_PASS" ] && { err "Postgres password cannot be empty."; exit 1; }

    read -rp  "Table name [metadata]: " PG_TABLE
    PG_TABLE=${PG_TABLE:-metadata}

    echo "SSL mode options: disable | require | verify-ca | verify-full"
    read -rp  "SSL mode [disable]: " PG_SSL
    PG_SSL=${PG_SSL:-disable}

    # Warn about fuzzystrmatch requirement
    echo
    warn "Cadence requires the 'fuzzystrmatch' extension for fuzzy search."
    warn "The cadence server will attempt: CREATE EXTENSION IF NOT EXISTS fuzzystrmatch"
    warn "This requires CREATE privilege on the database OR superuser."
    warn "If your user lacks that, run this manually as a superuser first:"
    warn "  psql -U postgres -d ${PG_DB} -c 'CREATE EXTENSION IF NOT EXISTS fuzzystrmatch;'"
    echo

    # Remove the postgres service from compose (we don't want to deploy it)
    REMOVE_PG_SERVICE=true

  else
    # ---- new postgres container ----
    PG_DEPLOY_NEW=true
    PG_HOST="cadence-postgres"
    PG_PORT="5432"
    PG_USER="postgres"
    PG_DB="cadence"
    PG_TABLE="metadata"
    PG_SSL="disable"

    read -rsp "Postgres password: " PG_PASS; echo
    [ -z "$PG_PASS" ] && { err "Postgres password cannot be empty."; exit 1; }

    REMOVE_PG_SERVICE=false
  fi
fi

# -----------------------------------------------------------------------
# Redis
# -----------------------------------------------------------------------
echo
read -rp "Enable Redis rate limiting? (y/N): " USE_REDIS
USE_REDIS=${USE_REDIS:-n}
REDIS_PASS=""
if [[ "$USE_REDIS" =~ ^[Yy]$ ]]; then
  read -rsp "Redis password (leave blank for none): " REDIS_PASS; echo
fi

# -----------------------------------------------------------------------
# Misc
# -----------------------------------------------------------------------
echo
read -rp "Admin token for /api/admin/rescan (leave blank to disable): " ADMIN_TOKEN
ADMIN_TOKEN=${ADMIN_TOKEN:-}

read -rp "Public stream URL (e.g. https://radio.lan.example.com) [leave blank]: " PUBLIC_STREAM
PUBLIC_STREAM=${PUBLIC_STREAM:-}

read -rp "Log level (debug/info/warn/error) [info]: " LOG_LEVEL
LOG_LEVEL=${LOG_LEVEL:-info}

# -----------------------------------------------------------------------
# Patch config files
# -----------------------------------------------------------------------
CONTAINER_MUSIC="/music"

sed_inplace() {
  sed -i.bak "$1" "$2" && rm -f "$2.bak"
}

# cadence.env patches
sed_inplace "s|^#\{0,1\}\s*CSERVER_MUSIC_DIR=.*|CSERVER_MUSIC_DIR=${CONTAINER_MUSIC}|"      config/cadence.env
sed_inplace "s|^#\{0,1\}\s*CSERVER_DB_BACKEND=.*|CSERVER_DB_BACKEND=${DB_BACKEND}|"          config/cadence.env
sed_inplace "s|^#\{0,1\}\s*CSERVER_LOGLEVEL=.*|CSERVER_LOGLEVEL=${LOG_LEVEL}|"                config/cadence.env

if [ "$DB_BACKEND" = "postgres" ]; then
  sed_inplace "s|^#\{0,1\}\s*CSERVER_POSTGRESADDRESS=.*|CSERVER_POSTGRESADDRESS=${PG_HOST}|"   config/cadence.env
  sed_inplace "s|^#\{0,1\}\s*CSERVER_POSTGRESPORT=.*|CSERVER_POSTGRESPORT=${PG_PORT}|"         config/cadence.env
  sed_inplace "s|^#\{0,1\}\s*CSERVER_POSTGRESUSER=.*|CSERVER_POSTGRESUSER=${PG_USER}|"         config/cadence.env
  sed_inplace "s|^#\{0,1\}\s*POSTGRES_PASSWORD=.*|POSTGRES_PASSWORD=${PG_PASS}|"               config/cadence.env
  sed_inplace "s|^#\{0,1\}\s*CSERVER_POSTGRESDBNAME=.*|CSERVER_POSTGRESDBNAME=${PG_DB}|"       config/cadence.env
  sed_inplace "s|^#\{0,1\}\s*CSERVER_POSTGRESTABLENAME=.*|CSERVER_POSTGRESTABLENAME=${PG_TABLE}|" config/cadence.env
  sed_inplace "s|^#\{0,1\}\s*CSERVER_POSTGRESSSL=.*|CSERVER_POSTGRESSSL=${PG_SSL}|"           config/cadence.env
fi

[ -n "$ADMIN_TOKEN" ]   && sed_inplace "s|^#\{0,1\}\s*CSERVER_ADMIN_TOKEN=.*|CSERVER_ADMIN_TOKEN=${ADMIN_TOKEN}|"             config/cadence.env
[ -n "$REDIS_PASS" ]    && sed_inplace "s|^#\{0,1\}\s*CSERVER_REDISPASSWORD=.*|CSERVER_REDISPASSWORD=${REDIS_PASS}|"           config/cadence.env
[ -n "$PUBLIC_STREAM" ] && sed_inplace "s|^#\{0,1\}\s*CSERVER_PUBLIC_STREAM_URL=.*|CSERVER_PUBLIC_STREAM_URL=${PUBLIC_STREAM}|" config/cadence.env

# Liquidsoap: always /music inside container
sed_inplace "s|CADENCE_PATH_EXAMPLE|${CONTAINER_MUSIC}|g" config/liquidsoap.liq
info "Patched config/liquidsoap.liq → music path = ${CONTAINER_MUSIC}"

# docker-compose.yml: stamp HOST_MUSIC_DIR
sed_inplace "s|\${HOST_MUSIC_DIR}|${HOST_MUSIC_DIR}|g" docker-compose.yml
info "Patched docker-compose.yml → HOST_MUSIC_DIR = ${HOST_MUSIC_DIR}"

# docker-compose.yml: patch all postgres env vars inline so Portainer shows real values
if [ "$DB_BACKEND" = "postgres" ]; then
  sed_inplace "s|CSERVER_POSTGRESADDRESS:.*|CSERVER_POSTGRESADDRESS: ${PG_HOST}|"       docker-compose.yml
  sed_inplace "s|CSERVER_POSTGRESPORT:.*|CSERVER_POSTGRESPORT: \"${PG_PORT}\"|"           docker-compose.yml
  sed_inplace "s|CSERVER_POSTGRESUSER:.*|CSERVER_POSTGRESUSER: ${PG_USER}|"               docker-compose.yml
  sed_inplace "s|POSTGRES_PASSWORD:.*changeme.*|POSTGRES_PASSWORD: ${PG_PASS}|"          docker-compose.yml
  sed_inplace "s|CSERVER_POSTGRESDBNAME:.*|CSERVER_POSTGRESDBNAME: ${PG_DB}|"             docker-compose.yml
  sed_inplace "s|CSERVER_POSTGRESTABLENAME:.*|CSERVER_POSTGRESTABLENAME: ${PG_TABLE}|"   docker-compose.yml
  sed_inplace "s|CSERVER_POSTGRESSSL:.*|CSERVER_POSTGRESSSL: ${PG_SSL}|"                 docker-compose.yml
  sed_inplace "s|CSERVER_DB_BACKEND:.*|CSERVER_DB_BACKEND: postgres|"                     docker-compose.yml
fi

if [ -n "$ADMIN_TOKEN" ]; then
  sed_inplace "s|CSERVER_ADMIN_TOKEN:.*|CSERVER_ADMIN_TOKEN: ${ADMIN_TOKEN}|" docker-compose.yml
fi
if [ -n "$PUBLIC_STREAM" ]; then
  sed_inplace "s|CSERVER_PUBLIC_STREAM_URL:.*|CSERVER_PUBLIC_STREAM_URL: ${PUBLIC_STREAM}|" docker-compose.yml
fi
sed_inplace "s|CSERVER_LOGLEVEL:.*|CSERVER_LOGLEVEL: ${LOG_LEVEL}|" docker-compose.yml

# If using existing postgres, comment out the postgres service block in compose
# so Portainer doesn't try to start a container we don't need.
if [ "$DB_BACKEND" = "postgres" ] && [ "${REMOVE_PG_SERVICE:-false}" = "true" ]; then
  # Mark the postgres profile service as disabled by prepending a clear comment.
  # The profiles: ["postgres"] block won't start unless --profile postgres is passed,
  # but we add a comment for clarity.
  sed_inplace "/^  postgres:$/i\\  # EXTERNAL POSTGRES — service below is disabled; using ${PG_HOST}" docker-compose.yml
  info "Noted: postgres container service left in compose but profile-gated (won't start by default)."
fi

warn "Edit config/icecast.xml and config/liquidsoap.liq: replace CADENCE_PASS_EXAMPLE with real passwords."

# -----------------------------------------------------------------------
# Build Docker images locally
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
# Done — Portainer instructions
# -----------------------------------------------------------------------
echo
info "===== Build complete ====="
echo
step "Next steps:"
echo
echo "  1. Copy docker-compose.yml to Portainer:"
echo "       Portainer → Stacks → Add stack → paste docker-compose.yml"
echo
echo "  2. Ensure config files are at the paths referenced in docker-compose.yml:"
echo "       /srv/ssd/HOMELAB/cadence/config/icecast.xml"
echo "       /srv/ssd/HOMELAB/cadence/config/liquidsoap.liq"
echo "       /srv/ssd/HOMELAB/cadence/custom.css  (empty file is fine)"
echo
echo "  3. Create data directories:"
echo "       mkdir -p /srv/data/hdd_01/HOMELAB/cadence/data"

if [ "$DB_BACKEND" = "postgres" ] && [ "${PG_DEPLOY_NEW}" = "false" ]; then
  echo
  warn "Existing Postgres checklist:"
  warn "  • User '${PG_USER}' must have CONNECT + CREATE TABLE privileges on '${PG_DB}'"
  warn "  • 'fuzzystrmatch' extension must exist in '${PG_DB}' (or the user needs CREATE privilege):"
  warn "      psql -U postgres -d ${PG_DB} -c 'CREATE EXTENSION IF NOT EXISTS fuzzystrmatch;'"
  warn "  • If using an external host, ensure the Cadence container can reach ${PG_HOST}:${PG_PORT}"
  warn "      (add to services-net or expose port, depending on your setup)"
fi

if [ "$DB_BACKEND" = "postgres" ] && [ "${PG_DEPLOY_NEW}" = "true" ]; then
  echo
  info "Postgres profile: add 'postgres' to COMPOSE_PROFILES in Portainer, or deploy the postgres"
  info "service separately. The postgres service is profile-gated in docker-compose.yml."
fi

echo
warn "Icecast passwords: edit config/icecast.xml and config/liquidsoap.liq (CADENCE_PASS_EXAMPLE)."
echo
info "Caddy snippet: see config/Caddyfile.example"
info "Logs after deploy: Portainer → cadence container → Logs"
