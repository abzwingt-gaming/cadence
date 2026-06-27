// aria.js - SSE listener, search, request, history.

const API = {
  search:   '/api/search',
  reqId:    '/api/request/id',
  reqBest:  '/api/request/bestmatch',
  art:      '/api/nowplaying/albumart',
  history:  '/api/history',
  listenurl:'/api/listenurl',
  sse:      '/api/radiodata/sse',
  version:  '/api/version',
  bitrate:  '/api/bitrate',
  listeners:'/api/listeners',
};

let streamSrcURL = '';

// esc() prevents XSS when inserting user-controlled strings into innerHTML.
function esc(str) {
  const d = document.createElement('div');
  d.appendChild(document.createTextNode(String(str)));
  return d.innerHTML;
}

// --- SSE ---
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
  document.getElementById('status').textContent = 'Connected';
  document.getElementById('status').className = 'nes-text is-success';
});
sse.addEventListener('listeners', () => scheduleInfoUpdate());
sse.addEventListener('bitrate',   () => scheduleInfoUpdate());
sse.addEventListener('history',   () => loadHistory());

sse.onerror = () => {
  document.getElementById('status').textContent = 'Waiting for stream...';
  document.getElementById('status').className = 'nes-text is-warning';
};

// --- Info bar: listeners + bitrate (debounced to avoid fetch storms) ---
let _infoTimer = null;
function scheduleInfoUpdate() {
  if (_infoTimer) return;
  _infoTimer = setTimeout(() => {
    _infoTimer = null;
    updateInfo();
  }, 300);
}

function updateInfo() {
  Promise.all([
    fetch(API.listeners).then(r => r.json()),
    fetch(API.bitrate).then(r => r.json()),
  ]).then(([l, b]) => {
    const listeners = (l.Listeners >= 0) ? l.Listeners : '?';
    const bitrate   = (b.Bitrate   >  0) ? b.Bitrate + ' kbps' : '';
    // Update the #listeners element (renamed from #version in a previous fix).
    document.getElementById('listeners').textContent =
      'Listeners: ' + listeners + (bitrate ? '  \xb7  ' + bitrate : '');
  }).catch(() => {});
}

// --- Version ---
fetch(API.version).then(r => r.json()).then(d => {
  document.getElementById('release').textContent = d.Version || 'dev';
}).catch(() => {});

// --- Album art ---
function fetchArt() {
  fetch(API.art).then(r => {
    // 204 = no art found; 404 = song not in DB; 503 = stream idle.
    if (r.status === 204 || r.status === 404 || r.status === 503) {
      document.getElementById('artwork').src = './static/blank.jpg';
      return null;
    }
    return r.json();
  }).then(d => {
    if (d && d.Picture) {
      // Use image/* so PNG covers aren't forced through a JPEG decoder.
      document.getElementById('artwork').src = 'data:image/*;base64,' + d.Picture;
    }
  }).catch(() => {
    document.getElementById('artwork').src = './static/blank.jpg';
  });
}

// --- Search (300ms debounce, 2-char minimum) ---
const SEARCH_MIN_LEN = 2;
let searchTimer;
document.getElementById('searchInput').addEventListener('keyup', e => {
  clearTimeout(searchTimer);
  const q = e.target.value.trim();
  // Clear results immediately when input is too short.
  if (q.length < SEARCH_MIN_LEN) {
    document.getElementById('searchResults').innerHTML = '';
    return;
  }
  searchTimer = setTimeout(() => doSearch(q), 300);
});

function doSearch(q) {
  if (!q || q.length < SEARCH_MIN_LEN) {
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
    // Build DOM nodes instead of innerHTML to prevent XSS.
    el.innerHTML = '';
    results.forEach(s => {
      const div = document.createElement('div');
      div.className = 'search-result nes-container';
      div.dataset.id = s.ID;

      const title  = document.createElement('span');
      title.className = 'song-title';
      title.textContent = s.Title;

      const sep = document.createTextNode(' \u2014 ');

      const artist = document.createElement('span');
      artist.className = 'song-artist nes-text is-primary';
      artist.textContent = s.Artist;

      const btn = document.createElement('button');
      btn.className = 'nes-btn is-primary request-btn';
      btn.dataset.id = s.ID;
      btn.innerHTML = '&#9654; Request';
      btn.addEventListener('click', () => requestSong(btn.dataset.id));

      div.appendChild(title);
      div.appendChild(sep);
      div.appendChild(artist);
      div.appendChild(btn);
      el.appendChild(div);
    });
  }).catch(() => {
    document.getElementById('searchResults').innerHTML = '<p class="nes-text is-error">Search error.</p>';
  });
}

let _reqClearTimer = null;
function requestSong(id) {
  const statusEl = document.getElementById('requestStatus');
  if (_reqClearTimer) clearTimeout(_reqClearTimer);
  fetch(API.reqId, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ ID: String(id) }),
  }).then(r => {
    if (r.status === 429) throw new Error('ratelimit');
    if (!r.ok) throw new Error('fail');
    statusEl.textContent = 'Requested!';
    statusEl.className = 'nes-text is-success';
  }).catch(err => {
    statusEl.textContent = err.message === 'ratelimit'
      ? 'Rate limited. Try again later.'
      : 'Request failed.';
    statusEl.className = 'nes-text is-error';
  }).finally(() => {
    // Auto-clear status message after 4 seconds.
    _reqClearTimer = setTimeout(() => {
      statusEl.textContent = '';
      statusEl.className = '';
    }, 4000);
  });
}

// --- History ---
function formatEnded(iso) {
  if (!iso) return '';
  try {
    const d = new Date(iso);
    // Relative time if within 24 h, otherwise locale date.
    const diffMs = Date.now() - d.getTime();
    const diffMin = Math.floor(diffMs / 60000);
    if (diffMin < 1)  return 'just now';
    if (diffMin < 60) return diffMin + 'm ago';
    const diffH = Math.floor(diffMin / 60);
    if (diffH < 24)   return diffH + 'h ago';
    return d.toLocaleDateString();
  } catch (_) { return ''; }
}

function loadHistory() {
  fetch(API.history).then(r => r.json()).then(items => {
    const el = document.getElementById('historyResults');
    if (!items || items.length === 0) {
      el.innerHTML = '<p class="nes-text">No history yet.</p>';
      return;
    }
    // Build DOM nodes to prevent XSS from song metadata.
    el.innerHTML = '';
    [...items].reverse().forEach(h => {
      const div = document.createElement('div');
      div.className = 'history-item nes-container';

      const title = document.createElement('span');
      title.className = 'song-title';
      title.textContent = h.Title;

      const sep = document.createTextNode(' \u2014 ');

      const artist = document.createElement('span');
      artist.className = 'song-artist nes-text is-primary';
      artist.textContent = h.Artist;

      div.appendChild(title);
      div.appendChild(sep);
      div.appendChild(artist);

      // Timestamp: show when the track ended.
      const ended = formatEnded(h.Ended);
      if (ended) {
        const ts = document.createElement('span');
        ts.className = 'history-ts';
        ts.textContent = ' (' + ended + ')';
        ts.style.cssText = 'opacity:0.55;font-size:0.8em;margin-left:0.4em';
        div.appendChild(ts);
      }

      el.appendChild(div);
    });
  }).catch(() => {});
}
loadHistory();

// --- Volume persistence ---
// Restore saved volume on load; save on change.
(function initVolume() {
  const volEl = document.getElementById('volume');
  if (!volEl) return;
  try {
    const saved = localStorage.getItem('cadence_volume');
    if (saved !== null) volEl.value = saved;
  } catch (_) {}
  volEl.addEventListener('input', () => {
    try { localStorage.setItem('cadence_volume', volEl.value); } catch (_) {}
  });
})();

// --- Keyboard shortcut: Space bar → play/pause ---
// Only fires when focus is NOT inside the search input.
document.addEventListener('keydown', e => {
  if (e.code !== 'Space') return;
  const tag = (document.activeElement || {}).tagName;
  if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'BUTTON') return;
  e.preventDefault();
  const btn = document.getElementById('playButton');
  if (btn) btn.click();
});

// Initial info load
updateInfo();
