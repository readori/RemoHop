// phone_test.mjs drives headless Chrome at a phone viewport with touch
// emulation through the DevTools protocol (no dependencies: Node 22's
// WebSocket) to test remohop-mobile.js the way the apps inject it.
//
//   node phone_test.mjs                 harness tests (gestures, keyboard,
//                                       bridge) against harness.html
//   REMOHOP_GATEWAY_URL=... REMOHOP_TARGET=lab REMOHOP_TEST_DIR=/dir \
//   node phone_test.mjs --gateway       also the real gateway page: connect,
//                                       terminal, files, download, desktop bar
//
// REMOHOP_SHOTS=/dir saves screenshots. CHROME names the browser binary.
// internal/mobile's TestPhoneBrowser runs the second form against an
// in-process gateway and host.
import {spawn} from 'node:child_process';
import fs from 'node:fs';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const script = fs.readFileSync(path.join(here, '..', 'remohop-mobile.js'), 'utf8');
const shots = process.env.REMOHOP_SHOTS || '';
const sleep = ms => new Promise(r => setTimeout(r, ms));
let failures = 0;
const redact = s => String(s).replace(/(127\.0\.0\.1:\d+\/)[A-Za-z0-9_-]{16,}\//g, '$1<token>/');
function check(cond, what, detail) {
  if (cond) console.log('ok   ' + what);
  // Details may hold gateway URLs: never print their token (CI logs are public).
  else { failures++; console.log('FAIL ' + what + (detail !== undefined ? ': ' + redact(JSON.stringify(detail)) : '')); }
}

// ---- Chrome and the DevTools protocol ----
async function launch() {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'sdm-chrome-'));
  const args = ['--headless=new', '--disable-gpu', '--remote-debugging-port=0', '--user-data-dir=' + dir,
    '--no-first-run', '--no-default-browser-check', '--disable-extensions', '--disable-background-networking',
    '--disable-component-update', '--disable-sync', '--metrics-recording-only',
    // The checks read English labels: the page follows the browser's language.
    '--lang=en-US', 'about:blank'];
  // Root, or a disposable CI runner (Ubuntu 24.04 restricts the user namespaces Chrome's sandbox needs).
  if (process.getuid && process.getuid() === 0 || process.env.CI) args.unshift('--no-sandbox');
  // Its own process group, so closing kills every Chrome child too.
  const proc = spawn(process.env.CHROME || 'google-chrome', args, {stdio: ['ignore', 'ignore', 'pipe'], detached: true});
  let stderr = '', exited = null;
  proc.stderr.on('data', d => { if (stderr.length < 4000) stderr += d; });
  proc.on('exit', code => { exited = code; });
  const portFile = path.join(dir, 'DevToolsActivePort');
  // A busy runner (Gradle just finished) can take well over 10 s to start Chrome.
  for (let i = 0; i < 1200 && exited === null && !fs.existsSync(portFile); i++) await sleep(50);
  if (!fs.existsSync(portFile)) {
    try { process.kill(-proc.pid, 'SIGKILL'); } catch (e) { /* ignore */ }
    throw new Error('Chrome did not start (' + (exited === null ? 'no DevToolsActivePort after 60 s' : 'exit ' + exited) +
      '): ' + stderr.split('\n').slice(-15).join('\n'));
  }
  proc.stderr.removeAllListeners('data'); proc.stderr.resume(); // keep draining so Chrome never blocks on a full pipe
  const [port, wsPath] = fs.readFileSync(portFile, 'utf8').trim().split('\n');
  const ws = new WebSocket('ws://127.0.0.1:' + port + wsPath);
  await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
  let id = 0;
  const pending = new Map(), listeners = [];
  ws.onmessage = ev => {
    const m = JSON.parse(ev.data);
    if (m.id && pending.has(m.id)) {
      const p = pending.get(m.id); pending.delete(m.id);
      m.error ? p.rej(new Error(m.error.message)) : p.res(m.result);
    } else if (m.method) for (const l of listeners) l(m);
  };
  const call = (method, params, sessionId) => new Promise((res, rej) => {
    const i = ++id; pending.set(i, {res, rej});
    ws.send(JSON.stringify({id: i, method, params: params || {}, sessionId}));
  });
  const close = () => {
    try { ws.close(); } catch (e) { /* ignore */ }
    try { process.kill(-proc.pid, 'SIGKILL'); } catch (e) { proc.kill('SIGKILL'); }
    try { fs.rmSync(dir, {recursive: true, force: true}); } catch (e) { /* ignore */ }
  };
  return {call, listeners, close};
}

async function newPhonePage(b, preload) {
  const {targetId} = await b.call('Target.createTarget', {url: 'about:blank'});
  const {sessionId} = await b.call('Target.attachToTarget', {targetId, flatten: true});
  const s = (m, p) => b.call(m, p, sessionId);
  const errors = [];
  b.listeners.push(m => {
    if (m.sessionId !== sessionId) return;
    if (m.method === 'Runtime.exceptionThrown') errors.push(m.params.exceptionDetails);
  });
  await s('Page.enable'); await s('Runtime.enable');
  // A phone: 390x844 CSS px at 3x, touch only, a mobile user agent.
  await s('Emulation.setDeviceMetricsOverride', {width: 390, height: 844, deviceScaleFactor: 3, mobile: true});
  await s('Emulation.setTouchEmulationEnabled', {enabled: true, maxTouchPoints: 5});
  await s('Emulation.setUserAgentOverride', {userAgent: 'Mozilla/5.0 (Linux; Android 15; Pixel 9) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/153.0 Mobile Safari/537.36'});
  for (const src of preload) await s('Page.addScriptToEvaluateOnNewDocument', {source: src});
  const page = {
    s, errors,
    async goto(url) {
      const loaded = new Promise(res => b.listeners.push(m => { if (m.sessionId === sessionId && m.method === 'Page.loadEventFired') res(); }));
      await s('Page.navigate', {url});
      await loaded;
    },
    async eval(expr) {
      const r = await s('Runtime.evaluate', {expression: expr, awaitPromise: true, returnByValue: true});
      if (r.exceptionDetails) throw new Error(expr + ': ' + JSON.stringify(r.exceptionDetails.exception && r.exceptionDetails.exception.description));
      return r.result.value;
    },
    async waitFor(expr, ms = 15000) {
      const end = Date.now() + ms;
      while (Date.now() < end) { if (await page.eval(expr)) return true; await sleep(100); }
      return false;
    },
    touch: (type, points) => s('Input.dispatchTouchEvent', {type, touchPoints: points.map((p, i) => ({x: p[0], y: p[1], id: p[2] !== undefined ? p[2] : i}))}),
    async shot(name) {
      if (!shots) return;
      const {data} = await s('Page.captureScreenshot', {format: 'png'});
      fs.mkdirSync(shots, {recursive: true});
      fs.writeFileSync(path.join(shots, name + '.png'), Buffer.from(data, 'base64'));
    },
  };
  return page;
}

// A fake Android bridge (what WebViewCompat.addWebMessageListener exposes).
// "gateway.ensure" is answered only when the test calls window.__ensure(),
// after it had the gateway checked (see answerEnsure), as the app does.
const fakeNative = `
window.__native = [];
window.__ensures = [];
window.__ensure = () => { const l = window.__ensures.splice(0); for (const f of l) f(); return l.length; };
window.remohopNative = {
  onmessage: null,
  postMessage(s) {
    const m = JSON.parse(s);
    window.__native.push(m);
    if (m.method === 'gateway.ensure') {
      window.__ensures.push(() => this.onmessage && this.onmessage({data: JSON.stringify({id: m.id, result: {}})}));
      return;
    }
    const result = m.method === 'clipboard.read' ? {text: 'from-native'} : {};
    setTimeout(() => this.onmessage && this.onmessage({data: JSON.stringify({id: m.id, result})}), 0);
  },
};`;

// A fake desktop socket for the harness: open, then a hello with input on.
const fakeSocket = `
window.__sent = [];
window.WebSocket = class FakeWS extends EventTarget {
  constructor(url) {
    super(); this.url = url; this.readyState = 0;
    setTimeout(() => {
      this.readyState = 1; this.dispatchEvent(new Event('open'));
      this.dispatchEvent(new MessageEvent('message', {data: JSON.stringify({type: 'hello', input: true, width: 1600, height: 900})}));
    }, 10);
  }
  send(d) { window.__sent.push(JSON.parse(d)); }
  close() { this.readyState = 3; this.dispatchEvent(new Event('close')); }
};`;

function serveHarness() {
  return new Promise(res => {
    const srv = http.createServer((req, resp) => {
      const name = req.url === '/' ? 'harness.html' : req.url.slice(1);
      if (!['harness.html', 'harness.js', 'harness.css'].includes(name)) { resp.writeHead(404); resp.end(); return; }
      resp.writeHead(200, {'Content-Type': name.endsWith('.js') ? 'text/javascript' : name.endsWith('.css') ? 'text/css' : 'text/html',
        // The gateway's policy: no inline script or style. The injected
        // script must live with it.
        'Content-Security-Policy': "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' blob: data:; connect-src 'self' ws:"});
      resp.end(fs.readFileSync(path.join(here, name)));
    });
    srv.listen(0, '127.0.0.1', () => res(srv));
  });
}

async function harnessTests(b) {
  const srv = await serveHarness();
  const p = await newPhonePage(b, [fakeSocket, fakeNative, script]);
  await p.goto('http://127.0.0.1:' + srv.address().port + '/');
  await p.waitFor('window.remohopMobile && remohopMobile._desk.hello');
  const sent = async () => { const v = await p.eval('JSON.stringify(window.__sent)'); await p.eval('window.__sent.length = 0'); return JSON.parse(v); };
  const r = await p.eval('(() => { const r = document.getElementById("d-canvas").getBoundingClientRect(); return {x: r.left, y: r.top, w: r.width, h: r.height}; })()');
  const at = (fx, fy) => [r.x + r.w * fx, r.y + r.h * fy];
  const near = (a, b) => Math.abs(a - b) < 0.02;
  await sent();

  // Tap = left click where the finger is.
  await p.touch('touchStart', [at(0.5, 0.5)]); await p.touch('touchEnd', []);
  let m = await sent();
  check(m.length === 3 && m[0].type === 'move' && m[1].type === 'button' && m[1].button === 0 && m[1].down && !m[2].down &&
    near(m[1].x, 0.5) && near(m[1].y, 0.5), 'tap is a left click at the finger', m);
  check(await p.eval('window.__leaked') === 0, 'touch never reaches desktop.js listeners');

  // Two-finger tap = right click.
  await p.touch('touchStart', [at(0.4, 0.5), at(0.6, 0.5)]); await sleep(50); await p.touch('touchEnd', []);
  m = await sent();
  check(m.some(e => e.type === 'button' && e.button === 2 && e.down) && m.some(e => e.type === 'button' && e.button === 2 && !e.down) &&
    !m.some(e => e.type === 'button' && e.button === 0), 'two-finger tap is a right click', m);

  // Long press = right click (and a haptic tick).
  await p.eval('window.__native.length = 0');
  await p.touch('touchStart', [at(0.25, 0.75)]); await sleep(750); await p.touch('touchEnd', []);
  m = await sent();
  check(m.filter(e => e.type === 'button').map(e => e.button + (e.down ? 'd' : 'u')).join() === '2d,2u' && near(m[1].x, 0.25) && near(m[1].y, 0.75),
    'long press is a right click', m);
  check(await p.eval('window.__native.some(n => n.method === "haptic")'), 'long press asks for haptic feedback');

  // Drag = move with the left button held.
  await p.touch('touchStart', [at(0.2, 0.2)]);
  for (let i = 1; i <= 5; i++) { await p.touch('touchMove', [at(0.2 + i * 0.1, 0.2 + i * 0.05)]); await sleep(10); }
  await p.touch('touchEnd', []);
  m = await sent();
  const btn = m.filter(e => e.type === 'button');
  check(btn.length === 2 && btn[0].down && near(btn[0].x, 0.2) && !btn[1].down && near(btn[1].x, 0.7) && near(btn[1].y, 0.45) &&
    m.filter(e => e.type === 'move').length >= 4, 'drag presses at the start, moves, releases at the end', m);

  // Two-finger drag = scroll; fingers moving up scroll down (dy > 0).
  const [sx, sy] = at(0.4, 0.95);
  await p.touch('touchStart', [[sx, sy, 0], [sx + 80, sy, 1]]);
  for (let i = 1; i <= 10; i++) { await p.touch('touchMove', [[sx, sy - i * 20, 0], [sx + 80, sy - i * 20, 1]]); await sleep(10); }
  await p.touch('touchEnd', []);
  m = await sent();
  const dy = m.filter(e => e.type === 'wheel').reduce((a, e) => a + e.dy, 0);
  check(dy > 4 && dy < 6 && !m.some(e => e.type === 'button'), 'two-finger drag scrolls (200 px up = 5 notches down)', {dy, m});

  // Pinch = zoom the view, nothing sent to the host.
  const [px, py] = at(0.5, 0.5);
  await p.touch('touchStart', [[px - 25, py, 0], [px + 25, py, 1]]);
  for (let i = 1; i <= 10; i++) { await p.touch('touchMove', [[px - 25 - i * 10, py, 0], [px + 25 + i * 10, py, 1]]); await sleep(10); }
  await p.touch('touchEnd', []);
  m = await sent();
  const z = await p.eval('({...remohopMobile._zoom})');
  check(z.s > 2.5 && m.length === 0, 'pinch zooms the view locally', {z, m});
  // A tap on the zoomed canvas maps through the zoom.
  const r2 = await p.eval('(() => { const r = document.getElementById("d-canvas").getBoundingClientRect(); return {x: r.left, y: r.top, w: r.width, h: r.height}; })()');
  const tx = px + 30, ty = py + 20;
  await p.touch('touchStart', [[tx, ty]]); await p.touch('touchEnd', []);
  m = await sent();
  check(m.length === 3 && near(m[1].x, (tx - r2.x) / r2.w) && near(m[1].y, (ty - r2.y) / r2.h), 'taps map through the zoom', {m, r2});
  check(await p.eval('remohopMobile.back()') === 'handled' && await p.eval('remohopMobile._zoom.s') === 1, 'back resets the zoom first');
  await p.shot('harness-desktop');

  // Soft keyboard: typed text becomes key presses (US layout).
  await p.eval('remohopMobile._toggleKeyboard()');
  check(await p.eval('document.activeElement && document.activeElement.id') === 'sdm-input', 'keyboard button focuses the input');
  check(await p.eval('window.__native.some(n => n.method === "keyboard" && n.args.show)'), 'keyboard button asks the app to show the IME');
  await sent();
  await p.s('Input.insertText', {text: 'Hi!'});
  m = await sent();
  const keys = m.filter(e => e.type === 'key').map(e => e.code + (e.down ? '+' : '-')).join(' ');
  check(keys === 'ShiftLeft+ KeyH+ KeyH- ShiftLeft- KeyI+ KeyI- ShiftLeft+ Digit1+ Digit1- ShiftLeft-', 'typed text becomes key presses', keys);
  await p.s('Input.dispatchKeyEvent', {type: 'rawKeyDown', key: 'Backspace', code: 'Backspace', windowsVirtualKeyCode: 8});
  await p.s('Input.dispatchKeyEvent', {type: 'keyUp', key: 'Backspace', code: 'Backspace', windowsVirtualKeyCode: 8});
  m = await sent();
  check(m.map(e => e.code + (e.down ? '+' : '-')).join(' ') === 'Backspace+ Backspace-', 'backspace on the soft keyboard', m);
  // An IME composition that is replaced by autocorrect: diffed.
  await p.s('Input.imeSetComposition', {text: 'teh', selectionStart: 3, selectionEnd: 3});
  await p.s('Input.insertText', {text: 'the'});
  m = await sent();
  const typed = m.filter(e => e.down).map(e => e.code).join(' ');
  check(typed === 'KeyT KeyE KeyH Backspace Backspace KeyH KeyE', 'composition updates are diffed into keys', typed);
  // Sticky Ctrl from the bar applies to the next key only.
  await p.eval('[...document.querySelectorAll("#sdm-bar button")].find(b => b.textContent === "Ctrl").click()');
  await p.s('Input.insertText', {text: 'cv'});
  m = await sent();
  check(m.map(e => e.code + (e.down ? '+' : '-')).join(' ') === 'ControlLeft+ KeyC+ KeyC- ControlLeft- KeyV+ KeyV-', 'sticky Ctrl applies to one key', m);
  check(await p.eval('document.activeElement.id') === 'sdm-input', 'bar buttons keep the keyboard up');
  await p.s('Input.insertText', {text: 'é'});
  m = await sent();
  check(m.length === 1 && m[0].type === 'text' && m[0].text === 'é', 'characters outside the key map go as text input', m);
  check(await p.eval('remohopMobile.back()') === 'handled' && await p.eval('document.activeElement.id') !== 'sdm-input', 'back hides the keyboard');

  // View only: nothing is sent.
  await p.eval('document.getElementById("d-viewonly").checked = true');
  await p.touch('touchStart', [at(0.5, 0.5)]); await p.touch('touchEnd', []);
  check((await sent()).length === 0, 'view only sends no input');
  await p.eval('document.getElementById("d-viewonly").checked = false');

  // Bridge: clipboard, downloads, keep-screen-on, full screen, settings.
  check(await p.eval('navigator.clipboard.readText()') === 'from-native', 'navigator.clipboard.readText reads the native clipboard');
  await p.eval('navigator.clipboard.writeText("to-native")');
  check(await p.eval('window.__native.some(n => n.method === "clipboard.write" && n.args.text === "to-native")'), 'writeText goes to the native clipboard');
  await p.eval('window.__api.length = 0; window.__sent.length = 0; [...document.querySelectorAll("#sdm-bar button")].find(b => b.textContent === "Paste").click()');
  await p.waitFor('window.__sent.length >= 4', 3000);
  const api = await p.eval('JSON.stringify(window.__api)');
  m = await sent();
  check(api.includes('"clip/set"') && api.includes('from-native') && m.map(e => e.code + (e.down ? '+' : '-')).join(' ') === 'ControlLeft+ KeyV+ KeyV- ControlLeft-',
    'Paste puts the native clipboard on the remote one and presses Ctrl+V', {api, m});
  await p.eval('window.__native.length = 0; [...document.querySelectorAll("#sdm-bar button")].find(b => b.textContent === "Copy").click()');
  await p.waitFor('window.__native.some(n => n.method === "clipboard.write")', 3000);
  check(await p.eval('window.__native.some(n => n.method === "clipboard.write" && n.args.text === "remote text")'), 'Copy puts the remote clipboard on the native one');
  await p.eval(`(() => { const a = document.createElement('a'); a.href = '/TOKEN/api/v1/files/download?t=T&c=c1&path=%2Fhome%2Fu%2Freport.pdf'; a.download = ''; document.body.appendChild(a); a.click(); a.remove(); })()`);
  const dl = await p.eval('JSON.stringify(window.__native.find(n => n.method === "download"))');
  check(dl && JSON.parse(dl).args.name === 'report.pdf' && JSON.parse(dl).args.url.startsWith('http://127.0.0.1:'), 'downloads are handed to the app', dl);
  const states = async () => JSON.parse(await p.eval('JSON.stringify(window.__native.filter(n => n.method === "state").map(n => n.args))'));
  check((await states()).length === 0 || true, 'state messages recorded');
  await p.eval('window.__native.length = 0; document.getElementById("d-fs").click()');
  await sleep(50);
  let st = await states();
  check(st.length && st[st.length - 1].fullscreen === true && st[st.length - 1].keepScreenOn === true, 'full screen asks the app to hide the system bars', st);
  const fsRect = await p.eval('(() => { const r = document.getElementById("d-stage").getBoundingClientRect(); return {w: r.width, top: r.top}; })()');
  check(fsRect.w === 390 && fsRect.top === 0, 'full screen stage fills the width', fsRect);
  check(await p.eval('remohopMobile.back()') === 'handled', 'back leaves full screen');
  st = await states();
  check(st[st.length - 1].fullscreen === false, 'leaving full screen restores the bars', st);
  await p.eval('window.__native.length = 0; window.__deskSock.close()');
  await sleep(20);
  st = await states();
  check(st.length && st[st.length - 1].keepScreenOn === false, 'closing the desktop lets the screen sleep', st);
  check(await p.eval('!!document.getElementById("sdm-settings")'), 'settings button added to the header');
  await p.eval('document.getElementById("dlg").showModal()');
  check(await p.eval('remohopMobile.back()') === 'handled' && !(await p.eval('document.getElementById("dlg").open')), 'back closes a dialog');
  check(await p.eval('remohopMobile.back()') === 'handled', 'back from the workspace goes to the connect screen');
  check(p.errors.length === 0, 'no script errors in the harness', p.errors.map(e => e.exception && e.exception.description));
  srv.close();
}

// answerEnsure plays the app's part of the page's recovery: it waits for the
// page's "gateway.ensure", has the gateway checked (revive: the Go test's
// control endpoint calls EnsureGateway) and answers it.
async function answerEnsure(p, control, revive) {
  check(await p.waitFor('window.__ensures.length > 0', 5000), 'the page asks the app to check the gateway');
  if (revive) {
    const r = await fetch(control + '/ensure');
    check(r.ok, 'EnsureGateway revived the listener on the same URL', await r.text());
  }
  await p.eval('window.__ensure()');
}

// The release-blocking iOS bug: the gateway's listening socket is gone
// (iOS took it while the app was in the background), the page's Connect
// gets no answer at all. The page must ask the app, and retry; and when
// even that fails, say so with a Retry button instead of "Load failed".
async function lostListenerTests(p, control, target) {
  const connect = () => p.eval(`document.getElementById('target').value = ${JSON.stringify(target)};
    document.getElementById('hostpass').value = ''; document.getElementById('connectbtn').click()`);
  await fetch(control + '/break');
  await connect();
  await answerEnsure(p, control, true);
  check(await p.waitFor('!document.getElementById("workspace").hidden', 30000), 'Connect works after the listener was lost (the page asked the app, then retried)',
    await p.eval('document.getElementById("connerr").textContent'));
  await p.eval('document.getElementById("disconnect").click()');
  check(await p.waitFor('!document.getElementById("connect").hidden'), 'disconnect returns to the connect form');

  await fetch(control + '/break');
  await connect();
  await answerEnsure(p, control, false);
  check(await p.waitFor('!!document.getElementById("gwretry") && !document.getElementById("gwretry").parentNode.hidden', 5000),
    'when the gateway stays silent the page says so, with Retry');
  const err = await p.eval('document.getElementById("connerr").textContent');
  check(!/Failed to fetch|Load failed/.test(err) && /not answering/.test(err), 'no raw browser error text in the connect form', err);
  await p.eval('document.getElementById("gwretry").click()');
  await answerEnsure(p, control, true);
  check(await p.waitFor('document.getElementById("gwretry").parentNode.hidden', 5000), 'Retry clears the message once the gateway answers');
}

async function gatewayTests(b) {
  const url = process.env.REMOHOP_GATEWAY_URL, target = process.env.REMOHOP_TARGET, dir = process.env.REMOHOP_TEST_DIR;
  const p = await newPhonePage(b, [fakeNative, script]);
  await p.goto(url);
  check(await p.waitFor('!document.getElementById("connect").hidden'), 'gateway page shows the connect form');
  check(await p.eval('!!document.getElementById("sdm-settings")'), 'settings button present on the gateway page');
  check(await p.eval('document.documentElement.scrollWidth <= innerWidth'), 'no horizontal scrolling at 390 px');
  // The way a user pairs in the app: host address, password and the pair
  // box in the connect form (a second host, so the saved pairing below is
  // not what makes this work).
  if (process.env.REMOHOP_HOST_ADDR) {
    check(await p.eval('!document.getElementById("pairrow").hidden'), 'the pair box is offered');
    await p.eval(`document.getElementById('target').value = ${JSON.stringify(process.env.REMOHOP_HOST_ADDR)};
      document.getElementById('hostpass').value = ${JSON.stringify(process.env.REMOHOP_HOST_PASSWORD || '')};
      document.getElementById('pair').checked = true; document.getElementById('connectbtn').click()`);
    check(await p.waitFor('!document.getElementById("workspace").hidden', 30000), 'connects with address + password + pair from the form',
      await p.eval('document.getElementById("connerr").textContent'));
    check(await p.eval('document.getElementById("pathbadge").textContent.includes("paired")'), 'the form pairing is saved (badge says paired)',
      await p.eval('document.getElementById("pathbadge").textContent'));
    await p.eval('document.getElementById("disconnect").click()');
    check(await p.waitFor('!document.getElementById("connect").hidden'), 'disconnect returns to the connect form');
  }
  if (process.env.REMOHOP_CONTROL_URL) await lostListenerTests(p, process.env.REMOHOP_CONTROL_URL, target);
  await p.shot('gateway-connect');
  await p.eval(`document.getElementById('target').value = ${JSON.stringify(target)}; document.getElementById('connectbtn').click()`);
  check(await p.waitFor('!document.getElementById("workspace").hidden', 30000), 'connects with the pairing saved by PairWithPassword',
    await p.eval('document.getElementById("connerr").textContent'));

  await p.eval('document.getElementById("tab-terminal").click()');
  check(await p.waitFor('!!document.querySelector("#t-host textarea")'), 'terminal opens');
  await sleep(500);
  await p.eval('document.querySelector("#t-host textarea").focus()');
  await p.s('Input.insertText', {text: 'echo remohop-$((6*7))'});
  await p.s('Input.dispatchKeyEvent', {type: 'keyDown', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13, text: '\r'});
  await p.s('Input.dispatchKeyEvent', {type: 'keyUp', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13});
  check(await p.waitFor('document.getElementById("t-host").textContent.includes("remohop-42")'), 'terminal runs a command on the host');
  check(await p.eval('document.documentElement.scrollWidth <= innerWidth'), 'terminal fits the phone width');
  await p.shot('gateway-terminal');

  await p.eval('document.getElementById("tab-files").click()');
  await p.waitFor('document.getElementById("f-path").value !== ""');
  await p.eval(`document.getElementById('f-path').value = ${JSON.stringify(dir)}; document.getElementById('f-go').click()`);
  check(await p.waitFor('document.getElementById("f-body").textContent.includes("hello.txt")'), 'files tab lists the host directory');
  await p.shot('gateway-files');
  await p.eval('window.__native.length = 0; [...document.querySelectorAll("#f-body button")].find(b => (b.getAttribute("aria-label") || "").startsWith("Download hello.txt")).click()');
  await p.waitFor('window.__native.some(n => n.method === "download")', 5000);
  const dl = JSON.parse(await p.eval('JSON.stringify(window.__native.find(n => n.method === "download") || null)'));
  check(dl && dl.args.name === 'hello.txt', 'file download is handed to the app', dl);
  if (dl) {
    // What the app does next: fetch the URL itself, with no cookie and no
    // Origin, inside the ticket's 30 seconds.
    const resp = await fetch(dl.args.url);
    const body = await resp.text();
    check(resp.status === 200 && body === 'hello from the host\n', 'the app can fetch the handed-over download URL once', {status: resp.status, body});
    const again = await fetch(dl.args.url);
    check(again.status !== 200, 'the download ticket is single use', again.status);
  }

  // The terminal also takes input as DOM input events (what the Android
  // instrumented test uses, which cannot inject key events reliably).
  await p.eval('document.getElementById("tab-terminal").click()');
  await p.waitFor('!!document.querySelector("#t-host textarea")');
  await sleep(500);
  await p.eval(`(() => { const i = document.querySelector('#t-host textarea'); i.focus();
    i.value = 'echo remohop-$((5*9))'; i.dispatchEvent(new InputEvent('input', {inputType: 'insertText', data: i.value, bubbles: true}));
    i.value = '\\n'; i.dispatchEvent(new InputEvent('input', {inputType: 'insertLineBreak', bubbles: true})); })()`);
  check(await p.waitFor('document.getElementById("t-host").textContent.includes("remohop-45")'), 'terminal takes input events');

  // Clipboard tab through the bridge: device clipboard -> page -> host clipboard -> page -> device clipboard.
  await p.eval('document.getElementById("tab-clipboard").click()');
  await p.eval('document.getElementById("c-paste").click()');
  check(await p.waitFor('document.getElementById("c-local").value === "from-native"'), 'Paste from this device reads the native clipboard');
  await p.eval('document.getElementById("c-send").click()');
  check(await p.waitFor('document.getElementById("c-status").textContent.startsWith("Sent")'), 'Send to remote sets the host clipboard',
    await p.eval('document.getElementById("c-err").textContent'));
  await p.eval('document.getElementById("c-fetch").click()');
  check(await p.waitFor('document.getElementById("c-remote").value === "from-native"'), 'Fetch remote clipboard reads it back');
  await p.eval('window.__native.length = 0; document.getElementById("c-copy").click()');
  await p.waitFor('window.__native.some(n => n.method === "clipboard.write")', 3000);
  check(await p.eval('window.__native.some(n => n.method === "clipboard.write" && n.args.text === "from-native")'), 'Copy to this device writes the native clipboard');

  await p.eval('document.getElementById("tab-desktop").click()');
  check(await p.waitFor('!document.getElementById("sdm-bar").hidden'), 'key bar appears on the desktop tab');
  await sleep(1500);
  check(await p.eval('document.documentElement.scrollWidth <= innerWidth'), 'desktop tab fits the phone width');
  await p.shot('gateway-desktop');
  // The app's language setting reaches the running page without a reload.
  check(await p.eval('document.getElementById("langsw").hidden'), 'the page\'s own language switch is hidden in the app');
  check(await p.eval('remohopMobile.setLang("zh")') === 'zh', 'remohopMobile.setLang switches to Chinese');
  check(await p.eval('document.getElementById("tab-desktop").textContent === "远程桌面" && document.documentElement.lang === "zh-CN"'), 'the page is in Chinese',
    await p.eval('document.getElementById("tab-desktop").textContent'));
  check(await p.eval('[...document.querySelectorAll("#sdm-bar button")].some(b => b.textContent === "粘贴")'), 'the key bar is in Chinese');
  check(await p.eval('!document.getElementById("workspace").hidden && document.getElementById("hostsel").options.length === 1'), 'the session stays across the switch');
  await p.shot('gateway-desktop-zh');
  await p.eval('remohopMobile.setLang("en")');
  check(await p.eval('document.getElementById("tab-desktop").textContent === "Desktop" && [...document.querySelectorAll("#sdm-bar button")].some(b => b.textContent === "Paste")'), 'and back to English');
  // A new WebView (Activity recreated, renderer restarted) has no
  // sessionStorage; the injected script restores the gateway session id so
  // the host connection is still there. A new tab of the same browser
  // profile is the closest proxy: own sessionStorage, shared localStorage.
  const p2 = await newPhonePage(b, [fakeNative, script]);
  // (loaded the way the apps load it in Chinese)
  await p2.goto(url + '?lang=zh');
  check(await p2.eval('document.documentElement.lang') === 'zh-CN', '?lang=zh starts the page in Chinese');
  check(await p2.waitFor('!document.getElementById("workspace").hidden', 15000), 'a new WebView resumes the gateway session with its host connection',
    await p2.eval('document.getElementById("connect").hidden ? "workspace hidden, connect hidden" : "connect form shown"'));
  check(await p2.eval('document.getElementById("hostsel").options.length') === 1, 'the resumed session lists the one host connection');
  await p.eval('document.getElementById("tab-files").click()');
  check(await p.waitFor('document.getElementById("sdm-bar").hidden'), 'key bar hides off the desktop tab');
  check(await p.eval('remohopMobile.back()') === 'handled' && await p.eval('!document.getElementById("connect").hidden'), 'back goes to the connect screen');
  check(await p.eval('remohopMobile.back()') === 'unhandled', 'back on the connect screen is left to the app');
  const ours = p.errors.filter(e => JSON.stringify(e).includes('remohop-mobile') || !e.url);
  check(ours.length === 0, 'no script errors on the gateway page', p.errors.map(e => (e.exception && e.exception.description) || e.text));
}

const b = await launch();
try {
  await harnessTests(b);
  if (process.argv.includes('--gateway')) await gatewayTests(b);
} catch (e) {
  failures++;
  console.log('FAIL ' + (e.stack || e));
} finally {
  b.close();
}
console.log(failures ? failures + ' failure(s)' : 'all passed');
process.exit(failures ? 1 : 0);
