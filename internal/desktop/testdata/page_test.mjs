// page_test.mjs drives the desktop app's page in headless Chrome through the
// DevTools protocol (no dependencies: Node 22's WebSocket). internal/desktop's
// TestPageInBrowser runs it against a real App with a rendezvous server and
// a simulated helper (REMOHOP_CONTROL_URL). REMOHOP_SHOTS=/dir keeps
// screenshots. The page's address carries a token: it is never printed.
import {spawn} from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';

const url = process.env.REMOHOP_APP_URL, ctl = process.env.REMOHOP_CONTROL_URL, signal = process.env.REMOHOP_SIGNAL_ADDR;
const shots = process.env.REMOHOP_SHOTS || '';
const sleep = ms => new Promise(r => setTimeout(r, ms));
let failures = 0;
const redact = s => String(s).replace(/(127\.0\.0\.1:\d+\/)[A-Za-z0-9_-]{16,}\//g, '$1<token>/');
function check(cond, what, detail) {
  if (cond) console.log('ok   ' + what);
  else { failures++; console.log('FAIL ' + what + (detail !== undefined ? ': ' + redact(JSON.stringify(detail)) : '')); }
}

async function launch() {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'rh-desk-chrome-'));
  const args = ['--headless=new', '--disable-gpu', '--remote-debugging-port=0', '--user-data-dir=' + dir, '--no-first-run',
    '--no-default-browser-check', '--disable-extensions', '--disable-background-networking', '--disable-sync', '--lang=en-US',
    '--hide-scrollbars', 'about:blank'];
  if (process.getuid && process.getuid() === 0 || process.env.CI) args.unshift('--no-sandbox');
  const proc = spawn(process.env.CHROME || 'google-chrome', args, {stdio: ['ignore', 'ignore', 'pipe'], detached: true});
  proc.stderr.resume();
  const portFile = path.join(dir, 'DevToolsActivePort');
  for (let i = 0; i < 1200 && !fs.existsSync(portFile); i++) await sleep(50);
  const [port, wsPath] = fs.readFileSync(portFile, 'utf8').trim().split('\n');
  const ws = new WebSocket('ws://127.0.0.1:' + port + wsPath);
  await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
  let id = 0;
  const pending = new Map(), listeners = [];
  ws.onmessage = ev => {
    const m = JSON.parse(ev.data);
    if (m.id && pending.has(m.id)) { const p = pending.get(m.id); pending.delete(m.id); m.error ? p.rej(new Error(m.error.message)) : p.res(m.result); }
    else if (m.method) for (const l of listeners) l(m);
  };
  const call = (method, params, sessionId) => new Promise((res, rej) => {
    const i = ++id; pending.set(i, {res, rej}); ws.send(JSON.stringify({id: i, method, params: params || {}, sessionId}));
  });
  const close = () => { try { ws.close(); } catch (e) { /* */ } try { process.kill(-proc.pid, 'SIGKILL'); } catch (e) { /* */ } fs.rmSync(dir, {recursive: true, force: true}); };
  return {call, listeners, close};
}

async function newPage(b, w, h) {
  const {targetId} = await b.call('Target.createTarget', {url: 'about:blank'});
  const {sessionId} = await b.call('Target.attachToTarget', {targetId, flatten: true});
  const s = (m, p) => b.call(m, p, sessionId);
  const errors = [];
  b.listeners.push(m => { if (m.sessionId === sessionId && m.method === 'Runtime.exceptionThrown') errors.push(m.params.exceptionDetails); });
  await s('Page.enable'); await s('Runtime.enable');
  const page = {
    s, errors,
    size: (w, h) => s('Emulation.setDeviceMetricsOverride', {width: w, height: h, deviceScaleFactor: 1, mobile: false}),
    async goto(u) { await s('Page.navigate', {url: u}); await sleep(800); },
    async eval(expr, contextId) {
      const r = await s('Runtime.evaluate', {expression: expr, awaitPromise: true, returnByValue: true, contextId});
      if (r.exceptionDetails) throw new Error(expr.slice(0, 80) + ': ' + JSON.stringify(r.exceptionDetails.exception && r.exceptionDetails.exception.description));
      return r.result.value;
    },
    async waitFor(expr, ms = 15000, contextId) {
      const end = Date.now() + ms;
      while (Date.now() < end) { try { if (await page.eval(expr, contextId)) return true; } catch (e) { /* frame not ready */ } await sleep(100); }
      return false;
    },
    async shot(name) {
      if (!shots) return;
      await sleep(400);
      const {data} = await s('Page.captureScreenshot', {format: 'png'});
      fs.mkdirSync(shots, {recursive: true});
      fs.writeFileSync(path.join(shots, name + '.png'), Buffer.from(data, 'base64'));
    },
    // frameContext returns an execution context in the first child frame.
    async frameContext() {
      const {frameTree} = await s('Page.getFrameTree');
      const kids = frameTree.childFrames || [];
      if (!kids.length) return 0;
      const {executionContextId} = await s('Page.createIsolatedWorld', {frameId: kids[kids.length - 1].frame.id, worldName: 't' + Date.now()});
      return executionContextId;
    },
  };
  await page.size(w, h);
  return page;
}

const control = async (p) => (await fetch(ctl + p)).text();

const b = await launch();
try {
  const p = await newPage(b, 900, 600);
  await p.goto(url);
  check(await p.waitFor(`!document.getElementById('v-wizard').hidden`), 'first run shows the wizard');
  check(await p.eval(`document.getElementById('bar').hidden`), 'no tabs during the wizard');
  await p.shot('wizard-1');
  await p.eval(`document.querySelector('#wiz-lang [data-lang=zh]').click()`);
  await p.shot('wizard-1-zh');
  await p.eval(`document.querySelector('#wiz-lang [data-lang=""]').click()`);
  await p.eval(`document.getElementById('wiz-next').click()`);
  await p.eval(`document.querySelector('input[name=wsrv][value=custom]').checked = true; document.getElementById('wiz-addr').value = 'not a server'`);
  await p.shot('wizard-2');
  await p.eval(`document.getElementById('wiz-done').click()`);
  check(await p.waitFor(`document.getElementById('wiz-err').textContent.includes('not a server address')`), 'a bad server address is refused');
  await p.eval(`document.getElementById('wiz-addr').value = ${JSON.stringify(signal)}; document.getElementById('wiz-done').click()`);
  check(await p.waitFor(`!document.getElementById('bar').hidden && !document.getElementById('v-connect').hidden`), 'after the wizard: the Connect tab');
  check(await p.eval(`!document.getElementById('peers-empty').hidden`), 'no saved computers yet');
  await p.shot('connect-empty');
  await p.goto(url);
  check(await p.waitFor(`!document.getElementById('v-computer').hidden`), 'a new start without saved computers opens on This computer');
  await p.eval(`document.getElementById('tab-connect').click()`);

  // This computer: off until switched on.
  await p.eval(`document.getElementById('tab-computer').click()`);
  check(await p.waitFor(`!document.getElementById('v-computer').hidden && document.getElementById('creds').hidden`), 'incoming connections are off at first');
  await p.eval(`document.getElementById('incoming').click()`);
  check(await p.waitFor(`/^\\d{3} \\d{3} \\d{3}$/.test(document.getElementById('in-id').textContent)`), 'switched on: the ID shows');
  const pw = await p.eval(`document.getElementById('in-pw').textContent`);
  check(pw.length >= 8, 'and the temporary password');
  await p.shot('computer');

  // A helper asks: new device, scam warning, view only by default. Deny.
  control('/dial?name=Alex-Laptop');
  check(await p.waitFor(`document.getElementById('dlg-req').open`), 'a connection request opens the dialog');
  check(await p.eval(`document.getElementById('req-title').textContent.includes('Alex-Laptop')`), 'it names the device');
  check(await p.eval(`!document.getElementById('req-warn').hidden && document.getElementById('req-chips').textContent.includes('New device')`), 'a new device: scam warning');
  check(await p.eval(`!document.getElementById('req-control').checked && !document.getElementById('req-files').checked`), 'view only by default');
  check(await p.eval(`document.activeElement && document.activeElement.id === 'req-deny'`), 'Deny has the focus');
  await p.shot('request');
  await p.eval(`document.getElementById('req-deny').click()`);
  check((await control('/result')).includes('denied'), 'Deny refuses the helper', await control('/result'));

  // Again: allowed with control, not files.
  control('/dial?name=Alex-Laptop');
  check(await p.waitFor(`document.getElementById('dlg-req').open`), 'second request');
  check(await p.eval(`document.getElementById('req-warn').hidden && document.getElementById('req-chips').textContent.includes('Connected before')`), 'asked before: no longer new');
  await p.eval(`document.getElementById('req-control').click(); document.getElementById('req-allow').click()`);
  check(await p.waitFor(`document.querySelectorAll('#sessions .sess').length === 1`), 'the helper is connected');
  { const r = await control('/result'); check(r.includes('connected'), 'helper sees the login succeed', r); }
  const chips = () => p.eval(`[...document.querySelectorAll('#sessions .chip')].map(c => c.textContent + '=' + c.getAttribute('aria-pressed')).join(',')`);
  check((await chips()) === 'View=true,Control=true,Files=false', 'session access shows', await chips());
  check(await p.eval(`!document.getElementById('badge').hidden`), 'the tab shows a badge');
  check((await control('/features')).includes('input') && !(await control('/features')).includes('file'), 'the host enforces control without files', await control('/features'));
  await p.shot('session');
  await p.eval(`[...document.querySelectorAll('#sessions .chip')].find(c => c.textContent === 'Files').click()`);
  check(await p.waitFor(`[...document.querySelectorAll('#sessions .chip')].find(c => c.textContent === 'Files').getAttribute('aria-pressed') === 'true'`), 'files granted on the live session');
  check((await control('/features')).includes('file'), 'the host allows files now');
  await p.eval(`document.querySelector('#sessions .btn.danger').click()`);
  check(await p.waitFor(`document.getElementById('sessions-box').hidden`), 'Disconnect ends the session');

  // Address book: this computer saved as Office-PC, opened in a tab.
  await control('/savepeer');
  await p.eval(`document.getElementById('tab-connect').click()`);
  check(await p.waitFor(`document.querySelectorAll('#peers .peer').length === 1`), 'the saved computer is listed');
  check(await p.waitFor(`document.querySelector('#peers .peer .s').textContent.includes('Online')`), 'with its presence');
  await p.shot('connect');
  await p.eval(`document.querySelector('#peers .peer .btn').click()`);
  check(await p.waitFor(`document.querySelectorAll('#tabs .tab.host').length === 1 && !document.querySelector('#hostviews .view').hidden`), 'Connect opens a tab');
  let fc = 0;
  for (let i = 0; i < 50 && !fc; i++) { fc = await p.frameContext(); if (!fc) await sleep(200); }
  const ok = await p.waitFor(`document.documentElement.classList.contains('embed') && !document.getElementById('connect').hidden`, 20000, fc);
  check(ok, 'the console is embedded and asks for the password', ok ? undefined : await p.eval(`location.href.slice(0,30) + ' ' + document.title + ' ' + (document.body ? document.body.innerText.slice(0, 300) : '')`, fc).catch(e => String(e)));
  await p.eval(`document.getElementById('hostpass').value = ${JSON.stringify(pw)}; document.getElementById('pair').checked = false; document.getElementById('connectform').requestSubmit()`, fc);
  check(await p.waitFor(`document.getElementById('dlg-req').open`), 'the console\'s own login asks too');
  await p.eval(`document.getElementById('req-allow').click()`);
  check(await p.waitFor(`document.querySelector('#tabs .tab.host .dot').classList.contains('on')`, 20000), 'the tab shows the session connected');
  check(await p.waitFor(`document.querySelector('#tabs .tab.host .name').textContent === 'Office-PC'`), 'the tab takes the saved name');
  // The computer's tab follows the theme chosen in the app's Settings.
  await p.eval(`document.querySelector('#set-theme [data-theme=dark]').click()`);
  check(await p.waitFor(`document.documentElement.dataset.theme === 'dark'`, 10000, fc), 'the computer tab follows the dark theme');
  await p.shot('console-dark');
  await p.eval(`document.querySelector('#set-theme [data-theme=""]').click()`);
  check(await p.waitFor(`!document.documentElement.dataset.theme`, 10000, fc), 'and back to the system theme');
  await sleep(1500);
  await p.shot('console');
  // With a desktop to show (the test runs under Xvfb), the view-only
  // session asks for control from the viewer and the owner allows it.
  if (await p.waitFor(`!document.getElementById('d-askctl').hidden`, 15000, fc)) {
    check(await p.eval(`document.getElementById('d-viewonly').checked`, fc), 'the console session only watches');
    await p.shot('console-viewonly');
    await p.eval(`document.getElementById('d-askctl').click()`, fc);
    check(await p.waitFor(`document.getElementById('dlg-req').open && document.getElementById('req-title').textContent.includes('asks for more')`), 'Request control asks the owner');
    check(await p.eval(`document.getElementById('req-kv').textContent.includes('control the mouse and keyboard')`), 'and says what for');
    await p.shot('request-access');
    await p.eval(`document.getElementById('req-allow').click()`);
    check(await p.waitFor(`document.getElementById('d-askctl').hidden && !document.getElementById('d-viewonly').checked`, 20000, fc), 'allowed: the viewer takes control');
    await p.shot('console-control');
  } else console.log('skip request-control (no desktop to show here)');
  await p.eval(`document.querySelector('#tabs .tab.host .x[aria-label="Disconnect and close"]').click()`);
  check(await p.waitFor(`document.querySelectorAll('#tabs .tab.host').length === 0 && !document.getElementById('v-connect').hidden`), 'closing the tab');
  await p.eval(`document.getElementById('tab-computer').click()`);
  check(await p.waitFor(`document.getElementById('sessions-box').hidden`, 10000), 'closing the tab ends the session');

  // The password: the user's own instead of a temporary one, and back.
  await p.eval(`document.getElementById('pw-set').click()`);
  check(await p.waitFor(`document.getElementById('dlg-pw').open && document.querySelector('input[name=pwmode][value=temporary]').checked`), 'the password dialog opens on "temporary"');
  await p.eval(`document.getElementById('pw-1').focus(); document.getElementById('pw-1').value = 'password'; document.getElementById('pw-2').value = 'password'; document.getElementById('pw-ok').click()`);
  check(await p.waitFor(`document.getElementById('pw-err').textContent !== ''`), 'a weak own password is refused in the dialog');
  await p.shot('password-dialog');
  await p.eval(`document.getElementById('pw-1').value = 'Garden-42-Ok'; document.getElementById('pw-2').value = 'Garden-42-Ok'; document.getElementById('pw-ok').click()`);
  check(await p.waitFor(`!document.getElementById('dlg-pw').open && document.getElementById('in-pw-l').textContent === 'My own password' && document.getElementById('in-pw').textContent.startsWith('••') && document.getElementById('regen').hidden`), 'the own password is in force, shown as dots');
  await p.eval(`document.getElementById('pw-reveal').click()`);
  check(await p.waitFor(`document.getElementById('in-pw').textContent === 'Garden-42-Ok'`), '"Show password" shows it');
  await p.shot('computer-own-password');
  await p.eval(`document.getElementById('pw-set').click()`);
  await p.eval(`document.querySelector('input[name=pwmode][value=temporary]').checked = true; document.getElementById('pw-ok').click()`);
  check(await p.waitFor(`document.getElementById('in-pw-l').textContent === 'Temporary password' && !document.getElementById('regen').hidden`), 'back to a temporary password');

  // Settings, theme, language, small window.
  await p.eval(`document.getElementById('tab-settings').click()`);
  check(await p.waitFor(`!document.getElementById('v-settings').hidden && document.querySelector('input[name=srv][value=custom]').checked`), 'Settings show the server choice');
  await p.shot('settings');
  await p.eval(`document.getElementById('srv-row').click()`);
  check(await p.waitFor(`!document.getElementById('srv-panel').hidden && document.getElementById('srv-val').textContent !== ''`), 'the server row opens its choices');
  await p.shot('settings-server');
  await p.eval(`document.getElementById('srv-row').click()`);
  await p.eval(`document.querySelector('#set-theme [data-theme=dark]').click()`);
  check(await p.waitFor(`document.documentElement.dataset.theme === 'dark'`), 'dark theme');
  await p.shot('settings-dark');
  await p.eval(`document.getElementById('tab-computer').click()`);
  await p.shot('computer-dark');
  await p.eval(`document.querySelector('#set-lang [data-lang=zh]').click()`);
  check(await p.waitFor(`document.getElementById('tab-computer').textContent.includes('这台电脑')`), 'Chinese');
  await p.shot('computer-dark-zh');
  await p.eval(`document.querySelector('#set-theme [data-theme=""]').click()`);
  await p.waitFor(`!document.documentElement.dataset.theme`);
  await p.shot('computer-zh');
  await p.eval(`document.getElementById('tab-settings').click()`);
  await p.shot('settings-zh');
  await p.eval(`document.getElementById('v-settings').scrollTop = 1e6`);
  await p.shot('settings-zh-end');
  await p.eval(`document.getElementById('v-settings').scrollTop = 0`);
  await p.eval(`document.getElementById('tab-computer').click()`);
  control('/dial?name=Laptop-2');
  check(await p.waitFor(`document.getElementById('dlg-req').open && document.getElementById('req-title').textContent.includes('请求连接')`), 'request in Chinese');
  await p.shot('request-zh');
  await p.eval(`document.getElementById('req-deny').click()`);
  await p.eval(`document.getElementById('tab-connect').click()`);
  await p.shot('connect-zh');
  await p.eval(`document.querySelector('#set-lang [data-lang=""]').click()`);
  await p.waitFor(`!document.documentElement.dataset.theme && document.getElementById('tab-computer').textContent.includes('This computer')`);
  await p.size(480, 720);
  await p.shot('computer-small');
  await p.eval(`document.getElementById('tab-settings').click()`);
  await p.shot('settings-small');
  await p.eval(`document.getElementById('tab-computer').click()`);
  control('/dial?name=Phone');
  check(await p.waitFor(`document.getElementById('dlg-req').open`), 'request in a small window');
  await p.shot('request-small');
  await p.eval(`document.getElementById('req-deny').click()`);
  await p.eval(`document.getElementById('tab-connect').click()`);
  await p.shot('connect-small');
  // Hostile names stay text.
  control('/dial?name=' + encodeURIComponent('<img src=x onerror=window.__x=1>'));
  check(await p.waitFor(`document.getElementById('dlg-req').open`), 'hostile request');
  check(await p.eval(`!window.__x && !document.querySelector('#dlg-req img')`), 'a hostile device name is shown as text');
  await p.eval(`document.getElementById('req-deny').click()`);
  check(p.errors.length === 0, 'no script errors', p.errors.map(e => e.text + ' ' + (e.exception && e.exception.description)));
} catch (e) {
  failures++; console.log('FAIL exception: ' + redact(e.stack || e));
} finally {
  b.close();
}
console.log(failures ? failures + ' failure(s)' : 'all passed');
process.exit(failures ? 1 : 0);
