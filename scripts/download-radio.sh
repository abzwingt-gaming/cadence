#!/usr/bin/env bash
# download-radio.sh — download YouTube playlists for Cadence radio.
#
# Usage:
#   MUSIC_DIR=/path/to/music bash download-radio.sh
# Or set MUSIC_DIR in your environment / cron job.

set -euo pipefail

: "${MUSIC_DIR:?MUSIC_DIR must be set (e.g. /srv/data/hdd_01/DLNA/MUSIC/radio)}"

PLAYLIST_FILE="${MUSIC_DIR}/playlist.m3u"

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
  # Reduced from 4 — safer with --split-chapters on long mixes.
  -N 2
)

# Optional: set YTDLP_PROXY in environment to route through a SOCKS5 proxy.
# Example: export YTDLP_PROXY="socks5://10.0.0.1:10808"
if [[ -n "${YTDLP_PROXY:-}" ]]; then
  YT_DLP_OPTS+=(--proxy "${YTDLP_PROXY}" --geo-bypass --geo-bypass-country US)
fi

# Add your YouTube playlist URLs here.
PLAYLISTS=(
  # "https://youtube.com/playlist?list=YOUR_PLAYLIST_ID"
)

if [[ ${#PLAYLISTS[@]} -eq 0 ]]; then
  echo "[error] No playlists configured. Edit the PLAYLISTS array in this script." >&2
  exit 1
fi

for pl in "${PLAYLISTS[@]}"; do
  yt-dlp "${YT_DLP_OPTS[@]}" "$pl"
done

# Regenerate M3U playlist sorted by date added.
find "$MUSIC_DIR" -maxdepth 1 -name '*.mp3' -printf '%T@ %p\n' \
  | sort -n | cut -d' ' -f2- > "$PLAYLIST_FILE"

# Signal Liquidsoap to reload the playlist via telnet.
echo "playlist.reload" | nc -w 2 liquidsoap 1234 \
  || echo "[warn] liquidsoap telnet reload failed — reload manually" >&2
