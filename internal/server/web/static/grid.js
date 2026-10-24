// The one place hypermedia is the wrong tool: a few hundred cells repainting
// many times a second. The canvas shares htmx's EventSource, so there is a
// single /events connection per browser tab.
(function () {
  'use strict';
  const canvas = document.getElementById('grid');
  const legend = document.getElementById('legend');
  const live = document.getElementById('live');
  if (!canvas) return;
  const ctx = canvas.getContext('2d');

  const CELL = 16, GAP = 3, SLOW_MS = 500;
  let cols = 0, rows = 0, cells = [], cursor = 0, dirty = true, dpr = 1;

  function resize() {
    const wrap = canvas.parentElement;
    const w = Math.max(200, wrap.clientWidth - 20);
    const h = Math.max(240, Math.min(560, Math.round(w * 0.52)));
    dpr = window.devicePixelRatio || 1;
    canvas.width = Math.round(w * dpr);
    canvas.height = Math.round(h * dpr);
    canvas.style.width = w + 'px';
    canvas.style.height = h + 'px';
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    const nc = Math.floor((w + GAP) / (CELL + GAP));
    const nr = Math.floor((h + GAP) / (CELL + GAP));
    if (nc !== cols || nr !== rows) {
      const old = cells.filter(Boolean);
      cols = nc; rows = nr;
      cells = new Array(cols * rows).fill(null);
      cursor = 0;
      old.slice(-cells.length).forEach(push);
    }
    dirty = true;
  }

  function push(hit) {
    if (!cells.length) return;
    cells[cursor] = hit;
    cursor = (cursor + 1) % cells.length;
    dirty = true;
  }

  function draw() {
    if (dirty) {
      dirty = false;
      const w = canvas.width / dpr, h = canvas.height / dpr;
      ctx.clearRect(0, 0, w, h);
      const ox = Math.floor((w - (cols * (CELL + GAP) - GAP)) / 2);
      let filled = 0;
      for (let i = 0; i < cells.length; i++) {
        const x = ox + (i % cols) * (CELL + GAP), y = Math.floor(i / cols) * (CELL + GAP);
        const c = cells[i];
        if (!c) {
          ctx.fillStyle = 'rgba(255,255,255,0.045)';
          ctx.fillRect(x, y, CELL, CELL);
          continue;
        }
        filled++;
        const failed = c.err || c.status >= 500;
        const slow = c.latency_ms > SLOW_MS;
        ctx.globalAlpha = slow ? 0.38 : 1;
        ctx.fillStyle = c.color || '#8E9AAD';
        ctx.fillRect(x, y, CELL, CELL);
        ctx.globalAlpha = 1;
        if (failed) {
          ctx.strokeStyle = '#FF5C5C';
          ctx.lineWidth = 2.2;
          ctx.beginPath();
          ctx.moveTo(x + 3, y + CELL - 3);
          ctx.lineTo(x + CELL - 3, y + 3);
          ctx.stroke();
        }
      }
      if (filled) {
        const i = (cursor - 1 + cells.length) % cells.length;
        const x = ox + (i % cols) * (CELL + GAP), y = Math.floor(i / cols) * (CELL + GAP);
        ctx.strokeStyle = '#F5C842';
        ctx.lineWidth = 2;
        ctx.strokeRect(x - 1, y - 1, CELL + 2, CELL + 2);
      } else {
        ctx.fillStyle = '#8E9AAD';
        ctx.font = '14px ' + getComputedStyle(document.body).fontFamily;
        ctx.textAlign = 'center';
        ctx.fillText('No traffic yet. Start it in the Traffic panel.', w / 2, h / 2);
      }
      updateLegend();
    }
    requestAnimationFrame(draw);
  }

  let legendAt = 0;
  function updateLegend() {
    const now = performance.now();
    if (now - legendAt < 250) return;
    legendAt = now;
    const by = new Map();
    let total = 0;
    for (const c of cells) {
      if (!c) continue;
      total++;
      const k = c.version || 'unknown';
      const e = by.get(k) || { color: c.color, n: 0, err: 0 };
      e.n++;
      if (c.err || c.status >= 500) e.err++;
      by.set(k, e);
    }
    const parts = [];
    [...by.entries()].sort().forEach(([v, e]) => {
      const pct = Math.round(100 * e.n / total);
      parts.push('<span><i class="swatch" style="background:' + (e.color || '#8E9AAD') + '"></i><b>' + esc(v) + '</b> ' + pct + '%' +
        (e.err ? ' <span class="warn">' + e.err + ' failed</span>' : '') + '</span>');
    });
    if (total) parts.push('<span>' + total + ' on screen</span>');
    legend.innerHTML = parts.join('');
  }

  function esc(s) { return String(s).replace(/[&<>"']/g, m => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[m])); }

  function setLive(state, label) {
    if (!live) return;
    live.dataset.state = state;
    live.lastElementChild.textContent = label;
  }

  // Share htmx's EventSource instead of opening a second one.
  document.body.addEventListener('htmx:sseOpen', function (e) {
    const src = e.detail && e.detail.source;
    if (!src || src.__canarybird) return;
    src.__canarybird = true;
    src.addEventListener('hit', ev => { try { push(JSON.parse(ev.data)); } catch (_) { } });
    src.addEventListener('hello', ev => {
      try { const id = JSON.parse(ev.data); setLive('live', 'live · ' + id.pod); } catch (_) { setLive('live', 'live'); }
    });
    setLive('live', 'live');
  });
  document.body.addEventListener('htmx:sseError', () => setLive('offline', 'reconnecting'));
  document.body.addEventListener('htmx:sseClose', () => setLive('offline', 'disconnected'));

  window.addEventListener('resize', resize);
  resize();
  requestAnimationFrame(draw);
})();
