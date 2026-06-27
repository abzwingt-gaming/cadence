// page.js - UI interaction: play/pause, volume, tabs, theme.

document.addEventListener('DOMContentLoaded', () => {
  const stream  = document.getElementById('stream');
  const volbar  = document.getElementById('volume');
  const playBtn = document.getElementById('playButton');

  // Restore volume
  volbar.value = stream.volume = parseFloat(localStorage.getItem('volumeKey') || '0.3');
  volbar.addEventListener('input', () => {
    stream.volume = parseFloat(volbar.value);
    localStorage.setItem('volumeKey', volbar.value);
  });

  // Play/pause
  playBtn.addEventListener('click', () => {
    if (stream.paused) {
      stream.src = streamSrcURL;
      stream.load();
      stream.play();
      playBtn.innerHTML = '&#9646;&#9646;';
    } else {
      stream.src = '';
      stream.load();
      stream.pause();
      playBtn.innerHTML = '&#9654;';
    }
  });

  // Tabs
  document.querySelectorAll('.tab-btn').forEach(btn => {
    btn.addEventListener('click', () => {
      document.querySelectorAll('.tab-btn').forEach(b => b.classList.remove('active', 'is-primary'));
      document.querySelectorAll('#tab-content section').forEach(s => s.classList.remove('is-active'));
      btn.classList.add('active', 'is-primary');
      document.querySelector(`section[data-content="${btn.dataset.tab}"]`).classList.add('is-active');
    });
  });

  // Theme: priority order:
  //   1. localStorage (user explicitly toggled)
  //   2. prefers-color-scheme (OS/browser setting)
  //   3. fallback: dark
  const html = document.documentElement;
  const saved = localStorage.getItem('theme');
  const systemDark = window.matchMedia('(prefers-color-scheme: dark)').matches;
  const initialTheme = saved || (systemDark ? 'dark' : 'light');
  html.setAttribute('data-theme', initialTheme);

  document.getElementById('themeToggle').addEventListener('click', () => {
    const next = html.getAttribute('data-theme') === 'dark' ? 'light' : 'dark';
    html.setAttribute('data-theme', next);
    localStorage.setItem('theme', next);
  });

  // Also react to OS theme changes live (e.g. auto dark mode at sunset)
  window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', e => {
    // Only follow OS if user hasn't overridden manually
    if (!localStorage.getItem('theme')) {
      html.setAttribute('data-theme', e.matches ? 'dark' : 'light');
    }
  });
});
