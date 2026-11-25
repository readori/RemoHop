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
//   - native dialogs: the page's confirmations, text prompts and action
//     menus (SD.confirm, SD.prompt, SD.menu) become the platform's own
//     alert (iOS UIAlertController, Android MaterialAlertDialogBuilder);
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

  // ---- app mode ----
  // The page's style.css styles html.app as a phone app (compact top bar,
  // grouped lists, 16px fields, help on demand). Only the apps inject this
  // script, so the browser page never gets the class.
  // At document start the <html> element may not exist yet.
  (function markApp() {
    // The platform picks the idiom (style.css): Material 3 on Android, the
    // HIG on iOS. The bridge says which app this is; the user agent is the
    // fallback.
    const ios = !!(window.webkit && window.webkit.messageHandlers && window.webkit.messageHandlers.remohop) ||
      (!window.remohopNative && /iPhone|iPad|iPod|Macintosh/.test(navigator.userAgent));
    const mark = () => {
      if (!document.documentElement) return false;
      document.documentElement.classList.add('app', ios ? 'platform-ios' : 'platform-android');
      return true;
    };
    if (mark()) return;
    const mo = new MutationObserver(() => { if (mark()) mo.disconnect(); });
    mo.observe(document, {childList: true});
  })();

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

  // ---- diagnostics: page errors go to the app's diagnostic log, which
  // keeps them only while the user has it on (and redacts them) ----
  function diagLog(text) {
    if (native.available()) native.post('log', {text: String(text).slice(0, 1500)});
  }
  addEventListener('error', e => diagLog('page error: ' + (e.message || '') +
    (e.filename ? ' (' + String(e.filename).split('/').pop() + ':' + e.lineno + ')' : '')));
  addEventListener('unhandledrejection', e => diagLog('unhandled rejection: ' + (e.reason && (e.reason.message || e.reason))));

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
  const clip = (v, n) => String(v == null ? '' : v).slice(0, n);

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
      'm.settings': 'Settings',
      'm.back': 'Hosts',
      'm.help': 'Help',
      'm.colon': 'Type a colon',
      'm.pair': 'Remember this computer',
      // App wording for the page's saved hosts.
      'peers.h': 'My computers',
      'peers.recent.h': 'Recent',
      // The first-connection notice, short (a line or two in the app's bar; a tap closes it).
      'sess.firstkey': 'First connection · key pinned: {fp}',
      'm.saved': 'Saved hosts',
      'm.active': 'Connected now',
      'm.live': 'Connected · tap to return',
      'm.more': 'Actions for {name}',
      'm.cancel': 'Cancel',
      'm.ok': 'OK',
      'm.done': 'Done',
      'm.help.title': 'Connecting to a host',
      'm.help.target.h': 'Host',
      'm.help.target': 'Its 9-digit ID (needs a rendezvous server, set in Settings), a name you saved, or its address, such as 192.168.1.20:21118.',
      'm.help.pass.h': 'Password',
      'm.help.pass': 'The password set on the host. Leave it empty for a host this device is paired with. It is used for this login only and not stored.',
      'm.help.pair.h': 'Remember this computer',
      'm.help.pair': 'On by default: after you log in with the password, this device is paired with the computer, so next time it connects without one. Some computers (quick support) ask for approval every time and cannot be remembered. Remove one in Settings → Saved hosts.',
      'm.help.sec.h': 'Security',
      'm.help.sec': 'The session runs directly between this device and the host and is end-to-end encrypted; a relay, if one is used, cannot read it. RemoHop collects no data.',
      'm.help.privacy': 'Privacy and about',
      // The page's connection errors, in the app's words (they name Settings).
      'reason.no_rendezvous': 'No rendezvous server is set, so a host can only be reached by its address (host:port). Set your server in Settings to connect by ID.',
      'reason.rendezvous_unreachable': 'The rendezvous server set in Settings cannot be reached. Check its address and the network, or connect by address (host:port).',
      'reason.signal_wrong_protocol': 'The address in Settings → Rendezvous server is not a RemoHop rendezvous server. Fix it there, or connect to the host by its address (host:port).',
      'reason.signal_is_host': 'The address in Settings → Rendezvous server is a RemoHop host, not a rendezvous server. Connect to that host by its address (host:port), and fix the setting.',
      'err.signal_wrong_protocol': 'The address in Settings → Rendezvous server is not a RemoHop rendezvous server.',
      'err.signal_is_host': 'The address in Settings → Rendezvous server is a RemoHop host, not a rendezvous server. Connect to it by its address (host:port).',
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
      'm.settings': '设置',
      'm.back': '被控端列表',
      'm.help': '帮助',
      'm.colon': '输入冒号',
      'm.pair': '记住这台电脑，下次免密码',
      'peers.h': '我的电脑',
      'peers.recent.h': '最近连接',
      'sess.firstkey': '首次连接 · 已固定密钥：{fp}',
      'm.saved': '已保存的被控端',
      'm.active': '当前连接',
      'm.live': '已连接 · 轻点返回',
      'm.more': '{name} 的操作',
      'm.cancel': '取消',
      'm.ok': '确定',
      'm.done': '完成',
      'm.help.title': '如何连接被控端',
      'm.help.target.h': '被控端',
      'm.help.target': '填写它的 9 位 ID（需要先在设置中填写汇合服务器）、你保存的名称，或它的地址，例如 192.168.1.20:21118。',
      'm.help.pass.h': '密码',
      'm.help.pass': '被控端上设置的密码。已与本设备配对的被控端可以留空。密码只用于这次登录，不会保存。',
      'm.help.pair.h': '记住这台电脑',
      'm.help.pair': '默认开启：用密码登录后，本设备会与这台电脑配对，下次连接无需密码。有些电脑（快速协助）每次都需要批准，无法记住。可在“设置 → 已保存的被控端”中移除。',
      'm.help.sec.h': '安全',
      'm.help.sec': '会话直接在本设备与被控端之间建立，并且端到端加密；即使经过中继，中继也无法读取内容。RemoHop 不收集任何数据。',
      'm.help.privacy': '隐私与关于',
      'reason.no_rendezvous': '尚未设置汇合服务器，只能通过地址（host:port）连接被控端。要用 ID 连接，请在设置中填写你的汇合服务器。',
      'reason.rendezvous_unreachable': '无法连接设置中的汇合服务器。请检查它的地址和网络，或直接用地址（host:port）连接。',
      'reason.signal_wrong_protocol': '“设置 → 汇合服务器”中填写的不是 RemoHop 汇合服务器。请在那里修改，或直接用地址（host:port）连接被控端。',
      'reason.signal_is_host': '“设置 → 汇合服务器”中填写的是一台 RemoHop 被控端，不是汇合服务器。请直接用该地址（host:port）连接它，并修改该设置。',
      'err.signal_wrong_protocol': '“设置 → 汇合服务器”中填写的不是 RemoHop 汇合服务器。',
      'err.signal_is_host': '“设置 → 汇合服务器”中填写的是一台 RemoHop 被控端，不是汇合服务器。请直接用该地址（host:port）连接它。',
    },
  };
  let I18N = null;
  function i18n() {
    if (I18N) return I18N;
    if (window.SDI18n) {
      window.SDI18n.add(MD);
      // Some of MD replace the page's own words (app wording): show them.
      if (window.SDI18n.apply) window.SDI18n.apply(document);
      I18N = window.SDI18n;
      return I18N;
    }
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
      toastEl = css(document.createElement('div'), {position: 'fixed', left: '50%', top: 'calc(env(safe-area-inset-top, 0px) + 64px)', transform: 'translateX(-50%)',
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
    nativeDialogs();
    addSettingsButton();
    setupAppChrome();
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
    // Full screen next to the keyboard: the far end of the bar is off screen.
    mk('⛶', ['m.fs'], () => setFullscreen(!fullscreen), 'fs');
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
  let exitBtn = null, exitDragged = false, dimTimer = 0;
  function setupFullscreen() {
    // On window in the capture phase, so it runs before desktop.js's own
    // handlers on the buttons.
    window.addEventListener('click', e => {
      const t = e.target && e.target.closest && e.target.closest('#d-fs, #d-exitfs');
      if (!t) return;
      e.stopImmediatePropagation(); e.preventDefault();
      if (t.id === 'd-exitfs') {
        if (exitDragged) { exitDragged = false; return; }
        setFullscreen(false);
      } else setFullscreen(!fullscreen);
    }, true);
    // The floating exit button: the page's #d-exitfs (or one of our own on
    // an older page). Always on top of the picture, dimmed after a moment,
    // and draggable out of the way.
    exitBtn = $('d-exitfs');
    if (!exitBtn) {
      exitBtn = document.createElement('button');
      exitBtn.type = 'button'; exitBtn.id = 'd-exitfs'; exitBtn.textContent = '✕';
      stage.appendChild(exitBtn);
    }
    i18n().sayAttr(exitBtn, 'aria-label', 'm.exitfs');
    css(exitBtn, {position: 'absolute', zIndex: '5', width: '44px', height: '44px', minHeight: '44px', padding: '0', border: '0',
      borderRadius: '22px', background: 'rgba(16,20,32,.6)', color: '#fff', display: 'none', alignItems: 'center',
      justifyContent: 'center', touchAction: 'none', transition: 'opacity .3s', top: 'calc(env(safe-area-inset-top, 0px) + 10px)',
      right: 'calc(env(safe-area-inset-right, 0px) + 10px)', left: 'auto'});
    let drag = null;
    exitBtn.addEventListener('pointerdown', e => {
      wake();
      const r = exitBtn.getBoundingClientRect();
      drag = {id: e.pointerId, x: e.clientX, y: e.clientY, dx: e.clientX - r.left, dy: e.clientY - r.top, moved: false};
      try { exitBtn.setPointerCapture(e.pointerId); } catch (er) { /* ignore */ }
    });
    exitBtn.addEventListener('pointermove', e => {
      if (!drag || e.pointerId !== drag.id) return;
      if (!drag.moved && Math.hypot(e.clientX - drag.x, e.clientY - drag.y) < SLOP) return;
      drag.moved = true;
      const sr = stage.getBoundingClientRect(), w = exitBtn.offsetWidth, h = exitBtn.offsetHeight;
      const x = Math.min(Math.max(e.clientX - drag.dx - sr.left, 4), sr.width - w - 4);
      const y = Math.min(Math.max(e.clientY - drag.dy - sr.top, 4), sr.height - h - 4);
      css(exitBtn, {left: x + 'px', top: y + 'px', right: 'auto'});
    });
    const end = e => { if (drag && e.pointerId === drag.id) { exitDragged = drag.moved; drag = null; wake(); } };
    exitBtn.addEventListener('pointerup', end);
    exitBtn.addEventListener('pointercancel', end);
  }
  function wake() {
    if (!exitBtn) return;
    exitBtn.style.opacity = '1';
    clearTimeout(dimTimer);
    dimTimer = setTimeout(() => { if (exitBtn) exitBtn.style.opacity = '.4'; }, 3000);
  }
  function showExit(on) {
    if (!exitBtn) return;
    exitBtn.style.display = on ? 'flex' : 'none';
    if (on) {
      css(exitBtn, {top: 'calc(env(safe-area-inset-top, 0px) + 10px)', right: 'calc(env(safe-area-inset-right, 0px) + 10px)', left: 'auto'});
      wake();
    }
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
    showExit(on);
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

  // ---- dialogs: the page's own become native ones ----
  // The bridge calls (answered when the user has chosen):
  //   confirm {title, message, confirm, cancel, destructive} -> {ok}
  //   prompt  {title, message, value, placeholder, confirm, cancel} -> {ok, text}
  //   actions {title, message, actions: [{label, destructive}], cancel} -> {index} (-1: cancelled)
  // If the app cannot show one (an error, not a cancel), the page's own
  // dialog is used.
  function nativeConfirm(o) {
    return native.call('confirm', {title: clip(o.title, 200), message: clip(o.message, 1000), confirm: clip(o.confirm || tr('m.ok'), 40),
      cancel: clip(o.cancel || tr('m.cancel'), 40), destructive: !!o.destructive}).then(r => !!(r && r.ok));
  }
  function nativeDialogs() {
    const SD = window.SD;
    if (!SD || !native.available() || SD.__native) return;
    SD.__native = true;
    const web = {confirm: SD.confirm, prompt: SD.prompt, menu: SD.menu};
    const t = k => (SD.t ? SD.t(k) : tr('m.' + k));
    if (web.confirm) {
      SD.confirm = (title, message, okLabel, opts) => nativeConfirm({title, message, confirm: okLabel || t('ok'), cancel: t('cancel'),
        destructive: !!(opts && opts.destructive)}).catch(() => web.confirm(title, message, okLabel, opts));
    }
    if (web.prompt) {
      SD.prompt = o => native.call('prompt', {title: clip(o.title, 200), message: clip(o.message, 1000), value: clip(o.value, 500),
        placeholder: clip(o.placeholder || o.label, 100), confirm: clip(o.okLabel || t('ok'), 40), cancel: clip(t('cancel'), 40)})
        .then(r => (r && r.ok ? String(r.text || '') : null)).catch(() => web.prompt(o));
    }
    if (web.menu) {
      SD.menu = o => native.call('actions', {title: clip(o.title, 200), message: clip(o.message, 1000), cancel: clip(t('cancel'), 40),
        actions: o.actions.slice(0, 8).map(a => ({label: clip(a.label, 60), destructive: !!a.destructive}))})
        .then(r => { const i = r ? r.index : -1; return i >= 0 && i < o.actions.length ? o.actions[i].value : null; })
        .catch(() => web.menu(o));
    }
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
  // ---- app chrome (styled by html.app rules in the page's style.css) ----
  function setupAppChrome() {
    const root = document.documentElement;
    // No pinch zoom of the page, and no zooming into fields; the remote
    // desktop zooms itself (setupGestures). viewport-fit=cover gives the
    // page the safe-area insets, so the top bar fills the status bar area.
    const vp = document.querySelector('meta[name=viewport]');
    if (vp) vp.setAttribute('content', 'width=device-width,initial-scale=1,maximum-scale=1,user-scalable=no,viewport-fit=cover');

    // The title: "RemoHop", or the host's name in a session with the
    // connection path under it.
    const brand = document.querySelector('header.top .brand');
    let titleName = null;
    if (brand && !$('sdm-title')) {
      for (const n of [...brand.childNodes]) if (n.nodeType === 3 && n.textContent.trim()) n.remove();
      const t = document.createElement('span');
      t.className = 'apptitle'; t.id = 'sdm-title';
      titleName = document.createElement('span');
      titleName.id = 'sdm-title-name'; titleName.textContent = 'RemoHop';
      t.appendChild(titleName);
      brand.insertBefore(t, brand.querySelector('.sub'));
      const badge = $('pathbadge');
      if (badge) t.appendChild(badge);
    }
    const nc = $('newconn');
    if (nc) i18n().sayAttr(nc, 'aria-label', 'm.back');

    // Help on demand instead of paragraphs: an ⓘ next to the heading.
    const h = $('connect-h');
    if (h && !$('sdm-info')) {
      const row = document.createElement('div');
      row.className = 'titlerow';
      h.parentNode.insertBefore(row, h);
      row.appendChild(h);
      const info = document.createElement('button');
      info.type = 'button'; info.id = 'sdm-info'; info.className = 'sdm-icon info';
      i18n().sayAttr(info, 'aria-label', 'm.help');
      info.addEventListener('click', showHelp);
      row.appendChild(info);
    }
    // The address field: a ":" key next to it (the phone keyboard hides
    // the colon on another layer), and typing slips fixed when it is left.
    const tg = $('target');
    if (tg && !$('sdm-colon')) {
      const wrap = document.createElement('div');
      wrap.className = 'fieldwrap';
      tg.parentNode.insertBefore(wrap, tg);
      wrap.appendChild(tg);
      const k = document.createElement('button');
      k.type = 'button'; k.id = 'sdm-colon'; k.className = 'fieldkey'; k.textContent = ':';
      i18n().sayAttr(k, 'aria-label', 'm.colon');
      k.addEventListener('pointerdown', e => e.preventDefault());
      k.addEventListener('mousedown', e => e.preventDefault());
      k.addEventListener('click', () => {
        const a = tg.selectionStart == null ? tg.value.length : tg.selectionStart, b = tg.selectionEnd == null ? a : tg.selectionEnd;
        tg.setRangeText(':', a, b, 'end');
        tg.focus({preventScroll: true});
        tg.dispatchEvent(new Event('input', {bubbles: true}));
      });
      wrap.appendChild(k);
      tg.addEventListener('change', () => { if (window.SD && window.SD.normTarget) tg.value = window.SD.normTarget(tg.value); });
    }
    // Shorter labels.
    const pairText = document.querySelector('#pairrow [data-i18n="connect.pair"]');
    if (pairText) i18n().say(pairText, 'm.pair');
    // Remember the computer by default (pairing after a password login).
    const pairBox = $('pair');
    if (pairBox) pairBox.checked = true;
    const banner = $('banner');
    if (banner) banner.addEventListener('click', () => { banner.hidden = true; });

    // Session state for the styles, the title and the list of open sessions.
    const ws = $('workspace'), conn = $('connect'), sess = $('sessname'), hostsel = $('hostsel'), hb = $('hostbar');
    const sync = () => {
      const inSession = !!(ws && !ws.hidden);
      root.classList.toggle('insession', inSession);
      root.classList.toggle('multi', !!(hostsel && hostsel.options.length > 1));
      if (titleName) titleName.textContent = inSession && sess && sess.textContent ? sess.textContent : 'RemoHop';
      renderActive();
    };
    const mo = new MutationObserver(sync);
    for (const el of [ws, conn, hb]) if (el) mo.observe(el, {attributes: true, attributeFilter: ['hidden']});
    if (sess) mo.observe(sess, {childList: true, characterData: true, subtree: true});
    if (hostsel) mo.observe(hostsel, {childList: true, subtree: true, characterData: true});
    sync();
    addEventListener('remohop:lang', () => { lastActive = ''; renderActive(); });

    // Files: rows with an icon and one "more" button that opens the row's
    // actions in a sheet (the page's own buttons stay, hidden, and do the work).
    const fb = $('f-body');
    if (fb) { new MutationObserver(() => decorateFiles(fb)).observe(fb, {childList: true}); decorateFiles(fb); }

    // Connection errors go to the diagnostic log too.
    const ce = $('connerr');
    if (ce) {
      new MutationObserver(() => {
        const d = ce.querySelector('.errdetail code');
        const t = (ce.textContent || '').trim();
        if (t) diagLog('connect error: ' + (d ? (ce.querySelector('.errmain') || ce).textContent + ' | ' + d.textContent : t));
      }).observe(ce, {childList: true});
    }
    trackViewport();
  }

  // The host sessions this page holds, as a list on the connect screen.
  let lastActive = '';
  function renderActive() {
    const conn = $('connect'), hostsel = $('hostsel'), hb = $('hostbar');
    if (!conn || !hostsel) return;
    const opts = hb && !hb.hidden ? [...hostsel.options] : [];
    const key = opts.map(o => o.value + '\u0000' + o.textContent).join('\u0001');
    if (key === lastActive && (opts.length === 0) === !$('sdm-active')) return;
    lastActive = key;
    let box = $('sdm-active');
    if (!opts.length) { if (box) box.remove(); return; }
    if (!box) {
      box = document.createElement('div');
      box.id = 'sdm-active'; box.className = 'peers';
      const form = $('connectform');
      conn.insertBefore(box, form);
    }
    box.textContent = '';
    const head = document.createElement('h3');
    i18n().say(head, 'm.active');
    const ul = document.createElement('ul');
    ul.className = 'peerlist';
    for (const o of opts) {
      const li = document.createElement('li');
      const b = document.createElement('button');
      b.type = 'button'; b.className = 'peer live';
      const icon = document.createElement('span');
      icon.className = 'peericon'; icon.setAttribute('aria-hidden', 'true');
      const text = document.createElement('span');
      text.className = 'peertext';
      const name = document.createElement('strong');
      name.textContent = o.textContent;
      const sub = document.createElement('small');
      i18n().say(sub, 'm.live');
      text.append(name, sub);
      b.append(icon, text);
      b.addEventListener('click', () => { hostsel.value = o.value; hostsel.dispatchEvent(new Event('change')); });
      li.appendChild(b);
      ul.appendChild(li);
    }
    box.append(head, ul);
  }

  const FOLDER = /^\u{1F4C1}\s*/u;
  function decorateFiles(fb) {
    for (const row of fb.rows) {
      if (row.dataset.sdm) continue;
      row.dataset.sdm = '1';
      const name = row.querySelector('td.name'), act = row.querySelector('td.act');
      if (!name || !act) continue;
      const link = name.querySelector('button.link');
      const label = link || name;
      const isDir = !!link || FOLDER.test(name.textContent);
      if (FOLDER.test(label.textContent)) label.textContent = label.textContent.replace(FOLDER, '');
      name.classList.add(isDir ? 'dir' : 'file');
      const acts = [...act.querySelectorAll('button')];
      if (!acts.length) continue;
      const more = document.createElement('button');
      more.type = 'button'; more.className = 'more';
      i18n().sayAttr(more, 'aria-label', 'm.more', {name: label.textContent});
      const items = () => acts.map(b => ({label: b.textContent, cls: b.getAttribute('data-i18n') === 'f.delete' ? 'danger' : '', run: () => b.click()}));
      const webSheet = () => sheet({title: label.textContent, close: tr('m.cancel'), actions: items()});
      // The row's actions in the platform's own action sheet (the page's sheet if the app cannot).
      const open = () => {
        if (!native.available()) { webSheet(); return; }
        const list = items();
        native.call('actions', {title: clip(label.textContent, 200), message: '', cancel: tr('m.cancel'),
          actions: list.map(a => ({label: clip(a.label, 60), destructive: a.cls === 'danger'}))})
          .then(r => { const i = r ? r.index : -1; if (i >= 0 && i < list.length) list[i].run(); }).catch(webSheet);
      };
      more.addEventListener('click', open);
      act.appendChild(more);
      if (!link) name.addEventListener('click', open);
    }
  }

  // sheet shows a bottom sheet: a title, nodes, action buttons and a close button.
  function sheet({title, body, actions, close}) {
    let d = $('sdm-sheet');
    if (!d) {
      d = document.createElement('dialog');
      d.id = 'sdm-sheet';
      // A tap outside the sheet (on the backdrop) closes it.
      d.addEventListener('click', e => {
        if (e.target !== d) return;
        const r = d.getBoundingClientRect();
        if (e.clientY < r.top || e.clientY > r.bottom || e.clientX < r.left || e.clientX > r.right) d.close();
      });
      document.body.appendChild(d);
    }
    d.textContent = '';
    d.setAttribute('aria-label', title || '');
    if (title) { const h = document.createElement('h2'); h.textContent = title; d.appendChild(h); }
    for (const n of body || []) d.appendChild(n);
    const box = document.createElement('div');
    box.className = 'actions';
    for (const a of actions || []) {
      const b = document.createElement('button');
      b.type = 'button'; b.textContent = a.label;
      if (a.cls) b.className = a.cls;
      b.addEventListener('click', () => { d.close(); if (a.run) a.run(); });
      box.appendChild(b);
    }
    const c = document.createElement('button');
    c.type = 'button'; c.className = 'link'; c.textContent = close || tr('m.done');
    c.addEventListener('click', () => d.close());
    box.appendChild(c);
    d.appendChild(box);
    if (d.open) d.close();
    d.showModal();
    c.focus({preventScroll: true});
  }

  function showHelp() {
    const el = (tag, k) => { const e = document.createElement(tag); e.textContent = tr(k); return e; };
    const body = [];
    for (const k of ['target', 'pass', 'pair', 'sec']) body.push(el('h3', 'm.help.' + k + '.h'), el('p', 'm.help.' + k));
    sheet({title: tr('m.help.title'), body,
      actions: native.available() ? [{label: tr('m.help.privacy'), run: () => native.post('openSettings', {page: 'privacy'})}] : []});
  }

  // On iOS the keyboard covers the page without resizing it: while it is
  // up (and not on the desktop tab, whose key bar follows it), the session
  // view takes the visible height, so the terminal and its keys stay above
  // it. Android resizes the web view itself (adjustResize): nothing to do.
  function trackViewport() {
    const vv = window.visualViewport, root = document.documentElement;
    if (!vv || !root.classList.contains('platform-ios')) return;
    const upd = () => {
      const kb = root.clientHeight - vv.height > 80 && vv.height > 120 && !desktopVisible();
      if (kb) root.style.setProperty('--app-h', Math.round(vv.height) + 'px');
      root.classList.toggle('vkb', kb);
    };
    vv.addEventListener('resize', upd);
    addEventListener('resize', upd);
    upd();
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
      const sh = $('sdm-sheet');
      if (sh && sh.open) { sh.close(); return 'handled'; }
      const dlg = $('dlg');
      if (dlg && dlg.open) { dlg.close(); return 'handled'; }
      if (keyboardShown()) { input.blur(); native.post('keyboard', {show: false}); return 'handled'; }
      if (fullscreen) { setFullscreen(false); return 'handled'; }
      if (zoom.s !== 1) { resetZoom(); return 'handled'; }
      const ws = $('workspace'), nc = $('newconn');
      if (ws && !ws.hidden && nc && !nc.hidden) { nc.click(); return 'handled'; }
      return 'unhandled';
    },
    // ensureGateway is called by the page (app.js) when a request got no
    // answer at all: the app checks its in-process gateway, reopens the
    // listening socket iOS takes from a suspended app, and resolves once
    // the gateway answers (it loads the page again if the address changed).
    ensureGateway() { return native.call('gateway.ensure'); },
    // confirm asks a yes/no question in a native dialog: {title, message,
    // confirm, cancel, destructive} -> Promise<boolean>.
    confirm(o) { return nativeConfirm(o || {}); },
    // peersChanged: the app's Settings renamed or forgot a saved host; the
    // page lists them again.
    peersChanged() { onReady(() => { if (window.SD && window.SD.reloadPeers) window.SD.reloadPeers(); }); return 'ok'; },
    // connect: Settings → Saved hosts → Connect. A paired host connects, a
    // recent one is filled in, waiting for its password.
    connect(target) { onReady(() => { if (window.SD && window.SD.connectTo) window.SD.connectTo(String(target)); }); return 'ok'; },
    // Test and debugging hooks; they expose nothing the page cannot see.
    _desk: desk,
    _zoom: zoom,
    _sendText: sendText,
    _setFullscreen: on => setFullscreen(on),
    _fullscreen: () => fullscreen,
    _toggleKeyboard: () => toggleKeyboard(),
  };
})();
