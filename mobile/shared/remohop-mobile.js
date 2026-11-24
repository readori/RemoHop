// remohop-mobile.js: injected by the RemoHop Android and iOS apps into the
// `remohop web` gateway page at document start (Android: androidx.webkit
// addDocumentStartJavaScript, iOS: WKUserScript). The page's own assets are
// unchanged; this script adds what a phone needs on top of them:
//
//   - touch gestures on the remote desktop: tap = click, two-finger tap and
//     long press = right click, drag = move with the left button held,
//     two-finger drag = scroll, pinch = zoom (and pan) the local view;
//   - a soft keyboard for the desktop with a bar of special keys and sticky
//     modifiers, clipboard buttons, zoom reset and full screen;
//   - native clipboard, downloads, keep-screen-on, full screen, back button
//     and settings through the app's bridge;
//   - its own labels in the page's language (English or Simplified
//     Chinese). The app picks the language: it loads the page with ?lang=
//     and calls remohopMobile.setLang when the user changes it in Settings.
//
// It never sees or sends the gateway token: it talks to the page's own
// WebSocket and API, and to the app through a bridge that the app exposes
// to the gateway's origin only. Text from the host is only ever placed with
// textContent.
(function () {
  'use strict';
  if (window.remohopMobile) return;

  // ---- keep the gateway session across a new WebView ----
  // The page keeps its gateway session id in sessionStorage, which a new
  // WebView does not have: after the Activity is recreated (the system
  // destroyed it in the background, a locale change) or the renderer
  // process died, the page would start a new session and the host
  // connections of the old one would be orphaned. Mirror the id into
  // localStorage (same gateway origin, so same process) and restore it
  // before the page reads it. A stale id is harmless: the gateway starts a
  // new session for an id it does not know.
  try {
    const KEY = 'sd.sid', MIRROR = 'remohop.mobile.sid';
    if (!sessionStorage.getItem(KEY)) {
      const v = localStorage.getItem(MIRROR);
      if (v) sessionStorage.setItem(KEY, v);
    }
    const setItem = Storage.prototype.setItem;
    Storage.prototype.setItem = function (k, v) {
      setItem.call(this, k, v);
      if (this === sessionStorage && k === KEY) { try { localStorage.setItem(MIRROR, String(v)); } catch (e) { /* ignore */ } }
    };
  } catch (e) { /* storage blocked: the page works, sessions just do not survive a new WebView */ }

  // ---- bridge to the app ----
  // iOS: WKScriptMessageHandlerWithReply "remohop" (postMessage returns a
  // promise). Android: WebViewCompat.addWebMessageListener "remohopNative"
  // (postMessage of a string; replies arrive on onmessage).
  const native = (function () {
    const pending = new Map();
    let seq = 0, hooked = null;
    const ios = () => window.webkit && window.webkit.messageHandlers && window.webkit.messageHandlers.remohop;
    const android = () => window.remohopNative;
    function hook(a) {
      if (hooked === a) return;
      hooked = a;
      a.onmessage = ev => {
        let m;
        try { m = JSON.parse(ev.data); } catch (e) { return; }
        const p = pending.get(m.id);
        if (!p) return;
        pending.delete(m.id);
        if (m.error) p.reject(new Error(m.error)); else p.resolve(m.result);
      };
    }
    return {
      available: () => !!(ios() || android()),
      call(method, args) {
        const i = ios();
        if (i) return Promise.resolve(i.postMessage({method, args: args || {}}));
        const a = android();
        if (!a) return Promise.reject(new Error('not running in the RemoHop app'));
        hook(a);
        return new Promise((resolve, reject) => {
          const id = ++seq;
          pending.set(id, {resolve, reject});
          a.postMessage(JSON.stringify({id, method, args: args || {}}));
        });
      },
      post(method, args) { this.call(method, args).catch(() => {}); },
    };
  })();

  // ---- the desktop WebSocket ----
  // desktop.js keeps its socket in a closure; wrapping the constructor is
  // how input reaches it without touching the gateway's assets.
  const desk = {sock: null, hello: null};
  const NativeWS = window.WebSocket;
  if (NativeWS) {
    window.WebSocket = class extends NativeWS {
      constructor(url, protocols) {
        super(url, protocols);
        try { if (/\/api\/v1\/desktop\/ws(\?|$)/.test(String(url))) trackDesktop(this); } catch (e) { /* never break the page */ }
      }
    };
  }
  function trackDesktop(s) {
    desk.sock = s; desk.hello = null;
    s.addEventListener('message', e => {
      if (typeof e.data !== 'string' || e.data.indexOf('"hello"') < 0) return;
      try { const m = JSON.parse(e.data); if (m.type === 'hello') desk.hello = m; } catch (er) { /* ignore */ }
    });
    s.addEventListener('open', state);
    s.addEventListener('close', () => { if (desk.sock === s) { desk.sock = null; desk.hello = null; } state(); });
  }
  const $ = id => document.getElementById(id);

  // ---- language ----
  // This script's strings go into the page's i18n.js (window.SDI18n), so
  // they switch with the page; without it (an older gateway, the test
  // harness) a small stand-in with the same calls does the job.
  const MD = {
    en: {
      'm.kbd.input': 'Keyboard input for the remote desktop',
      'm.bar': 'Remote keyboard',
      'm.kbd': 'Show or hide the keyboard',
      'm.sticky': '{key} (applies to the next key)',
      'm.left': 'Left',
      'm.up': 'Up',
      'm.down': 'Down',
      'm.right': 'Right',
      'm.pgup': 'Page up',
      'm.pgdn': 'Page down',
      'm.paste': 'Paste',
      'm.paste.aria': 'Paste this device\'s clipboard on the remote machine',
      'm.copy': 'Copy',
      'm.copy.aria': 'Copy the remote clipboard to this device',
      'm.zoom': 'Reset zoom',
      'm.fs': 'Full screen',
      'm.fs.btn': 'Fullscreen',
      'm.exitfs': 'Exit full screen',
      'm.noread': 'Could not read this device\'s clipboard.',
      'm.notext': 'This device\'s clipboard has no text.',
      'm.refused': 'The host refused: {msg}',
      'm.pasted': 'Pasted {n} characters.',
      'm.copied.remote': 'Copied {n} characters to the remote clipboard.',
      'm.image': 'The remote clipboard holds an image; only text is copied.',
      'm.empty': 'The remote clipboard is empty.',
      'm.copied.local': 'Copied {n} characters to this device.',
      'm.dlfailed': 'Download failed: {msg}',
      'm.settings': 'App settings: language, rendezvous server, device name, identity',
      'm.norv': 'No rendezvous server is set. Set your rendezvous server in the app\'s settings (⚙ at the top), or connect by address (host:port).',
    },
    zh: {
      'm.kbd.input': '远程桌面的键盘输入',
      'm.bar': '远程键盘',
      'm.kbd': '显示或隐藏键盘',
      'm.sticky': '{key}（作用于下一个按键）',
      'm.left': '左方向键',
      'm.up': '上方向键',
      'm.down': '下方向键',
      'm.right': '右方向键',
      'm.pgup': 'Page Up',
      'm.pgdn': 'Page Down',
      'm.paste': '粘贴',
      'm.paste.aria': '把本设备剪贴板里的内容粘贴到远程电脑',
      'm.copy': '复制',
      'm.copy.aria': '把远程剪贴板的内容复制到本设备',
      'm.zoom': '重置缩放',
      'm.fs': '全屏',
      'm.fs.btn': '全屏',
      'm.exitfs': '退出全屏',
      'm.noread': '无法读取本设备的剪贴板。',
      'm.notext': '本设备的剪贴板里没有文本。',
      'm.refused': '被控端拒绝了：{msg}',
      'm.pasted': '已粘贴 {n} 个字符。',
      'm.copied.remote': '已将 {n} 个字符复制到远程剪贴板。',
      'm.image': '远程剪贴板里是图片，只能复制文本。',
      'm.empty': '远程剪贴板是空的。',
      'm.copied.local': '已将 {n} 个字符复制到本设备。',
      'm.dlfailed': '下载失败：{msg}',
      'm.settings': '应用设置：语言、汇合服务器、设备名称、身份',
      'm.norv': '尚未设置汇合服务器。请在应用设置（顶部的 ⚙）中设置，或直接用地址（host:port）连接。',
    },
  };
  let I18N = null;
  function i18n() {
    if (I18N) return I18N;
    if (window.SDI18n) { window.SDI18n.add(MD); I18N = window.SDI18n; return I18N; }
    const norm = l => { l = String(l || '').toLowerCase(); return l.indexOf('zh') === 0 ? 'zh' : l.indexOf('en') === 0 ? 'en' : ''; };
    let q = '';
    try { q = norm(new URLSearchParams(location.search).get('lang')); } catch (e) { /* ignore */ }
    let lang = q || (norm(navigator.language) === 'zh' ? 'zh' : 'en');
    const t = (k, v) => ((MD[lang] && MD[lang][k]) || MD.en[k] || k).replace(/\{(\w+)\}/g, (m, x) => v && x in v ? String(v[x]) : m);
    const vars = (el, n) => { try { return JSON.parse(el.getAttribute(n + '-vars') || 'null') || undefined; } catch (e) { return undefined; } };
    const bind = (el, n, k, v) => { el.setAttribute(n, k); if (v) el.setAttribute(n + '-vars', JSON.stringify(v)); else el.removeAttribute(n + '-vars'); };
    I18N = {
      t, lang: () => lang,
      say(el, k, v) { bind(el, 'data-i18n', k, v); el.textContent = t(k, v); },
      sayAttr(el, a, k, v) { bind(el, 'data-i18n-' + a, k, v); el.setAttribute(a, t(k, v)); },
      set(l) {
        l = norm(l);
        if (!l || l === lang) return;
        lang = l;
        for (const el of document.querySelectorAll('[data-i18n]')) el.textContent = t(el.getAttribute('data-i18n'), vars(el, 'data-i18n'));
        for (const el of document.querySelectorAll('[data-i18n-aria-label]')) el.setAttribute('aria-label', t(el.getAttribute('data-i18n-aria-label'), vars(el, 'data-i18n-aria-label')));
        window.dispatchEvent(new CustomEvent('remohop:lang', {detail: {lang}}));
      },
    };
    return I18N;
  }
  const tr = (k, v) => i18n().t(k, v);

  const inputOK = () => !!(desk.sock && desk.sock.readyState === 1 && desk.hello && desk.hello.input &&
    !($('d-viewonly') && $('d-viewonly').checked));
  function send(m) {
    if (!inputOK()) return false;
    desk.sock.send(JSON.stringify(m));
    return true;
  }

  let fullscreen = false;
  let lastState = '';
  function state() {
    const s = {keepScreenOn: !!(desk.sock && desk.sock.readyState <= 1), fullscreen};
    const k = JSON.stringify(s);
    if (k !== lastState) { lastState = k; native.post('state', s); }
  }

  // ---- clipboard: the page's navigator.clipboard calls go to the app ----
  function patchClipboard() {
    const readText = () => native.call('clipboard.read').then(r => (r && r.text) || '');
    const writeText = text => native.call('clipboard.write', {text: String(text)}).then(() => undefined);
    try {
      if (navigator.clipboard) {
        const cb = navigator.clipboard, r0 = cb.readText && cb.readText.bind(cb), w0 = cb.writeText && cb.writeText.bind(cb);
        cb.readText = () => native.available() ? readText() : r0();
        cb.writeText = t => native.available() ? writeText(t) : w0(t);
      } else {
        Object.defineProperty(navigator, 'clipboard', {configurable: true, value: {readText, writeText}});
      }
    } catch (e) { /* leave the browser's own */ }
  }
  patchClipboard();

  // ---- keys ----
  // US layout: character -> [code, shift]. The host presses physical keys,
  // so other characters need the gateway's text input (see sendText).
  const keymap = (() => {
    const m = {' ': ['Space', false], '\n': ['Enter', false], '\t': ['Tab', false]};
    for (let i = 0; i < 26; i++) {
      const c = String.fromCharCode(97 + i), code = 'Key' + c.toUpperCase();
      m[c] = [code, false]; m[c.toUpperCase()] = [code, true];
    }
    const shifted = ')!@#$%^&*(';
    for (let i = 0; i < 10; i++) { m[String(i)] = ['Digit' + i, false]; m[shifted[i]] = ['Digit' + i, true]; }
    const pairs = [['-', '_', 'Minus'], ['=', '+', 'Equal'], ['[', '{', 'BracketLeft'], [']', '}', 'BracketRight'],
      ['\\', '|', 'Backslash'], [';', ':', 'Semicolon'], ['\'', '"', 'Quote'], [',', '<', 'Comma'], ['.', '>', 'Period'],
      ['/', '?', 'Slash'], ['`', '~', 'Backquote']];
    for (const [a, b, code] of pairs) { m[a] = [code, false]; m[b] = [code, true]; }
    return m;
  })();
  const sticky = new Set(); // ControlLeft, AltLeft, ShiftLeft, MetaLeft
  const key = (code, down) => send({type: 'key', code, down});
  function press(code, extra) {
    const mods = [...sticky, ...(extra || [])];
    for (const m of mods) key(m, true);
    key(code, true); key(code, false);
    for (const m of mods.reverse()) key(m, false);
    if (sticky.size) { sticky.clear(); renderSticky(); }
  }
  function sendText(text) {
    let other = '';
    const flush = () => { if (other) { send({type: 'text', text: other}); other = ''; } };
    for (const ch of text) {
      const k = keymap[ch];
      if (!k) { other += ch; continue; }
      flush();
      press(k[0], k[1] && !sticky.has('ShiftLeft') ? ['ShiftLeft'] : []);
    }
    flush();
  }

  // ---- UI built once the page's DOM exists ----
  function onReady(fn) {
    if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', fn, {once: true}); else fn();
  }
  const css = (el, o) => { for (const k of Object.keys(o)) el.style[k] = o[k]; return el; };

  let toastEl = null, toastTimer = 0;
  function toast(text) {
    if (!toastEl) {
      toastEl = css(document.createElement('div'), {position: 'fixed', left: '50%', top: '12px', transform: 'translateX(-50%)',
        maxWidth: '90vw', padding: '8px 14px', borderRadius: '8px', background: 'rgba(20,20,20,.92)', color: '#fff',
        font: '14px system-ui, sans-serif', zIndex: '2147483647', pointerEvents: 'none'});
      toastEl.setAttribute('role', 'status');
      document.body.appendChild(toastEl);
    }
    toastEl.textContent = text; toastEl.hidden = false;
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => { toastEl.hidden = true; }, 2600);
  }

  let canvas = null, stage = null, bar = null, input = null, kbdBtn = null, renderSticky = () => {};
  const zoom = {s: 1, tx: 0, ty: 0};

  onReady(() => {
    i18n();
    canvas = $('d-canvas'); stage = $('d-stage');
    interceptDownloads();
    addSettingsButton();
    watchConnectErrors();
    if (!canvas || !stage) return;
    css(canvas, {touchAction: 'none', webkitUserSelect: 'none', userSelect: 'none', webkitTouchCallout: 'none'});
    setupGestures();
    setupKeyboard();
    setupFullscreen();
    const panel = $('panel-desktop');
    if (panel) new MutationObserver(updateBar).observe(panel, {attributes: true, attributeFilter: ['hidden']});
    const ws = $('workspace');
    if (ws) new MutationObserver(updateBar).observe(ws, {attributes: true, attributeFilter: ['hidden']});
    addEventListener('resize', () => { resetZoom(); if (fullscreen) sizeFullscreen(); positionBar(); });
    if (window.visualViewport) {
      visualViewport.addEventListener('resize', positionBar);
      visualViewport.addEventListener('scroll', positionBar);
    }
    updateBar();
    native.post('ready', {});
  });

  // ---- zoom ----
  function applyZoom() {
    const id = zoom.s === 1 && zoom.tx === 0 && zoom.ty === 0;
    canvas.style.transformOrigin = '0 0';
    canvas.style.transform = id ? '' : 'translate(' + zoom.tx + 'px,' + zoom.ty + 'px) scale(' + zoom.s + ')';
    if (!fullscreen) stage.style.overflow = id ? '' : 'hidden';
  }
  function resetZoom() { if (!canvas) return; zoom.s = 1; zoom.tx = 0; zoom.ty = 0; applyZoom(); }
  // baseRect is the canvas's box without our transform.
  function baseRect() {
    const r = canvas.getBoundingClientRect();
    return {left: r.left - zoom.tx, top: r.top - zoom.ty, width: r.width / zoom.s, height: r.height / zoom.s};
  }
  // clampAxis keeps a zoomed canvas covering the stage, and an unzoomed
  // one inside it.
  function clampAxis(t, baseStart, baseSize, s, viewStart, viewSize) {
    const size = baseSize * s;
    let lo, hi;
    if (size >= viewSize) { lo = viewStart + viewSize - baseStart - size; hi = viewStart - baseStart; } else { lo = viewStart - baseStart; hi = viewStart + viewSize - baseStart - size; }
    return Math.min(Math.max(t, Math.min(lo, hi)), Math.max(lo, hi));
  }

  // ---- gestures ----
  const SLOP = 10, LONG_MS = 550, TAP2_MS = 350, WHEEL_PX = 40;
  function setupGestures() {
    const touches = new Map();
    let mode = 'idle', longTimer = 0, start = null, last = null, two = null, wheelAcc = {x: 0, y: 0};
    const norm = (cx, cy) => {
      const r = canvas.getBoundingClientRect();
      const c = v => Math.min(1, Math.max(0, v));
      return {x: c((cx - r.left) / r.width), y: c((cy - r.top) / r.height)};
    };
    const pts = () => [...touches.values()];
    const centroid = () => { const p = pts(); return {x: (p[0].x + p[1].x) / 2, y: (p[0].y + p[1].y) / 2}; };
    const dist = () => { const p = pts(); return Math.hypot(p[0].x - p[1].x, p[0].y - p[1].y); };
    const click = (p, button) => {
      const n = norm(p.x, p.y);
      send({type: 'move', ...n});
      send({type: 'button', button, down: true, ...n});
      send({type: 'button', button, down: false, ...n});
    };
    const stopLong = () => { clearTimeout(longTimer); longTimer = 0; };

    function down(e) {
      touches.set(e.pointerId, {x: e.clientX, y: e.clientY});
      if (touches.size === 1 && (mode === 'idle' || mode === 'done')) {
        mode = 'pending1'; start = {x: e.clientX, y: e.clientY, t: performance.now()}; last = start;
        stopLong();
        longTimer = setTimeout(() => {
          if (mode !== 'pending1') return;
          mode = 'done';
          click(start, 2);
          native.post('haptic');
        }, LONG_MS);
      } else if (touches.size === 2 && mode === 'pending1') {
        stopLong();
        mode = 'pending2';
        const c = centroid();
        two = {c0: c, cLast: c, d0: Math.max(1, dist()), t: performance.now(), moved: false, s0: zoom.s, base: baseRect()};
        two.p = {x: (c.x - two.base.left - zoom.tx) / zoom.s, y: (c.y - two.base.top - zoom.ty) / zoom.s};
        wheelAcc = {x: 0, y: 0};
      }
    }
    function move(e) {
      const t = touches.get(e.pointerId);
      if (!t) return;
      t.x = e.clientX; t.y = e.clientY;
      if (mode === 'pending1' && Math.hypot(t.x - start.x, t.y - start.y) > SLOP) {
        stopLong();
        mode = 'drag';
        const n0 = norm(start.x, start.y);
        send({type: 'move', ...n0});
        send({type: 'button', button: 0, down: true, ...n0});
      }
      if (mode === 'drag') { last = {x: t.x, y: t.y}; send({type: 'move', ...norm(t.x, t.y)}); return; }
      if (touches.size < 2 || !two) return;
      const c = centroid(), d = dist();
      if (mode === 'pending2') {
        if (Math.abs(d - two.d0) > 24 || Math.abs(d / two.d0 - 1) > 0.15) mode = 'pinch';
        else if (Math.hypot(c.x - two.c0.x, c.y - two.c0.y) > SLOP) { mode = 'scroll'; two.cLast = c; }
      }
      if (mode === 'scroll') {
        wheelAcc.x += -(c.x - two.cLast.x) / WHEEL_PX;
        wheelAcc.y += -(c.y - two.cLast.y) / WHEEL_PX;
        two.cLast = c;
        if (Math.abs(wheelAcc.x) >= 0.25 || Math.abs(wheelAcc.y) >= 0.25) {
          send({type: 'wheel', dx: +wheelAcc.x.toFixed(3), dy: +wheelAcc.y.toFixed(3)});
          wheelAcc = {x: 0, y: 0};
        }
      } else if (mode === 'pinch') {
        const s = Math.min(8, Math.max(1, two.s0 * d / two.d0));
        const b = two.base, sr = stage.getBoundingClientRect();
        zoom.s = s;
        zoom.tx = clampAxis(c.x - b.left - two.p.x * s, b.left, b.width, s, sr.left, sr.width);
        zoom.ty = clampAxis(c.y - b.top - two.p.y * s, b.top, b.height, s, sr.top, sr.height);
        if (s <= 1.01 && b.width <= sr.width + 1 && b.height <= sr.height + 1) { zoom.s = 1; zoom.tx = 0; zoom.ty = 0; }
        applyZoom();
      }
    }
    function up(e) {
      if (!touches.has(e.pointerId)) return;
      const cancel = e.type === 'pointercancel';
      if (mode === 'pending1' && touches.size === 1) {
        stopLong();
        if (!cancel) click(start, 0);
        mode = 'done';
      } else if (mode === 'drag' && touches.size === 1) {
        send({type: 'button', button: 0, down: false, ...norm(last.x, last.y)});
        mode = 'done';
      } else if (mode === 'pending2') {
        if (!cancel && performance.now() - two.t < TAP2_MS) click(two.c0, 2);
        mode = 'done';
      } else if (mode === 'scroll' || mode === 'pinch') {
        mode = 'done';
      }
      touches.delete(e.pointerId);
      if (touches.size === 0) { mode = 'idle'; two = null; stopLong(); }
    }
    // Capture on window runs before desktop.js's own listeners on the
    // canvas; touch never reaches them, a mouse or pen still does.
    const mine = e => e.pointerType === 'touch' && (e.target === canvas || touches.has(e.pointerId));
    for (const [type, fn] of [['pointerdown', down], ['pointermove', move], ['pointerup', up], ['pointercancel', up]]) {
      window.addEventListener(type, e => {
        if (!mine(e)) return;
        e.stopImmediatePropagation();
        if (e.cancelable) e.preventDefault();
        if (type === 'pointerdown') { try { canvas.setPointerCapture(e.pointerId); } catch (er) { /* ignore */ } }
        fn(e);
      }, {capture: true, passive: false});
    }
    // iOS sends touch events alongside pointer events; keep it from
    // scrolling or zooming the page under the canvas.
    canvas.addEventListener('touchstart', e => e.preventDefault(), {passive: false});
    canvas.addEventListener('touchmove', e => e.preventDefault(), {passive: false});
    const fit = $('d-fit');
    if (fit) fit.addEventListener('click', () => setTimeout(resetZoom, 0));
  }

  // ---- soft keyboard and key bar ----
  const SENTINEL = '________________________________';
  function setupKeyboard() {
    input = document.createElement('textarea');
    input.id = 'sdm-input';
    i18n().sayAttr(input, 'aria-label', 'm.kbd.input');
    for (const [k, v] of [['autocapitalize', 'off'], ['autocomplete', 'off'], ['autocorrect', 'off'], ['spellcheck', 'false'], ['enterkeyhint', 'enter']]) input.setAttribute(k, v);
    // 16px: iOS zooms the page into any smaller focused field.
    css(input, {position: 'fixed', left: '0', bottom: '0', width: '2px', height: '2px', opacity: '0', fontSize: '16px',
      border: '0', padding: '0', resize: 'none', zIndex: '-1', caretColor: 'transparent'});
    document.body.appendChild(input);
    let lastVal = SENTINEL, composing = false;
    const reset = () => { input.value = SENTINEL; lastVal = SENTINEL; try { input.setSelectionRange(SENTINEL.length, SENTINEL.length); } catch (e) { /* ignore */ } };
    reset();
    input.addEventListener('focus', () => { reset(); updateBar(); });
    input.addEventListener('blur', () => { updateBar(); });
    input.addEventListener('compositionstart', () => { composing = true; });
    input.addEventListener('compositionend', () => { composing = false; setTimeout(onInput, 0); });
    function onInput() {
      const cur = input.value;
      let p = 0;
      while (p < cur.length && p < lastVal.length && cur[p] === lastVal[p]) p++;
      const del = lastVal.length - p, ins = cur.slice(p);
      for (let i = 0; i < del; i++) press('Backspace');
      if (ins) sendText(ins);
      lastVal = cur;
      if (!composing && (cur.length < SENTINEL.length / 2 || cur.length > SENTINEL.length + 64 || cur.indexOf('\n') >= 0)) reset();
    }
    input.addEventListener('input', onInput);
    // Keys that produce no text (and hardware keyboards' shortcuts).
    const special = new Set(['Backspace', 'Enter', 'Tab', 'Escape', 'Delete', 'Insert', 'Home', 'End', 'PageUp', 'PageDown',
      'ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown']);
    input.addEventListener('keydown', e => {
      if (e.isComposing || e.keyCode === 229 || !e.code) return;
      const fkey = /^F([1-9]|1[0-2])$/.test(e.key);
      const chord = (e.ctrlKey || e.altKey || e.metaKey) && e.key.length === 1;
      if (!special.has(e.key) && !fkey && !chord) return;
      e.preventDefault();
      const mods = [];
      if (e.ctrlKey) mods.push('ControlLeft');
      if (e.altKey) mods.push('AltLeft');
      if (e.metaKey) mods.push('MetaLeft');
      if (e.shiftKey && (chord || special.has(e.key) || fkey)) mods.push('ShiftLeft');
      press(e.code, mods);
    });

    bar = css(document.createElement('div'), {position: 'fixed', left: '0', right: '0', bottom: '0', display: 'flex', gap: '6px',
      overflowX: 'auto', padding: '6px 8px calc(6px + env(safe-area-inset-bottom)) 8px', background: 'rgba(24,24,28,.96)',
      zIndex: '2147483000', webkitOverflowScrolling: 'touch', boxSizing: 'border-box'});
    bar.id = 'sdm-bar';
    bar.setAttribute('role', 'toolbar');
    i18n().sayAttr(bar, 'aria-label', 'm.bar');
    bar.hidden = true;
    const btns = {};
    // label: the text (a key name), or {k} to translate; aria: [key, vars]
    // for the accessible name, or a plain key name.
    const mk = (label, aria, fn, id) => {
      const b = css(document.createElement('button'), {flex: '0 0 auto', minWidth: '44px', height: '36px', padding: '0 10px',
        border: '1px solid #555', borderRadius: '6px', background: '#2e2e34', color: '#eee', font: '15px system-ui, sans-serif'});
      b.type = 'button';
      if (typeof label === 'object') i18n().say(b, label.k); else b.textContent = label;
      if (Array.isArray(aria)) i18n().sayAttr(b, 'aria-label', aria[0], aria[1]);
      else if (aria) b.setAttribute('aria-label', aria);
      // Keep the focus (and the soft keyboard) where it is.
      b.addEventListener('pointerdown', e => e.preventDefault());
      b.addEventListener('mousedown', e => e.preventDefault());
      b.addEventListener('click', e => { e.preventDefault(); fn(b); });
      if (id) btns[id] = b;
      bar.appendChild(b);
      return b;
    };
    kbdBtn = mk('\u2328', ['m.kbd'], toggleKeyboard);
    mk('Esc', 'Escape', () => press('Escape'));
    mk('Tab', 'Tab', () => press('Tab'));
    for (const [label, code] of [['Ctrl', 'ControlLeft'], ['Alt', 'AltLeft'], ['Shift', 'ShiftLeft'], ['Win', 'MetaLeft']]) {
      mk(label, ['m.sticky', {key: label}], () => { if (sticky.has(code)) sticky.delete(code); else sticky.add(code); renderSticky(); }, code);
    }
    renderSticky = () => {
      for (const code of ['ControlLeft', 'AltLeft', 'ShiftLeft', 'MetaLeft']) {
        const b = btns[code], on = sticky.has(code);
        b.setAttribute('aria-pressed', String(on));
        b.style.background = on ? '#3b6fd8' : '#2e2e34';
      }
    };
    renderSticky();
    for (const [label, code, aria] of [['←', 'ArrowLeft', ['m.left']], ['↑', 'ArrowUp', ['m.up']], ['↓', 'ArrowDown', ['m.down']],
      ['→', 'ArrowRight', ['m.right']], ['Del', 'Delete', 'Delete'], ['Home', 'Home'], ['End', 'End'], ['PgUp', 'PageUp', ['m.pgup']],
      ['PgDn', 'PageDown', ['m.pgdn']]]) mk(label, aria || label, () => press(code));
    for (let i = 1; i <= 12; i++) mk('F' + i, 'F' + i, () => press('F' + i));
    mk({k: 'm.paste'}, ['m.paste.aria'], pasteToRemote);
    mk({k: 'm.copy'}, ['m.copy.aria'], copyFromRemote);
    mk('1:1', ['m.zoom'], resetZoom);
    mk('⛶', ['m.fs'], () => setFullscreen(!fullscreen), 'fs');
    document.body.appendChild(bar);
  }

  function keyboardShown() { return input && document.activeElement === input; }
  function toggleKeyboard() {
    if (keyboardShown()) { input.blur(); native.post('keyboard', {show: false}); return; }
    input.focus({preventScroll: true});
    native.post('keyboard', {show: true});
  }

  function desktopVisible() {
    const p = $('panel-desktop'), w = $('workspace');
    return !!(p && !p.hidden && w && !w.hidden);
  }
  function updateBar() {
    if (!bar) return;
    const show = desktopVisible();
    if (!show && fullscreen) setFullscreen(false);
    if (!show && keyboardShown()) input.blur();
    if (!show) resetZoom();
    bar.hidden = !show;
    document.body.style.paddingBottom = show ? (bar.offsetHeight + 8) + 'px' : '';
    if (kbdBtn) kbdBtn.setAttribute('aria-pressed', String(keyboardShown()));
    positionBar();
  }
  // On iOS the layout viewport does not shrink for the keyboard; follow the
  // visual viewport so the bar sits on top of the keyboard.
  function positionBar() {
    if (!bar || bar.hidden) return;
    const vv = window.visualViewport;
    if (vv && Math.abs(vv.height - window.innerHeight) > 1) {
      bar.style.bottom = 'auto';
      bar.style.top = (vv.offsetTop + vv.height - bar.offsetHeight) + 'px';
    } else {
      bar.style.top = 'auto';
      bar.style.bottom = '0';
    }
  }

  // ---- clipboard to and from the remote machine (user actions) ----
  const osCache = new Map();
  async function remoteOS(c) {
    if (osCache.has(c)) return osCache.get(c);
    let os = '';
    try { os = ((await window.SD.json('GET', 'info', undefined, {c})).info || {}).os || ''; } catch (e) { /* unknown */ }
    osCache.set(c, os);
    return os;
  }
  async function pasteToRemote() {
    const SD = window.SD, c = SD && SD.cur();
    if (!c) return;
    let text;
    try { text = await navigator.clipboard.readText(); } catch (e) { toast(tr('m.noread')); return; }
    if (!text) { toast(tr('m.notext')); return; }
    try {
      await SD.json('POST', 'clip/set', {c, text});
    } catch (e) { toast(e.code === 'feature_off' || e.code === 'host_refused' ? tr('m.refused', {msg: e.message}) : SD.errText(e)); return; }
    if (inputOK()) {
      press('KeyV', [(await remoteOS(c)) === 'darwin' ? 'MetaLeft' : 'ControlLeft']);
      toast(tr('m.pasted', {n: text.length}));
    } else toast(tr('m.copied.remote', {n: text.length}));
  }
  async function copyFromRemote() {
    const SD = window.SD, c = SD && SD.cur();
    if (!c) return;
    try {
      const j = await SD.json('POST', 'clip/get', {c});
      if (j.kind !== 'text' || !j.text) { toast(tr(j.kind === 'image' ? 'm.image' : 'm.empty')); return; }
      await navigator.clipboard.writeText(j.text);
      toast(tr('m.copied.local', {n: j.text.length}));
    } catch (e) { toast(e.code === 'feature_off' || e.code === 'host_refused' ? tr('m.refused', {msg: e.message}) : (SD.errText ? SD.errText(e) : String(e))); }
  }

  // ---- full screen: the app hides the system bars; the stage fills the
  // screen above the key bar. (Element full screen is unreliable in
  // WebViews.) ----
  function setupFullscreen() {
    // On window in the capture phase, so it runs before desktop.js's own
    // handler on the button.
    window.addEventListener('click', e => {
      if (!e.target || !e.target.closest || !e.target.closest('#d-fs')) return;
      e.stopImmediatePropagation(); e.preventDefault();
      setFullscreen(!fullscreen);
    }, true);
  }
  function sizeFullscreen() {
    const h = window.innerHeight - (bar && !bar.hidden ? bar.offsetHeight : 0);
    css(stage, {position: 'fixed', left: '0', top: '0', width: '100vw', height: h + 'px', zIndex: '2147482000', borderRadius: '0',
      alignItems: 'center', overflow: 'hidden'});
  }
  function setFullscreen(on) {
    if (!stage || on === fullscreen) return;
    fullscreen = on;
    if (on) sizeFullscreen();
    else for (const p of ['position', 'left', 'top', 'width', 'height', 'zIndex', 'borderRadius', 'alignItems', 'overflow']) stage.style[p] = '';
    resetZoom();
    const b = $('d-fs');
    if (b) i18n().say(b, on ? 'm.exitfs' : 'm.fs.btn');
    state();
    // desktop.js lays the canvas out on resize.
    setTimeout(() => window.dispatchEvent(new Event('resize')), 0);
  }

  // ---- downloads go to the app, which saves them where the user says ----
  function interceptDownloads() {
    document.addEventListener('click', e => {
      if (!native.available()) return;
      const a = e.target && e.target.closest && e.target.closest('a[href]');
      if (!a) return;
      let u;
      try { u = new URL(a.href, location.href); } catch (er) { return; }
      if (u.origin !== location.origin || !/\/api\/v1\/files\/download$/.test(u.pathname)) return;
      e.preventDefault(); e.stopImmediatePropagation();
      const path = u.searchParams.get('path') || 'download';
      const name = path.split(/[\\/]/).filter(Boolean).pop() || 'download';
      native.call('download', {url: u.toString(), name}).catch(er => toast(tr('m.dlfailed', {msg: er.message})));
    }, true);
  }

  // ---- settings button and hints ----
  function addSettingsButton() {
    if (!native.available()) return;
    // The app's Settings choose the language; the page's own switch would
    // only disagree with them.
    const sw = $('langsw');
    if (sw) sw.hidden = true;
    const top = document.querySelector('header.top');
    if (!top) return;
    const b = css(document.createElement('button'), {marginLeft: 'auto'});
    b.type = 'button'; b.id = 'sdm-settings'; b.textContent = '⚙';
    i18n().sayAttr(b, 'aria-label', 'm.settings');
    b.addEventListener('click', () => native.post('openSettings'));
    top.appendChild(b);
  }
  function watchConnectErrors() {
    const el = $('connerr');
    if (!el || !native.available()) return;
    new MutationObserver(() => {
      if (el.textContent.indexOf('no rendezvous server configured') >= 0 && el.getAttribute('data-i18n') !== 'm.norv') i18n().say(el, 'm.norv');
    }).observe(el, {childList: true, characterData: true, subtree: true});
  }

  // ---- called by the app ----
  window.remohopMobile = {
    // setLang switches the page and this script to "zh" or "en" (the app's
    // language setting), without reloading: the sessions stay.
    setLang(l) {
      // Before the page's scripts have run, i18n.js is not there yet.
      if (!I18N && !window.SDI18n && document.readyState === 'loading') { onReady(() => i18n().set(l, {persist: false})); return l; }
      i18n().set(l, {persist: false});
      return i18n().lang();
    },
    // back handles the system back button: "handled" or "unhandled" (the
    // app then goes to the background, keeping its sessions).
    back() {
      const dlg = $('dlg');
      if (dlg && dlg.open) { dlg.close(); return 'handled'; }
      if (keyboardShown()) { input.blur(); native.post('keyboard', {show: false}); return 'handled'; }
      if (fullscreen) { setFullscreen(false); return 'handled'; }
      if (zoom.s !== 1) { resetZoom(); return 'handled'; }
      const ws = $('workspace'), nc = $('newconn');
      if (ws && !ws.hidden && nc && !nc.hidden) { nc.click(); return 'handled'; }
      return 'unhandled';
    },
    // Test and debugging hooks; they expose nothing the page cannot see.
    _desk: desk,
    _zoom: zoom,
    _sendText: sendText,
    _setFullscreen: on => setFullscreen(on),
    _toggleKeyboard: () => toggleKeyboard(),
  };
})();
