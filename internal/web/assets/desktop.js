// desktop.js: the Desktop tab. The WebCodecs viewer logic is a copy of the
// minimal parts of internal/svc/desktop/viewer.html (video down a WebSocket,
// acks and input up), adapted to the gateway's WebSocket protocol.
'use strict';
(function () {
  const $ = id => document.getElementById(id);
  const SD = window.SD;
  const canvas = $('d-canvas'), g = canvas.getContext('2d', {alpha: false});
  const nowUs = () => (performance.timeOrigin + performance.now()) * 1000;

  let ws = null, decoder = null, cfg = null, hello = null, gen = 0, conn = null;
  let configuring = false, pending = [], waitKey = true, ended = false;
  let viewOnly = false, hostInput = false, fit = true, skippedSeq = -1, lastKey = 0, lastBehind = 0;
  const meta = new Map(), pressed = new Set(), buttons = new Set();
  const stats = {decoded: 0, skipped: 0, errors: 0, fps: 0, mbps: 0, latency: 0, rtt: 0, host: null, wf: 0, wb: 0, ws: performance.now(), bytes: 0};
  window.remohopDesktop = {stats: () => Object.assign({}, stats, {hello, configured: !!decoder && decoder.state === 'configured'})};

  // msg shows key (with vars) over the picture; no key clears it.
  const msg = (key, vars, canRetry) => { SD.say($('d-msg'), key, vars); $('d-reconnect').hidden = !canRetry; };
  const send = o => { if (ws && ws.readyState === 1) ws.send(JSON.stringify(o)); };
  const ewma = (o, v) => o === 0 ? v : o + (v - o) / 8;

  function b64(s) { const b = atob(s), a = new Uint8Array(b.length); for (let i = 0; i < b.length; i++) a[i] = b.charCodeAt(i); return a; }
  async function supported(c) { try { return (await VideoDecoder.isConfigSupported(c)).supported; } catch (e) { return false; } }

  async function decodable() {
    const out = [];
    if (typeof VideoDecoder === 'undefined') return out;
    if (await supported({codec: 'av01.0.08M.08'})) out.push('av1');
    if (await supported({codec: 'avc1.42001f'})) out.push('h264');
    return out;
  }

  function resetDecoder() {
    if (decoder && decoder.state !== 'closed') decoder.close();
    decoder = null; meta.clear(); pending = []; waitKey = true;
  }
  function newDecoder() {
    decoder = new VideoDecoder({output: onFrame, error: e => {
      stats.errors++;
      if (!cfg) return;
      newDecoder(); decoder.configure(cfg); waitKey = true; requestKey();
    }});
  }
  function requestKey() { const t = performance.now(); if (t - lastKey > 1000) { lastKey = t; send({type: 'keyframe'}); } }
  function behind() { const t = performance.now(); if (t - lastBehind > 1000) { lastBehind = t; send({type: 'behind'}); } }
  setInterval(() => { if (skippedSeq >= 0 && (!decoder || decoder.decodeQueueSize === 0)) { send({type: 'ack', seq: skippedSeq}); skippedSeq = -1; } }, 50);

  async function open() {
    const my = ++gen;
    close(false);
    ended = false;
    msg('d.connecting', null, false);
    let t, dec;
    try { [t, dec] = await Promise.all([SD.ticket('ws'), decodable()]); } catch (e) { msg('raw', {s: SD.errVar(e)}, true); return; }
    if (my !== gen) return;
    if (!dec.length) { msg('d.nocodec', null, false); return; }
    const params = {t, c: conn, decode: dec.join(','), view_only: viewOnly ? '1' : '0'};
    const mh = +$('d-quality').value; if (mh) params.max_height = mh;
    const sock = new WebSocket(SD.wsURL('desktop/ws', params));
    sock.binaryType = 'arraybuffer';
    ws = sock;
    sock.onmessage = e => { if (my !== gen) return; if (typeof e.data === 'string') onText(JSON.parse(e.data)); else onVideo(e.data); };
    sock.onclose = () => { if (my !== gen) return; releaseAll(); resetDecoder(); if (!ended) msg('d.disconnected', null, true); };
  }

  function close(clear) {
    if (ws) { try { ws.close(); } catch (e) { /* ignore */ } ws = null; }
    resetDecoder(); cfg = null;
    if (clear) msg(null);
  }

  function onText(m) {
    if (m.type === 'hello') onHello(m);
    else if (m.type === 'stats') { stats.host = m.host; stats.rtt = m.rtt_ms; }
    else if (m.type === 'end') {
      ended = true;
      if (m.refused) msg('d.refused', {err: m.error}, true);
      else msg('d.ended', {err: m.error || {k: 'd.closed'}}, true);
      if (ws) ws.close();
    }
  }

  async function onHello(h) {
    hello = h;
    hostInput = !!h.input;
    $('d-viewonly').disabled = !hostInput;
    if (!hostInput) { $('d-viewonly').checked = true; viewOnly = true; }
    const ds = $('d-display');
    if (h.displays && h.displays.length > 1 && ds.options.length === 0) {
      ds.textContent = '';
      const all = document.createElement('option'); all.value = '-1'; SD.say(all, 'd.monitor.all'); if (!(h.unsupported || {}).all_monitors) ds.appendChild(all);
      h.displays.forEach((d, i) => { const o = document.createElement('option'); o.value = String(i + 1); SD.say(o, 'd.monitor.n', {n: i + 1, w: d.width, h: d.height}); ds.appendChild(o); });
      ds.hidden = false;
    }
    if (ds.options.length > 1) ds.value = String(h.all_displays ? -1 : (h.display || 1));
    const want = {codec: h.codec, codedWidth: h.width, codedHeight: h.height, optimizeForLatency: true, hardwareAcceleration: 'prefer-hardware'};
    if (h.codec_config) want.description = b64(h.codec_config);
    if (cfg && cfg.codec === want.codec && cfg.codedWidth === want.codedWidth && cfg.codedHeight === want.codedHeight && decoder && decoder.state === 'configured') { waitKey = true; return; }
    configuring = true;
    if (!await supported(want)) {
      want.hardwareAcceleration = 'no-preference';
      if (!await supported(want)) { configuring = false; ended = true; msg('d.cannot', {codec: h.codec}, false); return; }
    }
    cfg = want; canvas.width = h.width; canvas.height = h.height; layout();
    if (!decoder || decoder.state === 'closed') newDecoder();
    decoder.configure(cfg);
    waitKey = true; configuring = false;
    msg(null);
    const q = pending; pending = []; q.forEach(onVideo);
    canvas.focus({preventScroll: true});
  }

  function onVideo(buf) {
    if (configuring) { pending.push(buf); return; }
    const dv = new DataView(buf);
    if (buf.byteLength < 30 || dv.getUint8(0) !== 0x56) return;
    const key = (dv.getUint8(1) & 1) === 1, seq = dv.getUint32(2), pts = Number(dv.getBigInt64(6)), cap = Number(dv.getBigInt64(14)), arr = Number(dv.getBigInt64(22));
    const data = new Uint8Array(buf, 30);
    stats.wb += data.length;
    const skip = () => { stats.skipped++; skippedSeq = seq; };
    if (!decoder || decoder.state !== 'configured') return skip();
    if (waitKey && !key) return skip();
    if (!key && decoder.decodeQueueSize > 2) { waitKey = true; behind(); return skip(); }
    waitKey = false;
    meta.set(pts, {seq, cap, arr});
    if (meta.size > 240) meta.delete(meta.keys().next().value);
    try { decoder.decode(new EncodedVideoChunk({type: key ? 'key' : 'delta', timestamp: pts, data})); }
    catch (e) { stats.errors++; waitKey = true; requestKey(); skip(); }
  }

  function onFrame(frame) {
    const m = meta.get(frame.timestamp); meta.delete(frame.timestamp);
    g.drawImage(frame, 0, 0, canvas.width, canvas.height);
    frame.close(); stats.decoded++; stats.wf++;
    if (m) {
      send({type: 'ack', seq: Math.max(m.seq, skippedSeq)});
      if (m.seq >= skippedSeq) skippedSeq = -1;
      const now = nowUs();
      if (m.cap > 0) stats.latency = ewma(stats.latency, (now - m.cap) / 1000);
    }
  }

  function layout() {
    const stage = $('d-stage');
    if (!fit) { canvas.style.width = canvas.width + 'px'; canvas.style.height = canvas.height + 'px'; return; }
    const w = stage.clientWidth, h = stage.clientHeight;
    const s = Math.min(w / canvas.width, h / canvas.height);
    canvas.style.width = Math.max(1, Math.floor(canvas.width * s)) + 'px';
    canvas.style.height = Math.max(1, Math.floor(canvas.height * s)) + 'px';
  }
  addEventListener('resize', layout);
  document.addEventListener('fullscreenchange', () => {
    if (!document.fullscreenElement && navigator.keyboard && navigator.keyboard.unlock) navigator.keyboard.unlock();
    layout();
  });

  setInterval(() => {
    const t = performance.now(), dt = (t - stats.ws) / 1000;
    if (dt >= 0.5) { stats.fps = stats.wf / dt; stats.mbps = stats.wb * 8 / dt / 1e6; stats.wf = 0; stats.wb = 0; stats.ws = t; }
    if (!hello || $('d-hud').hidden) return;
    const h = stats.host || {}, tr = SD.t;
    $('d-hud').textContent = `${hello.width}×${hello.height} ${hello.codec}\n${hello.encoder}${hello.hardware ? ' (GPU)' : ' (CPU)'}\n` +
      `${stats.fps.toFixed(0)} fps ${stats.mbps.toFixed(2)} Mbit/s\n${tr('d.hud.latency')} ${stats.latency ? stats.latency.toFixed(0) + ' ms' : tr('d.hud.na')} ${tr('d.hud.rtt')} ${stats.rtt.toFixed(1)} ms\n` +
      tr('d.hud.host', {fps: (h.fps || 0).toFixed(0), n: h.dropped || 0}) + '\n' + t(viewOnly ? 'd.hud.viewonly' : 'd.hud.input');
  }, 500);

  // ---- input ----
  const inputOK = () => hostInput && !viewOnly;
  function pos(e) {
    const r = canvas.getBoundingClientRect();
    return {x: Math.min(1, Math.max(0, (e.clientX - r.left) / r.width)), y: Math.min(1, Math.max(0, (e.clientY - r.top) / r.height))};
  }
  canvas.addEventListener('pointermove', e => { if (inputOK()) send({type: 'move', ...pos(e)}); });
  canvas.addEventListener('pointerdown', e => {
    canvas.focus();
    if (!inputOK()) return;
    e.preventDefault(); canvas.setPointerCapture(e.pointerId); buttons.add(e.button);
    send({type: 'button', button: e.button, down: true, ...pos(e)});
  });
  canvas.addEventListener('pointerup', e => {
    if (!inputOK()) return;
    e.preventDefault(); buttons.delete(e.button);
    send({type: 'button', button: e.button, down: false, ...pos(e)});
  });
  canvas.addEventListener('contextmenu', e => e.preventDefault());
  canvas.addEventListener('wheel', e => {
    if (!inputOK()) return;
    e.preventDefault();
    const k = e.deltaMode === 1 ? 1 / 3 : e.deltaMode === 2 ? 1 : 1 / 100;
    send({type: 'wheel', dx: e.deltaX * k, dy: e.deltaY * k});
  }, {passive: false});
  canvas.addEventListener('keydown', e => {
    if (!inputOK()) return;
    if (e.key === 'Tab' && !e.repeat && false) return;
    e.preventDefault(); e.stopPropagation();
    if (e.repeat || !e.code) return;
    pressed.add(e.code); send({type: 'key', code: e.code, down: true});
  });
  canvas.addEventListener('keyup', e => {
    if (!inputOK()) return;
    e.preventDefault();
    if (!e.code) return;
    pressed.delete(e.code); send({type: 'key', code: e.code, down: false});
  });
  function releaseAll() { if (pressed.size || buttons.size) { pressed.clear(); buttons.clear(); send({type: 'release_all'}); } }
  canvas.addEventListener('blur', releaseAll);
  addEventListener('blur', releaseAll);

  // ---- toolbar ----
  $('d-viewonly').addEventListener('change', () => { viewOnly = $('d-viewonly').checked; send({type: 'view_only', on: viewOnly}); canvas.style.cursor = viewOnly ? 'default' : 'crosshair'; });
  $('d-fit').addEventListener('click', () => { fit = !fit; $('d-fit').setAttribute('aria-pressed', String(fit)); SD.say($('d-fit'), fit ? 'd.fit' : 'd.actual'); layout(); });
  $('d-key').addEventListener('click', () => { lastKey = 0; requestKey(); canvas.focus(); });
  $('d-stats').addEventListener('click', () => { const h = $('d-hud'); h.hidden = !h.hidden; $('d-stats').setAttribute('aria-pressed', String(!h.hidden)); });
  $('d-reconnect').addEventListener('click', open);
  $('d-quality').addEventListener('change', () => { const v = +$('d-quality').value; send({type: 'config', max_height: v || -1}); });
  $('d-display').addEventListener('change', () => send({type: 'display', display: +$('d-display').value}));
  $('d-cad').addEventListener('click', () => {
    if (!hostInput) { msg('d.noinput', null, true); return; }
    for (const c of ['ControlLeft', 'AltLeft', 'Delete']) send({type: 'key', code: c, down: true});
    for (const c of ['Delete', 'AltLeft', 'ControlLeft']) send({type: 'key', code: c, down: false});
    canvas.focus();
  });
  $('d-fs').addEventListener('click', async () => {
    if (document.fullscreenElement) { await document.exitFullscreen(); return; }
    await $('d-stage').requestFullscreen();
    if (navigator.keyboard && navigator.keyboard.lock) navigator.keyboard.lock().catch(() => {});
    canvas.focus();
  });

  SD.tabs.desktop = {
    show(c) { conn = c; $('d-viewonly').checked = viewOnly; layout(); open(); },
    hide() { gen++; close(true); hello = null; },
  };
})();
