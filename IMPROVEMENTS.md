# Improvements Backlog

Future work for the homelab fork. Check off as implemented.

---

## High priority

- [ ] **`CSERVER_VERSION` build arg** — wire into `cadence.Dockerfile` via `ARG` so the binary reports the correct tag (currently env-set to `homelab`)
- [ ] **`/readyz` endpoint** — separate from `/healthz`; readyz = DB up; healthz = DB + Icecast + Redis
- [ ] **Config validation fatal errors** — if `CSERVER_MUSIC_DIR` set but path missing, or DB connect fails after 3 retries: fatal log + non-zero exit
- [ ] **Liquidsoap HTTP API** — liquidsoap 2.x deprecated telnet by default; switch to `harbor` HTTP or enable `server.telnet` explicitly in `.liq`
- [ ] **`go.sum` verify** — check GitHub Actions passed the `go mod tidy` auto-commit on first run after merge

---

## Medium priority

- [ ] **Search normalization** — lowercase + strip punctuation on insert and search (helps yt-dlp titles: `Song (Official Video) [HD]` → `song official video hd`)
- [ ] **yt-dlp title cleanup** — optional configurable regex list to strip common suffixes: `(Official Video)`, `[Lyrics]`, `- Topic`, `(Audio)`
- [ ] **Playlist/queue display** — expose `/api/queue` from Liquidsoap; show upcoming in UI
- [ ] **Metadata rescan endpoint** — `POST /api/admin/rescan` (devmode only) to trigger `dbPopulate()` without restart
- [ ] **Artwork directory fallback** — if no embedded art, check `cover.jpg` / `folder.jpg` in the same directory
- [ ] **Stream bitrate in UI** — `/api/bitrate` exists, just not rendered in player
- [ ] **`prefers-color-scheme` default** — use system dark/light preference as initial theme before localStorage override

---

## Low priority / nice-to-have

- [ ] **Multiple Icecast mounts** — support more than one mountpoint (128k MP3 + 320k FLAC)
- [ ] **PWA manifest** — `manifest.json` + service worker for mobile "Add to Home Screen"
- [ ] **Scrobbling** — Last.fm / ListenBrainz on track change, env-gated (`CSERVER_LASTFM_KEY` etc.)
- [ ] **Admin panel** — password-protected page: rescan, skip, log tail
- [ ] **Renovate bot** — auto-PRs for Go module + Docker base image updates
- [ ] **Multi-stage `go mod download` cache** — separate Dockerfile layer so rebuilds skip dep downloads
- [ ] **Dockerfile `CSERVER_VERSION` ARG** — pass tag name at build time: `--build-arg CSERVER_VERSION=$(git describe --tags)`

---

## Releasing

```bash
# Tag and push — CI builds multi-arch image + GitHub Release automatically
git tag v1.0.0
git push origin v1.0.0
```

Image published to: `ghcr.io/abzwingt-gaming/cadence:v1.0.0`
