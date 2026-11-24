// term.js: draws a TermCore.Terminal into the DOM and runs the Terminal tab.
'use strict';
(function () {
  const $ = id => document.getElementById(id);
  const SD = window.SD, TC = window.TermCore;

  const PAL16 = ['#000000', '#cd3131', '#0dbc79', '#e5e510', '#2472c8', '#bc3fbc', '#11a8cd', '#e5e5e5',
    '#666666', '#f14c4c', '#23d18b', '#f5f543', '#3b8eea', '#d670d6', '#29b8db', '#ffffff'];
  function palette(n) {
    if (n < 16) return PAL16[n];
    if (n < 232) { n -= 16; const f = v => v ? 55 + v * 40 : 0; return `rgb(${f(Math.floor(n / 36))},${f(Math.floor(n / 6) % 6)},${f(n % 6)})`; }
    const g = 8 + (n - 232) * 10; return `rgb(${g},${g},${g})`;
  }
  const DEF_FG = '#d4d4d4', DEF_BG = '#0c0c0c';
  function colour(c) { return c >= TC.RGB ? `rgb(${(c >> 16) & 255},${(c >> 8) & 255},${c & 255})` : palette(c); }

  class TermView {
    constructor(host, cb) {
      this.cb = cb;
      this.fontPx = 14;
      this.root = document.createElement('div'); this.root.className = 'tv';
      this.scroll = document.createElement('div'); this.scroll.className = 'tv-scroll';
      this.sb = document.createElement('div');
      this.live = document.createElement('div');
      this.scroll.append(this.sb, this.live);
      this.input = document.createElement('textarea'); this.input.className = 'tv-input';
      SD.sayAttr(this.input, 'aria-label', 't.input');
      for (const a of ['autocapitalize', 'autocorrect', 'autocomplete']) this.input.setAttribute(a, 'off');
      this.input.spellcheck = false;
      this.meas = document.createElement('span'); this.meas.className = 'tv-meas'; this.meas.textContent = 'W'.repeat(20);
      this.root.append(this.scroll, this.input, this.meas);
      host.textContent = ''; host.appendChild(this.root);
      this.root.style.position = 'absolute'; this.root.style.inset = '0';
      this.dec = new TextDecoder('utf-8');
      this.rowEls = []; this.lastCur = -1; this.raf = 0; this.ctrl = false;
      this.term = new TC.Terminal(24, 80, {
        onData: s => { if (!this.muted) cb.onData(s); }, onTitle: t => cb.onTitle && cb.onTitle(t),
      });
      this.measure();
      this.buildRows();
      this.ro = new ResizeObserver(() => this.fit());
      this.ro.observe(host);
      this.bind();
      this.fit();
    }

    measure() {
      this.root.style.fontSize = this.fontPx + 'px';
      const r = this.meas.getBoundingClientRect();
      this.cw = r.width / 20 || 8; this.ch = Math.round(this.fontPx * 1.25 * 100) / 100;
      this.root.style.lineHeight = '1.25';
    }
    buildRows() {
      this.live.textContent = ''; this.rowEls = [];
      for (let y = 0; y < this.term.rows; y++) {
        const el = document.createElement('div'); el.className = 'tv-row';
        this.live.appendChild(el); this.rowEls.push(el);
      }
      this.term.allDirty();
    }
    setFont(px) {
      this.fontPx = Math.max(9, Math.min(28, px)); this.measure(); this.fit(true);
    }
    fit(force) {
      const w = this.scroll.clientWidth - 8, h = this.scroll.clientHeight;
      if (w <= 0 || h <= 0) return;
      const cols = Math.max(10, Math.floor(w / this.cw)), rows = Math.max(3, Math.floor(h / this.ch));
      if (!force && cols === this.term.cols && rows === this.term.rows) return;
      this.term.resize(rows, cols);
      this.buildRows();
      this.schedule();
      this.cb.onResize(rows, cols);
    }

    // While the host replays a re-attached session, the old output holds
    // device queries (DA, CPR) whose answers must not be typed into the
    // shell again: replies are muted until the replay burst has ended.
    quiet() {
      this.muted = true;
      const start = performance.now();
      const arm = () => {
        clearTimeout(this.quietT);
        this.quietT = setTimeout(() => { this.muted = false; }, performance.now() - start > 2000 ? 0 : 200);
      };
      this.rearm = arm; arm();
    }
    write(bytes) {
      if (this.muted && this.rearm) this.rearm();
      this.term.write(this.dec.decode(bytes, {stream: true}));
      this.schedule();
    }
    schedule() { if (!this.raf) this.raf = requestAnimationFrame(() => { this.raf = 0; this.render(); }); }

    render() {
      const t = this.term;
      const atBottom = this.scroll.scrollHeight - this.scroll.scrollTop - this.scroll.clientHeight < 4;
      for (const r of t.takeScrolled()) {
        const el = document.createElement('div'); el.className = 'tv-row';
        this.fillRow(el, r, -1); this.sb.appendChild(el);
      }
      while (this.sb.childElementCount > t.scrollbackMax) this.sb.removeChild(this.sb.firstChild);
      const cur = t.mode.cursor && !this.scroll.dataset.noCursor ? t.cy : -1;
      for (let y = 0; y < t.rows; y++) {
        const r = t.lines[y];
        if (r.dirty || y === cur || y === this.lastCur) { this.fillRow(this.rowEls[y], r, y === cur ? t.cx : -1); r.dirty = false; }
      }
      this.lastCur = cur;
      this.root.style.background = t.mode.reverse ? DEF_FG : DEF_BG;
      this.root.style.color = t.mode.reverse ? DEF_BG : DEF_FG;
      if (atBottom) this.scroll.scrollTop = this.scroll.scrollHeight;
    }

    fillRow(el, row, curX) {
      el.textContent = '';
      const cells = row.cells;
      // trailing default blanks are not drawn, so copied text carries none
      let end = cells.length;
      while (end > 0 && end - 1 !== curX && cells[end - 1].c === ' ' && cells[end - 1].bg < 0 && !(cells[end - 1].fl & (TC.INVERSE | TC.UNDERLINE | TC.STRIKE))) end--;
      let i = 0;
      while (i < end) {
        const c = cells[i];
        if (c.w === 0) { i++; continue; }
        const isCur = i === curX;
        if (c.w === 2) { el.appendChild(this.span(c.c, c, isCur, true)); i++; continue; }
        let text = c.c, j = i + 1;
        if (!isCur) {
          while (j < end && j !== curX && cells[j].w === 1 && cells[j].fg === c.fg && cells[j].bg === c.bg && cells[j].fl === c.fl) { text += cells[j].c; j++; }
        }
        el.appendChild(this.span(text, c, isCur, false));
        i = j;
      }
    }
    span(text, c, isCur, wide) {
      const s = document.createElement('span');
      let fg = c.fg, bg = c.bg, fl = c.fl;
      if (fl & TC.INVERSE) { const f = fg < 0 ? DEF_FG : colour(fg), b = bg < 0 ? DEF_BG : colour(bg); s.style.color = b; s.style.background = f; }
      else {
        if (fg >= 0) { if ((fl & TC.BOLD) && fg < 8) fg += 8; s.style.color = colour(fg); }
        if (bg >= 0) s.style.background = colour(bg);
      }
      let cls = '';
      if (fl & TC.BOLD) cls += ' b'; if (fl & TC.DIM) cls += ' d'; if (fl & TC.ITALIC) cls += ' i';
      if (fl & TC.UNDERLINE) cls += ' u'; if (fl & TC.STRIKE) cls += ' s';
      if (wide) cls += ' w'; if (isCur) cls += ' cur';
      if (cls) s.className = cls.trim();
      if (fl & TC.HIDDEN) s.style.color = 'transparent';
      s.textContent = text;
      return s;
    }

    // ---- input ----
    send(s) { if (s) this.cb.onData(s); }
    bind() {
      const inp = this.input, t = this.term;
      this.root.addEventListener('mousedown', e => {
        if (this.mouseOn(e)) { e.preventDefault(); this.mouse('down', e); this.dragBtn = e.button; }
      });
      window.addEventListener('mouseup', e => { if (this.dragBtn !== undefined) { this.mouse('up', e); this.dragBtn = undefined; } });
      this.root.addEventListener('mousemove', e => { if (this.dragBtn !== undefined || t.mode.mouse === 1003) this.mouse('move', e); });
      this.root.addEventListener('click', () => { if (!String(getSelection())) inp.focus({preventScroll: true}); });
      this.root.addEventListener('wheel', e => {
        if (t.mode.mouse && !e.shiftKey) { e.preventDefault(); this.mouse('wheel', e); }
        else if (t.isAlt && !t.mode.mouse) { e.preventDefault(); this.send((e.deltaY < 0 ? '\x1b[A' : '\x1b[B').repeat(3)); }
      }, {passive: false});
      this.root.addEventListener('contextmenu', e => { if (t.mode.mouse && !e.shiftKey) e.preventDefault(); });
      inp.addEventListener('focus', () => { this.root.classList.add('focus'); this.schedule(); if (t.mode.focus) this.send('\x1b[I'); });
      inp.addEventListener('blur', () => { this.root.classList.remove('focus'); this.schedule(); if (t.mode.focus) this.send('\x1b[O'); });
      inp.addEventListener('keydown', e => {
        if (e.isComposing || e.key === 'Process' || e.key === 'Dead') return;
        const sel = String(getSelection());
        if (e.ctrlKey && e.shiftKey && (e.key === 'C' || e.key === 'c')) { e.preventDefault(); this.copy(); return; }
        if (e.ctrlKey && (e.key === 'v' || e.key === 'V')) return; // becomes a paste event
        if (e.ctrlKey && !e.shiftKey && (e.key === 'c' || e.key === 'C') && sel) { this.copy(); return; }
        if (e.shiftKey && e.key === 'Insert') return;
        if ((e.metaKey) && e.key !== 'Meta') return; // browser/OS shortcuts
        const ev = {key: e.key, ctrlKey: e.ctrlKey || (this.ctrl && e.key.length === 1), altKey: e.altKey, shiftKey: e.shiftKey};
        const seq = t.keySeq(ev);
        if (this.ctrl) this.setCtrl(false);
        if (seq !== null) { e.preventDefault(); this.send(seq); }
      });
      inp.addEventListener('input', e => {
        if (e.isComposing) return;
        let v = inp.value; inp.value = '';
        if (!v) { if (e.inputType === 'deleteContentBackward') this.send('\x7f'); return; }
        if (e.inputType === 'insertLineBreak' || v === '\n') { this.send('\r'); return; }
        if (this.ctrl && v.length === 1) { const s = t.keySeq({key: v, ctrlKey: true}); this.setCtrl(false); if (s) { this.send(s); return; } }
        this.send(v);
      });
      inp.addEventListener('compositionend', e => { inp.value = ''; if (e.data) this.send(e.data); });
      inp.addEventListener('paste', e => {
        e.preventDefault();
        const text = (e.clipboardData || window.clipboardData).getData('text');
        this.paste(text);
      });
    }
    setCtrl(on) { this.ctrl = on; if (this.cb.onCtrl) this.cb.onCtrl(on); }
    paste(text) { if (text) this.send(this.term.pasteSeq(text)); }
    async copy() {
      const s = String(getSelection());
      if (!s) return false;
      try { await navigator.clipboard.writeText(s); } catch (e) { document.execCommand('copy'); }
      return true;
    }
    mouseOn(e) { return this.term.mode.mouse && !e.shiftKey; }
    mouse(type, e) {
      const r = this.live.getBoundingClientRect();
      const x = Math.floor((e.clientX - r.left - 0) / this.cw), y = Math.floor((e.clientY - r.top) / this.ch);
      const btn = type === 'wheel' ? (e.deltaY < 0 ? -1 : 1) : (type === 'move' ? (this.dragBtn === undefined ? 0 : this.dragBtn) : e.button);
      const s = this.term.mouseSeq(type, btn, Math.min(x, this.term.cols - 1), Math.min(Math.max(y, 0), this.term.rows - 1),
        {shift: e.shiftKey, alt: e.altKey, ctrl: e.ctrlKey});
      if (s) this.send(s);
    }
    focus() { this.input.focus({preventScroll: true}); }
    destroy() { this.ro.disconnect(); if (this.raf) cancelAnimationFrame(this.raf); }
  }
  SD.TermView = TermView;

  // ---- the tab ----
  let view = null, ws = null, conn = null, sessionName = '', gen = 0, current = '';
  const enc = new TextEncoder();
  // status shows a translated line (null: clear it).
  const status = (key, vars) => SD.status('t-status', key, vars);

  async function loadSessions(select) {
    try {
      const j = await SD.json('GET', 'term/sessions', undefined, {c: conn});
      const sel = $('t-sessions'); sel.textContent = '';
      for (const s of j.sessions) {
        const o = document.createElement('option'); o.value = s.name;
        SD.say(o, s.attached ? 't.attached' : 't.detached', {name: s.name});
        sel.appendChild(o);
      }
      if (select && j.sessions.some(s => s.name === select)) sel.value = select;
      return j.sessions;
    } catch (e) { SD.showErr('t-err', e); return null; }
  }

  function closeWS() { gen++; if (ws) { try { ws.close(); } catch (e) { /* ignore */ } ws = null; } }

  async function open(name, isNew) {
    closeWS();
    const my = gen;
    SD.showErr('t-err'); status('t.connecting');
    if (!view) {
      view = new TermView($('t-host'), {
        onData: s => { if (ws && ws.readyState === 1) ws.send(JSON.stringify({type: 'in', data: s})); },
        onResize: (rows, cols) => { if (ws && ws.readyState === 1) ws.send(JSON.stringify({type: 'resize', rows, cols})); },
        onTitle: t => { current = t; },
        onCtrl: on => { $('t-ctrl').setAttribute('aria-pressed', on ? 'true' : 'false'); },
      });
    } else view.term.reset();
    let t;
    try { t = await SD.ticket('ws'); } catch (e) { SD.showErr('t-err', e); status(null); return; }
    if (my !== gen) return;
    const params = {t, c: conn, rows: view.term.rows, cols: view.term.cols};
    if (name) params.session = name;
    if (isNew) params.new = '1';
    const sock = new WebSocket(SD.wsURL('term/ws', params));
    sock.binaryType = 'arraybuffer';
    ws = sock;
    sock.onmessage = ev => {
      if (my !== gen) return;
      if (typeof ev.data !== 'string') { view.write(new Uint8Array(ev.data)); return; }
      let m; try { m = JSON.parse(ev.data); } catch (e) { return; }
      switch (m.type) {
        case 'attached': view.quiet(); sessionName = m.session; status(m.created ? 't.st.new' : 't.st.reattached', {name: m.session}); loadSessions(m.session); view.focus(); break;
        case 'exit': status(m.signal ? 't.st.exitsig' : 't.st.exit', {code: m.code, signal: m.signal}); loadSessions(); break;
        case 'detached': status('t.st.detached', {reason: m.reason}); break;
        case 'refused': SD.showErr('t-err', {code: 'host_refused', message: m.message}); status('t.st.refused'); break;
        case 'error': SD.showErr('t-err', {message: m.message}); status('t.st.error'); break;
      }
    };
    sock.onclose = () => { if (my === gen) { status('t.st.closed'); if (SD.heartbeat) SD.heartbeat(); } };
    sock.onopen = () => {};
  }

  $('t-new').addEventListener('click', () => open('', true));
  $('t-sessions').addEventListener('change', () => open($('t-sessions').value, false));
  $('t-kill').addEventListener('click', async () => {
    const n = $('t-sessions').value || sessionName; if (!n) return;
    if (!await SD.confirm(SD.t('t.kill.title'), SD.t('t.kill.msg', {name: n}), SD.t('t.kill.ok'))) return;
    try { await SD.json('POST', 'term/kill', {c: conn, name: n}); } catch (e) { SD.showErr('t-err', e); }
    closeWS(); if (view) view.term.reset();
    const l = await loadSessions();
    if (l && l.length) open(l[0].name, false); else status('t.st.none');
  });
  $('t-copy').addEventListener('click', () => { if (view) view.copy(); });
  $('t-paste').addEventListener('click', async () => {
    try { view.paste(await navigator.clipboard.readText()); view.focus(); }
    catch (e) { status('t.st.noclip'); view && view.focus(); }
  });
  $('t-bigger').addEventListener('click', () => view && view.setFont(view.fontPx + 1));
  $('t-smaller').addEventListener('click', () => view && view.setFont(view.fontPx - 1));
  $('t-ctrl').addEventListener('click', () => { if (view) { view.setCtrl(!view.ctrl); view.focus(); } });
  for (const b of $('t-keys').querySelectorAll('button[data-k],button[data-t]')) {
    b.addEventListener('click', () => {
      if (!view) return;
      const s = b.dataset.k ? view.term.keySeq({key: b.dataset.k}) : b.dataset.t;
      view.send(view.ctrl && b.dataset.t ? view.term.keySeq({key: b.dataset.t, ctrlKey: true}) || s : s);
      view.setCtrl(false); view.focus();
    });
  }

  SD.tabs.terminal = {
    async show(c) {
      const changed = c !== conn;
      conn = c;
      if (changed) sessionName = '';
      if (changed && view) { closeWS(); view.term.reset(); }
      if (ws && ws.readyState <= 1) { view.fit(true); view.focus(); return; }
      const list = await loadSessions();
      if (list === null) return;
      if (view) view.fit(true);
      // After a reconnection, the shell this tab had (the host kept it).
      const pick = list.find(s => s.name === sessionName) || list.find(s => !s.attached) || list[0];
      open(pick ? pick.name : '', !pick);
    },
    hide() { closeWS(); status(null); },
  };
  // The sample text of the title is host-controlled; it is only ever shown via textContent.
  void enc; void current;
})();
