// page.js - UI interaction: play/pause, volume, tabs, theme toggle.

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

  // Theme toggle - persisted in localStorage
  const html = document.documentElement;
  const savedTheme = localStorage.getItem('theme') || 'dark';
  html.setAttribute('data-theme', savedTheme);
  document.getElementById('themeToggle').addEventListener('click', () => {
    const current = html.getAttribute('data-theme');
    const next    = current === 'dark' ? 'light' : 'dark';
    html.setAttribute('data-theme', next);
    localStorage.setItem('theme', next);
  });
});
