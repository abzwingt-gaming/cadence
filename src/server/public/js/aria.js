// aria.js - API communication layer. No jQuery.

let streamSrcURL = '';

document.addEventListener('DOMContentLoaded', () => {
  fetchVersion();
  fetchListenURL();
  fetchHistory();
  fetchNowPlayingMetadata();
  fetchNowPlayingAlbumArt();
  connectSSE();
  bindSearch();
  bindRequestButtons();
});

function apiFetch(url, opts = {}) {
  return fetch(url, opts).then(r => {
    if (!r.ok) throw new Error(r.status);
    if (r.status === 204) return null;
    return r.json();
  });
}

function fetchVersion() {
  apiFetch('/api/version')
    .then(d => { document.getElementById('release').textContent = d.Version; })
    .catch(() => { document.getElementById('release').textContent = '(N/A)'; });
}

function fetchNowPlayingMetadata() {
  apiFetch('/api/nowplaying/metadata')
    .then(d => {
      document.getElementById('song').textContent   = d ? d.Title  : '-';
      document.getElementById('artist').textContent = d ? d.Artist : '-';
    })
    .catch(() => {
      document.getElementById('song').textContent   = '-';
      document.getElementById('artist').textContent = '-';
    });
}

function fetchNowPlayingAlbumArt() {
  apiFetch('/api/nowplaying/albumart')
    .then(d => {
      const img = document.getElementById('artwork');
      img.src = (d && d.Picture)
        ? 'data:image/jpeg;base64,' + d.Picture
        : './static/blank.jpg';
    })
    .catch(() => {
      document.getElementById('artwork').src = './static/blank.jpg';
    });
}

function fetchListenURL() {
  apiFetch('/api/listenurl')
    .then(d => setStreamURL(d ? d.ListenURL : null))
    .catch(() => setStreamURL(null));
}

function setStreamURL(url) {
  const statusEl = document.getElementById('status');
  const streamEl = document.getElementById('stream');
  if (!url || url === '-/-') {
    streamEl.src = '';
    statusEl.textContent = 'Waiting for stream...';
    statusEl.className = 'nes-text is-warning';
    return;
  }
  // If URL already has a protocol leave it alone; otherwise build from page protocol
  if (!url.startsWith('http')) {
    streamSrcURL = location.protocol + '//' + url;
  } else {
    streamSrcURL = url;
  }
  streamEl.src = streamSrcURL;
  statusEl.innerHTML = 'Connected: <a href="' + streamSrcURL + '">' + streamSrcURL + '</a>';
  statusEl.className = 'nes-text is-success';
}

function fetchHistory() {
  apiFetch('/api/history')
    .then(d => renderHistory(d))
    .catch(() => {
      document.getElementById('historyStatus').textContent = 'Error loading history.';
    });
}

function renderHistory(data) {
  const statusEl  = document.getElementById('historyStatus');
  const resultsEl = document.getElementById('historyResults');
  if (!data || data.length === 0) {
    statusEl.textContent = 'No history yet.';
    resultsEl.innerHTML  = '';
    return;
  }
  statusEl.textContent = '';
  const rows = [...data].reverse().map(song => {
    const delta = Math.round((Date.now() - new Date(song.Ended)) / 1000);
    const timeAgo = formatDelta(delta);
    return `<tr><td>${timeAgo}</td><td>${esc(song.Artist)}</td><td>${esc(song.Title)}</td></tr>`;
  }).join('');
  resultsEl.innerHTML =
    `<table><thead><tr><th>Ended</th><th>Artist</th><th>Title</th></tr></thead><tbody>${rows}</tbody></table>`;
}

function formatDelta(s) {
  if (s < 30)         return 'just now';
  if (s < 60)         return s + ' sec ago';
  if (s < 120)        return '1 min ago';
  if (s < 3600)       return Math.floor(s/60) + ' min ago';
  if (s < 7200)       return '1 hour ago';
  if (s < 86400)      return Math.floor(s/3600) + ' hours ago';
  return Math.floor(s/86400) + ' days ago';
}

function esc(str) {
  return String(str).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;');
}

function bindSearch() {
  let debounceTimer;
  const input = document.getElementById('searchInput');
  input.addEventListener('keyup', e => {
    if (e.key === 'Enter') { doSearch(); return; }
    clearTimeout(debounceTimer);
    debounceTimer = setTimeout(doSearch, 300);
  });
}

function doSearch() {
  const query = document.getElementById('searchInput').value.trim();
  if (!query) return;
  apiFetch('/api/search', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ search: query }),
  })
  .then(data => renderSearchResults(data))
  .catch(() => {
    document.getElementById('requestStatus').textContent = 'Search error.';
  });
}

function renderSearchResults(data) {
  const resultsEl = document.getElementById('searchResults');
  const statusEl  = document.getElementById('requestStatus');
  if (!data || data.length === 0) {
    statusEl.textContent = 'No results.';
    resultsEl.innerHTML  = '';
    return;
  }
  statusEl.textContent = 'Results: ' + data.length;
  const rows = data.map(song =>
    `<tr>
      <td>${esc(song.Artist)}</td>
      <td>${esc(song.Title)}</td>
      <td><button class="nes-btn is-primary is-small req-btn" data-id="${song.ID}">Request</button></td>
    </tr>`
  ).join('');
  resultsEl.innerHTML =
    `<table><thead><tr><th>Artist</th><th>Title</th><th></th></tr></thead><tbody>${rows}</tbody></table>`;
}

function bindRequestButtons() {
  document.getElementById('searchResults').addEventListener('click', e => {
    if (!e.target.classList.contains('req-btn')) return;
    const id = e.target.dataset.id;
    apiFetch('/api/request/id', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ ID: id }),
    })
    .then(() => {
      document.getElementById('requestStatus').textContent = 'Request accepted!';
    })
    .catch(err => {
      const msg = err.message === '429'
        ? 'Rate limited. Try again later.'
        : 'Request failed.';
      document.getElementById('requestStatus').textContent = msg;
    });
  });
}

function connectSSE() {
  const es = new EventSource('/api/radiodata/sse');
  es.onerror = () => {
    es.close();
    setTimeout(connectSSE, 10000);
  };
  es.addEventListener('title',   e => { document.getElementById('song').textContent   = e.data; });
  es.addEventListener('artist',  e => { document.getElementById('artist').textContent = e.data; });
  es.addEventListener('listeners', e => {
    document.getElementById('listeners').textContent = e.data == '-1' ? 'N/A' : e.data;
  });
  // On title or artist update, refresh art + history
  ['title', 'artist'].forEach(ev => {
    es.addEventListener(ev, () => {
      fetchNowPlayingAlbumArt();
      fetchHistory();
    });
  });
  es.addEventListener('listenurl', e => setStreamURL(e.data));
  es.addEventListener('history',   () => fetchHistory());
}
