// pointer.js: the remote machine's mouse pointer drawn by the page (see
// "The pointer" in protocol.go), and the mouse lock. Shared by the
// standalone viewer (viewer.html, which gets it inlined where it is
// served) and the web console's Desktop tab (internal/web serves it as
// assets/pointer.js). Helpers only: each page keeps its own state and
// wording.
'use strict';
window.RemoHopPointer = (function () {
  // Shapes kept: more than the host remembers having sent
  // (CursorShapesKept, 32), since it names those by ID only.
  const maxShapes = 64;

  function bytes(b64) {
    const s = atob(b64 || '');
    const out = new Uint8Array(s.length);
    for (let i = 0; i < s.length; i++) out[i] = s.charCodeAt(i);
    return out;
  }

  // shapes holds one session's pointer images by ID. add takes the shapes
  // of a "cursor" message; each is decoded to an ImageBitmap (bmp), and
  // ready is called once it is. Entries are {w, h, hx, hy, bmp}: width,
  // height and hotspot in host pixels.
  function shapes(ready) {
    const m = new Map();
    return {
      get: id => m.get(id),
      size: () => m.size,
      decoded: () => [...m.values()].filter(e => e.bmp).length,
      clear: () => m.clear(),
      add(list) {
        for (const sh of list || []) {
          const e = {w: sh.width, h: sh.height, hx: sh.hot_x, hy: sh.hot_y, bmp: null, css: new Map()};
          m.delete(sh.id);
          m.set(sh.id, e);
          if (m.size > maxShapes) m.delete(m.keys().next().value);
          createImageBitmap(new Blob([bytes(sh.png)], {type: 'image/png'}))
            .then(b => { e.bmp = b; ready(); })
            .catch(err => console.warn('pointer image', err));
        }
      },
    };
  }

  function scaled(e, w, h) {
    const c = document.createElement('canvas');
    c.width = w; c.height = h;
    const g = c.getContext('2d');
    g.imageSmoothingQuality = 'high';
    g.drawImage(e.bmp, 0, 0, w, h);
    return c.toDataURL('image/png');
  }

  // css is the CSS cursor that shows shape e at k CSS pixels per host
  // pixel: {plain, sharp}, sharp being for a high-density display
  // (image-set, where the browser takes it for cursors). Browsers ignore
  // cursor images larger than 128 CSS pixels, so a large one is made
  // smaller.
  function css(e, k) {
    k = Math.min(k, 128 / e.w, 128 / e.h);
    const dpr = window.devicePixelRatio || 1;
    const key = k.toFixed(3) + '@' + dpr;
    let v = e.css.get(key);
    if (v) return v;
    const cw = Math.max(1, Math.round(e.w * k)), ch = Math.max(1, Math.round(e.h * k));
    const hx = Math.min(cw - 1, Math.round(e.hx * k)), hy = Math.min(ch - 1, Math.round(e.hy * k));
    v = {plain: `url(${scaled(e, cw, ch)}) ${hx} ${hy}, default`, sharp: ''};
    if (dpr !== 1) {
      const dw = Math.max(1, Math.round(cw * dpr)), dh = Math.max(1, Math.round(ch * dpr));
      v.sharp = `image-set(url(${scaled(e, dw, dh)}) ${dpr}x) ${hx} ${hy}, default`;
    }
    if (e.css.size >= 16) e.css.clear();
    e.css.set(key, v);
    return v;
  }

  // setCSS puts cursor v (a keyword, or what css returns) on el.
  function setCSS(el, v) {
    if (typeof v === 'string') { el.style.cursor = v; return; }
    el.style.cursor = v.plain;
    if (v.sharp) el.style.cursor = v.sharp; // left as plain where the browser rejects it
  }

  // look decides how the page shows the pointer, from o: on (the latest
  // hello has cursor set), input (the page sends input), state (the
  // host's latest {id, x, y, visible}), shapes, k (CSS pixels per host
  // pixel), w and h (hello.screen_width, screen_height), locked, inside
  // (the mouse is over the picture), warped (the host moved the pointer
  // itself) and inVideo (the CSS cursor while the pointer is in the
  // video). The result: css, the cursor over the picture; overlay,
  // whether to draw the pointer where the host says it is, because the
  // local one cannot stand for it; and shape, the decoded shape (null: a
  // plain arrow).
  function look(o) {
    const s = o.state;
    const e = o.on && s && s.visible ? o.shapes.get(s.id) : null;
    const shape = e && e.bmp ? e : null;
    let c;
    if (!o.on) c = o.inVideo;
    else if (!o.input || !s) c = 'default';
    else if (!s.visible) c = 'none';
    else if (shape) c = css(shape, o.k);
    else c = 'default';
    const overlay = !!(o.on && s && s.visible && (o.locked || !o.input || !o.inside || o.warped) &&
      s.x >= 0 && s.y >= 0 && s.x < o.w && s.y < o.h);
    return {css: c, overlay, shape};
  }

  // paint draws shape e (null: a plain arrow) at k CSS pixels per host
  // pixel into canvas o, sized to it, and returns the hotspot in CSS
  // pixels from o's top-left.
  const painted = new WeakMap();
  function paint(o, e, k) {
    const dpr = window.devicePixelRatio || 1;
    const kk = e ? k : 1;
    const w = e ? e.w : 12, h = e ? e.h : 19, hx = e ? e.hx : 0, hy = e ? e.hy : 0;
    const cw = Math.max(1, Math.round(w * kk)), ch = Math.max(1, Math.round(h * kk));
    const key = cw + 'x' + ch + '@' + dpr;
    const was = painted.get(o);
    if (!was || was.e !== e || was.key !== key) {
      painted.set(o, {e, key});
      o.width = Math.round(cw * dpr); o.height = Math.round(ch * dpr);
      o.style.width = cw + 'px'; o.style.height = ch + 'px';
      const g = o.getContext('2d');
      g.clearRect(0, 0, o.width, o.height);
      if (e) {
        g.imageSmoothingQuality = 'high';
        g.drawImage(e.bmp, 0, 0, o.width, o.height);
      } else {
        const u = o.width / 12;
        g.beginPath();
        [[0, 0], [0, 16], [4, 12.5], [7, 18], [9, 17], [6.5, 11.5], [11, 11.5]].forEach(([x, y], i) => i ? g.lineTo(x * u + u / 2, y * u + u / 2) : g.moveTo(x * u + u / 2, y * u + u / 2));
        g.closePath();
        g.fillStyle = '#000'; g.fill();
        g.lineWidth = u; g.strokeStyle = '#fff'; g.stroke();
      }
    }
    return {x: hx * kk, y: hy * kk};
  }

  // placed remembers where the page recently put the remote pointer, in
  // host pixels, to tell those moves from ones made on the host (an
  // application moved it): near says whether x, y is within tol of one
  // made in the last ms milliseconds.
  function placed() {
    let a = [];
    return {
      add(x, y) {
        a.push({t: performance.now(), x, y});
        if (a.length > 256) a.splice(0, a.length - 256);
      },
      near(x, y, tol, ms) {
        const now = performance.now();
        a = a.filter(p => now - p.t < ms);
        return a.some(p => Math.abs(p.x - x) <= tol && Math.abs(p.y - y) <= tol);
      },
      clear() { a = []; },
    };
  }

  // lock locks the mouse to el (Pointer Lock), asking for raw movement
  // (unadjustedMovement) first, so the local pointer acceleration (macOS's
  // in particular) does not add to the host's; fail(error) is called if
  // the browser refuses. The pointerlockchange event says when it holds.
  function lock(el, fail) {
    let p;
    try { p = el.requestPointerLock({unadjustedMovement: true}); } catch (e) { fail(e); return; }
    if (p && p.catch) {
      p.catch(e => {
        if (!e || e.name !== 'NotSupportedError') { fail(e); return; }
        // No raw movement on this system: the plain lock.
        let q;
        try { q = el.requestPointerLock(); } catch (e2) { fail(e2); return; }
        if (q && q.catch) q.catch(fail);
      });
    }
  }

  // isLockKey: Ctrl+Alt+Shift+M, the pages' shortcut that locks or
  // releases the mouse, which is never passed on to the host.
  const isLockKey = e => e.code === 'KeyM' && e.ctrlKey && e.altKey && e.shiftKey;

  return {bytes, shapes, css, setCSS, look, paint, placed, lock, isLockKey};
})();
