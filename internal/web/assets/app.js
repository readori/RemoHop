// app.js: the RemoHop web client shell: session, login, connecting, tabs,
// clipboard and info. All text from the host reaches the DOM through
// textContent only.
'use strict';
(function () {
  const $ = id => document.getElementById(id);

  // Every password field gets an eye button that shows what was typed, so
  // it can be checked before connecting. It never leaves the field shown:
  // submitting the form hides it again.
  function eyeIcon(open) {
    const ns = 'http://www.w3.org/2000/svg';
    const svg = document.createElementNS(ns, 'svg');
    svg.setAttribute('viewBox', '0 0 24 24'); svg.setAttribute('aria-hidden', 'true');
    const path = document.createElementNS(ns, 'path');
    path.setAttribute('fill', 'currentColor');
    path.setAttribute('d', open
      ? 'M12 5C6.5 5 2.7 9 1.5 12c1.2 3 5 7 10.5 7s9.3-4 10.5-7C21.3 9 17.5 5 12 5Zm0 11.5a4.5 4.5 0 1 1 0-9 4.5 4.5 0 0 1 0 9Zm0-2.5a2 2 0 1 0 0-4 2 2 0 0 0 0 4Z'
      : 'M2.3 3.7 3.7 2.3l18 18-1.4 1.4-3.2-3.2A11.4 11.4 0 0 1 12 19c-5.5 0-9.3-4-10.5-7 .6-1.5 1.8-3.3 3.5-4.8L2.3 3.7Zm5.4 5.4A4.5 4.5 0 0 0 14.9 15.3l-1.6-1.6A2 2 0 0 1 10.3 10.7L7.7 9.1ZM12 5c5.5 0 9.3 4 10.5 7-.5 1.3-1.5 2.8-2.8 4.1l-3.3-3.3A4.5 4.5 0 0 0 11.2 7.6L8.9 5.4C9.9 5.1 10.9 5 12 5Z');
    svg.appendChild(path);
    return svg;
  }
  function addEye(input) {
    if (!input || input.dataset.eye) return;
    input.dataset.eye = '1';
    const wrap = document.createElement('span');
    wrap.className = 'pwwrap';
    input.parentNode.insertBefore(wrap, input);
    wrap.appendChild(input);
    const b = document.createElement('button');
    b.type = 'button'; b.className = 'pweye';
    const set = shown => {
      input.type = shown ? 'text' : 'password';
      b.textContent = ''; b.appendChild(eyeIcon(!shown));
      b.setAttribute('aria-pressed', String(shown));
      I.sayAttr(b, 'aria-label', shown ? 'pw.hide' : 'pw.show');
      I.sayAttr(b, 'title', shown ? 'pw.hide' : 'pw.show');
    };
    b.addEventListener('click', () => { set(input.type === 'password'); input.focus(); });
    if (input.form) input.form.addEventListener('submit', () => set(false));
    wrap.appendChild(b);
    set(false);
  }
  const base = new URL('.', location.href);
  const SD = window.SD = {tabs: {}, state: {sid: null, csrf: null, mode: null, conns: [], cur: null}};
  const st = SD.state;
  // Text goes through i18n.js: SD.t for a string now, SD.say/SD.sayAttr for
  // text that follows a language switch, SD.plain for untranslated text.
  const I = SD.i18n = window.SDI18n;
  for (const id of ['hostpass', 'gwpass']) addEye($(id));
  SD.t = I.t; SD.say = I.say; SD.plain = I.plain; SD.sayAttr = I.sayAttr;

  class ApiError extends Error {
    constructor(status, code, message) { super(message); this.status = status; this.code = code; }
  }
  SD.ApiError = ApiError;

  SD.url = (path, params) => {
    const u = new URL('api/v1/' + path, base);
    if (params) for (const [k, v] of Object.entries(params)) if (v !== undefined && v !== null) u.searchParams.set(k, v);
    return u;
  };
  SD.headers = (extra) => {
    const h = Object.assign({}, extra);
    if (st.sid) h['X-RemoHop-Session'] = st.sid;
    if (st.csrf) h['X-RemoHop-CSRF'] = st.csrf;
    return h;
  };
  async function parseErr(r) {
    let code = 'http_' + r.status, msg = r.statusText || 'request failed';
    try { const j = await r.json(); if (j.error) { code = j.error.code; msg = j.error.message; } } catch (e) { /* not JSON */ }
    return new ApiError(r.status, code, msg);
  }
  // ---- reaching the gateway ----
  // A fetch that gets no HTTP answer at all ("Load failed" in WebKit,
  // "Failed to fetch" in Chrome) means the gateway did not answer. In the
  // phone apps that is what happens when the system took the in-process
  // gateway's listening socket away while the app was in the background:
  // the app is asked to check and reopen it (remohop-mobile.js
  // ensureGateway, over the app's bridge) and the request is sent once more.
  // If that fails too, the page says so with a Retry button instead of the
  // browser's raw text.
  const LOST = 'The gateway is not answering. Try again.';
  const wait = ms => new Promise(r => setTimeout(r, ms));
  async function ensureGateway() {
    const m = window.remohopMobile;
    if (m && typeof m.ensureGateway === 'function') {
      try { await Promise.race([m.ensureGateway(), wait(8000)]); return; } catch (e) { /* not in the app, or it failed: retry anyway */ }
    }
    await wait(700);
  }
  let lostBar = null;
  function gatewayLost(on) {
    if (!on) { if (lostBar) lostBar.hidden = true; return; }
    if (!lostBar) {
      lostBar = document.createElement('div');
      lostBar.className = 'banner bad';
      lostBar.setAttribute('role', 'alert');
      const msg = document.createElement('span');
      I.say(msg, 'gw.lost');
      const btn = document.createElement('button');
      btn.type = 'button'; btn.id = 'gwretry';
      I.say(btn, 'gw.retry');
      btn.addEventListener('click', async () => {
        btn.disabled = true;
        try { await ensureGateway(); if (st.csrf) await SD.api('GET', 'connections'); else await start(); } catch (e) { /* the bar stays */ }
        btn.disabled = false;
      });
      lostBar.append(msg, ' ', btn);
      $('banner').after(lostBar);
    }
    lostBar.hidden = false;
  }
  async function gwFetch(u, init) {
    for (let attempt = 0; ; attempt++) {
      try {
        const r = await fetch(u, init);
        gatewayLost(false);
        return r;
      } catch (e) {
        if (e && e.name === 'AbortError') throw e;
        if (attempt > 0) { gatewayLost(true); throw new ApiError(0, 'gateway_unreachable', LOST); }
        await ensureGateway();
      }
    }
  }

  SD.api = async function (method, path, body, params) {
    const init = {method, headers: SD.headers(body === undefined ? {} : {'Content-Type': 'application/json'}), credentials: 'same-origin'};
    if (body !== undefined) init.body = JSON.stringify(body);
    const r = await gwFetch(SD.url(path, params), init);
    if (!r.ok) {
      const e = await parseErr(r);
      if (e.code === 'login_required' || (e.code === 'bad_csrf')) sessionLost();
      throw e;
    }
    return r;
  };
  SD.json = async (method, path, body, params) => (await SD.api(method, path, body, params)).json();
  SD.ticket = async purpose => (await SD.json('POST', 'ticket', {purpose})).ticket;
  SD.wsURL = (path, params) => {
    const u = SD.url(path, params);
    u.protocol = u.protocol === 'https:' ? 'wss:' : 'ws:';
    return u.toString();
  };
  // errVar is an error as an i18n variable: the translation of its code
  // where there is one (English keeps the gateway's own message).
  SD.errVar = e => {
    const msg = (e && e.message) ? e.message : String(e);
    return e && e.code && I.has('err.' + e.code, 'zh') ? {k: 'err.' + e.code, fb: msg} : msg;
  };
  SD.errText = e => { const v = SD.errVar(e); return typeof v === 'string' ? v : I.t('raw', {s: v}); };
  SD.sayErr = (el, e) => I.say(el, 'raw', {s: SD.errVar(e)});

  // ---- small UI helpers ----
  SD.show = (el, on) => { el.hidden = !on; };
  SD.setText = (id, t) => I.plain($(id), t);
  // SD.status shows a translated status line (null key: clear it).
  SD.status = (id, key, vars) => I.say($(id), key, vars);
  let dlgResolve = null;
  // How the user last interacted: a dialog opened from the keyboard puts
  // the focus on a button (shown with :focus-visible); one opened by mouse
  // or touch focuses the dialog itself, so no button wears a focus ring.
  let byKeyboard = false;
  addEventListener('keydown', e => { if (!e.ctrlKey && !e.metaKey) byKeyboard = true; }, true);
  addEventListener('pointerdown', () => { byKeyboard = false; }, true);
  // SD.ask shows a modal dialog; buttons is [{label, value, primary,
  // danger, cancel}], and the promise resolves to the chosen value (or null
  // when dismissed) together with the input text and the "apply to all"
  // box. A dialog with a danger button starts on its cancel button.
  SD.ask = function ({title, message, buttons, input, all}) {
    return new Promise(resolve => {
      const dlg = $('dlg');
      if (dlgResolve) { const r = dlgResolve; dlgResolve = null; r({value: null, text: '', all: false}); }
      $('dlg-title').textContent = title;
      $('dlg-msg').textContent = message || '';
      $('dlg-msg').hidden = !message;
      const inp = $('dlg-input');
      inp.hidden = !input; inp.value = input && input.value || '';
      if (input) { inp.setAttribute('aria-label', input.label || title); inp.placeholder = input.placeholder || ''; }
      $('dlg-all-row').hidden = !all; $('dlg-all').checked = false;
      const box = $('dlg-buttons');
      box.textContent = '';
      dlgResolve = resolve;
      let first = null;
      const finish = b => { const r = {value: b.value, text: inp.value, all: $('dlg-all').checked}; dlgResolve = null; dlg.close(); resolve(r); };
      for (const b of buttons) {
        const el = document.createElement('button');
        el.type = 'button'; el.textContent = b.label;
        el.className = [b.primary ? 'primary' : '', b.danger ? 'danger' : '', b.cancel ? 'cancel' : ''].filter(Boolean).join(' ');
        el.addEventListener('click', () => finish(b));
        box.appendChild(el);
      }
      const danger = buttons.some(b => b.danger);
      inp.onkeydown = e => { if (e.key === 'Enter') { e.preventDefault(); const p = buttons.find(b => b.primary || b.danger); if (p) finish(p); } };
      dlg.onclose = () => { if (dlgResolve) { dlgResolve = null; resolve({value: null, text: '', all: false}); } };
      dlg.showModal();
      if (input) first = inp;
      else if (byKeyboard) first = (danger ? box.querySelector('.cancel') : box.querySelector('.primary')) || box.firstChild;
      if (first) first.focus();
      else { dlg.tabIndex = -1; dlg.focus({preventScroll: true}); }
    });
  };
  // SD.confirm asks a yes/no question; opts.destructive styles the
  // confirming button as destructive. The phone apps answer it with a
  // native dialog (remohop-mobile.js replaces SD.confirm, SD.prompt and
  // SD.menu there).
  SD.confirm = async (title, message, okLabel, opts) => {
    const d = !!(opts && opts.destructive);
    return (await SD.ask({title, message, buttons: [{label: I.t('cancel'), value: false, cancel: true},
      {label: okLabel || I.t('ok'), value: true, primary: !d, danger: d}]})).value === true;
  };
  // SD.prompt asks for one line of text: the text, or null when cancelled.
  SD.prompt = async ({title, message, value, label, placeholder, okLabel}) => {
    const r = await SD.ask({title, message, input: {value: value || '', label, placeholder},
      buttons: [{label: I.t('cancel'), value: false, cancel: true}, {label: okLabel || I.t('ok'), value: true, primary: true}]});
    return r.value === true ? r.text : null;
  };
  // SD.menu offers a few actions ({label, value, destructive}): the chosen
  // value, or null.
  SD.menu = async ({title, message, actions}) => {
    const r = await SD.ask({title, message, buttons: [{label: I.t('cancel'), value: null, cancel: true},
      ...actions.map(a => ({label: a.label, value: a.value, danger: !!a.destructive}))]});
    return r.value === undefined ? null : r.value;
  };
  SD.fmtSize = n => {
    if (n < 1024) return n + ' B';
    const u = ['KiB', 'MiB', 'GiB', 'TiB']; let i = -1;
    do { n /= 1024; i++; } while (n >= 1024 && i < u.length - 1);
    return n.toFixed(n < 10 ? 1 : 0) + ' ' + u[i];
  };
  SD.cur = () => st.cur;

  // banner shows key (with vars) in the bar under the header; no key hides it.
  function banner(key, vars, bad) {
    const b = $('banner');
    I.say(b, key, vars); b.hidden = !key; b.classList.toggle('bad', !!bad);
  }

  // ---- session ----
  function store(k, v) { try { if (v === null) sessionStorage.removeItem(k); else sessionStorage.setItem(k, v); } catch (e) { /* private mode */ } }
  function load(k) { try { return sessionStorage.getItem(k); } catch (e) { return null; } }

  // ---- embedded in the desktop app ----
  // The desktop app (internal/desktop) shows each host in a tab of its own
  // window: this page in an iframe, with ?embed=1&connect=TARGET&parent=
  // ORIGIN. The page then serves that one host: no header or host
  // switcher, it connects at once (or asks for the password), and tells
  // the app how the session is doing. It takes orders from that one parent
  // origin only: disconnect, and hand over the session id when the user
  // pops the host out into a window (which loads the page with #sid=...).
  const params = new URLSearchParams(location.search);
  const embed = params.get('embed') === '1';
  const embedTarget = (params.get('connect') || '').trim();
  const parentOrigin = (() => { try { const o = new URL(params.get('parent') || ''); return o.protocol === 'http:' && (o.hostname === '127.0.0.1' || o.hostname === 'localhost') ? o.origin : ''; } catch (e) { return ''; } })();
  if (/^#sid=[A-Za-z0-9_-]{16,64}$/.test(location.hash)) {
    try { sessionStorage.setItem('sd.sid', location.hash.slice(5)); } catch (e) { /* storage blocked */ }
    history.replaceState(null, '', location.pathname + location.search);
  }
  if (embed) document.documentElement.classList.add('embed');
  // The desktop app's theme (light, dark, or "" for the system's): from the
  // address at first, then from the app's messages.
  function applyTheme(t) {
    if (t === 'light' || t === 'dark') document.documentElement.dataset.theme = t;
    else delete document.documentElement.dataset.theme;
  }
  if (embed) applyTheme(params.get('theme') || '');
  const inFrame = embed && parentOrigin && window.parent !== window;
  function tellParent(m) { if (inFrame) window.parent.postMessage(m, parentOrigin); }
  let embedConn = null; // the key of the session this page serves
  if (inFrame) {
    window.addEventListener('message', async ev => {
      if (ev.source !== window.parent || ev.origin !== parentOrigin || !ev.data || typeof ev.data !== 'object') return;
      if (ev.data.type === 'remohop-disconnect' && embedConn) {
        callTabs('hide');
        try { await SD.api('POST', 'disconnect', {c: embedConn}); } catch (e) { /* gone */ }
      } else if (ev.data.type === 'remohop-theme') {
        applyTheme(String(ev.data.theme || ''));
      } else if (ev.data.type === 'remohop-sid-request' && st.sid) {
        tellParent({type: 'remohop-sid', sid: st.sid});
      }
    });
  }
  function embedFollow() {
    if (!embed) return;
    const c = st.conns.find(x => x.key === embedConn) || st.conns.find(x => x.target === embedTarget || x.key === embedTarget);
    if (c) {
      embedConn = c.key;
      const saved = (st.peers || []).find(p => p.target === c.target && p.alias);
      tellParent({type: 'remohop-session', state: c.state || 'connected', title: saved ? saved.alias : c.target});
    } else if (embedConn) {
      tellParent({type: 'remohop-closed'});
      embedConn = null;
    }
  }

  function applySession(j) {
    st.csrf = j.csrf; st.mode = j.mode; st.version = j.version;
    if (j.sid) { st.sid = j.sid; store('sd.sid', j.sid); }
    st.conns = j.conns || [];
    st.savedPeers = j.saved_peers !== false;
    $('ver').textContent = 'RemoHop ' + (j.version || '');
    if (j.target && !$('target').value) $('target').value = j.target;
    $('logout').hidden = j.mode !== 'password';
  }

  async function start() {
    try {
      const r = await gwFetch(SD.url('session'), {method: 'POST', credentials: 'same-origin',
        headers: {'Content-Type': 'application/json'}, body: JSON.stringify({sid: load('sd.sid') || ''})});
      if (r.status === 401) { showLogin(); return; }
      if (!r.ok) throw await parseErr(r);
      applySession(await r.json());
      await afterLogin();
    } catch (e) {
      I.say($('loading'), 'gw.unreachable', {msg: SD.errVar(e)});
    }
  }

  function showLogin() {
    for (const id of ['loading', 'connect', 'workspace']) $(id).hidden = true;
    $('hostbar').hidden = true;
    $('login').hidden = false;
    $('gwpass').focus();
  }

  function sessionLost() {
    if (st.mode === 'password') { st.csrf = null; teardownAll(); showLogin(); banner('gw.ended.login', null, true); }
    else {
      // Loopback: the gateway closes a page's connections after 15 minutes
      // without its heartbeat (the computer slept, the tab was paused or
      // discarded). Say so, with a button.
      banner('gw.ended.reload', null, true);
      const b = document.createElement('button');
      b.type = 'button'; b.className = 'secondary'; b.id = 'reloadbtn';
      I.say(b, 'gw.reload');
      b.addEventListener('click', () => location.reload());
      $('banner').append(' ', b);
    }
  }

  $('loginform').addEventListener('submit', async ev => {
    ev.preventDefault();
    I.plain($('loginerr'), '');
    const pw = $('gwpass').value;
    try {
      const r = await fetch(SD.url('login'), {method: 'POST', credentials: 'same-origin',
        headers: {'Content-Type': 'application/json'}, body: JSON.stringify({password: pw})});
      if (!r.ok) throw await parseErr(r);
      $('gwpass').value = '';
      applySession(await r.json());
      banner(null);
      await afterLogin();
    } catch (e) {
      $('gwpass').value = '';
      SD.sayErr($('loginerr'), e);
    }
  });
  $('logout').addEventListener('click', async () => {
    try { await SD.api('POST', 'logout', {}); } catch (e) { /* already gone */ }
    st.csrf = null; teardownAll(); store('sd.sid', null);
    if (st.mode === 'password') showLogin(); else location.reload();
  });

  async function afterLogin() {
    $('loading').hidden = true; $('login').hidden = true;
    await loadPeers();
    $('peerrow').hidden = !(st.peers && st.peers.length);
    $('pairrow').hidden = !st.savedPeers;
    $('passhint').hidden = !st.savedPeers;
    // Remembering the host is what a desktop user wants on their own
    // gateway (loopback); on a shared one (password mode) it stays a choice.
    if (st.mode !== 'password' && !pairDefaulted) { pairDefaulted = true; $('pair').checked = true; }
    refreshConns(st.conns);
    if (embed) {
      const c = st.conns.find(x => x.target === embedTarget || x.key === embedTarget);
      if (c) selectConn(c.key);
      else {
        showConnect();
        if (embedTarget) {
          const p = (st.peers || []).find(q => q.target === embedTarget || q.alias === embedTarget);
          if (p) pickPeer(p); else { $('target').value = embedTarget; $('hostpass').focus(); }
        }
      }
    } else if (st.conns.length) selectConn(st.conns[0].key); else showConnect();
    // One heartbeat, however often the user logs in again or retries.
    if (!heartbeatTimer) heartbeatTimer = setInterval(heartbeat, 15000);
  }
  let heartbeatTimer = 0, pairDefaulted = false;

  async function loadPeers() {
    st.peers = [];
    if (!st.savedPeers) return;
    try {
      st.peers = (await SD.json('GET', 'peers')).peers || [];
      const sel = $('peersel');
      sel.length = 1;
      for (const p of st.peers) {
        const o = document.createElement('option');
        o.value = p.target;
        const label = (p.alias ? p.alias + ' · ' : '') + p.target;
        if (p.paired || p.key_login) I.say(o, p.paired ? 'peer.opt.paired' : 'peer.opt.key', {label});
        else if (p.saved_password) I.say(o, 'peer.opt.saved', {label}); else I.plain(o, label);
        sel.appendChild(o);
      }
      renderPeerCards();
    } catch (e) { /* the form still works without the list */ }
  }
  // The saved hosts under the form, in two lists with plain states: paired
  // (or authorised by key) hosts connect straight away, since they need no
  // password; the others ("recent") fill in the form and wait for the
  // password, with no connection attempt and so no error.
  function renderPeerCards() {
    const ready = p => p.paired || p.key_login || p.saved_password;
    const lists = [[$('peercards'), $('peers-h'), st.peers.filter(ready)], [$('peercards-recent'), $('peers-recent-h'), st.peers.filter(p => !ready(p))]];
    for (const [ul, h, list] of lists) {
      ul.textContent = '';
      ul.hidden = h.hidden = !list.length;
      for (const p of list) renderPeerCard(ul, p);
    }
  }
  const peerLabel = p => p.alias || p.target;
  // pickPeer does what a tap on a saved host does: a host that needs no
  // password connects at once; one that does is filled in, waiting for it.
  function pickPeer(p) {
    const ready = p.paired || p.key_login || p.saved_password;
    $('target').value = p.alias || p.target;
    $('peersel').value = p.target;
    I.plain($('connerr'), '');
    if (ready && !attemptId) { $('hostpass').value = ''; $('connectform').requestSubmit(); } else $('hostpass').focus();
  }
  function renderPeerCard(ul, p) {
    const li = document.createElement('li');
    li.className = 'peeritem';
    const b = document.createElement('button');
    b.type = 'button'; b.className = 'peer';
    const icon = document.createElement('span');
    icon.className = 'peericon'; icon.setAttribute('aria-hidden', 'true');
    const text = document.createElement('span');
    text.className = 'peertext';
    const name = document.createElement('strong');
    name.textContent = peerLabel(p);
    const sub = document.createElement('small');
    const ready = p.paired || p.key_login || p.saved_password;
    I.say(sub, p.alias ? 'peer.sub.alias' : 'raw', {target: p.target, s: {k: ready ? 'peer.ready' : 'peer.needspw'}, state: {k: ready ? 'peer.ready' : 'peer.needspw'}});
    text.append(name, sub);
    b.append(icon, text);
    I.sayAttr(b, 'aria-label', ready ? 'peer.aria.ready' : 'peer.aria.form', {label: (p.alias ? p.alias + ', ' : '') + p.target});
    b.addEventListener('click', () => pickPeer(p));
    // Long press (touch) or right click: the same actions as the ⋯ button.
    b.addEventListener('contextmenu', e => { e.preventDefault(); peerActions(p); });
    const more = document.createElement('button');
    more.type = 'button'; more.className = 'peermore';
    I.sayAttr(more, 'aria-label', 'peer.more', {label: peerLabel(p)});
    more.addEventListener('click', () => peerActions(p));
    li.append(b, more);
    ul.appendChild(li);
  }
  // A saved host's actions: rename it, or forget it (with its pairing).
  async function peerActions(p) {
    const label = peerLabel(p);
    const actions = [{label: I.t('peer.rename'), value: 'rename'}];
    if (p.saved_password) actions.push({label: I.t('peer.showpw'), value: 'showpw'}, {label: I.t('peer.forgetpw'), value: 'forgetpw', destructive: true});
    actions.push({label: I.t('peer.forget'), value: 'forget', destructive: true});
    const a = await SD.menu({title: label, message: p.alias ? p.target : '', actions});
    if (a === 'showpw') {
      let pw;
      try { pw = (await SD.json('POST', 'peers/password', {target: p.target})).password; }
      catch (e) { banner('raw', {s: SD.errVar(e)}, true); return; }
      if (await SD.confirm(I.t('peer.pw.title', {label}), pw, I.t('peer.pw.copy'))) {
        try { await navigator.clipboard.writeText(pw); } catch (e) { /* the dialog showed it */ }
      }
      return;
    }
    if (a === 'forgetpw') {
      try { await SD.json('POST', 'peers/password', {target: p.target, forget: true}); }
      catch (e) { banner('raw', {s: SD.errVar(e)}, true); return; }
      banner('peer.forgetpw.done', null, false);
      await reloadPeers();
      return;
    }
    if (a === 'rename') {
      const name = await SD.prompt({title: I.t('peer.rename.title', {label}), message: I.t('peer.rename.msg', {target: p.target}),
        value: p.alias || '', label: I.t('peer.rename.input'), placeholder: p.target, okLabel: I.t('save')});
      if (name === null || name.trim() === (p.alias || '')) return;
      try { await SD.json('POST', 'peers/alias', {target: p.target, alias: name.trim()}); }
      catch (e) { banner('raw', {s: SD.errVar(e)}, true); return; }
      await reloadPeers();
    } else if (a === 'forget') {
      if (!await SD.confirm(I.t('peer.forget.title', {label}), I.t('peer.forget.msg'), I.t('peer.forget.ok'), {destructive: true})) return;
      try { await SD.json('POST', 'peers/forget', {target: p.target}); }
      catch (e) { banner('raw', {s: SD.errVar(e)}, true); return; }
      await reloadPeers();
      heartbeat();
    }
  }
  async function reloadPeers() {
    if (!st.csrf) return;
    await loadPeers();
    $('peerrow').hidden = !(st.peers && st.peers.length);
    if (!st.peers.length) renderPeerCards();
    const c = st.conns.find(x => x.key === st.cur);
    if (c) {
      const saved = (st.peers || []).find(q => q.target === c.target && q.alias);
      if (saved) I.say($('sessname'), 'sess.alias', {alias: saved.alias, target: c.target}); else I.plain($('sessname'), c.target);
    }
  }
  // The phone apps' Settings rename and forget hosts too: they ask the page
  // to show the list again (remohop-mobile.js peersChanged).
  SD.reloadPeers = reloadPeers;
  // SD.connectTo connects to a saved host as a tap on it does (the apps'
  // Settings → Saved hosts → Connect), or shows its session if it has one.
  SD.connectTo = async target => {
    await reloadPeers();
    const c = st.conns.find(x => x.target === target);
    if (c) { selectConn(c.key); return; }
    const p = (st.peers || []).find(q => q.target === target) || {target};
    showConnect();
    pickPeer(p);
  };
  $('peersel').addEventListener('change', () => { if ($('peersel').value) { $('target').value = $('peersel').value; $('hostpass').focus(); } });

  async function heartbeat() {
    if (!st.csrf) return;
    try { refreshConns((await SD.json('GET', 'connections')).conns); } catch (e) { /* sessionLost handles auth */ }
  }
  SD.heartbeat = heartbeat; // a tab whose socket closed asks whether the session is still there

  function refreshConns(list) {
    const old = st.cur;
    st.conns = list || [];
    const sel = $('hostsel');
    sel.textContent = '';
    for (const c of st.conns) {
      const o = document.createElement('option');
      o.value = c.key; o.textContent = c.target;
      sel.appendChild(o);
    }
    if (old && !st.conns.some(c => c.key === old)) {
      st.cur = null;
      callTabs('hide');
      banner('sess.ended', {target: old}, true);
      if (embed) { embedFollow(); showConnect(); return; }
      if (st.conns.length) selectConn(st.conns[0].key); else showConnect();
      return;
    }
    if (st.cur) sel.value = st.cur;
    followState();
    embedFollow();
  }

  // In the phone apps the gateway re-establishes a session that dropped:
  // its state is then "reconnecting" or "waiting_approval" (the host asks
  // its owner again), and "failed" if it cannot be done. The page says so
  // in the banner, keeps the workspace, polls faster, and re-opens the tab
  // once the session is "connected" again (the terminal re-attaches to its
  // shell, files reload, the desktop restarts).
  const connStates = {};
  let fastPoll = null;
  function followState() {
    const c = st.conns.find(x => x.key === st.cur);
    const state = (c && c.state) || 'connected';
    const now = c ? state + '/' + (c.reconnects || 0) : '';
    const prev = c ? (connStates[c.key] || 'connected/' + (c.reconnects || 0)) : '';
    if (c) connStates[c.key] = now;
    if (c && now !== prev) {
      if (state === 'reconnecting') { callTabs('hide'); banner('sess.reconnecting', {target: c.target}, false); }
      else if (state === 'waiting_approval') { callTabs('hide'); banner('sess.reapproval', {target: c.target}, false); }
      else if (state === 'failed') { callTabs('hide'); banner('sess.lost', {target: c.target, msg: c.error ? SD.errVar(c.error) : ''}, true); }
      else { banner(null); if (!$('workspace').hidden) showTab(load('sd.tab') || 'desktop'); }
    }
    if (state !== 'connected' && state !== 'failed' && !fastPoll) fastPoll = setTimeout(() => { fastPoll = null; heartbeat(); }, 2000);
  }

  // ---- connecting ----
  let attemptId = null, polling = false;
  // Connection failures are explained in the page's own words: reason.* in
  // i18n.js for a known code, the host's own words for a refusal, a generic
  // line otherwise. The gateway's technical text goes under "Details" (for
  // support), not in the message itself.
  function reasonKey(code, msg) {
    if (code === 'bad_target') return 'reason.bad_target';
    // Cases the gateway has no code for yet (they come as remote_error)
    // are recognised by their words.
    if (!code || code === 'remote_error') {
      if (/no rendezvous server configured/i.test(msg)) return 'reason.no_rendezvous';
      if (/unknown target /i.test(msg)) return 'reason.bad_target';
      if (/no application protocol/i.test(msg)) return 'reason.signal_wrong_protocol';
      if (/rendezvous server .*unreachable/i.test(msg)) return 'reason.rendezvous_unreachable';
    }
    if (code && I.has('reason.' + code, 'en')) return 'reason.' + code;
    return null;
  }
  function showConnErr(e) {
    const box = $('connerr');
    I.plain(box, '');
    const code = e && e.code, msg = (e && e.message) || (e ? String(e) : '');
    const main = document.createElement('div');
    main.className = 'errmain';
    const key = reasonKey(code, msg);
    if (key) I.say(main, key);
    else if (code === 'host_refused' || code === 'feature_off') I.say(main, 'refused', {msg});
    else if (code && I.has('err.' + code, 'zh')) I.say(main, 'err.' + code);
    else I.say(main, 'connect.failed');
    box.appendChild(main);
    // An expected case (the host needs its password): no technical detail.
    if (code === 'password_required' || code === 'saved_password_rejected') { $('hostpass').focus(); return; }
    if (msg && msg !== main.textContent) {
      const d = document.createElement('details');
      d.className = 'errdetail';
      const sum = document.createElement('summary');
      I.say(sum, 'connect.details');
      const pre = document.createElement('code');
      pre.textContent = msg + (code ? ' [' + code + ']' : '');
      d.append(sum, pre);
      box.appendChild(d);
    }
  }
  SD.showConnErr = showConnErr;
  function showConnect() {
    $('connect').hidden = false; $('workspace').hidden = true; $('hostbar').hidden = !st.conns.length;
    $('hostbar').querySelector('#newconn').hidden = true;
    $('target').focus();
  }
  $('newconn').addEventListener('click', showConnect);

  // normTarget fixes what phone keyboards make of an address: full-width
  // punctuation from a Chinese keyboard, and "1.2.3.4.21118" typed with a
  // dot for the colon (four octets and a port can only mean IPv4:PORT).
  function normTarget(v) {
    v = String(v || '').replace(/\uff1a/g, ':').replace(/[\u3002\uff0e]/g, '.').trim();
    const m = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,5})$/.exec(v);
    if (m && m.slice(1, 5).every(o => +o <= 255) && +m[5] >= 1 && +m[5] <= 65535) v = m.slice(1, 5).join('.') + ':' + m[5];
    return v;
  }
  SD.normTarget = normTarget;

  $('connectform').addEventListener('submit', async ev => {
    ev.preventDefault();
    if (attemptId) return;
    const fixed = normTarget($('target').value);
    if (fixed !== $('target').value) $('target').value = fixed;
    I.plain($('connerr'), ''); I.say($('connstatus'), 'connect.connecting');
    $('connectbtn').disabled = true; $('cancelconn').hidden = false;
    // Pairing needs a password login; with none (a paired host) there is nothing to remember.
    const body = {target: $('target').value, password: $('hostpass').value, pair: $('pair').checked && $('hostpass').value !== ''};
    $('hostpass').value = '';
    try {
      const a = await SD.json('POST', 'connect', body);
      attemptId = a.attempt;
      await poll();
    } catch (e) {
      endAttempt();
      showConnErr(e);
    }
  });
  $('cancelconn').addEventListener('click', async () => {
    if (attemptId) { try { await SD.api('DELETE', 'connect/' + attemptId); } catch (e) { /* ignore */ } }
  });
  function endAttempt() {
    attemptId = null; polling = false;
    $('connectbtn').disabled = false; $('cancelconn').hidden = true; I.say($('connstatus'), null);
  }
  async function poll() {
    polling = true;
    while (attemptId && polling) {
      let j;
      try { j = await SD.json('GET', 'connect/' + attemptId); } catch (e) { endAttempt(); showConnErr(e); return; }
      if (j.state === 'waiting_approval') I.say($('connstatus'), 'connect.waiting');
      else if (j.state === 'dialing') {
        if (j.notices && j.notices.length) I.say($('connstatus'), 'connect.dialing', {notice: j.notices[j.notices.length - 1]});
        else I.say($('connstatus'), 'connect.connecting');
      }
      if (j.state === 'connected') {
        endAttempt();
        I.plain($('connerr'), '');
        // A "remember" that did not work is said in plain words (the session
        // works, the host still needs its password next time): by its code
        // (pair_refused: the host does not allow it), or for an older gateway
        // by its notice.
        const notices = (j.notices || []).filter(n => !/^pairing failed/.test(n));
        const pairFailed = (j.pair && j.pair.ok === false) || notices.length < (j.notices || []).length;
        let pairMsg = pairFailed ? (j.pair && j.pair.code === 'pair_refused' ? 'connect.pairrefused' : 'connect.pairfailed') : null;
        if (j.pair && j.pair.ok && j.pair.code === 'password_saved') pairMsg = 'connect.pwsaved';
        if (notices.length) banner('raw', {s: notices.join(' ')}, false);
        await loadPeers();
        $('peerrow').hidden = !(st.peers && st.peers.length);
        if (embed) embedConn = j.conn.key;
        refreshConns((await SD.json('GET', 'connections')).conns);
        selectConn(j.conn.key);
        if (pairMsg) {
          // Shown last, so it stays: with the first-connection notice when there is one.
          const b = $('banner');
          const first = !b.hidden && b.getAttribute('data-i18n') === 'sess.firstkey';
          if (first) banner('pair2', {a: {k: pairMsg}, b: {k: 'sess.firstkey', v: JSON.parse(b.getAttribute('data-i18n-vars') || 'null')}}, false);
          else banner(pairMsg, null, false);
        }
        return;
      }
      if (j.state === 'failed' || j.state === 'canceled') {
        endAttempt();
        const e = j.error || {};
        if (j.state === 'canceled') I.say($('connerr'), 'connect.canceled');
        else showConnErr(e);
        return;
      }
      await new Promise(r => setTimeout(r, 600));
    }
  }

  // pathVar names a connection path (lan, direct, punched, relayed...).
  SD.pathVar = p => ({k: 'path.' + (p || 'unknown'), fb: p || '?'});

  function selectConn(key) {
    const c = st.conns.find(x => x.key === key);
    if (!c) return;
    callTabs('hide');
    st.cur = key;
    $('hostsel').value = key;
    $('connect').hidden = true; $('workspace').hidden = false; $('hostbar').hidden = false;
    $('newconn').hidden = false;
    const saved = (st.peers || []).find(p => p.target === c.target && p.alias);
    if (saved) I.say($('sessname'), 'sess.alias', {alias: saved.alias, target: c.target}); else I.plain($('sessname'), c.target);
    const pb = $('pathbadge');
    const path = SD.pathVar(c.path);
    I.say(pb, 'join', {a: path, b: c.paired ? {k: 'path.paired'} : ''});
    pb.classList.toggle('relayed', !!c.relayed);
    if (c.relayed) I.sayAttr(pb, 'title', 'path.relayed.title'); else I.sayAttr(pb, 'title', 'path.via', {path});
    if (c.new_host) banner('sess.firstkey', {fp: c.host_fingerprint || ''}, false);
    const savedTab = load('sd.tab');
    if (savedTab) { showTab(savedTab); return; }
    // First tab: the desktop, where the host has one (Desktop leads the tab
    // bar); a headless server opens on its terminal.
    firstTab(key).then(t => { if (st.cur === key && !active) showTab(t); });
  }
  async function firstTab(key) {
    try {
      const j = await SD.json('GET', 'info', undefined, {c: key});
      return j.info && j.info.display === false ? 'terminal' : 'desktop';
    } catch (e) { return 'desktop'; }
  }
  $('hostsel').addEventListener('change', () => selectConn($('hostsel').value));
  $('disconnect').addEventListener('click', async e => {
    const k = st.cur; if (!k) return;
    // A tap asks first (a script's click() does not: the tests use it).
    if (e.isTrusted) {
      const host = $('sessname').textContent || (st.conns.find(x => x.key === k) || {}).target || '';
      if (!await SD.confirm(I.t('disc.title', {host}), I.t('disc.msg'), I.t('disc.ok'), {destructive: true})) return;
      if (st.cur !== k) return;
    }
    callTabs('hide'); st.cur = null;
    try { await SD.api('POST', 'disconnect', {c: k}); } catch (e) { /* ignore */ }
    refreshConns((await SD.json('GET', 'connections')).conns);
    if (st.conns.length) selectConn(st.conns[0].key); else showConnect();
  });

  function teardownAll() { callTabs('hide'); st.cur = null; st.conns = []; }

  // ---- tabs ----
  const order = ['desktop', 'terminal', 'files', 'clipboard', 'info'];
  let active = null;
  function callTabs(what) { if (what === 'hide' && active && SD.tabs[active] && SD.tabs[active].hide) SD.tabs[active].hide(); if (what === 'hide') active = null; }
  function showTab(id) {
    if (!order.includes(id)) id = 'files';
    if (active && active !== id) callTabs('hide');
    for (const t of order) {
      const sel = t === id;
      $('tab-' + t).setAttribute('aria-selected', sel ? 'true' : 'false');
      $('tab-' + t).tabIndex = sel ? 0 : -1;
      $('panel-' + t).hidden = !sel;
    }
    store('sd.tab', id);
    const prev = active; active = id;
    if (SD.tabs[id] && SD.tabs[id].show && (prev !== id || true)) SD.tabs[id].show(st.cur);
  }
  SD.showTab = showTab;
  for (const t of order) {
    $('tab-' + t).addEventListener('click', () => showTab(t));
    $('tab-' + t).addEventListener('keydown', e => {
      const i = order.indexOf(t); let n = -1;
      if (e.key === 'ArrowRight') n = (i + 1) % order.length;
      else if (e.key === 'ArrowLeft') n = (i + order.length - 1) % order.length;
      else if (e.key === 'Home') n = 0; else if (e.key === 'End') n = order.length - 1;
      if (n >= 0) { e.preventDefault(); showTab(order[n]); $('tab-' + order[n]).focus(); }
    });
  }

  // Report per-feature API errors the same way everywhere: the host's own
  // words for a refusal, in the tab's error line.
  SD.showErr = (id, e) => {
    const el = $(id);
    I.plain(el, '');
    if (!e) return;
    if (e.code === 'feature_off' || e.code === 'host_refused') I.say(el, 'refused', {msg: e.message});
    else if (e.code === 'disconnected' || e.code === 'reconnecting') { SD.sayErr(el, e); heartbeat(); }
    else SD.sayErr(el, e);
  };

  // ---- clipboard tab ----
  async function clipFetch() {
    SD.showErr('c-err'); SD.status('c-status', null);
    try {
      const j = await SD.json('POST', 'clip/get', {c: st.cur});
      if (j.kind === 'text') { $('c-remote').value = j.text; SD.status('c-status', 'c.fetched', {n: j.text.length}); }
      else if (j.kind === 'image') { $('c-remote').value = ''; SD.status('c-status', 'c.image', {size: SD.fmtSize(j.size)}); }
      else { $('c-remote').value = ''; SD.status('c-status', 'c.empty'); }
    } catch (e) { SD.showErr('c-err', e); }
  }
  async function clipSend() {
    SD.showErr('c-err'); SD.status('c-status', null);
    const text = $('c-local').value;
    if (!text) { SD.status('c-status', 'c.nothing'); return; }
    try {
      await SD.json('POST', 'clip/set', {c: st.cur, text});
      SD.status('c-status', 'c.sent', {n: text.length});
    } catch (e) { SD.showErr('c-err', e); }
  }
  $('c-fetch').addEventListener('click', clipFetch);
  $('c-send').addEventListener('click', clipSend);
  $('c-copy').addEventListener('click', async () => {
    const t = $('c-remote').value;
    try { await navigator.clipboard.writeText(t); SD.status('c-status', 'c.copied'); }
    catch (e) { $('c-remote').select(); SD.status('c-status', 'c.selected'); }
  });
  $('c-paste').addEventListener('click', async () => {
    try { $('c-local').value = await navigator.clipboard.readText(); }
    catch (e) { $('c-local').focus(); SD.status('c-status', 'c.blocked'); }
  });
  SD.tabs.clipboard = {show() { SD.showErr('c-err'); }};

  // ---- info tab ----
  // [label key, value]: a value is text from the host or an i18n variable.
  const free = (f, t) => ({k: 'i.free', v: {free: SD.fmtSize(f), total: SD.fmtSize(t)}});
  const infoRows = [
    ['i.host', i => i.hostname], ['i.system', i => [i.distro_name, i.distro_version].filter(Boolean).join(' ') || i.os],
    ['i.osarch', i => i.os + ' / ' + i.arch], ['i.kernel', i => i.kernel], ['i.cpu', i => ({k: 'i.cpu.v', v: {model: i.cpu_model, n: i.cpu_cores}})],
    ['i.memory', i => free(i.mem_available_bytes, i.mem_total_bytes)],
    ['i.disk', i => free(i.root_free_bytes, i.root_total_bytes)],
    ['i.uptime', i => ({k: 'i.uptime.v', v: {h: Math.floor(i.uptime_seconds / 3600), m: Math.floor(i.uptime_seconds % 3600 / 60)}})],
    ['i.load', i => i.load_average.map(x => x.toFixed(2)).join(' ')], ['i.user', i => i.user], ['i.home', i => i.home], ['i.shell', i => i.shell],
    ['i.time', i => i.local_time + ' ' + i.timezone], ['i.display', i => ({k: i.display ? 'i.yes' : 'i.no'})], ['remohop', i => i.remohop_version],
  ];
  async function infoLoad() {
    SD.showErr('i-err');
    try {
      const j = await SD.json('GET', 'info', undefined, {c: st.cur});
      const dl = $('i-list'); dl.textContent = '';
      for (const [k, f] of infoRows) {
        let v; try { v = f(j.info); } catch (e) { v = ''; }
        if (v === undefined || v === '') continue;
        const dt = document.createElement('dt'), dd = document.createElement('dd');
        if (k === 'remohop') dt.textContent = k; else I.say(dt, k);
        if (typeof v === 'object') I.say(dd, 'raw', {s: v}); else dd.textContent = String(v);
        dl.append(dt, dd);
      }
      const c = j.conn || {};
      const dt = document.createElement('dt'), dd = document.createElement('dd');
      I.say(dt, 'i.conn');
      I.say(dd, 'join', {a: c.path ? SD.pathVar(c.path) : '?',
        b: {k: 'join', v: {a: c.auth ? {k: 'i.conn.auth', v: {auth: {k: 'auth.' + c.auth, fb: c.auth}}} : '',
          b: c.host_fingerprint ? {k: 'i.conn.key', v: {fp: c.host_fingerprint}} : ''}}});
      dl.append(dt, dd);
    } catch (e) { SD.showErr('i-err', e); }
  }
  let shotURL = null;
  async function shot() {
    SD.showErr('i-err');
    try {
      const r = await SD.api('GET', 'screenshot', undefined, {c: st.cur});
      const blob = await r.blob();
      if (shotURL) URL.revokeObjectURL(shotURL);
      shotURL = URL.createObjectURL(blob);
      $('i-img').src = shotURL; $('i-fig').hidden = false;
      I.say($('i-cap'), 'i.taken', {time: new Date().toLocaleTimeString(I.locale()), size: SD.fmtSize(blob.size)});
    } catch (e) { SD.showErr('i-err', e); }
  }
  $('i-refresh').addEventListener('click', infoLoad);
  $('i-shot').addEventListener('click', shot);
  SD.tabs.info = {show() { infoLoad(); }, hide() { $('i-fig').hidden = true; }};

  start();
})();
