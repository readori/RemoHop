// app.js: the RemoHop web client shell: session, login, connecting, tabs,
// clipboard and info. All text from the host reaches the DOM through
// textContent only.
'use strict';
(function () {
  const $ = id => document.getElementById(id);
  const base = new URL('.', location.href);
  const SD = window.SD = {tabs: {}, state: {sid: null, csrf: null, mode: null, conns: [], cur: null}};
  const st = SD.state;
  // Text goes through i18n.js: SD.t for a string now, SD.say/SD.sayAttr for
  // text that follows a language switch, SD.plain for untranslated text.
  const I = SD.i18n = window.SDI18n;
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
  SD.api = async function (method, path, body, params) {
    const init = {method, headers: SD.headers(body === undefined ? {} : {'Content-Type': 'application/json'}), credentials: 'same-origin'};
    if (body !== undefined) init.body = JSON.stringify(body);
    const r = await fetch(SD.url(path, params), init);
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
  // SD.ask shows a modal dialog; buttons is [{label, value, primary}], and the
  // promise resolves to the chosen value (or null when dismissed) together
  // with the input text and the "apply to all" box.
  SD.ask = function ({title, message, buttons, input, all}) {
    return new Promise(resolve => {
      const dlg = $('dlg');
      $('dlg-title').textContent = title;
      $('dlg-msg').textContent = message || '';
      const inp = $('dlg-input');
      inp.hidden = !input; inp.value = input && input.value || '';
      if (input) inp.setAttribute('aria-label', input.label || title);
      $('dlg-all-row').hidden = !all; $('dlg-all').checked = false;
      const box = $('dlg-buttons');
      box.textContent = '';
      dlgResolve = resolve;
      for (const b of buttons) {
        const el = document.createElement('button');
        el.type = 'button'; el.textContent = b.label;
        if (b.primary) el.className = 'primary';
        el.addEventListener('click', () => { const r = {value: b.value, text: inp.value, all: $('dlg-all').checked}; dlg.close(); resolve(r); dlgResolve = null; });
        box.appendChild(el);
      }
      dlg.onclose = () => { if (dlgResolve) { dlgResolve({value: null, text: '', all: false}); dlgResolve = null; } };
      dlg.showModal();
      (input ? inp : box.querySelector('.primary') || box.firstChild).focus();
    });
  };
  SD.confirm = async (title, message, okLabel) =>
    (await SD.ask({title, message, buttons: [{label: I.t('cancel'), value: false}, {label: okLabel || I.t('ok'), value: true, primary: true}]})).value === true;
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
      const r = await fetch(SD.url('session'), {method: 'POST', credentials: 'same-origin',
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
    else { banner('gw.ended.reload', null, true); }
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
    refreshConns(st.conns);
    if (st.conns.length) selectConn(st.conns[0].key); else showConnect();
    setInterval(heartbeat, 15000);
  }

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
        if (p.paired || p.key_login) I.say(o, p.paired ? 'peer.opt.paired' : 'peer.opt.key', {label}); else I.plain(o, label);
        sel.appendChild(o);
      }
      renderPeerCards();
    } catch (e) { /* the form still works without the list */ }
  }
  // The saved hosts as cards under the form. Choosing one fills the form;
  // a host this gateway is paired with (or authorised by key) connects
  // straight away, since it needs no password.
  function renderPeerCards() {
    const ul = $('peercards');
    ul.textContent = '';
    for (const p of st.peers) {
      const li = document.createElement('li');
      const b = document.createElement('button');
      b.type = 'button'; b.className = 'peer';
      const icon = document.createElement('span');
      icon.className = 'peericon'; icon.setAttribute('aria-hidden', 'true');
      const text = document.createElement('span');
      text.className = 'peertext';
      const name = document.createElement('strong');
      name.textContent = p.alias || p.target;
      const sub = document.createElement('small');
      if (p.alias) sub.textContent = p.target; else I.say(sub, p.paired ? 'peer.paired' : p.key_login ? 'peer.key' : 'peer.saved');
      text.append(name, sub);
      b.append(icon, text);
      const ready = p.paired || p.key_login;
      if (ready) {
        const tag = document.createElement('span');
        tag.className = 'tag'; I.say(tag, p.paired ? 'peer.tag.paired' : 'peer.tag.key');
        b.appendChild(tag);
      }
      I.sayAttr(b, 'aria-label', ready ? 'peer.aria.ready' : 'peer.aria.form', {label: (p.alias ? p.alias + ', ' : '') + p.target});
      b.addEventListener('click', () => {
        $('target').value = p.alias || p.target;
        $('peersel').value = p.target;
        if (ready && !attemptId) { $('hostpass').value = ''; $('connectform').requestSubmit(); } else $('hostpass').focus();
      });
      li.appendChild(b);
      ul.appendChild(li);
    }
  }
  $('peersel').addEventListener('change', () => { if ($('peersel').value) { $('target').value = $('peersel').value; $('hostpass').focus(); } });

  async function heartbeat() {
    if (!st.csrf) return;
    try { refreshConns((await SD.json('GET', 'connections')).conns); } catch (e) { /* sessionLost handles auth */ }
  }

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
      if (st.conns.length) selectConn(st.conns[0].key); else showConnect();
      return;
    }
    if (st.cur) sel.value = st.cur;
  }

  // ---- connecting ----
  let attemptId = null, polling = false;
  // Connection failures with a known code are explained in the page's own
  // words (reason.* in i18n.js), followed by the gateway's detail.
  const reason = (code, detail) => I.has('reason.' + code, 'en') ? {k: 'pair2', v: {a: {k: 'reason.' + code}, b: detail || ''}} : null;
  function showConnect() {
    $('connect').hidden = false; $('workspace').hidden = true; $('hostbar').hidden = !st.conns.length;
    $('hostbar').querySelector('#newconn').hidden = true;
    $('target').focus();
  }
  $('newconn').addEventListener('click', showConnect);

  $('connectform').addEventListener('submit', async ev => {
    ev.preventDefault();
    if (attemptId) return;
    I.plain($('connerr'), ''); I.say($('connstatus'), 'connect.connecting');
    $('connectbtn').disabled = true; $('cancelconn').hidden = false;
    const body = {target: $('target').value, password: $('hostpass').value, pair: $('pair').checked};
    $('hostpass').value = '';
    try {
      const a = await SD.json('POST', 'connect', body);
      attemptId = a.attempt;
      await poll();
    } catch (e) {
      endAttempt();
      I.say($('connerr'), 'raw', {s: reason(e.code, e.message) || SD.errVar(e)});
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
      try { j = await SD.json('GET', 'connect/' + attemptId); } catch (e) { endAttempt(); SD.sayErr($('connerr'), e); return; }
      if (j.state === 'waiting_approval') I.say($('connstatus'), 'connect.waiting');
      else if (j.state === 'dialing') {
        if (j.notices && j.notices.length) I.say($('connstatus'), 'connect.dialing', {notice: j.notices[j.notices.length - 1]});
        else I.say($('connstatus'), 'connect.connecting');
      }
      if (j.state === 'connected') {
        endAttempt();
        I.plain($('connerr'), '');
        if (j.notices && j.notices.length) banner('raw', {s: j.notices.join(' ')}, false);
        await loadPeers();
        $('peerrow').hidden = !(st.peers && st.peers.length);
        refreshConns((await SD.json('GET', 'connections')).conns);
        selectConn(j.conn.key);
        return;
      }
      if (j.state === 'failed' || j.state === 'canceled') {
        endAttempt();
        const e = j.error || {};
        if (j.state === 'canceled') I.say($('connerr'), 'connect.canceled');
        else I.say($('connerr'), 'raw', {s: reason(e.code, e.message) || (e.message ? SD.errVar(e) : {k: 'connect.failed'})});
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
    showTab(load('sd.tab') || 'files');
  }
  $('hostsel').addEventListener('change', () => selectConn($('hostsel').value));
  $('disconnect').addEventListener('click', async () => {
    const k = st.cur; if (!k) return;
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
    else if (e.code === 'disconnected') { SD.sayErr(el, e); heartbeat(); }
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
