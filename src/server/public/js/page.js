// page.js - UI interaction: play/pause, volume, tabs, theme.
// Runs before aria.js; DOM is guaranteed by DOMContentLoaded.

document.addEventListener('DOMContentLoaded', () => {
  const stream  = document.getElementById('stream');
  const volbar  = document.getElementById('volume');
  const playBtn = document.getElementById('playButton');

  // ── Volume ────────────────────────────────────────────────
  // Key must match aria.js ('cadence_volume') so both scripts share the same value.
  const VOLUME_KEY = 'cadence_volume';
  const savedVol = parseFloat(localStorage.getItem(VOLUME_KEY) ?? '0.3');
  volbar.value = String(isNaN(savedVol) ? 0.3 : savedVol);
  stream.volume = isNaN(savedVol) ? 0.3 : savedVol;

  volbar.addEventListener('input', () => {
    const v = parseFloat(volbar.value);
    stream.volume = v;
    try { localStorage.setItem(VOLUME_KEY, String(v)); } catch (_) {}
  });

  // ── Play / Pause ──────────────────────────────────────────
  playBtn.addEventListener('click', () => {
    if (stream.paused) {
      // streamSrcURL is set by the SSE 'listenurl' event (aria.js).
      // Guard against clicking play before the stream URL has arrived.
      if (typeof streamSrcURL === 'undefined' || !streamSrcURL) {
        const st = document.getElementById('status');
        st.textContent = 'Stream URL not yet received — please wait...';
        st.className = 'nes-text is-warning';
        return;
      }
      stream.src = streamSrcURL;
      stream.load();
      stream.play().catch(() => {});
      playBtn.innerHTML = '&#9646;&#9646;';
      playBtn.setAttribute('aria-label', 'Pause stream');
    } else {
      stream.src = '';
      stream.load();
      stream.pause();
      playBtn.innerHTML = '&#9654;';
      playBtn.setAttribute('aria-label', 'Play stream');
    }
  });

  // ── Tabs ──────────────────────────────────────────────────
  const tabBtns     = document.querySelectorAll('.tab-btn');
  const tabSections = document.querySelectorAll('#tab-content section[role="tabpanel"]');

  tabBtns.forEach(btn => {
    btn.addEventListener('click', () => {
      tabBtns.forEach(b => {
        b.classList.remove('active', 'is-primary');
        b.setAttribute('aria-selected', 'false');
      });
      tabSections.forEach(s => {
        s.classList.remove('is-active');
        s.hidden = true;
      });
      btn.classList.add('active', 'is-primary');
      btn.setAttribute('aria-selected', 'true');
      const target = document.getElementById('tab-' + btn.dataset.tab);
      if (target) {
        target.classList.add('is-active');
        target.hidden = false;
      }
    });
  });

  // ── Theme ─────────────────────────────────────────────────
  // Priority: 1) localStorage (user explicit), 2) prefers-color-scheme, 3) dark.
  const html = document.documentElement;
  const saved = localStorage.getItem('theme');
  const systemDark = window.matchMedia('(prefers-color-scheme: dark)').matches;
  html.setAttribute('data-theme', saved || (systemDark ? 'dark' : 'light'));

  document.getElementById('themeToggle').addEventListener('click', () => {
    const next = html.getAttribute('data-theme') === 'dark' ? 'light' : 'dark';
    html.setAttribute('data-theme', next);
    try { localStorage.setItem('theme', next); } catch (_) {}
  });

  // React to OS theme changes only when user hasn't overridden manually.
  window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', e => {
    if (!localStorage.getItem('theme')) {
      html.setAttribute('data-theme', e.matches ? 'dark' : 'light');
    }
  });

  // ── Album art fade-in ─────────────────────────────────────
  // aria.js calls fetchArt() on title change; we hook the img events here
  // so the loading dim + fade-in works regardless of where src is set.
  const artwork = document.getElementById('artwork');
  if (artwork) {
    artwork.addEventListener('loadstart', () => artwork.classList.add('loading'));
    artwork.addEventListener('load',      () => artwork.classList.remove('loading'));
    artwork.addEventListener('error',     () => artwork.classList.remove('loading'));
  }
});
