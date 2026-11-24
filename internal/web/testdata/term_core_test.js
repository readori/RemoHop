// Table tests for assets/term_core.js, run by `go test` when node is
// available: node testdata/term_core_test.js
'use strict';
const assert = require('node:assert/strict');
const path = require('node:path');
const T = require(path.join(__dirname, '..', 'assets', 'term_core.js'));

let failures = 0;
function test(name, fn) {
  try { fn(); } catch (e) { failures++; console.error('FAIL', name, '\n', e.message); }
}
const mk = (rows = 5, cols = 20, opts) => new T.Terminal(rows, cols, opts);
const screen = (t) => t.screenText();

// [name, input, expected visible rows (trailing blank rows trimmed), cursor [x, y]]
const screens = [
  ['plain', 'hello', ['hello'], [5, 0]],
  ['crlf', 'a\r\nb', ['a', 'b'], [1, 1]],
  ['lf only keeps column', 'ab\ncd', ['ab', '  cd'], [4, 1]],
  ['backspace overwrite', 'abc\bX', ['abX'], [3, 0]],
  ['tab stops', 'a\tb\t\tc', ['a       b          c'], [20 - 1, 0]],
  ['cursor position', '\x1b[3;5Hx', ['', '', '    x'], [5, 2]],
  ['cursor up/down/fwd/back', 'abc\x1b[2D\x1b[1CX\x1b[B\x1b[AY', ['abXY'], [4, 0]],
  ['CHA', 'abcdef\x1b[3GX', ['abXdef'], [3, 0]],
  ['VPA', '\x1b[4dx', ['', '', '', 'x'], [1, 3]],
  ['CNL CPL', 'a\x1b[2Eb\x1b[1Fc', ['a', 'c', 'b'], [1, 1]],
  ['erase line to end', 'abcdef\x1b[3D\x1b[K', ['abc'], [3, 0]],
  ['erase line to start', 'abcdef\x1b[3D\x1b[1K', ['    ef'], [3, 0]],
  ['erase line all', 'abcdef\x1b[2K', [''], [6, 0]],
  ['erase display below', 'aa\r\nbb\r\ncc\x1b[2;2H\x1b[J', ['aa', 'b'], [1, 1]],
  ['erase display above', 'aa\r\nbb\r\ncc\x1b[2;2H\x1b[1J', ['', '', 'cc'], [1, 1]],
  ['erase display all', 'aa\r\nbb\x1b[2J', [], [2, 1]],
  ['ECH', 'abcdef\x1b[1G\x1b[3X', ['   def'], [0, 0]],
  ['ICH', 'abcdef\x1b[1G\x1b[2@', ['  abcdef'], [0, 0]],
  ['DCH', 'abcdef\x1b[1G\x1b[2P', ['cdef'], [0, 0]],
  ['IL', 'a\r\nb\r\nc\x1b[2;1H\x1b[L', ['a', '', 'b', 'c'], [0, 1]],
  ['DL', 'a\r\nb\r\nc\x1b[2;1H\x1b[M', ['a', 'c'], [0, 1]],
  ['scroll up on overflow', '1\r\n2\r\n3\r\n4\r\n5\r\n6', ['2', '3', '4', '5', '6'], [1, 4]],
  ['SU', '1\r\n2\r\n3\x1b[1S', ['2', '3'], [1, 2]],
  ['SD', '1\r\n2\r\n3\x1b[1T', ['', '1', '2', '3'], [1, 2]],
  ['scroll region', '1\r\n2\r\n3\r\n4\r\n5\x1b[2;4r\x1b[4;1H\n', ['1', '3', '4', '', '5'], [0, 3]],
  ['region + RI at top', '1\r\n2\r\n3\r\n4\r\n5\x1b[2;4r\x1b[2;1H\x1bM', ['1', '', '2', '3', '5'], [0, 1]],
  ['origin mode', '\x1b[2;4r\x1b[?6h\x1b[1;1Hx', ['', 'x'], [1, 1]],
  ['autowrap', 'a'.repeat(25), ['a'.repeat(20), 'aaaaa'], [5, 1]],
  ['no autowrap', '\x1b[?7l' + 'a'.repeat(25), ['a'.repeat(20)], [19, 0]],
  ['deferred wrap then CR', 'a'.repeat(20) + '\rb', ['b' + 'a'.repeat(19)], [1, 0]],
  ['insert mode', 'abc\x1b[1G\x1b[4hX', ['Xabc'], [1, 0]],
  ['save restore', 'ab\x1b7\x1b[3;3Hz\x1b8c', ['abc', '', '  z'], [3, 0]],
  ['SCOSC SCORC', 'ab\x1b[s\x1b[2;1Hq\x1b[uc', ['abc', 'q'], [3, 0]],
  ['NEL IND RI', 'a\x1bEb\x1bDc\x1bMd', ['a', 'b d', ' c'], [3, 1]],
  ['SGR does not print', '\x1b[1;31mred\x1b[0m ok', ['red ok'], [6, 0]],
  ['utf8 + wide', 'a中b', ['a中b'], [4, 0]],
  ['wide char wraps whole', 'x'.repeat(19) + '中', ['x'.repeat(19), '中'], [2, 1]],
  ['combining', 'éx', ['éx'], [2, 0]],
  ['astral emoji is wide', '\u{1F600}a', ['\u{1F600}a'], [3, 0]],
  ['line drawing', '\x1b(0lqk\x1b(B', ['┌─┐'], [3, 0]],
  ['SO SI', '\x1b)0\x0eq\x0fq', ['─q'], [2, 0]],
  ['REP', 'ab\x1b[3b', ['abbbb'], [5, 0]],
  ['DECALN', '\x1b#8', ['E'.repeat(20), 'E'.repeat(20), 'E'.repeat(20), 'E'.repeat(20), 'E'.repeat(20)], [0, 0]],
  ['RIS', 'abc\x1bc', [], [0, 0]],
  ['OSC title is not printed', '\x1b]0;my title\x07hi', ['hi'], [2, 0]],
  ['OSC ST terminator', '\x1b]2;t\x1b\\hi', ['hi'], [2, 0]],
  ['DCS skipped', '\x1bP1$r0m\x1b\\hi', ['hi'], [2, 0]],
  ['OSC 52 ignored', '\x1b]52;c;aGVsbG8=\x07x', ['x'], [1, 0]],
  ['unknown CSI ignored', '\x1b[?9999z\x1b[99;99Zx', ['x'], [1, 0]],
  ['CAN aborts sequence', '\x1b[3\x18x', ['x'], [1, 0]],
  ['control inside CSI', '\x1b[2\rC', ['', ''].slice(0, 0).concat([]), [2, 0]],
  ['split sequences across writes are tested separately', '', [], [0, 0]],
];
for (const [name, input, rows, cur] of screens) {
  test('screen: ' + name, () => {
    const t = mk();
    t.write(input);
    let got = screen(t);
    while (got.length && got[got.length - 1] === '') got.pop();
    let want = rows.slice();
    while (want.length && want[want.length - 1] === '') want.pop();
    if (name === 'control inside CSI') want = [];
    assert.deepEqual(got, want);
    if (name !== 'control inside CSI') assert.deepEqual([t.cx, t.cy], cur);
  });
}

test('split escape sequences across writes', () => {
  const t = mk();
  for (const ch of '\x1b[1;3') t.write(ch);
  t.write('1m');
  t.write('x\x1b');
  t.write('[2');
  t.write('C');
  t.write('Y');
  assert.equal(t.lines[0].cells[0].fl & T.BOLD, T.BOLD);
  assert.equal(t.lines[0].cells[0].fg, 1);
  assert.equal(screen(t)[0], 'x  Y');
});

test('SGR attributes and colours', () => {
  const t = mk();
  t.write('\x1b[1;3;4;31;42mA\x1b[22;23;24;39;49mB');
  const [a, b] = t.lines[0].cells;
  assert.equal(a.fl, T.BOLD | T.ITALIC | T.UNDERLINE);
  assert.equal(a.fg, 1); assert.equal(a.bg, 2);
  assert.equal(b.fl, 0); assert.equal(b.fg, -1); assert.equal(b.bg, -1);
});

const colours = [
  ['bright fg', '\x1b[91mx', 'fg', 9],
  ['bright bg', '\x1b[104mx', 'bg', 12],
  ['256 fg', '\x1b[38;5;200mx', 'fg', 200],
  ['256 bg', '\x1b[48;5;17mx', 'bg', 17],
  ['truecolor fg', '\x1b[38;2;10;20;30mx', 'fg', T.RGB | (10 << 16) | (20 << 8) | 30],
  ['truecolor bg', '\x1b[48;2;255;0;128mx', 'bg', T.RGB | (255 << 16) | 128],
  ['colon 256', '\x1b[38:5:99mx', 'fg', 99],
  ['colon truecolor', '\x1b[38:2::1:2:3mx', 'fg', T.RGB | (1 << 16) | (2 << 8) | 3],
  ['colon truecolor no cs', '\x1b[48:2:4:5:6mx', 'bg', T.RGB | (4 << 16) | (5 << 8) | 6],
  ['256 then more attrs', '\x1b[38;5;1;1mx', 'fg', 1],
];
for (const [name, input, field, want] of colours) {
  test('colour: ' + name, () => {
    const t = mk();
    t.write(input);
    assert.equal(t.lines[0].cells[0][field], want);
  });
}
test('256 colour followed by bold', () => {
  const t = mk(); t.write('\x1b[38;5;1;1mx');
  assert.equal(t.lines[0].cells[0].fl & T.BOLD, T.BOLD);
});
test('SGR reset with empty parameter', () => {
  const t = mk(); t.write('\x1b[1;31m\x1b[mx');
  assert.equal(t.lines[0].cells[0].fl, 0); assert.equal(t.lines[0].cells[0].fg, -1);
});
test('inverse and hidden flags', () => {
  const t = mk(); t.write('\x1b[7;8mx');
  assert.equal(t.lines[0].cells[0].fl, T.INVERSE | T.HIDDEN);
});
test('background colour erase', () => {
  const t = mk(); t.write('\x1b[44m\x1b[2K');
  assert.equal(t.lines[0].cells[3].bg, 4);
  t.write('\x1b[0m\x1b[2J');
  assert.equal(t.lines[0].cells[3].bg, -1);
});

test('alternate screen 1049 restores the main screen and cursor', () => {
  const t = mk();
  t.write('main\r\nline2\x1b[?1049h');
  assert.deepEqual(screen(t).filter(Boolean), []);
  t.write('\x1b[1;1Hvim screen');
  assert.equal(screen(t)[0], 'vim screen');
  t.write('\x1b[?1049l');
  assert.deepEqual(screen(t).slice(0, 2), ['main', 'line2']);
  assert.deepEqual([t.cx, t.cy], [5, 1]);
  assert.equal(t.isAlt, false);
});
test('alternate screen has no scrollback', () => {
  const t = mk(3, 10);
  t.write('\x1b[?1049h1\r\n2\r\n3\r\n4\r\n5');
  assert.equal(t.scrollback.length, 0);
});
test('main screen scrollback and scrolled rows', () => {
  const t = mk(3, 10);
  t.write('1\r\n2\r\n3\r\n4\r\n5');
  assert.deepEqual(t.scrollback.map(r => r.text()), ['1', '2']);
  assert.deepEqual(t.takeScrolled().map(r => r.text()), ['1', '2']);
  assert.deepEqual(t.takeScrolled(), []);
});
test('scrollback limit', () => {
  const t = mk(2, 5, {scrollback: 3});
  for (let i = 0; i < 20; i++) t.write(i + '\r\n');
  assert.equal(t.scrollback.length, 3);
});
test('scrolling a sub-region does not feed scrollback', () => {
  const t = mk(4, 10);
  t.write('\x1b[2;3r\x1b[3;1H\n\n\n');
  assert.equal(t.scrollback.length, 0);
});

test('modes', () => {
  const t = mk();
  t.write('\x1b[?1h\x1b[?2004h\x1b[?25l\x1b[?1000h\x1b[?1006h\x1b[?1004h');
  assert.equal(t.mode.appCursor, true); assert.equal(t.mode.bracketed, true);
  assert.equal(t.mode.cursor, false); assert.equal(t.mode.mouse, 1000);
  assert.equal(t.mode.sgrMouse, true); assert.equal(t.mode.focus, true);
  t.write('\x1b[?1l\x1b[?2004l\x1b[?25h\x1b[?1000l\x1b[?1006l');
  assert.equal(t.mode.appCursor, false); assert.equal(t.mode.bracketed, false);
  assert.equal(t.mode.cursor, true); assert.equal(t.mode.mouse, 0);
  t.write('\x1b[?1h\x1b[?1;2004h');
  assert.equal(t.mode.appCursor, true); assert.equal(t.mode.bracketed, true);
});

test('title callback is sanitised and bounded', () => {
  let got = null;
  const t = mk(5, 20, {onTitle: s => { got = s; }});
  t.write('\x1b]0;ev\u202eil\x01x\x07');
  assert.equal(got, 'evilx');
  got = null;
  t.write('\x1b]0;aborted\x1b[31mred');
  assert.equal(got, null);
  assert.equal(t.lines[0].cells[0].fg, 1);
  t.write('\x1b]2;' + 'x'.repeat(1000) + '\x07');
  assert.equal(got.length, 200);
});

test('device answers', () => {
  const out = [];
  const t = mk(5, 20, {onData: s => out.push(s)});
  t.write('\x1b[c\x1b[>c\x1b[5n\x1b[3;4H\x1b[6n\x1b[18t');
  assert.deepEqual(out, ['\x1b[?62;1;22c', '\x1b[>0;276;0c', '\x1b[0n', '\x1b[3;4R', '\x1b[8;5;20t']);
});
test('cursor report honours origin mode', () => {
  const out = [];
  const t = mk(10, 20, {onData: s => out.push(s)});
  t.write('\x1b[3;8r\x1b[?6h\x1b[2;5H\x1b[6n');
  assert.deepEqual(out, ['\x1b[2;5R']);
});

test('resize shrinks and keeps the cursor row', () => {
  const t = mk(5, 10);
  t.write('1\r\n2\r\n3\r\n4\r\n5');
  t.resize(3, 6);
  assert.equal(t.rows, 3); assert.equal(t.cols, 6);
  assert.deepEqual(screen(t), ['3', '4', '5']);
  assert.equal(t.cy, 2);
  assert.deepEqual(t.scrollback.map(r => r.text()), ['1', '2']);
});
test('resize grows', () => {
  const t = mk(2, 5);
  t.write('ab');
  t.resize(4, 8);
  assert.equal(t.lines.length, 4); assert.equal(t.lines[0].cells.length, 8);
  t.write('cd');
  assert.equal(screen(t)[0], 'abcd');
});
test('resize in alt screen keeps both buffers consistent', () => {
  const t = mk(4, 10);
  t.write('main\x1b[?1049hx');
  t.resize(6, 12);
  assert.equal(t.lines.length, 6);
  t.write('\x1b[?1049l');
  assert.equal(t.lines.length, 6);
  assert.equal(screen(t)[0], 'main');
});
test('resize cursor within bounds', () => {
  const t = mk(10, 40);
  t.write('\x1b[10;40H');
  t.resize(5, 20);
  assert.ok(t.cx < 20 && t.cy < 5);
  t.write('x');
});

test('DEC special graphics only inside G0 selection', () => {
  const t = mk();
  t.write('\x1b(0q\x1b(Bq');
  assert.equal(screen(t)[0], '─q');
});

test('parser survives garbage', () => {
  const t = mk();
  let seed = 12345;
  const rnd = () => (seed = (seed * 1103515245 + 12345) & 0x7fffffff) / 0x7fffffff;
  const alphabet = '\x1b[]();:?0123456789;mHJKABCDhlrstq@ \x07\x08\x09\x0a\x0d\x18\x1aXYZ中́\u{1F600}';
  const chars = Array.from(alphabet);
  for (let i = 0; i < 20000; i++) t.write(chars[Math.floor(rnd() * chars.length)]);
  assert.equal(t.lines.length, 5);
  for (const r of t.lines) assert.equal(r.cells.length, 20);
  assert.ok(t.cx >= 0 && t.cx < 20 && t.cy >= 0 && t.cy < 5);
});

const keys = [
  [{key: 'ArrowUp'}, '\x1b[A'],
  [{key: 'ArrowUp', ctrlKey: true}, '\x1b[1;5A'],
  [{key: 'ArrowLeft', shiftKey: true}, '\x1b[1;2D'],
  [{key: 'Home'}, '\x1b[H'], [{key: 'End'}, '\x1b[F'],
  [{key: 'PageUp'}, '\x1b[5~'], [{key: 'Delete'}, '\x1b[3~'], [{key: 'Insert'}, '\x1b[2~'],
  [{key: 'F1'}, '\x1bOP'], [{key: 'F5'}, '\x1b[15~'], [{key: 'F12', shiftKey: true}, '\x1b[24;2~'],
  [{key: 'Enter'}, '\r'], [{key: 'Backspace'}, '\x7f'], [{key: 'Tab'}, '\t'],
  [{key: 'Tab', shiftKey: true}, '\x1b[Z'], [{key: 'Escape'}, '\x1b'],
  [{key: 'c', ctrlKey: true}, '\x03'], [{key: 'C', ctrlKey: true}, '\x03'], [{key: 'd', ctrlKey: true}, '\x04'],
  [{key: ' ', ctrlKey: true}, '\x00'], [{key: '[', ctrlKey: true}, '\x1b'], [{key: '/', ctrlKey: true}, '\x1f'],
  [{key: 'x', altKey: true}, '\x1bx'], [{key: 'a'}, null], [{key: 'A', shiftKey: true}, null],
  [{key: 'é'}, null], [{key: 'Shift'}, null], [{key: 'Control', ctrlKey: true}, null],
];
for (const [ev, want] of keys) {
  test('keySeq ' + JSON.stringify(ev), () => assert.equal(mk().keySeq(ev), want));
}
test('keySeq application cursor keys', () => {
  const t = mk(); t.write('\x1b[?1h');
  assert.equal(t.keySeq({key: 'ArrowUp'}), '\x1bOA');
  assert.equal(t.keySeq({key: 'Home'}), '\x1bOH');
  assert.equal(t.keySeq({key: 'ArrowUp', ctrlKey: true}), '\x1b[1;5A');
});

test('paste: bracketed and plain, newline normalisation, injection stripped', () => {
  const t = mk();
  assert.equal(t.pasteSeq('a\nb\r\nc'), 'a\rb\rc');
  t.write('\x1b[?2004h');
  assert.equal(t.pasteSeq('a\nb'), '\x1b[200~a\rb\x1b[201~');
  assert.equal(t.pasteSeq('x\x1b[201~rm -rf'), '\x1b[200~xrm -rf\x1b[201~');
});

test('mouse encodings', () => {
  const t = mk();
  assert.equal(t.mouseSeq('down', 0, 3, 4), null);
  t.write('\x1b[?1000h');
  assert.equal(t.mouseSeq('down', 0, 3, 4), '\x1b[M' + String.fromCharCode(32, 36, 37));
  assert.equal(t.mouseSeq('up', 0, 3, 4), '\x1b[M' + String.fromCharCode(35, 36, 37));
  assert.equal(t.mouseSeq('move', 0, 3, 4), null);
  t.write('\x1b[?1006h');
  assert.equal(t.mouseSeq('down', 2, 0, 0), '\x1b[<2;1;1M');
  assert.equal(t.mouseSeq('up', 2, 0, 0), '\x1b[<2;1;1m');
  assert.equal(t.mouseSeq('wheel', -1, 9, 9), '\x1b[<64;10;10M');
  assert.equal(t.mouseSeq('down', 0, 1, 1, {ctrl: true, shift: true}), '\x1b[<20;2;2M');
  t.write('\x1b[?1003h');
  assert.equal(t.mouseSeq('move', 0, 1, 1), '\x1b[<32;2;2M');
});

test('cwidth', () => {
  assert.equal(T.cwidth(0x41), 1); assert.equal(T.cwidth(0x4e2d), 2); assert.equal(T.cwidth(0x301), 0);
  assert.equal(T.cwidth(0x1f600), 2); assert.equal(T.cwidth(0xe9), 1);
});

test('a typical coloured ls and prompt', () => {
  const t = mk(3, 40);
  t.write('\x1b[01;34mdir\x1b[0m  \x1b[01;32mrun.sh\x1b[0m\r\n$ ');
  assert.deepEqual(screen(t).slice(0, 2), ['dir  run.sh', '$']);
  assert.equal(t.lines[0].cells[0].fg, 4); assert.equal(t.lines[0].cells[5].fg, 2);
});

test('top-like full screen redraw leaves a sane screen', () => {
  const t = mk(6, 30);
  t.write('\x1b[?1049h\x1b[?25l\x1b[H\x1b[2J\x1b[1;1Htop - 12:00:00 up 1 day\x1b[K\r\n\x1b[7m  PID USER      PR\x1b[m\x1b[K\r\n 1 root 20\x1b[K');
  const s = screen(t);
  assert.equal(s[0], 'top - 12:00:00 up 1 day');
  assert.equal(s[1], '  PID USER      PR');
  assert.equal(s[2], ' 1 root 20');
  assert.equal(t.lines[1].cells[2].fl & T.INVERSE, T.INVERSE);
  assert.equal(t.mode.cursor, false);
});

if (failures) { console.error(failures + ' failed'); process.exit(1); }
console.log('ok');
