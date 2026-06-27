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
};

let streamSrcURL = '';

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
sse.addEventListener('listeners', e => {
  updateInfo();
});
sse.addEventListener('bitrate', e => {
  updateInfo();
});
sse.addEventListener('history', () => loadHistory());

sse.onerror = () => {
  document.getElementById('status').textContent = 'Waiting for stream...';
  document.getElementById('status').className = 'nes-text is-warning';
};

// --- Info bar: listeners + bitrate ---
function updateInfo() {
  Promise.all([
    fetch('/api/listeners').then(r => r.json()),
    fetch(API.bitrate).then(r => r.json()),
  ]).then(([l, b]) => {
    const listeners = l.Listeners >= 0 ? l.Listeners : '?';
    const bitrate   = b.Bitrate   >  0 ? b.Bitrate + ' kbps' : '';
    document.getElementById('version').textContent =
      'Listeners: ' + listeners + (bitrate ? '  ·  ' + bitrate : '');
  }).catch(() => {});
}

// --- Version ---
fetch(API.version).then(r => r.json()).then(d => {
  document.getElementById('release').textContent = d.Version || 'dev';
}).catch(() => {});

// --- Album art ---
function fetchArt() {
  fetch(API.art).then(r => {
    if (r.status === 204 || r.status === 404) {
      document.getElementById('artwork').src = './static/blank.jpg';
      return null;
    }
    return r.json();
  }).then(d => {
    if (d && d.Picture) {
      document.getElementById('artwork').src = 'data:image/jpeg;base64,' + d.Picture;
    }
  }).catch(() => {
    document.getElementById('artwork').src = './static/blank.jpg';
  });
}

// --- Search (300ms debounce) ---
let searchTimer;
document.getElementById('searchInput').addEventListener('keyup', e => {
  clearTimeout(searchTimer);
  searchTimer = setTimeout(() => doSearch(e.target.value.trim()), 300);
});

function doSearch(q) {
  if (!q) {
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
    el.innerHTML = results.map(s =>
      `<div class="search-result nes-container" data-id="${s.ID}">
        <span class="song-title">${s.Title}</span>
        <span class="song-artist nes-text is-primary"> — ${s.Artist}</span>
        <button class="nes-btn is-primary request-btn" data-id="${s.ID}">&#9654; Request</button>
      </div>`
    ).join('');
    el.querySelectorAll('.request-btn').forEach(btn => {
      btn.addEventListener('click', () => requestSong(btn.dataset.id));
    });
  }).catch(() => {
    document.getElementById('searchResults').innerHTML = '<p class="nes-text is-error">Search error.</p>';
  });
}

function requestSong(id) {
  const statusEl = document.getElementById('requestStatus');
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
  });
}

// --- History ---
function loadHistory() {
  fetch(API.history).then(r => r.json()).then(items => {
    const el = document.getElementById('historyResults');
    if (!items || items.length === 0) {
      el.innerHTML = '<p class="nes-text">No history yet.</p>';
      return;
    }
    el.innerHTML = [...items].reverse().map(h =>
      `<div class="history-item nes-container">
        <span class="song-title">${h.Title}</span>
        <span class="song-artist nes-text is-primary"> — ${h.Artist}</span>
      </div>`
    ).join('');
  }).catch(() => {});
}
loadHistory();

// Initial info load
updateInfo();
