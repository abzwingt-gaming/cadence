# Cadence — Homelab Fork

Self-hosted web radio. Fork of [kenellorando/cadence](https://github.com/kenellorando/cadence), hardened for homelab use.

## What's different from upstream

| Feature | Upstream | This fork |
|---|---|---|
| DB | Postgres only | Postgres **or** SQLite (pure Go) |
| Redis | Required | Optional (graceful skip) |
| Redis auth | No | Password + DB index |
| Music scan | Single-threaded | Parallel workers (`CSERVER_SCAN_WORKERS`) |
| Bad/empty tags | Crash / skip | Filename fallback, always inserts |
| Audio formats | mp3 flac ogg | + m4a opus wav aac |
| Icecast URL | One var (public+internal mixed) | Internal status URL + public stream URL separate |
| Album art | Re-reads disk every request | In-memory cache, cleared on track change |
| Frontend | jQuery + Bulma | Vanilla JS + NES.css + dark theme |
| CSS customisation | Rebuild required | Mount `custom.css` volume |
| Healthcheck | `/ready` only | `/healthz` with DB + Icecast + Redis status |
| Compose | One profile | Profiles: default / `postgres` / `redis` |
| Proxy config | nginx example | Caddy example (no snippets) |
| Install | Interactive shell | Env-driven, sets up profiles |
| CI | None | Build + lint on push/PR + release on tags |
| Docker image | Manual build | Published to `ghcr.io` on tag |

## Quick start

```bash
git clone https://github.com/abzwingt-gaming/cadence
cd cadence
bash install.sh
```

Or manually:

```bash
cp config/cadence.env.example config/cadence.env
# Edit config/cadence.env
docker compose up -d
```

For Postgres + Redis:
```bash
docker compose --profile postgres --profile redis up -d
```

## Configuration

All config in `config/cadence.env`. Key variables:

```env
# DB backend
CSERVER_DB_BACKEND=sqlite          # or postgres
CSERVER_SQLITE_PATH=/data/cadence.db

# Internal Icecast URL (Docker only, never sent to browser)
CSERVER_ICECAST_STATUS_URL=http://icecast2:8000

# Public stream URL sent to browser
CSERVER_PUBLIC_STREAM_URL=https://radio.example.com/cadence1

# Redis (optional)
CSERVER_REDISPASSWORD=secret
CSERVER_REDISDB=0

# Scan workers
CSERVER_SCAN_WORKERS=4
```

See `config/cadence.env.example` for all options.

## Custom CSS

Edit `src/server/public/css/custom.css` — it is mounted into the container, no rebuild needed.

## Caddy

See `config/Caddyfile.example`.

## Releasing

```bash
git tag v1.0.0 && git push origin v1.0.0
```

CI builds multi-arch image and pushes to `ghcr.io/abzwingt-gaming/cadence:v1.0.0` + creates a GitHub Release.

## Improvements backlog

See [IMPROVEMENTS.md](./IMPROVEMENTS.md).
