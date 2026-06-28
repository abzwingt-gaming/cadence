#!/usr/bin/env bash
# download-radio.sh — download YouTube playlists for Cadence radio.
#
# Usage:
#   MUSIC_DIR=/path/to/music bash download-radio.sh
#
# Environment variables:
#   MUSIC_DIR      (required) Directory where MP3s are stored.
#   YTDLP_PROXY    (optional) SOCKS5 proxy, e.g. socks5://10.0.0.1:10808
#   LIQUIDSOAP_HOST (optional) Liquidsoap telnet host. Default: liquidsoap
#   LIQUIDSOAP_PORT (optional) Liquidsoap telnet port. Default: 1234
#
# Cron example (every 6 hours):
#   0 */6 * * * MUSIC_DIR=/srv/data/hdd_01/DLNA/MUSIC/radio \
#               YTDLP_PROXY=socks5://10.0.0.180:10808 \
#               /path/to/download-radio.sh >> /var/log/download-radio.log 2>&1

set -euo pipefail

# ── Guards ──────────────────────────────────────────────────────────────────
: "${MUSIC_DIR:?MUSIC_DIR must be set (e.g. /srv/data/hdd_01/DLNA/MUSIC/radio)}"

for cmd in yt-dlp ffmpeg nc flock find sort; do
  command -v "$cmd" >/dev/null 2>&1 \
    || { echo "[error] required command not found: $cmd" >&2; exit 1; }
done

mkdir -p "$MUSIC_DIR"

LOCKFILE="${MUSIC_DIR}/.download-radio.lock"
PLAYLIST_FILE="${MUSIC_DIR}/playlist.m3u"
LIQUIDSOAP_HOST="${LIQUIDSOAP_HOST:-liquidsoap}"
LIQUIDSOAP_PORT="${LIQUIDSOAP_PORT:-1234}"

# ── Flock: prevent overlapping runs ─────────────────────────────────────────
exec 9>"$LOCKFILE"
if ! flock -n 9; then
  echo "[warn] another download-radio.sh is already running — exiting" >&2
  exit 0
fi

echo "[info] download-radio.sh started at $(date -u +%Y-%m-%dT%H:%M:%SZ)"

# ── yt-dlp options ───────────────────────────────────────────────────────────
YT_DLP_OPTS=(
  --split-chapters
  --force-keyframes-at-cuts
  --embed-metadata
  --parse-metadata "title:%(section_title)s"
  --parse-metadata "artist:%(uploader)s"
  --parse-metadata "album:%(playlist_title)s"
  --parse-metadata "track:%(section_number)s"
  --embed-thumbnail
  --extract-audio
  --audio-format mp3
  --audio-quality 192K
  --output "${MUSIC_DIR}/%(title)s [%(id)s].%(ext)s"
  --write-info-json
  --no-overwrites
  --download-archive "${MUSIC_DIR}/.archive.txt"
  --retries 10
  --fragment-retries 10
  --retry-sleep 5
  # 2 workers is safer than 4 when --split-chapters is active on long mixes.
  -N 2
)

if [[ -n "${YTDLP_PROXY:-}" ]]; then
  YT_DLP_OPTS+=(--proxy "${YTDLP_PROXY}" --geo-bypass --geo-bypass-country US)
fi

# ── Playlists ────────────────────────────────────────────────────────────────
# Add your YouTube playlist URLs here.
PLAYLISTS=(
  # "https://youtube.com/playlist?list=YOUR_PLAYLIST_ID"
)

if [[ ${#PLAYLISTS[@]} -eq 0 ]]; then
  echo "[error] No playlists configured. Edit the PLAYLISTS array in this script." >&2
  exit 1
fi

for pl in "${PLAYLISTS[@]}"; do
  echo "[info] downloading: $pl"
  yt-dlp "${YT_DLP_OPTS[@]}" "$pl"
done

# ── Regenerate M3U atomically ────────────────────────────────────────────────
TMP_PLAYLIST="${PLAYLIST_FILE}.tmp"
find "$MUSIC_DIR" -maxdepth 1 -name '*.mp3' -printf '%T@ %p\n' \
  | sort -n | cut -d' ' -f2- > "$TMP_PLAYLIST"
mv "$TMP_PLAYLIST" "$PLAYLIST_FILE"
echo "[info] playlist regenerated: $PLAYLIST_FILE"

# ── Signal Liquidsoap ────────────────────────────────────────────────────────
echo "playlist.reload" | nc -w 2 "$LIQUIDSOAP_HOST" "$LIQUIDSOAP_PORT" \
  && echo "[info] liquidsoap playlist reload sent" \
  || echo "[warn] liquidsoap telnet reload failed (host=${LIQUIDSOAP_HOST} port=${LIQUIDSOAP_PORT}) — reload manually" >&2

echo "[info] download-radio.sh finished at $(date -u +%Y-%m-%dT%H:%M:%SZ)"
