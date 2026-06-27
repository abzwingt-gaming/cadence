# Cadence — Homelab Fork

Self-hosted web radio. Fork of [kenellorando/cadence](https://github.com/kenellorando/cadence), hardened for homelab use.

> **This fork does not send PRs upstream.** It is maintained independently.

## Quick start

```bash
git clone https://github.com/abzwingt-gaming/cadence
cd cadence
bash install.sh
```

Or manually:

```bash
cp config/cadence.env.example config/cadence.env
# Edit config/cadence.env — set CSERVER_MUSIC_DIR at minimum
docker compose up -d
```

With Postgres + Redis:

```bash
docker compose --profile postgres --profile redis up -d
```

## Configuration

All config lives in `config/cadence.env`. See `config/cadence.env.example` for every option with comments.

### Key variables

| Variable | Default | Description |
|---|---|---|
| `CSERVER_MUSIC_DIR` | *(required)* | Path to music files |
| `CSERVER_DB_BACKEND` | `sqlite` | `sqlite` or `postgres` |
| `CSERVER_SQLITE_PATH` | `/data/cadence.db` | SQLite file location |
| `CSERVER_ICECAST_STATUS_URL` | `http://icecast2:8000` | Internal Icecast URL (Docker only) |
| `CSERVER_PUBLIC_STREAM_URL` | *(auto)* | Public stream URL sent to browser |
| `CSERVER_REDISPASSWORD` | *(empty)* | Redis auth password |
| `CSERVER_REDISDB` | `0` | Redis DB index |
| `CSERVER_SCAN_WORKERS` | `4` | Parallel tag-read goroutines |
| `CSERVER_DB_RETRIES` | `5` | DB connect attempts before fatal exit |
| `CSERVER_DB_RETRY_DELAY_MS` | `3000` | Delay between DB retries |
| `CSERVER_TITLE_CLEANUP_PATTERNS` | *(built-in)* | Pipe-separated regex to strip from titles |

## Endpoints

| Path | Purpose |
|---|---|
| `/readyz` | **Liveness probe** — DB connected; use for Docker `HEALTHCHECK` |
| `/healthz` | **Readiness probe** — DB + Icecast + Redis status JSON |
| `/api/search` | POST search |
| `/api/request/id` | POST request by ID |
| `/api/nowplaying/metadata` | GET current track |
| `/api/nowplaying/albumart` | GET base64 album art |
| `/api/history` | GET play history |
| `/api/listenurl` | GET public stream URL |
| `/api/radiodata/sse` | SSE stream for live track/listener updates |

## Custom CSS

Edit `src/server/public/css/custom.css` — mounted into the container as a volume. No rebuild needed.

Example overrides:

```css
:root { --art-size: 320px; }        /* bigger album art */
#version { display: none; }          /* hide listener count */
```

## Caddy

See `config/Caddyfile.example` — plain config, no snippets required.

## Releasing

```bash
git tag v1.0.0
git push origin v1.0.0
```

CI automatically:
1. Builds multi-arch image (`linux/amd64` + `linux/arm64`)
2. Pushes to `ghcr.io/abzwingt-gaming/cadence:v1.0.0` + `:latest`
3. Creates GitHub Release with changelog

## Changes vs upstream

### Backend

| Area | Upstream | This fork |
|---|---|---|
| DB backends | Postgres only | **Postgres or SQLite** (pure Go, no CGO) |
| DB init | Drops and recreates DB | `CREATE TABLE IF NOT EXISTS` + upsert — **non-destructive** |
| DB init failure | Silent warn | **Fatal exit after N retries** with clear error |
| Redis | Required | **Optional** — graceful skip if unreachable |
| Redis auth | None | **Password + DB index** (`CSERVER_REDISPASSWORD`, `CSERVER_REDISDB`) |
| Music scan | Single-threaded | **Parallel goroutine pool** (`CSERVER_SCAN_WORKERS`) |
| Bad/empty tags | Crash or skip | **Filename fallback**, always inserts |
| Title cleanup | None | **Auto-strips yt-dlp suffixes** `(Official Video)`, `[HD]`, `- Topic`, etc. |
| Audio formats | `.mp3 .flac .ogg` | **+ `.m4a .opus .wav .aac`** |
| Icecast URL | One mixed-use var | **Internal status URL** separate from **public stream URL** |
| Album art | Re-reads disk every request | **In-memory `sync.Map` cache**, cleared on track change |
| Version | Env var only | **`ldflags` at build time** from git tag |
| Liveness probe | `/ready` (200 always) | **`/readyz`** (checks DB) |
| Readiness probe | None | **`/healthz`** (DB + Icecast + Redis JSON) |

### Frontend

| Area | Upstream | This fork |
|---|---|---|
| CSS framework | Bulma | **NES.css** (retro pixel art) |
| Theme | Light only | **Dark + Light toggle**, persisted in `localStorage` |
| Theme default | Hardcoded light | **Follows `prefers-color-scheme`** (OS dark mode) |
| JS dependencies | jQuery | **Vanilla `fetch()` + DOM** — no jQuery |
| Search | Fire on Enter only | **300ms debounce** on keyup |
| CSS customisation | Rebuild required | **`custom.css` Docker volume mount** — edit without rebuild |
| Stream status | `Disconnected` (binary) | `Waiting / Connected` with colour coding |
| Rate limit message | Generic fail | **`Rate limited. Try again later.`** (HTTP 429) |

### Infrastructure

| Area | Upstream | This fork |
|---|---|---|
| Docker Compose | Single profile | **Profiles**: default / `--profile postgres` / `--profile redis` |
| Proxy config | nginx only | **Caddy** (`config/Caddyfile.example`) |
| Healthcheck | None in compose | **`wget /readyz`** in image + compose |
| Install script | Interactive (basic) | **Env-driven, profile-aware**, sets up all options |
| CI | None | **GitHub Actions**: build + lint on push/PR, release on tag |
| Docker image | Manual build | **Published to `ghcr.io`** on `v*.*.*` tag, multi-arch |
| Non-root container | No | **`adduser cadence`**, runs as non-root |

## Improvements backlog

See [IMPROVEMENTS.md](./IMPROVEMENTS.md).
