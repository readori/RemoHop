// term_core.js: the terminal emulator of the RemoHop web client, without any
// DOM. A VT100/xterm subset, written for a login shell, nano/vim/top/less,
// coloured output and ncurses programs. term.js draws it; the Go tests run
// testdata/term_core_test.js against it under Node.
//
// Supported: UTF-8 text, wide and combining characters (best effort, table
// based); C0 controls (BS HT LF VT FF CR SO SI BEL); cursor movement (CUU CUD
// CUF CUB CNL CPL CHA CUP HVP HPA HPR VPA VPR CHT CBT, IND NEL RI, DECSC/DECRC,
// SCOSC/SCORC); erase (ED 0-3, EL 0-2, ECH, ICH, DCH, IL, DL); scrolling (SU, SD,
// DECSTBM regions, origin mode DECOM, scrollback on the main screen); tab stops
// (HTS TBC); SGR (bold dim italic underline blink inverse hidden strike, 16
// colours, 256-colour and truecolour in both ; and : forms, background colour
// erase); DEC special graphics line drawing (ESC ( 0, SO/SI); modes: insert,
// DECCKM, reverse video, DECOM, DECAWM, cursor visibility, alternate screen
// (47, 1047, 1049), bracketed paste (2004), focus events (1004), mouse
// reporting (1000, 1002, 1003 with the default or SGR 1006 encoding); device
// answers (DA1, DA2, DSR 5 and 6, window size 18 t); titles (OSC 0/1/2);
// DECSTR, RIS, DECALN, DECSCUSR (cursor style recorded).
//
// Not supported (parsed and ignored): sixel and other DCS/APC/PM/SOS strings,
// OSC 52 clipboard access and every other OSC but the titles, OSC 8 links,
// colour queries and palette changes, DECCOLM and left/right margins, soft
// scrolling, double-width/height lines, DEC rectangular operations, character
// sets other than ASCII and DEC graphics, bidirectional text, grapheme
// clusters beyond a base character plus combining marks, reflow of wrapped
// lines on resize, 8-bit C1 controls.
(function (root) {
'use strict';

const BOLD = 1, DIM = 2, ITALIC = 4, UNDERLINE = 8, BLINK = 16, INVERSE = 32, HIDDEN = 64, STRIKE = 128;
const RGB = 0x1000000; // colours >= RGB are truecolor, RGB | 0xRRGGBB

// ---- character widths (best effort) -------------------------------------
const ZERO = [[0x0300, 0x036f], [0x0483, 0x0489], [0x0591, 0x05bd], [0x0610, 0x061a], [0x064b, 0x065f],
  [0x0670, 0x0670], [0x06d6, 0x06dc], [0x0e31, 0x0e31], [0x0e34, 0x0e3a], [0x0e47, 0x0e4e],
  [0x1ab0, 0x1aff], [0x1dc0, 0x1dff], [0x200b, 0x200f], [0x2028, 0x202e], [0x2060, 0x2064],
  [0x20d0, 0x20ff], [0x302a, 0x302d], [0x3099, 0x309a], [0xfe00, 0xfe0f], [0xfe20, 0xfe2f],
  [0xfeff, 0xfeff], [0xe0100, 0xe01ef]];
const WIDE = [[0x1100, 0x115f], [0x231a, 0x231b], [0x2329, 0x232a], [0x23e9, 0x23ec], [0x23f0, 0x23f0],
  [0x23f3, 0x23f3], [0x25fd, 0x25fe], [0x2614, 0x2615], [0x2648, 0x2653], [0x267f, 0x267f],
  [0x2693, 0x2693], [0x26a1, 0x26a1], [0x26aa, 0x26ab], [0x26bd, 0x26be], [0x26c4, 0x26c5],
  [0x26ce, 0x26ce], [0x26d4, 0x26d4], [0x26ea, 0x26ea], [0x26f2, 0x26f3], [0x26f5, 0x26f5],
  [0x26fa, 0x26fa], [0x26fd, 0x26fd], [0x2705, 0x2705], [0x270a, 0x270b], [0x2728, 0x2728],
  [0x274c, 0x274c], [0x274e, 0x274e], [0x2753, 0x2755], [0x2757, 0x2757], [0x2795, 0x2797],
  [0x27b0, 0x27b0], [0x27bf, 0x27bf], [0x2b1b, 0x2b1c], [0x2b50, 0x2b50], [0x2b55, 0x2b55],
  [0x2e80, 0x303e], [0x3041, 0x33ff], [0x3400, 0x4dbf], [0x4e00, 0xa4cf], [0xa960, 0xa97f],
  [0xac00, 0xd7a3], [0xf900, 0xfaff], [0xfe10, 0xfe19], [0xfe30, 0xfe6f], [0xff00, 0xff60],
  [0xffe0, 0xffe6], [0x1f004, 0x1f004], [0x1f0cf, 0x1f0cf], [0x1f18e, 0x1f18e], [0x1f191, 0x1f19a],
  [0x1f200, 0x1f320], [0x1f32d, 0x1f335], [0x1f337, 0x1f37c], [0x1f37e, 0x1f393], [0x1f3a0, 0x1f3ca],
  [0x1f3cf, 0x1f3d3], [0x1f3e0, 0x1f3f0], [0x1f3f4, 0x1f3f4], [0x1f3f8, 0x1f43e], [0x1f440, 0x1f440],
  [0x1f442, 0x1f4fc], [0x1f4ff, 0x1f53d], [0x1f54b, 0x1f54e], [0x1f550, 0x1f567], [0x1f57a, 0x1f57a],
  [0x1f595, 0x1f596], [0x1f5a4, 0x1f5a4], [0x1f5fb, 0x1f64f], [0x1f680, 0x1f6c5], [0x1f6cc, 0x1f6cc],
  [0x1f6d0, 0x1f6d2], [0x1f6d5, 0x1f6d7], [0x1f6eb, 0x1f6ec], [0x1f6f4, 0x1f6fc], [0x1f7e0, 0x1f7eb],
  [0x1f90c, 0x1f93a], [0x1f93c, 0x1f945], [0x1f947, 0x1f9ff], [0x1fa70, 0x1faff], [0x20000, 0x3fffd]];

function inTable(t, cp) {
  let lo = 0, hi = t.length - 1;
  while (lo <= hi) {
    const m = (lo + hi) >> 1;
    if (cp < t[m][0]) hi = m - 1; else if (cp > t[m][1]) lo = m + 1; else return true;
  }
  return false;
}
function cwidth(cp) {
  if (cp < 0x300) return 1;
  if (inTable(ZERO, cp)) return 0;
  if (inTable(WIDE, cp)) return 2;
  return 1;
}

// DEC special graphics, 0x5f..0x7e
const DEC_GFX = ' ◆▒␉␌␍␊°±␤␋┘┐┌└┼⎺⎻─⎼⎽├┤┴┬│≤≥π≠£·';

class Cell {
  constructor(c, w, fg, bg, fl) { this.c = c; this.w = w; this.fg = fg; this.bg = bg; this.fl = fl; }
}

class Row {
  constructor(cols, bg) {
    this.cells = new Array(cols);
    for (let i = 0; i < cols; i++) this.cells[i] = new Cell(' ', 1, -1, bg === undefined ? -1 : bg, 0);
    this.wrapped = false;
    this.dirty = true;
  }
  text() {
    let s = '';
    for (const c of this.cells) if (c.w !== 0) s += c.c;
    return s.replace(/ +$/, '');
  }
}

const G = 0, ESC = 1, ESC_INT = 2, CSI = 3, OSC = 4, OSC_ESC = 5, STR = 6, STR_ESC = 7;

class Terminal {
  constructor(rows, cols, opts) {
    opts = opts || {};
    this.scrollbackMax = opts.scrollback === undefined ? 2000 : opts.scrollback;
    this.onData = opts.onData || null;      // answers to the host (DA, CPR)
    this.onTitle = opts.onTitle || null;
    this.onBell = opts.onBell || null;
    this.rows = Math.max(1, rows | 0);
    this.cols = Math.max(1, cols | 0);
    this.scrolled = [];                     // rows pushed into scrollback since the last take
    this.scrollback = [];
    this.reset();
  }

  reset() {
    this.main = this._lines();
    this.alt = this._lines();
    this.lines = this.main;
    this.isAlt = false;
    this.cx = 0; this.cy = 0;
    this.wrapPending = false;
    this.fg = -1; this.bg = -1; this.fl = 0;
    this.top = 0; this.bottom = this.rows - 1;
    this.charsets = ['B', 'B']; this.gl = 0;
    this.saved = null;
    this.tabs = new Set();
    for (let i = 8; i < this.cols; i += 8) this.tabs.add(i);
    this.mode = {appCursor: false, reverse: false, origin: false, autowrap: true, cursor: true,
      insert: false, lnm: false, bracketed: false, focus: false, mouse: 0, sgrMouse: false, appKeypad: false};
    this.cursorStyle = 0;
    this.title = '';
    this.st = G; this.buf = ''; this.inter = '';
    this.lastCh = 0;
    this.allDirty();
  }

  _lines() {
    const a = new Array(this.rows);
    for (let i = 0; i < this.rows; i++) a[i] = new Row(this.cols);
    return a;
  }
  _blankRow() { return new Row(this.cols, this.bg); }
  allDirty() { for (const r of this.lines) r.dirty = true; }
  takeScrolled() { const s = this.scrolled; this.scrolled = []; return s; }

  // ---- output to host ----
  _reply(s) { if (this.onData) this.onData(s); }

  // ---- text input ----
  write(str) {
    for (let i = 0; i < str.length; i++) {
      let c = str.charCodeAt(i);
      if (c >= 0xd800 && c < 0xdc00 && i + 1 < str.length) {
        const d = str.charCodeAt(i + 1);
        if (d >= 0xdc00 && d < 0xe000) { c = 0x10000 + ((c - 0xd800) << 10) + (d - 0xdc00); i++; }
      }
      this._ch(c);
    }
  }

  _ch(c) {
    // These controls act in every state.
    if (c === 0x18 || c === 0x1a) { this.st = G; return; }
    switch (this.st) {
      case G:
        if (c < 0x20) return this._c0(c);
        if (c === 0x7f || (c >= 0x80 && c < 0xa0)) return;
        return this._print(c);
      case ESC:
        if (c === 0x1b) return;
        if (c < 0x20) return this._c0(c);
        return this._escFinal(c);
      case ESC_INT:
        if (c === 0x1b) { this.st = ESC; return; }
        if (c < 0x20) return this._c0(c);
        if (c >= 0x20 && c <= 0x2f) { this.inter += String.fromCharCode(c); return; }
        this.st = G;
        return this._escInter(String.fromCharCode(c));
      case CSI:
        if (c === 0x1b) { this.st = ESC; return; }
        if (c < 0x20) return this._c0(c);
        if (c >= 0x40 && c <= 0x7e) { this.st = G; return this._csi(String.fromCharCode(c)); }
        if (c >= 0x20 && c <= 0x3f) { if (this.buf.length < 256) this.buf += String.fromCharCode(c); return; }
        this.st = G;
        return;
      case OSC:
        if (c === 0x07 || c === 0x9c) { this.st = G; return this._osc(); }
        if (c === 0x1b) { this.st = OSC_ESC; return; }
        if (c >= 0x20 && this.buf.length < 4096) this.buf += String.fromCodePoint(c);
        return;
      case OSC_ESC:
        if (c === 0x5c) { this.st = G; return this._osc(); }
        this.st = ESC;
        return this._ch(c);
      case STR: // DCS, SOS, PM, APC: skipped until ST
        if (c === 0x07 || c === 0x9c) { this.st = G; return; }
        if (c === 0x1b) this.st = STR_ESC;
        return;
      case STR_ESC:
        if (c === 0x5c) { this.st = G; return; }
        this.st = ESC;
        return this._ch(c);
    }
  }

  _c0(c) {
    switch (c) {
      case 0x07: if (this.onBell) this.onBell(); break;
      case 0x08: if (this.cx > 0) this.cx--; this.wrapPending = false; break;
      case 0x09: this._tab(1); break;
      case 0x0a: case 0x0b: case 0x0c:
        this._index(); if (this.mode.lnm) this.cx = 0; this.wrapPending = false; break;
      case 0x0d: this.cx = 0; this.wrapPending = false; break;
      case 0x0e: this.gl = 1; break;
      case 0x0f: this.gl = 0; break;
      case 0x1b: this.st = ESC; this.inter = ''; break;
    }
  }

  _escFinal(c) {
    const ch = String.fromCharCode(c);
    this.st = G;
    switch (ch) {
      case '[': this.st = CSI; this.buf = ''; break;
      case ']': this.st = OSC; this.buf = ''; break;
      case 'P': case 'X': case '^': case '_': this.st = STR; break;
      case ' ': case '#': case '(': case ')': case '*': case '+': case '%': case '-': case '.': case '/':
        this.st = ESC_INT; this.inter = ch; break;
      case '7': this._save(); break;
      case '8': this._restore(); break;
      case 'D': this._index(); break;
      case 'E': this.cx = 0; this._index(); break;
      case 'H': this.tabs.add(this.cx); break;
      case 'M': this._revIndex(); break;
      case 'c': this.reset(); break;
      case '=': this.mode.appKeypad = true; break;
      case '>': this.mode.appKeypad = false; break;
      default: break; // includes '\\' (ST)
    }
  }

  _escInter(final) {
    const i = this.inter;
    if (i === '(' || i === ')') {
      this.charsets[i === '(' ? 0 : 1] = final === '0' ? '0' : 'B';
    } else if (i === '#' && final === '8') { // DECALN
      for (let y = 0; y < this.rows; y++) {
        const r = new Row(this.cols);
        for (const c of r.cells) c.c = 'E';
        this.lines[y] = r;
      }
      this.cx = this.cy = 0; this.wrapPending = false;
    }
  }

  // ---- cursor helpers ----
  _save() {
    this.saved = {cx: this.cx, cy: this.cy, fg: this.fg, bg: this.bg, fl: this.fl, wrap: this.wrapPending,
      origin: this.mode.origin, autowrap: this.mode.autowrap, gl: this.gl, cs: this.charsets.slice()};
  }
  _restore() {
    const s = this.saved;
    if (!s) { this.cx = this.cy = 0; this.fg = this.bg = -1; this.fl = 0; this.wrapPending = false; this.mode.origin = false; return; }
    this.cx = Math.min(s.cx, this.cols - 1); this.cy = Math.min(s.cy, this.rows - 1);
    this.fg = s.fg; this.bg = s.bg; this.fl = s.fl; this.wrapPending = s.wrap;
    this.mode.origin = s.origin; this.mode.autowrap = s.autowrap; this.gl = s.gl; this.charsets = s.cs.slice();
  }
  _index() {
    if (this.cy === this.bottom) this._scrollUp(1);
    else if (this.cy < this.rows - 1) this.cy++;
  }
  _revIndex() {
    if (this.cy === this.top) this._scrollDown(1);
    else if (this.cy > 0) this.cy--;
  }
  _scrollUp(n) {
    n = Math.min(n, this.bottom - this.top + 1);
    for (let i = 0; i < n; i++) {
      const gone = this.lines.splice(this.top, 1)[0];
      this.lines.splice(this.bottom, 0, this._blankRow());
      if (this.top === 0 && !this.isAlt && this.scrollbackMax > 0) {
        gone.dirty = true;
        this.scrollback.push(gone);
        this.scrolled.push(gone);
        if (this.scrollback.length > this.scrollbackMax) this.scrollback.shift();
      }
    }
    for (let y = this.top; y <= this.bottom; y++) this.lines[y].dirty = true;
  }
  _scrollDown(n) {
    n = Math.min(n, this.bottom - this.top + 1);
    for (let i = 0; i < n; i++) {
      this.lines.splice(this.bottom, 1);
      this.lines.splice(this.top, 0, this._blankRow());
    }
    for (let y = this.top; y <= this.bottom; y++) this.lines[y].dirty = true;
  }
  _tab(n) {
    let x = this.cx;
    while (n-- > 0) {
      x++;
      while (x < this.cols - 1 && !this.tabs.has(x)) x++;
    }
    this.cx = Math.min(x, this.cols - 1);
    this.wrapPending = false;
  }
  _backTab(n) {
    let x = this.cx;
    while (n-- > 0) {
      x--;
      while (x > 0 && !this.tabs.has(x)) x--;
    }
    this.cx = Math.max(x, 0);
    this.wrapPending = false;
  }

  // ---- printing ----
  _print(cp) {
    let ch = cp;
    if (this.charsets[this.gl] === '0' && cp >= 0x5f && cp <= 0x7e) ch = DEC_GFX.codePointAt(cp - 0x5f);
    const w = cwidth(ch);
    if (w === 0) { // combining mark: joins the previous cell
      let x = this.wrapPending ? this.cx : this.cx - 1;
      const row = this.lines[this.cy];
      if (x >= 0 && row.cells[x].w === 0 && x > 0) x--;
      if (x >= 0) { row.cells[x].c += String.fromCodePoint(ch); row.dirty = true; }
      return;
    }
    this.lastCh = ch;
    if (this.wrapPending) {
      this.wrapPending = false;
      if (this.mode.autowrap) this._wrap();
    }
    if (w === 2 && this.cx === this.cols - 1) {
      if (!this.mode.autowrap) return;
      this._put(this.cx, ' ', 1);
      this._wrap();
    }
    if (this.mode.insert) this._insertCells(w);
    this._put(this.cx, String.fromCodePoint(ch), w);
    if (w === 2) this._put(this.cx + 1, '', 0);
    this.cx += w;
    if (this.cx >= this.cols) { this.cx = this.cols - 1; this.wrapPending = true; }
  }
  _wrap() {
    this.lines[this.cy].wrapped = true;
    this.cx = 0;
    this._index();
  }
  _put(x, c, w) {
    const row = this.lines[this.cy];
    const old = row.cells[x];
    // overwriting half a wide character blanks the other half
    if (old.w === 2 && x + 1 < this.cols) row.cells[x + 1] = new Cell(' ', 1, -1, this.bg, 0);
    if (old.w === 0 && x > 0) row.cells[x - 1] = new Cell(' ', 1, -1, this.bg, 0);
    row.cells[x] = new Cell(c, w, this.fg, this.bg, this.fl);
    row.dirty = true;
  }
  _blank() { return new Cell(' ', 1, -1, this.bg, 0); }
  _insertCells(n) {
    const row = this.lines[this.cy];
    n = Math.min(n, this.cols - this.cx);
    for (let i = 0; i < n; i++) { row.cells.pop(); row.cells.splice(this.cx, 0, this._blank()); }
    this._fixWideEdges(row);
    row.dirty = true;
  }
  _fixWideEdges(row) {
    const last = row.cells[this.cols - 1];
    if (last.w === 2) row.cells[this.cols - 1] = this._blank();
    if (row.cells[0].w === 0) row.cells[0] = this._blank();
  }

  // ---- CSI ----
  _params() {
    let s = this.buf, priv = '';
    if (s.length && '<=>?'.includes(s[0])) { priv = s[0]; s = s.slice(1); }
    let inter = '';
    const m = /[ -\/]+$/.exec(s);
    if (m) { inter = m[0]; s = s.slice(0, -inter.length); }
    const groups = s === '' ? [] : s.split(';').map(g => g.split(':').map(v => v === '' ? 0 : Math.min(parseInt(v, 10) || 0, 1 << 20)));
    return {priv, inter, groups};
  }

  _csi(f) {
    const {priv, inter, groups} = this._params();
    const raw = (i, d) => (i < groups.length ? groups[i][0] : d);
    const num = (i, d) => { const v = raw(i, 0); return v === 0 ? d : v; };
    const cols = this.cols, rows = this.rows;
    if (priv === '?' && (f === 'h' || f === 'l')) { for (const g of groups) this._privMode(g[0], f === 'h'); return; }
    if (priv === '>' && f === 'c') { this._reply('\x1b[>0;276;0c'); return; }
    if (priv === '=' && f === 'c') { this._reply('\x1bP!|00000000\x1b\\'); return; }
    if (priv) return;
    if (inter === '!' && f === 'p') { this._softReset(); return; }
    if (inter === ' ' && f === 'q') { this.cursorStyle = raw(0, 0); return; }
    if (inter) return;
    const vminY = () => (this.cy >= this.top ? this.top : 0);
    const vmaxY = () => (this.cy <= this.bottom ? this.bottom : rows - 1);
    switch (f) {
      case '@': this._insertCells(num(0, 1)); break;
      case 'A': this.cy = Math.max(vminY(), this.cy - num(0, 1)); this.wrapPending = false; break;
      case 'B': case 'e': this.cy = Math.min(vmaxY(), this.cy + num(0, 1)); this.wrapPending = false; break;
      case 'C': case 'a': this.cx = Math.min(cols - 1, this.cx + num(0, 1)); this.wrapPending = false; break;
      case 'D': this.cx = Math.max(0, this.cx - num(0, 1)); this.wrapPending = false; break;
      case 'E': this.cy = Math.min(vmaxY(), this.cy + num(0, 1)); this.cx = 0; this.wrapPending = false; break;
      case 'F': this.cy = Math.max(vminY(), this.cy - num(0, 1)); this.cx = 0; this.wrapPending = false; break;
      case 'G': case '`': this.cx = Math.min(cols, num(0, 1)) - 1; this.wrapPending = false; break;
      case 'H': case 'f': this._moveTo(num(1, 1) - 1, num(0, 1) - 1); break;
      case 'I': this._tab(num(0, 1)); break;
      case 'Z': this._backTab(num(0, 1)); break;
      case 'J': this._eraseDisplay(raw(0, 0)); break;
      case 'K': this._eraseLine(raw(0, 0)); break;
      case 'L': this._insertLines(num(0, 1)); break;
      case 'M': this._deleteLines(num(0, 1)); break;
      case 'P': this._deleteCells(num(0, 1)); break;
      case 'S': this._scrollUp(num(0, 1)); break;
      case 'T': this._scrollDown(num(0, 1)); break;
      case 'X': this._eraseCells(this.cx, Math.min(cols, this.cx + num(0, 1))); break;
      case 'b': for (let n = Math.min(num(0, 1), 65536); n > 0 && this.lastCh; n--) this._print(this.lastCh); break;
      case 'c': if (raw(0, 0) === 0) this._reply('\x1b[?62;1;22c'); break;
      case 'd': this._moveTo(this.cx, num(0, 1) - 1); break;
      case 'g':
        if (raw(0, 0) === 0) this.tabs.delete(this.cx); else if (raw(0, 0) === 3) this.tabs.clear();
        break;
      case 'h': case 'l':
        for (const g of groups) {
          if (g[0] === 4) this.mode.insert = f === 'h';
          else if (g[0] === 20) this.mode.lnm = f === 'h';
        }
        break;
      case 'm': this._sgr(groups); break;
      case 'n':
        if (raw(0, 0) === 5) this._reply('\x1b[0n');
        else if (raw(0, 0) === 6) {
          const y = this.mode.origin ? this.cy - this.top : this.cy;
          this._reply(`\x1b[${y + 1};${this.cx + 1}R`);
        }
        break;
      case 'r': {
        const t = num(0, 1) - 1, b = Math.min(rows, num(1, rows)) - 1;
        if (t < b) { this.top = t; this.bottom = b; this._moveTo(0, 0); }
        break;
      }
      case 's': this._save(); break;
      case 'u': this._restore(); break;
      case 't':
        if (raw(0, 0) === 18) this._reply(`\x1b[8;${rows};${cols}t`);
        break;
      default: break;
    }
  }

  _moveTo(x, y) {
    if (this.mode.origin) y = Math.min(this.top + y, this.bottom);
    this.cy = Math.max(0, Math.min(this.rows - 1, y));
    this.cx = Math.max(0, Math.min(this.cols - 1, x));
    this.wrapPending = false;
  }

  _softReset() {
    this.mode.insert = false; this.mode.origin = false; this.mode.autowrap = true; this.mode.cursor = true;
    this.mode.appCursor = false; this.mode.appKeypad = false;
    this.fg = this.bg = -1; this.fl = 0;
    this.top = 0; this.bottom = this.rows - 1;
    this.charsets = ['B', 'B']; this.gl = 0; this.saved = null; this.wrapPending = false;
  }

  _privMode(n, on) {
    const m = this.mode;
    switch (n) {
      case 1: m.appCursor = on; break;
      case 5: m.reverse = on; this.allDirty(); break;
      case 6: m.origin = on; this._moveTo(0, 0); break;
      case 7: m.autowrap = on; break;
      case 25: m.cursor = on; break;
      case 47: case 1047: this._altScreen(on, false); break;
      case 1048: if (on) this._save(); else this._restore(); break;
      case 1049: this._altScreen(on, true); break;
      case 1000: case 1002: case 1003: m.mouse = on ? n : (m.mouse === n ? 0 : m.mouse); break;
      case 1006: m.sgrMouse = on; break;
      case 1004: m.focus = on; break;
      case 2004: m.bracketed = on; break;
      default: break; // 12 (blink), 2026 (sync), 1007, DECCOLM...: accepted and ignored
    }
  }

  _altScreen(on, saveCursor) {
    if (on === this.isAlt) return;
    if (on) {
      if (saveCursor) this._save();
      this.alt = this._lines();
      this.lines = this.alt;
      this.isAlt = true;
    } else {
      this.lines = this.main;
      this.isAlt = false;
      if (saveCursor) this._restore();
    }
    this.top = 0; this.bottom = this.rows - 1;
    this.allDirty();
  }

  // ---- erase / insert / delete ----
  _eraseCells(x0, x1) {
    const row = this.lines[this.cy];
    for (let x = x0; x < x1; x++) row.cells[x] = this._blank();
    // erasing half of a wide character blanks the other half too
    if (x0 > 0 && row.cells[x0 - 1].w === 2) row.cells[x0 - 1] = this._blank();
    if (x1 < this.cols && row.cells[x1].w === 0) row.cells[x1] = this._blank();
    row.dirty = true;
    this.wrapPending = false;
  }
  _eraseLine(mode) {
    if (mode === 0) this._eraseCells(this.cx, this.cols);
    else if (mode === 1) this._eraseCells(0, this.cx + 1);
    else this._eraseCells(0, this.cols);
    if (mode !== 1) this.lines[this.cy].wrapped = false;
  }
  _eraseDisplay(mode) {
    const cy = this.cy, cx = this.cx;
    if (mode === 0) {
      this._eraseLine(0);
      for (let y = cy + 1; y < this.rows; y++) this.lines[y] = this._blankRow();
    } else if (mode === 1) {
      for (let y = 0; y < cy; y++) this.lines[y] = this._blankRow();
      this._eraseLine(1);
    } else if (mode === 2) {
      for (let y = 0; y < this.rows; y++) this.lines[y] = this._blankRow();
    } else if (mode === 3) {
      this.scrollback = [];
      this.scrolledClear = true;
    }
    this.cx = cx; this.cy = cy;
  }
  _insertLines(n) {
    if (this.cy < this.top || this.cy > this.bottom) return;
    n = Math.min(n, this.bottom - this.cy + 1);
    for (let i = 0; i < n; i++) {
      this.lines.splice(this.bottom, 1);
      this.lines.splice(this.cy, 0, this._blankRow());
    }
    for (let y = this.cy; y <= this.bottom; y++) this.lines[y].dirty = true;
    this.cx = 0; this.wrapPending = false;
  }
  _deleteLines(n) {
    if (this.cy < this.top || this.cy > this.bottom) return;
    n = Math.min(n, this.bottom - this.cy + 1);
    for (let i = 0; i < n; i++) {
      this.lines.splice(this.cy, 1);
      this.lines.splice(this.bottom, 0, this._blankRow());
    }
    for (let y = this.cy; y <= this.bottom; y++) this.lines[y].dirty = true;
    this.cx = 0; this.wrapPending = false;
  }
  _deleteCells(n) {
    const row = this.lines[this.cy];
    n = Math.min(n, this.cols - this.cx);
    row.cells.splice(this.cx, n);
    for (let i = 0; i < n; i++) row.cells.push(this._blank());
    this._fixWideEdges(row);
    row.dirty = true;
    this.wrapPending = false;
  }

  // ---- SGR ----
  _sgr(groups) {
    if (groups.length === 0) groups = [[0]];
    for (let i = 0; i < groups.length; i++) {
      const g = groups[i], p = g[0];
      switch (true) {
        case p === 0: this.fg = this.bg = -1; this.fl = 0; break;
        case p === 1: this.fl |= BOLD; break;
        case p === 2: this.fl |= DIM; break;
        case p === 3: this.fl |= ITALIC; break;
        case p === 4: this.fl = (g.length > 1 && g[1] === 0) ? this.fl & ~UNDERLINE : this.fl | UNDERLINE; break;
        case p === 5 || p === 6: this.fl |= BLINK; break;
        case p === 7: this.fl |= INVERSE; break;
        case p === 8: this.fl |= HIDDEN; break;
        case p === 9: this.fl |= STRIKE; break;
        case p === 21: this.fl |= UNDERLINE; break;
        case p === 22: this.fl &= ~(BOLD | DIM); break;
        case p === 23: this.fl &= ~ITALIC; break;
        case p === 24: this.fl &= ~UNDERLINE; break;
        case p === 25: this.fl &= ~BLINK; break;
        case p === 27: this.fl &= ~INVERSE; break;
        case p === 28: this.fl &= ~HIDDEN; break;
        case p === 29: this.fl &= ~STRIKE; break;
        case p >= 30 && p <= 37: this.fg = p - 30; break;
        case p === 39: this.fg = -1; break;
        case p >= 40 && p <= 47: this.bg = p - 40; break;
        case p === 49: this.bg = -1; break;
        case p >= 90 && p <= 97: this.fg = p - 90 + 8; break;
        case p >= 100 && p <= 107: this.bg = p - 100 + 8; break;
        case p === 38 || p === 48: {
          let col = null, used = 0;
          if (g.length > 1) { // 38:5:n or 38:2:[cs:]r:g:b
            if (g[1] === 5 && g.length > 2) col = g[2] & 255;
            else if (g[1] === 2 && g.length >= 5) { const k = g.length - 3; col = RGB | ((g[k] & 255) << 16) | ((g[k + 1] & 255) << 8) | (g[k + 2] & 255); }
          } else {
            const n = (j) => (groups[i + j] ? groups[i + j][0] : 0);
            if (n(1) === 5) { col = n(2) & 255; used = 2; }
            else if (n(1) === 2) { col = RGB | ((n(2) & 255) << 16) | ((n(3) & 255) << 8) | (n(4) & 255); used = 4; }
            else used = 1;
          }
          i += used;
          if (col !== null) { if (p === 38) this.fg = col; else this.bg = col; }
          break;
        }
        default: break;
      }
    }
  }

  // ---- OSC ----
  _osc() {
    const m = /^(\d+);?([\s\S]*)$/.exec(this.buf);
    if (!m) return;
    const n = +m[1];
    if (n === 0 || n === 1 || n === 2) {
      // Titles are host-controlled text: strip controls, bound the length.
      this.title = m[2].replace(/[\u0000-\u001f\u007f-\u009f\u202a-\u202e\u2066-\u2069]/g, '').slice(0, 200);
      if (this.onTitle) this.onTitle(this.title);
    }
    // everything else, OSC 52 clipboard writes and colour queries included, is ignored
  }

  // ---- size ----
  resize(rows, cols) {
    rows = Math.max(1, rows | 0); cols = Math.max(1, cols | 0);
    if (rows === this.rows && cols === this.cols) return;
    for (const lines of [this.main, this.alt]) {
      for (const r of lines) {
        if (r.cells.length > cols) r.cells.length = cols;
        while (r.cells.length < cols) r.cells.push(new Cell(' ', 1, -1, -1, 0));
        if (r.cells[cols - 1].w === 2) r.cells[cols - 1] = new Cell(' ', 1, -1, -1, 0);
        r.dirty = true;
      }
    }
    const main = this.main;
    if (main.length > rows) {
      // keep the cursor row visible: lose lines from the top (into the
      // scrollback) while the cursor sits below the new bottom
      const drop = main.length - rows;
      const fromTop = this.isAlt ? 0 : Math.max(0, Math.min(drop, this.cy - rows + 1));
      for (const g of main.splice(0, fromTop)) {
        this.scrollback.push(g); this.scrolled.push(g);
        if (this.scrollback.length > this.scrollbackMax) this.scrollback.shift();
      }
      main.length = rows;
      if (!this.isAlt) this.cy -= fromTop;
    }
    while (main.length < rows) main.push(new Row(cols));
    const alt = this.alt;
    alt.length = Math.min(alt.length, rows);
    while (alt.length < rows) alt.push(new Row(cols));
    this.lines = this.isAlt ? alt : main;
    this.rows = rows; this.cols = cols;
    this.top = 0; this.bottom = rows - 1;
    this.cx = Math.min(this.cx, cols - 1); this.cy = Math.max(0, Math.min(this.cy, rows - 1));
    this.wrapPending = false;
    this.tabs = new Set();
    for (let i = 8; i < cols; i += 8) this.tabs.add(i);
    this.allDirty();
  }

  // ---- reading the screen (tests, copy) ----
  screenText() { return this.lines.map(r => r.text()); }
  textAt(y) { return this.lines[y].text(); }

  // ---- host-bound input encodings ----
  // keySeq turns a keydown (key, ctrlKey, altKey, shiftKey, metaKey) into
  // the bytes a terminal sends, or null if the key is plain text that the
  // page should take from the input event instead.
  keySeq(e) {
    const key = e.key, ctrl = !!e.ctrlKey, alt = !!e.altKey, shift = !!e.shiftKey;
    const mod = 1 + (shift ? 1 : 0) + (alt ? 2 : 0) + (ctrl ? 4 : 0);
    const app = this.mode.appCursor;
    const csi = (code, final) => mod > 1 ? `\x1b[${code};${mod}${final}` : (code === 1 ? `\x1b[${final}` : `\x1b[${code}${final}`);
    switch (key) {
      case 'ArrowUp': return mod > 1 ? csi(1, 'A') : (app ? '\x1bOA' : '\x1b[A');
      case 'ArrowDown': return mod > 1 ? csi(1, 'B') : (app ? '\x1bOB' : '\x1b[B');
      case 'ArrowRight': return mod > 1 ? csi(1, 'C') : (app ? '\x1bOC' : '\x1b[C');
      case 'ArrowLeft': return mod > 1 ? csi(1, 'D') : (app ? '\x1bOD' : '\x1b[D');
      case 'Home': return mod > 1 ? csi(1, 'H') : (app ? '\x1bOH' : '\x1b[H');
      case 'End': return mod > 1 ? csi(1, 'F') : (app ? '\x1bOF' : '\x1b[F');
      case 'PageUp': return csi(5, '~');
      case 'PageDown': return csi(6, '~');
      case 'Insert': return csi(2, '~');
      case 'Delete': return csi(3, '~');
      case 'F1': return mod > 1 ? csi(1, 'P') : '\x1bOP';
      case 'F2': return mod > 1 ? csi(1, 'Q') : '\x1bOQ';
      case 'F3': return mod > 1 ? csi(1, 'R') : '\x1bOR';
      case 'F4': return mod > 1 ? csi(1, 'S') : '\x1bOS';
      case 'F5': return csi(15, '~');
      case 'F6': return csi(17, '~');
      case 'F7': return csi(18, '~');
      case 'F8': return csi(19, '~');
      case 'F9': return csi(20, '~');
      case 'F10': return csi(21, '~');
      case 'F11': return csi(23, '~');
      case 'F12': return csi(24, '~');
      case 'Enter': return alt ? '\x1b\r' : '\r';
      case 'Backspace': return ctrl ? '\x08' : (alt ? '\x1b\x7f' : '\x7f');
      case 'Tab': return shift ? '\x1b[Z' : (alt ? '\x1b\t' : '\t');
      case 'Escape': return '\x1b';
    }
    if (key.length === 1 || (key.length === 2 && key.codePointAt(0) > 0xffff)) {
      if (ctrl && !alt) {
        const c = key.toLowerCase().charCodeAt(0);
        if (c >= 0x61 && c <= 0x7a) return String.fromCharCode(c & 0x1f);
        if (key === ' ' || key === '@') return '\x00';
        if (key === '[') return '\x1b';
        if (key === '\\') return '\x1c';
        if (key === ']') return '\x1d';
        if (key === '^' || key === '6') return '\x1e';
        if (key === '_' || key === '-' || key === '/') return '\x1f';
        return null;
      }
      if (alt && !ctrl) return '\x1b' + key;
      if (alt && ctrl) {
        const c = key.toLowerCase().charCodeAt(0);
        if (c >= 0x61 && c <= 0x7a) return '\x1b' + String.fromCharCode(c & 0x1f);
      }
    }
    return null;
  }

  // paste wraps pasted text for bracketed paste mode and normalises newlines.
  pasteSeq(text) {
    text = text.replace(/\r\n|\n/g, '\r');
    if (this.mode.bracketed) text = text.replace(/\x1b\[20[01]~/g, '');
    return this.mode.bracketed ? '\x1b[200~' + text + '\x1b[201~' : text;
  }

  // mouseSeq encodes a mouse event for the host, or returns null when mouse
  // reporting is off. type: 'down' | 'up' | 'move' | 'wheel'; button 0..2,
  // for wheel -1 up / +1 down; x, y are 0-based cells.
  mouseSeq(type, button, x, y, mods) {
    const m = this.mode;
    if (!m.mouse) return null;
    if (type === 'move' && m.mouse === 1000) return null;
    let b;
    if (type === 'wheel') b = button < 0 ? 64 : 65;
    else if (type === 'up' && !m.sgrMouse) b = 3;
    else b = button;
    if (mods) b += (mods.shift ? 4 : 0) + (mods.alt ? 8 : 0) + (mods.ctrl ? 16 : 0);
    if (type === 'move') b += 32;
    x = Math.max(0, x) + 1; y = Math.max(0, y) + 1;
    if (m.sgrMouse) return `\x1b[<${b};${x};${y}${type === 'up' ? 'm' : 'M'}`;
    if (x > 223 || y > 223) return null;
    return '\x1b[M' + String.fromCharCode(32 + b, 32 + x, 32 + y);
  }
}

const api = {Terminal, Row, Cell, cwidth, BOLD, DIM, ITALIC, UNDERLINE, BLINK, INVERSE, HIDDEN, STRIKE, RGB};
if (typeof module !== 'undefined' && module.exports) module.exports = api;
else root.TermCore = api;
})(typeof self !== 'undefined' ? self : this);
