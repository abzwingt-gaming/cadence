// aria.js — SSE listener, search, request, history.
// Loaded after page.js.

const API = {
  search:    '/api/search',
  reqId:     '/api/request/id',
  reqBest:   '/api/request/bestmatch',
  art:       '/api/nowplaying/albumart',
  history:   '/api/history',
  listenurl: '/api/listenurl',
  sse:       '/api/radiodata/sse',
  version:   '/api/version',
  status:    '/api/status',
};

// var so page.js can check `typeof streamSrcURL`.
var streamSrcURL = '';

const VOLUME_KEY    = 'cadence_volume';
const SEARCH_MIN    = 2;

// ── SSE ──────────────────────────────────────────────────────
const sse = new EventSource(API.sse);

sse.addEventListener('title', e => {
  document.getElementById('song').textContent = e.data;
  fetchArt();
});
sse.addEventListener('artist', e => {
  document.getElementById('artist').textContent = e.data;
});
sse.addEventListener('listenurl', e => {
  streamSrcURL = e.data;
  const st = document.getElementById('status');
  st.textContent = 'Connected';
  st.className   = 'nes-text is-success';
});
sse.addEventListener('listeners', () => scheduleInfoUpdate());
sse.addEventListener('bitrate',   () => scheduleInfoUpdate());
sse.addEventListener('history',   () => loadHistory());

sse.onerror = () => {
  const st = document.getElementById('status');
  st.textContent = 'Waiting for stream...';
  st.className   = 'nes-text is-warning';
};

// ── Info bar ─────────────────────────────────────────────────
// Single /api/status call replaces two separate fetch calls,
// halving the number of round-trips on every SSE listener/bitrate event.
let _infoTimer = null;
function scheduleInfoUpdate() {
  if (_infoTimer) return;
  _infoTimer = setTimeout(() => { _infoTimer = null; updateInfo(); }, 300);
}

function updateInfo() {
  fetch(API.status)
    .then(r => r.json())
    .then(d => {
      const n  = (d.Listeners >= 0) ? d.Listeners : '?';
      const br = (d.Bitrate > 0) ? ' \xb7 ' + d.Bitrate + ' kbps' : '';
      document.getElementById('listeners').textContent = '\u{1F464} ' + n + br;
    })
    .catch(() => {});
}

// ── Version ───────────────────────────────────────────────────
fetch(API.version).then(r => r.json()).then(d => {
  document.getElementById('release').textContent = d.Version || 'dev';
}).catch(() => {});

// ── Album art ─────────────────────────────────────────────────
function fetchArt() {
  fetch(API.art).then(r => {
    if (r.status === 204 || r.status === 404 || r.status === 503) {
      document.getElementById('artwork').src = './static/blank.jpg';
      return null;
    }
    return r.json();
  }).then(d => {
    if (d && d.Picture)
      document.getElementById('artwork').src = 'data:image/*;base64,' + d.Picture;
  }).catch(() => {
    document.getElementById('artwork').src = './static/blank.jpg';
  });
}

// ── Search ────────────────────────────────────────────────────
let searchTimer;
document.getElementById('searchInput').addEventListener('input', e => {
  clearTimeout(searchTimer);
  const q = e.target.value.trim();
  if (q.length < SEARCH_MIN) {
    document.getElementById('searchResults').innerHTML = '';
    return;
  }
  searchTimer = setTimeout(() => doSearch(q), 300);
});

function doSearch(q) {
  if (!q || q.length < SEARCH_MIN) {
    document.getElementById('searchResults').innerHTML = '';
    return;
  }
  fetch(API.search, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ search: q }),
  }).then(r => r.json()).then(results => {
    const el = document.getElementById('searchResults');
    if (!results || results.length === 0) {
      el.innerHTML = '<p class="nes-text">No results.</p>';
      return;
    }
    el.innerHTML = '';
    results.forEach(s => {
      const div    = document.createElement('div');
      div.className = 'search-result';
      div.setAttribute('role', 'listitem');

      const title  = document.createElement('span');
      title.className   = 'song-title';
      title.textContent = s.Title;

      const artist = document.createElement('span');
      artist.className   = 'song-artist';
      artist.textContent = s.Artist;

      const btn = document.createElement('button');
      btn.className = 'nes-btn is-primary request-btn';
      btn.dataset.id = s.ID;
      btn.setAttribute('aria-label', 'Request ' + s.Title + ' by ' + s.Artist);
      btn.textContent = '\u25B6 Request';
      btn.addEventListener('click', () => requestSong(String(s.ID)));

      div.appendChild(title);
      div.appendChild(artist);
      div.appendChild(btn);
      el.appendChild(div);
    });
  }).catch(() => {
    document.getElementById('searchResults').innerHTML =
      '<p class="nes-text is-error">Search error.</p>';
  });
}

// ── Request ───────────────────────────────────────────────────
let _reqClearTimer = null;
function requestSong(id) {
  const statusEl = document.getElementById('requestStatus');
  if (_reqClearTimer) clearTimeout(_reqClearTimer);
  fetch(API.reqId, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ ID: id }),
  }).then(r => {
    if (r.status === 429) throw new Error('ratelimit');
    if (!r.ok) throw new Error('fail');
    statusEl.textContent = '\u2713 Requested!';
    statusEl.className   = 'nes-text is-success';
  }).catch(err => {
    statusEl.textContent = err.message === 'ratelimit'
      ? '\u26A0 Rate limited. Try again later.'
      : '\u2717 Request failed.';
    statusEl.className = 'nes-text is-error';
  }).finally(() => {
    _reqClearTimer = setTimeout(() => {
      statusEl.textContent = '';
      statusEl.className   = '';
    }, 4000);
  });
}

// ── History ───────────────────────────────────────────────────
function formatEnded(iso) {
  if (!iso) return '';
  try {
    const d       = new Date(iso);
    const diffMin = Math.floor((Date.now() - d.getTime()) / 60000);
    if (diffMin <  1)  return 'just now';
    if (diffMin < 60)  return diffMin + 'm ago';
    const diffH = Math.floor(diffMin / 60);
    if (diffH   < 24)  return diffH + 'h ago';
    return d.toLocaleDateString();
  } catch (_) { return ''; }
}

function loadHistory() {
  fetch(API.history).then(r => r.json()).then(items => {
    const el = document.getElementById('historyResults');
    if (!items || items.length === 0) {
      el.innerHTML = '<p class="nes-text hint-text">No history yet.</p>';
      return;
    }
    el.innerHTML = '';
    [...items].reverse().forEach(h => {
      const div    = document.createElement('div');
      div.className = 'history-item';
      div.setAttribute('role', 'listitem');

      const title  = document.createElement('span');
      title.className   = 'song-title';
      title.textContent = h.Title;

      const artist = document.createElement('span');
      artist.className   = 'song-artist';
      artist.textContent = h.Artist;

      div.appendChild(title);
      div.appendChild(artist);

      const ended = formatEnded(h.Ended);
      if (ended) {
        const ts = document.createElement('span');
        ts.className   = 'history-ts';
        ts.textContent = ended;
        div.appendChild(ts);
      }
      el.appendChild(div);
    });
  }).catch(() => {});
}
loadHistory();

// ── Volume save (aria.js side) ────────────────────────────────
// page.js owns the initial restore; we just persist on change here.
(function () {
  const vol = document.getElementById('volume');
  if (!vol) return;
  vol.addEventListener('input', () => {
    try { localStorage.setItem(VOLUME_KEY, vol.value); } catch (_) {}
  });
})();

// ── Space bar: play/pause ─────────────────────────────────────
document.addEventListener('keydown', e => {
  if (e.code !== 'Space') return;
  const tag = (document.activeElement || {}).tagName;
  if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'BUTTON') return;
  e.preventDefault();
  document.getElementById('playButton').click();
});

// Initial load
updateInfo();
