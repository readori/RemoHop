// desktop.js: the Desktop tab. The WebCodecs viewer logic is a copy of the
// minimal parts of internal/svc/desktop/viewer.html (video down a WebSocket,
// acks and input up), adapted to the gateway's WebSocket protocol.
'use strict';
(function () {
  const $ = id => document.getElementById(id);
  const SD = window.SD;
  const canvas = $('d-canvas'), g = canvas.getContext('2d', {alpha: false});
  // Wall-clock microseconds, the clock the gateway stamps capture times
  // with. performance.timeOrigin + performance.now() is not that clock: the
  // monotonic clock stops while the device sleeps or the app is suspended
  // (iOS), so after a while it lags the wall clock by seconds. Date.now()
  // is resynchronised every second for its milliseconds; performance.now()
  // adds the fraction between.
  let wallOff = Date.now() - performance.now();
  setInterval(() => { wallOff = Date.now() - performance.now(); }, 1000);
  const nowUs = () => (performance.now() + wallOff) * 1000;
  // A decoder with more than two frames queued for longer than behindMs,
  // or more than maxDecodeQueue at once, is behind: the page skips to the
  // next key frame and tells the host, which lowers its frame rate. A
  // burst of frames (Wi-Fi power saving delivers them in clumps) drains far
  // faster than that on any hardware decoder, and is no reason to skip,
  // which costs the host an encoder restart.
  const maxDecodeQueue = 6, behindMs = 100;
  let queuedSince = 0;
  // Heights to step down through when the decoder cannot take the stream
  // at the size the host sends (a phone that decodes 1440p but not 4K).
  const fallbackHeights = [2160, 1440, 1080, 720, 480];

  let ws = null, decoder = null, cfg = null, hello = null, gen = 0, conn = null;
  let configuring = false, pending = [], waitKey = true, ended = false;
  // viewOnly is the user's choice (the checkbox); a host that gives no
  // input makes the session view-only without changing it, so the next
  // session (to that host once fixed, or to another) has input again.
  // openedViewOnly: this session was opened view-only.
  let viewOnly = false, openedViewOnly = false, hostInput = false, fit = true, skippedSeq = -1;
  // Decoder errors with no frame decoded in between, and whether the
  // decoder was moved to software because of them (see newDecoder).
  let decodeErrors = 0, softwareDecode = false;
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
  // A decoder can accept a configuration and then fail on every frame
  // (hardware decoders, Android's in particular). Two errors with no frame
  // decoded between move it to software; two more ask the host for the
  // next smaller size; with none left the page says so instead of
  // retrying forever.
  function newDecoder() {
    decoder = new VideoDecoder({output: onFrame, error: e => {
      stats.errors++; decodeErrors++;
      if (!cfg) return;
      if (decodeErrors >= 2 && !softwareDecode) {
        softwareDecode = true;
        cfg = Object.assign({}, cfg, {hardwareAcceleration: 'prefer-software'});
      } else if (decodeErrors >= 4) {
        decodeErrors = 0;
        if (stepDown(cfg.codedHeight)) return;
        const codec = cfg.codec;
        resetDecoder(); cfg = null; ended = true;
        msg('d.cannot', {codec}, false); if (ws) ws.close();
        return;
      }
      newDecoder(); decoder.configure(cfg); waitKey = true; requestKey();
    }});
  }
  // stepDown asks the host for the next size below height (it follows with
  // a new hello), and keeps it in the quality menu so a reconnect starts
  // there instead of stepping down again. false if there is none.
  function stepDown(height) {
    const next = fallbackHeights.find(v => v < height);
    if (!next || height <= 480) return false;
    resetDecoder(); cfg = null;
    if ([...$('d-quality').options].some(o => +o.value === next)) $('d-quality').value = String(next);
    send({type: 'config', max_height: next});
    return true;
  }
  // "behind" and "keyframe" go at most once a second each. One asked for
  // sooner is sent when the second is up, if the page still waits for a
  // key frame then: dropping it left the picture frozen until the host's
  // scheduled key frame when the decoder fell behind twice in a row.
  function limited(type) {
    const r = {last: -1e9, timer: null};
    const fire = () => { r.timer = null; r.last = performance.now(); send({type}); };
    r.send = force => {
      const wait = r.last + 1000 - performance.now();
      if (force || wait <= 0) { clearTimeout(r.timer); fire(); }
      else if (!r.timer) r.timer = setTimeout(() => { if (waitKey) fire(); else r.timer = null; }, wait);
    };
    return r;
  }
  const behindMsg = limited('behind'), keyMsg = limited('keyframe');
  const behind = () => behindMsg.send();
  const requestKey = force => keyMsg.send(force);
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
    openedViewOnly = viewOnly;
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
    else if (m.type === 'stats') {
      stats.host = m.host; stats.rtt = m.rtt_ms;
      // The host cannot capture its screen right now (Windows' secure
      // desktop: a UAC prompt, the lock screen); the session goes on.
      const p = $('d-paused'), why = m.host && m.host.paused;
      if (why) SD.say(p, 'd.paused', {s: why});
      p.hidden = !why;
    } else if (m.type === 'end') {
      ended = true;
      $('d-paused').hidden = true;
      if (m.refused) msg('d.refused', {err: m.error}, true);
      else ended_(m.error);
      if (ws) ws.close();
    }
  }
  // ended_ shows why the session ended: the message's first part, with the
  // host's technical detail (every X display it tried, say) under Details.
  function ended_(err) {
    if (!err) { msg('d.ended', {err: {k: 'd.closed'}}, true); return; }
    const i = err.indexOf(' Details: ');
    msg('d.ended', {err: i < 0 ? err : err.slice(0, i)}, true);
    if (i < 0) return;
    const d = document.createElement('details');
    d.className = 'errdetail';
    const sum = document.createElement('summary');
    SD.say(sum, 'connect.details');
    const code = document.createElement('code');
    code.textContent = err.slice(i + 10);
    d.append(sum, code);
    $('d-msg').appendChild(d);
  }

  async function onHello(h) {
    hello = h;
    hostInput = !!h.input;
    // A session opened view-only has no input from the host either; the
    // box stays the user's to untick (which reopens the session).
    const cb = $('d-viewonly');
    cb.disabled = !hostInput && !openedViewOnly;
    cb.checked = viewOnly || !hostInput;
    canvas.style.cursor = inputOK() ? 'crosshair' : 'default';
    // The monitor menu is rebuilt from every hello: another host has other
    // monitors.
    const ds = $('d-display');
    ds.textContent = '';
    if (h.displays && h.displays.length > 1) {
      const all = document.createElement('option'); all.value = '-1'; SD.say(all, 'd.monitor.all'); if (!(h.unsupported || {}).all_monitors) ds.appendChild(all);
      h.displays.forEach((d, i) => { const o = document.createElement('option'); o.value = String(i + 1); SD.say(o, 'd.monitor.n', {n: i + 1, w: d.width, h: d.height}); ds.appendChild(o); });
      ds.value = String(h.all_displays ? -1 : (h.display || 1));
    }
    ds.hidden = ds.options.length < 2;
    const want = {codec: h.codec, codedWidth: h.width, codedHeight: h.height, optimizeForLatency: true, hardwareAcceleration: softwareDecode ? 'prefer-software' : 'prefer-hardware'};
    if (h.codec_config) want.description = b64(h.codec_config);
    // The same stream only if the avcC record is the same too: hardware
    // encoders change their SPS (HRD parameters) with the bitrate.
    const sameDesc = (a, b) => (!a && !b) || (a && b && a.length === b.length && a.every((v, i) => v === b[i]));
    if (cfg && cfg.codec === want.codec && cfg.codedWidth === want.codedWidth && cfg.codedHeight === want.codedHeight &&
        sameDesc(cfg.description, want.description) && decoder && decoder.state === 'configured') { waitKey = true; return; }
    configuring = true;
    if (!await supported(want)) {
      want.hardwareAcceleration = 'no-preference';
      if (!await supported(want)) {
        // Too large for this decoder (4K on a phone that tops out at
        // 1440p, say): ask the host for the next smaller size, which it
        // sends with a new hello, rather than give up.
        configuring = false;
        if (stepDown(h.height)) return;
        ended = true; msg('d.cannot', {codec: h.codec}, false); return;
      }
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
    if (decoder.decodeQueueSize <= 2) queuedSince = 0;
    else if (!queuedSince) queuedSince = performance.now();
    if (!key && (decoder.decodeQueueSize > maxDecodeQueue || (queuedSince && performance.now() - queuedSince > behindMs))) {
      waitKey = true; queuedSince = 0; behind(); return skip();
    }
    waitKey = false;
    meta.set(pts, {seq, cap, arr});
    if (meta.size > 240) meta.delete(meta.keys().next().value);
    try { decoder.decode(new EncodedVideoChunk({type: key ? 'key' : 'delta', timestamp: pts, data})); }
    catch (e) { stats.errors++; waitKey = true; requestKey(); skip(); }
  }

  // A decoded frame is acknowledged before it is drawn: the decoder keeps
  // up, and the host's flow control should not wait on the page's paint.
  function onFrame(frame) {
    const m = meta.get(frame.timestamp); meta.delete(frame.timestamp);
    stats.decoded++; decodeErrors = 0;
    if (m) {
      send({type: 'ack', seq: Math.max(m.seq, skippedSeq)});
      if (m.seq >= skippedSeq) skippedSeq = -1;
    }
    g.drawImage(frame, 0, 0, canvas.width, canvas.height);
    frame.close(); stats.wf++;
    if (m && m.cap > 0) {
      const ms = (nowUs() - m.cap) / 1000;
      if (ms > -50 && ms < 10000) stats.latency = ewma(stats.latency, Math.max(ms, 0.1));
    }
  }

  function layout() {
    const stage = $('d-stage');
    if (!fit) {
      // Actual size: one stream pixel on one device pixel, so the picture
      // stays sharp on a HiDPI screen instead of being magnified by
      // devicePixelRatio.
      const dpr = window.devicePixelRatio || 1;
      canvas.style.width = canvas.width / dpr + 'px'; canvas.style.height = canvas.height / dpr + 'px';
      return;
    }
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
    $('d-hud').textContent = `${hello.width}×${hello.height} ${hello.codec}\n${hello.encoder}${hello.hardware ? ' (GPU)' : ' (CPU)'} ${((h.bitrate || hello.bitrate || 0) / 1e6).toFixed(0)} Mbit/s\n` +
      `${stats.fps.toFixed(0)} fps ${stats.mbps.toFixed(2)} Mbit/s` + (h.pipeline_ms ? ` (${tr('d.hud.encode')} ${h.pipeline_ms.toFixed(0)} ms)` : '') + `\n${tr('d.hud.latency')} ${stats.latency ? stats.latency.toFixed(0) + ' ms' : tr('d.hud.na')} ${tr('d.hud.rtt')} ${stats.rtt.toFixed(1)} ms\n` +
      tr('d.hud.host', {fps: (h.fps || 0).toFixed(0), n: h.dropped || 0}) + (h.restarts ? ' ' + tr('d.hud.restarts', {n: h.restarts}) : '') +
      (stats.skipped ? ' ' + tr('d.hud.skipped', {n: stats.skipped}) : '') + '\n' + tr(inputOK() ? 'd.hud.input' : 'd.hud.viewonly');
  }, 500);

  // ---- input ----
  const inputOK = () => hostInput && !viewOnly;
  function pos(e) {
    const r = canvas.getBoundingClientRect();
    return {x: Math.min(1, Math.max(0, (e.clientX - r.left) / r.width)), y: Math.min(1, Math.max(0, (e.clientY - r.top) / r.height))};
  }
  // Buttons follow e.buttons, compared with what was sent: Pointer Events
  // fire pointerdown only for the first button pressed and pointerup only
  // for the last one released, and report a second button as a
  // pointermove, so a chord (left held, right clicked) would otherwise
  // lose the right press and leave the left one stuck down on the host.
  const bitButton = [0, 2, 1, 3, 4]; // bits of e.buttons -> DOM button numbers
  let btnMask = 0;
  function syncButtons(e, mask) {
    const changed = mask ^ btnMask;
    if (!changed) return false;
    const p = pos(e);
    bitButton.forEach((b, i) => {
      if (!(changed & (1 << i))) return;
      const down = !!(mask & (1 << i));
      if (down) buttons.add(b); else buttons.delete(b);
      send({type: 'button', button: b, down, ...p});
    });
    btnMask = mask;
    return true;
  }
  function maskOf(e, down) {
    if (typeof e.buttons === 'number' && (e.buttons || !down)) return e.buttons & 31;
    const i = bitButton.indexOf(e.button);
    if (i < 0) return btnMask;
    return down ? btnMask | (1 << i) : btnMask & ~(1 << i);
  }
  canvas.addEventListener('pointermove', e => { if (inputOK() && !syncButtons(e, maskOf(e, false))) send({type: 'move', ...pos(e)}); });
  canvas.addEventListener('pointerdown', e => {
    canvas.focus();
    if (!inputOK()) return;
    e.preventDefault(); canvas.setPointerCapture(e.pointerId);
    syncButtons(e, maskOf(e, true));
  });
  canvas.addEventListener('pointerup', e => {
    if (!inputOK()) return;
    e.preventDefault();
    syncButtons(e, maskOf(e, false));
  });
  // A cancelled pointer (a touch taken over by the browser) lets go.
  canvas.addEventListener('pointercancel', e => { if (inputOK()) syncButtons(e, 0); });
  canvas.addEventListener('contextmenu', e => e.preventDefault());
  canvas.addEventListener('wheel', e => {
    if (!inputOK()) return;
    e.preventDefault();
    const k = e.deltaMode === 1 ? 1 / 3 : e.deltaMode === 2 ? 1 : 1 / 100;
    send({type: 'wheel', dx: e.deltaX * k, dy: e.deltaY * k});
  }, {passive: false});
  // A held key repeats: the browser's repeated keydowns are forwarded (not
  // for modifiers), because only an X11 host repeats an injected key by
  // itself (and ignores the extra presses); Windows and macOS produce
  // autorepeat below where input is injected.
  const noRepeat = new Set(['ShiftLeft', 'ShiftRight', 'ControlLeft', 'ControlRight', 'AltLeft', 'AltRight',
                            'MetaLeft', 'MetaRight', 'OSLeft', 'OSRight', 'CapsLock', 'NumLock', 'ScrollLock', 'Fn']);
  canvas.addEventListener('keydown', e => {
    if (!inputOK()) return;
    e.preventDefault(); e.stopPropagation();
    if (!e.code) return;
    if (e.repeat && pressed.has(e.code) && noRepeat.has(e.code)) return;
    pressed.add(e.code); send({type: 'key', code: e.code, down: true});
  });
  canvas.addEventListener('keyup', e => {
    if (!inputOK()) return;
    e.preventDefault();
    if (!e.code) return;
    pressed.delete(e.code); send({type: 'key', code: e.code, down: false});
  });
  function releaseAll() { btnMask = 0; if (pressed.size || buttons.size) { pressed.clear(); buttons.clear(); send({type: 'release_all'}); } }
  canvas.addEventListener('blur', releaseAll);
  addEventListener('blur', releaseAll);
  document.addEventListener('visibilitychange', () => { if (document.hidden) releaseAll(); });

  // ---- toolbar ----
  $('d-viewonly').addEventListener('change', () => {
    viewOnly = $('d-viewonly').checked;
    // A session opened view-only has no input on the host: input needs a
    // new one.
    if (!viewOnly && openedViewOnly) { open(); return; }
    if (viewOnly) releaseAll();
    send({type: 'view_only', on: viewOnly});
    canvas.style.cursor = inputOK() ? 'crosshair' : 'default';
  });
  $('d-fit').addEventListener('click', () => { fit = !fit; $('d-fit').setAttribute('aria-pressed', String(fit)); SD.say($('d-fit'), fit ? 'd.fit' : 'd.actual'); layout(); });
  $('d-key').addEventListener('click', () => { requestKey(true); canvas.focus(); });
  $('d-stats').addEventListener('click', () => { const h = $('d-hud'); h.hidden = !h.hidden; $('d-stats').setAttribute('aria-pressed', String(!h.hidden)); });
  $('d-reconnect').addEventListener('click', open);
  $('d-quality').addEventListener('change', () => { const v = +$('d-quality').value; send({type: 'config', max_height: v || -1}); });
  $('d-display').addEventListener('change', () => send({type: 'display', display: +$('d-display').value}));
  $('d-cad').addEventListener('click', () => {
    if (!hostInput) { msg('d.noinput', null, true); return; }
    // A host that cannot raise the secure attention sequence says why
    // (a Windows host outside the unattended service, say).
    const no = hello && (hello.unsupported || {}).ctrl_alt_del;
    if (no) { const p = $('d-paused'); SD.say(p, 'raw', {s: 'Ctrl+Alt+Del: ' + no}); p.hidden = false; setTimeout(() => { p.hidden = true; }, 5000); return; }
    for (const c of ['ControlLeft', 'AltLeft', 'Delete']) send({type: 'key', code: c, down: true});
    for (const c of ['Delete', 'AltLeft', 'ControlLeft']) send({type: 'key', code: c, down: false});
    canvas.focus();
  });
  // Full screen: the Fullscreen API where the browser has it for any
  // element; otherwise (iPhone Safari) the stage covers the window. Either
  // way the stage shows a floating exit button (#d-exitfs), so a touch
  // screen without an Esc key can always get out.
  const pseudoOn = () => $('d-stage').classList.contains('pseudo-fs');
  function pseudoFs(on) {
    $('d-stage').classList.toggle('pseudo-fs', on);
    document.documentElement.classList.toggle('pseudo-fs', on);
    SD.say($('d-fs'), on ? 'd.exitfs' : 'd.fs');
    layout();
  }
  function exitFs() {
    if (document.fullscreenElement) document.exitFullscreen().catch(() => {});
    else if (pseudoOn()) pseudoFs(false);
  }
  $('d-fs').addEventListener('click', async () => {
    if (document.fullscreenElement || pseudoOn()) { exitFs(); return; }
    const st = $('d-stage');
    try {
      if (!st.requestFullscreen) throw new Error('no element full screen');
      await st.requestFullscreen();
    } catch (e) { pseudoFs(true); canvas.focus({preventScroll: true}); return; }
    if (navigator.keyboard && navigator.keyboard.lock) navigator.keyboard.lock().catch(() => {});
    canvas.focus();
  });
  $('d-exitfs').addEventListener('click', exitFs);
  document.addEventListener('keydown', e => { if (e.key === 'Escape' && pseudoOn() && document.activeElement !== canvas) pseudoFs(false); });
  document.addEventListener('fullscreenchange', () => SD.say($('d-fs'), document.fullscreenElement ? 'd.exitfs' : 'd.fs'));

  SD.tabs.desktop = {
    show(c) { conn = c; $('d-viewonly').checked = viewOnly; $('d-viewonly').disabled = false; layout(); open(); },
    hide() {
      gen++; close(true); hello = null; hostInput = false;
      const ds = $('d-display'); ds.textContent = ''; ds.hidden = true;
      $('d-paused').hidden = true;
      if (pseudoOn()) pseudoFs(false);
    },
  };
})();
