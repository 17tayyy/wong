(() => {
  const css = (name) => getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  const notePath = (p) => '/notes/' + p.split('/').map(encodeURIComponent).join('/');

  // A force layout in the manner of d3-force: links pull, notes push each other apart, a weak gravity keeps orphans near.
  function graph(canvas, data, opts) {
    const ctx = canvas.getContext('2d');
    const nodes = data.nodes.map((n, i) => {
      const angle = i * Math.PI * (3 - Math.sqrt(5));
      const r = 12 * Math.sqrt(i + 0.5);
      return { ...n, i, x: r * Math.cos(angle), y: r * Math.sin(angle), vx: 0, vy: 0, deg: 0, near: new Set() };
    });
    const edges = data.edges.map(([a, b]) => ({ a: nodes[a], b: nodes[b] }));
    for (const e of edges) {
      e.a.deg++;
      e.b.deg++;
      e.a.near.add(e.b);
      e.b.near.add(e.a);
    }
    const focus = nodes.find((n) => n.p === opts.focus);
    let shown = new Set(nodes);
    let found = new Set();
    let colors = {};
    let view = { x: 0, y: 0, k: opts.local ? 1.4 : 1 };
    let hover = null;
    let drag = null;
    let alpha = 1;
    let frame = 0;
    let width = 0;
    let height = 0;
    const dpr = () => window.devicePixelRatio || 1;
    const radius = (n) => (opts.local ? 5 : 3) + Math.sqrt(n.deg) * (opts.local ? 1.8 : 1.6);
    const steps = () => Math.round(Math.min(300, Math.max(80, 60000 / Math.max(1, shown.size))));

    function palette() {
      colors = {
        bg: css('--bg'), text: css('--text'), muted: css('--muted'), node: css('--faint'), line: css('--line-strong'),
        accent: css('--accent'), red: css('--red-dot'), yellow: css('--yellow-dot'), font: css('--sans'),
      };
    }

    function resize() {
      const box = canvas.getBoundingClientRect();
      width = box.width;
      height = box.height;
      canvas.width = Math.round(width * dpr());
      canvas.height = Math.round(height * dpr());
      draw();
    }

    function tick() {
      const visible = nodes.filter((n) => shown.has(n));
      for (const e of edges) {
        if (!shown.has(e.a) || !shown.has(e.b)) continue;
        const dx = e.b.x + e.b.vx - e.a.x - e.a.vx || 1e-6;
        const dy = e.b.y + e.b.vy - e.a.y - e.a.vy || 1e-6;
        const l = Math.hypot(dx, dy);
        const strength = 1 / Math.min(e.a.deg, e.b.deg);
        const pull = ((l - (opts.local ? 60 : 46)) / l) * alpha * strength;
        const bias = e.a.deg / (e.a.deg + e.b.deg);
        e.b.vx -= dx * pull * bias;
        e.b.vy -= dy * pull * bias;
        e.a.vx += dx * pull * (1 - bias);
        e.a.vy += dy * pull * (1 - bias);
      }
      const charge = opts.local ? -220 : -70;
      for (let i = 0; i < visible.length; i++) {
        const a = visible[i];
        for (let j = i + 1; j < visible.length; j++) {
          const b = visible[j];
          let dx = b.x - a.x;
          let dy = b.y - a.y;
          let l2 = dx * dx + dy * dy;
          if (l2 > 160000) continue;
          if (l2 < 1) {
            dx = Math.random() - 0.5;
            dy = Math.random() - 0.5;
            l2 = 1;
          }
          const f = (charge * alpha) / l2;
          a.vx += dx * f;
          a.vy += dy * f;
          b.vx -= dx * f;
          b.vy -= dy * f;
        }
      }
      for (const n of visible) {
        n.vx -= n.x * 0.04 * alpha;
        n.vy -= n.y * 0.04 * alpha;
        if (n === drag?.node) {
          n.vx = n.vy = 0;
          continue;
        }
        n.vx *= 0.6;
        n.vy *= 0.6;
        n.x += n.vx;
        n.y += n.vy;
      }
      alpha += (0 - alpha) * 0.0228;
    }

    function run() {
      cancelAnimationFrame(frame);
      const step = () => {
        tick();
        draw();
        if (alpha > 0.005 || drag) frame = requestAnimationFrame(step);
      };
      frame = requestAnimationFrame(step);
    }

    function heat(to) {
      alpha = Math.max(alpha, to);
      run();
    }

    const screen = (n) => [width / 2 + view.x + n.x * view.k, height / 2 + view.y + n.y * view.k];

    function draw() {
      const s = dpr();
      ctx.setTransform(s, 0, 0, s, 0, 0);
      ctx.fillStyle = colors.bg;
      ctx.fillRect(0, 0, width, height);
      const lit = hover || (found.size ? null : focus);
      const near = lit ? lit.near : null;
      const dim = (n) => (lit && n !== lit && !near.has(n)) || (found.size && !found.has(n));
      ctx.lineWidth = 1;
      for (const e of edges) {
        if (!shown.has(e.a) || !shown.has(e.b)) continue;
        const on = lit && (e.a === lit || e.b === lit);
        ctx.globalAlpha = on ? 1 : dim(e.a) || dim(e.b) ? 0.12 : 0.7;
        ctx.strokeStyle = on ? colors.accent : colors.line;
        const [x1, y1] = screen(e.a);
        const [x2, y2] = screen(e.b);
        ctx.beginPath();
        ctx.moveTo(x1, y1);
        ctx.lineTo(x2, y2);
        ctx.stroke();
      }
      const size = (n) => radius(n) * Math.min(1.6, Math.max(0.7, view.k));
      for (const n of nodes) {
        if (!shown.has(n)) continue;
        const [x, y] = screen(n);
        if (x < -40 || y < -40 || x > width + 40 || y > height + 40) continue;
        ctx.globalAlpha = dim(n) ? 0.18 : 1;
        ctx.fillStyle = n === lit || found.has(n) ? colors.accent : n.st === 'stale' ? colors.red : n.st === 'review' ? colors.yellow : colors.node;
        ctx.beginPath();
        ctx.arc(x, y, size(n), 0, Math.PI * 2);
        ctx.fill();
      }
      ctx.font = `${opts.local ? 11.5 : 12}px ${colors.font}`;
      ctx.textAlign = 'center';
      ctx.textBaseline = 'top';
      const placed = [];
      const wanted = (n) => n === lit || (near && near.has(n)) || found.has(n);
      const order = nodes.filter((n) => shown.has(n)).sort((a, b) => wanted(b) - wanted(a) || b.deg - a.deg);
      for (const n of order) {
        const must = wanted(n);
        if (!must && !opts.local && view.k < 0.8 && n.deg < 4) continue;
        const [x, y] = screen(n);
        const name = n.n.length > 28 ? n.n.slice(0, 27) + '…' : n.n;
        const w = ctx.measureText(name).width;
        const box = [x - w / 2 - 2, y + size(n) + 2, x + w / 2 + 2, y + size(n) + 17];
        if (!must && placed.some((o) => box[0] < o[2] && box[2] > o[0] && box[1] < o[3] && box[3] > o[1])) continue;
        placed.push(box);
        ctx.globalAlpha = dim(n) ? 0.25 : 1;
        ctx.fillStyle = n === lit ? colors.text : colors.muted;
        ctx.fillText(name, x, y + size(n) + 3);
      }
      ctx.globalAlpha = 1;
    }

    function at(px, py) {
      let best = null;
      let bestD = Infinity;
      for (const n of nodes) {
        if (!shown.has(n)) continue;
        const [x, y] = screen(n);
        const d = Math.hypot(px - x, py - y);
        if (d < Math.max(8, radius(n) * view.k + 4) && d < bestD) {
          best = n;
          bestD = d;
        }
      }
      return best;
    }

    const pointers = new Map();
    let pinch = null;
    let moved = false;
    const local = (e) => {
      const box = canvas.getBoundingClientRect();
      return [e.clientX - box.left, e.clientY - box.top];
    };

    canvas.addEventListener('pointerdown', (e) => {
      canvas.setPointerCapture(e.pointerId);
      const [px, py] = local(e);
      pointers.set(e.pointerId, [px, py]);
      moved = false;
      if (pointers.size === 2) {
        const [[ax, ay], [bx, by]] = [...pointers.values()];
        pinch = { d: Math.hypot(ax - bx, ay - by), k: view.k };
        drag = null;
        return;
      }
      const n = at(px, py);
      drag = n ? { node: n, px, py } : { pan: true, px, py, x: view.x, y: view.y };
      if (n) heat(0.3);
    });
    canvas.addEventListener('pointermove', (e) => {
      const [px, py] = local(e);
      if (pointers.has(e.pointerId)) pointers.set(e.pointerId, [px, py]);
      if (pinch && pointers.size === 2) {
        const [[ax, ay], [bx, by]] = [...pointers.values()];
        view.k = Math.min(4, Math.max(0.2, (pinch.k * Math.hypot(ax - bx, ay - by)) / pinch.d));
        moved = true;
        draw();
        return;
      }
      if (drag) {
        if (Math.hypot(px - drag.px, py - drag.py) > 3) moved = true;
        if (drag.node) {
          drag.node.x = (px - width / 2 - view.x) / view.k;
          drag.node.y = (py - height / 2 - view.y) / view.k;
        } else {
          view.x = drag.x + px - drag.px;
          view.y = drag.y + py - drag.py;
          draw();
        }
        return;
      }
      const n = at(px, py);
      if (n !== hover) {
        hover = n;
        canvas.style.cursor = n ? 'pointer' : 'grab';
        canvas.title = n ? n.n + ' · ' + n.s : '';
        draw();
      }
    });
    const end = (e) => {
      pointers.delete(e.pointerId);
      if (pointers.size < 2) pinch = null;
      const d = drag;
      drag = null;
      if (d && d.node && !moved) {
        if (e.ctrlKey || e.metaKey || e.button === 1) open(notePath(d.node.p), '_blank', 'noopener');
        else location.assign(notePath(d.node.p));
      }
    };
    canvas.addEventListener('pointerup', end);
    canvas.addEventListener('pointercancel', end);
    canvas.addEventListener('pointerleave', () => {
      if (!drag && hover) {
        hover = null;
        draw();
      }
    });
    canvas.addEventListener(
      'wheel',
      (e) => {
        e.preventDefault();
        const [px, py] = local(e);
        const k = Math.min(4, Math.max(0.2, view.k * Math.exp(-e.deltaY * 0.0015)));
        view.x = px - width / 2 - ((px - width / 2 - view.x) * k) / view.k;
        view.y = py - height / 2 - ((py - height / 2 - view.y) * k) / view.k;
        view.k = k;
        draw();
      },
      { passive: false },
    );
    canvas.addEventListener('keydown', (e) => {
      const pan = { ArrowLeft: [40, 0], ArrowRight: [-40, 0], ArrowUp: [0, 40], ArrowDown: [0, -40] }[e.key];
      if (pan) {
        view.x += pan[0];
        view.y += pan[1];
      } else if (e.key === '+' || e.key === '=') view.k = Math.min(4, view.k * 1.2);
      else if (e.key === '-') view.k = Math.max(0.2, view.k / 1.2);
      else return;
      e.preventDefault();
      draw();
    });

    function filter(by) {
      const pass = (n) =>
        (!by.s || n.s === by.s) && (!by.k || n.k === by.k) && (!by.t || (n.t || []).some((t) => t.toLowerCase() === by.t));
      let keep = new Set(nodes.filter(pass));
      if (!by.orphans) {
        keep = new Set([...keep].filter((n) => [...n.near].some((m) => keep.has(m))));
      }
      shown = keep;
      const links = edges.filter((e) => keep.has(e.a) && keep.has(e.b)).length;
      alpha = 0.6;
      settle(steps());
      return [keep.size, links];
    }

    function find(text) {
      const q = text.trim().toLowerCase();
      found = new Set(q ? nodes.filter((n) => shown.has(n) && (n.n.toLowerCase().includes(q) || n.p.toLowerCase().includes(q))) : []);
      draw();
      return found;
    }

    // fit zooms and centres the view on the notes shown, leaving room for their labels.
    function fit() {
      const list = nodes.filter((n) => shown.has(n));
      if (!list.length || !width) return;
      const xs = list.map((n) => n.x);
      const ys = list.map((n) => n.y);
      const [x0, x1, y0, y1] = [Math.min(...xs), Math.max(...xs), Math.min(...ys), Math.max(...ys)];
      const pad = opts.local ? 46 : 70;
      view.k = Math.min(opts.local ? 1.6 : 2.2, Math.max(0.2, Math.min((width - pad * 2) / (x1 - x0 || 1), (height - pad * 2) / (y1 - y0 || 1))));
      view.x = -((x0 + x1) / 2) * view.k;
      view.y = -((y0 + y1) / 2) * view.k;
      draw();
    }

    function settle(ticks) {
      for (let i = 0; i < ticks; i++) tick();
      fit();
      run();
    }

    function center(n) {
      view.x = -n.x * view.k;
      view.y = -n.y * view.k;
      draw();
    }

    palette();
    new ResizeObserver(resize).observe(canvas);
    matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => {
      palette();
      draw();
    });
    resize();
    settle(steps());
    return { filter, find, center };
  }

  document.addEventListener('DOMContentLoaded', () => {
    for (const canvas of document.querySelectorAll('canvas[data-graph]')) {
      const g = graph(canvas, JSON.parse(canvas.dataset.graph), { local: canvas.classList.contains('local'), focus: canvas.dataset.focus });
      const bar = document.querySelector('.graph-bar');
      if (!bar || canvas.classList.contains('local')) continue;
      const count = bar.querySelector('.graph-count');
      const apply = () => {
        const by = { orphans: bar.querySelector('#g-orphans').checked };
        for (const select of bar.querySelectorAll('select[data-filter]')) by[select.dataset.filter] = select.value;
        const [n, m] = g.filter(by);
        count.textContent = `${n} notes · ${m} links`;
        g.find(bar.querySelector('#g-find').value);
      };
      bar.addEventListener('change', apply);
      const findInput = bar.querySelector('#g-find');
      findInput.addEventListener('input', () => g.find(findInput.value));
      findInput.addEventListener('keydown', (e) => {
        if (e.key !== 'Enter') return;
        const [first] = g.find(findInput.value);
        if (first) g.center(first);
      });
    }
  });
})();
