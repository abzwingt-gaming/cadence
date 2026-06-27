// page.js — UI interaction: play/pause, volume, tabs, theme, copy stream, search clear.
// Loaded before aria.js; DOM ready via DOMContentLoaded.

document.addEventListener('DOMContentLoaded', () => {
  const stream  = document.getElementById('stream');
  const volbar  = document.getElementById('volume');
  const volDisp = document.getElementById('volDisplay');
  const playBtn = document.getElementById('playButton');

  // ── Volume ────────────────────────────────────────────────
  const VOLUME_KEY = 'cadence_volume';
  const savedVol = parseFloat(localStorage.getItem(VOLUME_KEY) ?? '0.3');
  const initVol  = isNaN(savedVol) ? 0.3 : Math.min(1, Math.max(0, savedVol));
  volbar.value   = String(initVol);
  stream.volume  = initVol;
  if (volDisp) volDisp.textContent = Math.round(initVol * 100) + '%';

  volbar.addEventListener('input', () => {
    const v = parseFloat(volbar.value);
    stream.volume = v;
    if (volDisp) volDisp.textContent = Math.round(v * 100) + '%';
    try { localStorage.setItem(VOLUME_KEY, String(v)); } catch (_) {}
  });

  // ── Play / Pause ──────────────────────────────────────────
  function setPlaying(playing) {
    if (playing) {
      playBtn.innerHTML = '&#9646;&#9646; Pause';
      playBtn.setAttribute('aria-label', 'Pause stream');
      document.body.classList.add('is-playing');
    } else {
      playBtn.innerHTML = '&#9654; Play';
      playBtn.setAttribute('aria-label', 'Play stream');
      document.body.classList.remove('is-playing');
    }
  }

  playBtn.addEventListener('click', () => {
    if (stream.paused) {
      if (typeof streamSrcURL === 'undefined' || !streamSrcURL) {
        const st = document.getElementById('status');
        st.textContent = 'Stream URL not yet received — please wait...';
        st.className = 'nes-text is-warning';
        return;
      }
      stream.src = streamSrcURL;
      stream.load();
      stream.play().catch(() => {});
      setPlaying(true);
    } else {
      stream.src = '';
      stream.load();
      stream.pause();
      setPlaying(false);
    }
  });

  // Sync play state if browser pauses the stream (e.g. network error).
  stream.addEventListener('pause',  () => setPlaying(false));
  stream.addEventListener('playing', () => setPlaying(true));

  // ── Copy stream URL ───────────────────────────────────────
  const copyBtn = document.getElementById('copyStream');
  const copyFb  = document.getElementById('copyFeedback');
  let   copyTimer = null;

  if (copyBtn) {
    copyBtn.addEventListener('click', () => {
      const url = (typeof streamSrcURL !== 'undefined' && streamSrcURL)
        ? streamSrcURL
        : window.location.origin; // fallback
      navigator.clipboard.writeText(url).then(() => {
        if (copyFb) { copyFb.textContent = 'Copied!'; }
        clearTimeout(copyTimer);
        copyTimer = setTimeout(() => { if (copyFb) copyFb.textContent = ''; }, 2500);
      }).catch(() => {
        if (copyFb) { copyFb.textContent = 'Failed'; }
      });
    });
  }

  // ── Artwork fade-in ───────────────────────────────────────
  const artwork = document.getElementById('artwork');
  if (artwork) {
    artwork.addEventListener('loadstart', () => artwork.classList.add('loading'));
    artwork.addEventListener('load',      () => artwork.classList.remove('loading'));
    artwork.addEventListener('error',     () => artwork.classList.remove('loading'));
  }

  // ── Tabs ──────────────────────────────────────────────────
  const tabBtns     = document.querySelectorAll('.tab-btn');
  const tabSections = document.querySelectorAll('#tab-content section[role="tabpanel"]');

  tabBtns.forEach(btn => {
    btn.addEventListener('click', () => {
      tabBtns.forEach(b => {
        b.classList.remove('active');
        b.setAttribute('aria-selected', 'false');
      });
      tabSections.forEach(s => {
        s.classList.remove('is-active');
        s.hidden = true;
      });
      btn.classList.add('active');
      btn.setAttribute('aria-selected', 'true');
      const target = document.getElementById('tab-' + btn.dataset.tab);
      if (target) { target.classList.add('is-active'); target.hidden = false; }
    });
  });

  // ── Search clear button ───────────────────────────────────
  const searchInput = document.getElementById('searchInput');
  const searchClear = document.getElementById('searchClear');
  const searchHint  = document.getElementById('searchHint');

  if (searchInput && searchClear) {
    searchInput.addEventListener('input', () => {
      const hasVal = searchInput.value.length > 0;
      searchClear.hidden = !hasVal;
      if (searchHint) searchHint.style.display = searchInput.value.length >= 2 ? 'none' : '';
    });
    searchClear.addEventListener('click', () => {
      searchInput.value = '';
      searchClear.hidden = true;
      document.getElementById('searchResults').innerHTML = '';
      if (searchHint) searchHint.style.display = '';
      searchInput.focus();
    });
  }

  // ── Theme ─────────────────────────────────────────────────
  const html    = document.documentElement;
  const themeBtn = document.getElementById('themeToggle');

  function applyTheme(t) {
    html.setAttribute('data-theme', t);
    if (themeBtn) themeBtn.textContent = t === 'dark' ? '\u2600' : '\u263D';
  }

  const saved     = localStorage.getItem('theme');
  const systemDark = window.matchMedia('(prefers-color-scheme: dark)').matches;
  applyTheme(saved || (systemDark ? 'dark' : 'light'));

  if (themeBtn) {
    themeBtn.addEventListener('click', () => {
      const next = html.getAttribute('data-theme') === 'dark' ? 'light' : 'dark';
      applyTheme(next);
      try { localStorage.setItem('theme', next); } catch (_) {}
    });
  }

  window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', e => {
    if (!localStorage.getItem('theme')) applyTheme(e.matches ? 'dark' : 'light');
  });
});
