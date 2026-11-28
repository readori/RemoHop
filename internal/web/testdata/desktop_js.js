// Drives the web console's desktop.js in headless Chromium (Playwright)
// against a stand-in for the gateway: PAGE_URL serves the desktop panel of
// index.html, style.css and desktop.js with a stub window.SD and a fake
// WebSocket that records what the page sends and lets the script deliver
// messages. Prints "RESULT <json>".
'use strict';
const {chromium} = require('playwright');
const sleep = ms => new Promise(r => setTimeout(r, ms));

(async () => {
  const browser = await chromium.launch({args: ['--no-sandbox']});
  const page = await browser.newPage({viewport: {width: 1200, height: 800}, deviceScaleFactor: 2});
  const errors = [];
  page.on('pageerror', e => errors.push(String(e)));
  // Headless Chromium has no Pointer Lock: a fake one that records what
  // was asked for.
  await page.addInitScript(() => {
    let locked = null;
    window.__lockReq = [];
    const changed = () => setTimeout(() => document.dispatchEvent(new Event('pointerlockchange')), 0);
    Object.defineProperty(Document.prototype, 'pointerLockElement', {get() { return locked; }, configurable: true});
    Element.prototype.requestPointerLock = function (opts) { window.__lockReq.push(opts || null); locked = this; changed(); return Promise.resolve(); };
    Document.prototype.exitPointerLock = function () { locked = null; changed(); };
  });
  const out = {};
  const ws = () => page.evaluate(() => window.__wss.length);
  async function waitWS(n) {
    for (let i = 0; i < 100 && await ws() < n; i++) await sleep(50);
    if (await ws() < n) throw new Error('no WebSocket ' + n);
  }
  const deliver = m => page.evaluate(m => window.__wss[window.__wss.length - 1].onmessage({data: JSON.stringify(m)}), m);
  const hello = (input, displays) => ({type: 'hello', input, codec: 'avc1.42001f', width: 640, height: 480,
                                      encoder: 'libx264', displays: Array.from({length: displays}, () => ({width: 640, height: 480}))});
  const state = () => page.evaluate(() => ({
    viewOnly: document.getElementById('d-viewonly').checked, viewOnlyDisabled: document.getElementById('d-viewonly').disabled,
    monitors: document.getElementById('d-display').options.length, monitorsHidden: document.getElementById('d-display').hidden,
    url: window.__wss.length ? window.__wss[window.__wss.length - 1].url : ''}));
  try {
    await page.goto(process.env.PAGE_URL);
    out.quality = await page.evaluate(() => [...document.getElementById('d-quality').options].map(o => o.value));

    // A host without input, with two monitors.
    await page.evaluate(() => window.SD.tabs.desktop.show('c1'));
    await waitWS(1);
    await deliver(hello(false, 2));
    await sleep(300);
    out.first = await state();
    // Another host, with input and one monitor.
    await page.evaluate(() => { window.SD.tabs.desktop.hide(); window.SD.tabs.desktop.show('c2'); });
    await waitWS(2);
    out.secondBefore = await state();
    await deliver(hello(true, 1));
    await sleep(300);
    out.second = await state();

    // Mouse chord and a cancelled pointer.
    const box = await page.locator('#d-canvas').boundingBox();
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
    await page.evaluate(() => { window.__sent.length = 0; });
    await page.mouse.down({button: 'left'});
    await page.mouse.down({button: 'right'});
    await page.mouse.up({button: 'left'});
    await page.mouse.up({button: 'right'});
    await page.mouse.down({button: 'left'});
    await page.evaluate(() => document.getElementById('d-canvas').dispatchEvent(new PointerEvent('pointercancel', {pointerId: 1, bubbles: true})));
    await page.mouse.up({button: 'left'});
    // Held keys.
    await page.locator('#d-canvas').focus();
    await page.keyboard.down('Backspace');
    await page.keyboard.down('Backspace');
    await page.keyboard.up('Backspace');
    await page.keyboard.down('Shift');
    await page.keyboard.down('Shift');
    await page.keyboard.up('Shift');
    await sleep(100);
    out.input = await page.evaluate(() => window.__sent.filter(m => m.type === 'button' || m.type === 'key'));

    // The host cannot capture for now.
    await deliver({type: 'stats', host: {fps: 0, paused: 'Windows is showing a secure screen'}, rtt_ms: 1});
    out.paused = await page.evaluate(() => ({hidden: document.getElementById('d-paused').hidden, text: document.getElementById('d-paused').textContent}));
    await deliver({type: 'stats', host: {fps: 30}, rtt_ms: 1});
    out.unpaused = await page.evaluate(() => document.getElementById('d-paused').hidden);

    // The pointer drawn by the page (the page asked for it), with mouse
    // lock, against a host that draws it apart and takes relative input.
    out.askedCursor = (await state()).url.includes('cursor=1');
    const cursorHello = (relative, input) => Object.assign(hello(input, 1), {cursor: true, relative, screen_width: 1280, screen_height: 960});
    await deliver(cursorHello(true, true));
    const png = await page.evaluate(() => {
      const c = document.createElement('canvas'); c.width = 16; c.height = 20;
      const g = c.getContext('2d'); g.fillStyle = '#f00'; g.fillRect(0, 0, 16, 20);
      return c.toDataURL('image/png').split(',')[1];
    });
    await deliver({type: 'cursor', shapes: [{id: 7, width: 16, height: 20, hot_x: 4, hot_y: 6, png}], state: {id: 7, x: 640, y: 480, visible: true}});
    const cbox = await page.locator('#d-canvas').boundingBox();
    const ccx = cbox.x + cbox.width / 2, ccy = cbox.y + cbox.height / 2;
    await page.mouse.move(ccx - 5, ccy - 5);
    await page.mouse.move(ccx, ccy);
    await sleep(300);
    out.pointerInside = await page.evaluate(() => window.remohopDesktop.cursor());
    // Outside the picture the page draws it where the host says it is.
    await page.mouse.move(2, 2);
    await sleep(300);
    out.pointerOutside = await page.evaluate(() => {
      const o = document.getElementById('d-rcursor'), r = o.getBoundingClientRect(), c = document.getElementById('d-canvas').getBoundingClientRect();
      return {hidden: o.hidden, left: r.left, top: r.top, width: r.width, canvasLeft: c.left, canvasTop: c.top, k: c.width / 1280};
    });
    // A hidden remote pointer: none over the picture, and the lock offered.
    await deliver({type: 'cursor', state: {id: 0, x: 640, y: 480, visible: false}});
    await page.mouse.move(ccx, ccy);
    await page.mouse.move(ccx + 3, ccy + 3);
    await sleep(900);
    out.hiddenPointer = await page.evaluate(() => ({css: document.getElementById('d-canvas').style.cursor,
      toast: document.getElementById('d-toast').hidden ? '' : document.getElementById('d-toast').textContent,
      lockDisabled: document.getElementById('d-plock').disabled}));
    // Ctrl+Alt+Shift+M locks; movement goes as move_by, buttons as rel;
    // the shortcut releases. M never reaches the host.
    await page.locator('#d-canvas').focus();
    await page.evaluate(() => { window.__sent.length = 0; window.__lockReq.length = 0; });
    await page.keyboard.press('Control+Alt+Shift+KeyM');
    await sleep(200);
    out.locked = await page.evaluate(() => ({locked: window.remohopDesktop.cursor().locked, req: window.__lockReq,
      button: document.getElementById('d-plock').textContent, pressed: document.getElementById('d-plock').getAttribute('aria-pressed')}));
    await page.evaluate(() => document.getElementById('d-canvas').dispatchEvent(
      new PointerEvent('pointermove', {bubbles: true, pointerId: 1, pointerType: 'mouse', movementX: 3, movementY: -2})));
    await page.mouse.down();
    await page.mouse.up();
    await page.keyboard.press('Control+Alt+Shift+KeyM');
    await sleep(200);
    out.lockedSent = await page.evaluate(() => window.__sent.filter(m => ['key', 'move', 'move_by', 'button'].includes(m.type)));
    out.unlocked = await page.evaluate(() => !window.remohopDesktop.cursor().locked);
    // Auto lock: a click while the remote pointer is hidden locks, and is
    // not passed on; the choice is remembered.
    await page.click('#d-plauto');
    await page.evaluate(() => { window.__sent.length = 0; });
    await page.mouse.move(ccx, ccy);
    await page.mouse.down();
    await page.mouse.up();
    await sleep(200);
    out.auto = await page.evaluate(() => ({locked: window.remohopDesktop.cursor().locked, stored: localStorage.getItem('sd.autolock'),
      buttons: window.__sent.filter(m => m.type === 'button').length}));
    await page.evaluate(() => document.exitPointerLock());
    await page.click('#d-plauto');
    // View only: no lock, the pointer drawn by the page, nothing sent.
    await page.click('#d-viewonly');
    await page.evaluate(() => { window.__sent.length = 0; });
    await deliver({type: 'cursor', state: {id: 7, x: 100, y: 100, visible: true}});
    await page.mouse.move(ccx + 10, ccy + 10);
    await page.evaluate(() => document.getElementById('d-canvas').dispatchEvent(
      new PointerEvent('pointermove', {bubbles: true, pointerId: 1, pointerType: 'mouse', movementX: 3, movementY: -2})));
    await sleep(300);
    out.viewOnly = await page.evaluate(() => ({lockDisabled: document.getElementById('d-plock').disabled,
      overlay: !document.getElementById('d-rcursor').hidden, css: document.getElementById('d-canvas').style.cursor,
      sent: window.__sent.filter(m => ['move', 'move_by', 'button'].includes(m.type)).length}));
    await page.click('#d-viewonly');
    // A host without relative input: no lock.
    await deliver(cursorHello(false, true));
    await sleep(100);
    out.noRelative = await page.evaluate(() => ({disabled: document.getElementById('d-plock').disabled, title: document.getElementById('d-plock').title}));
    // Audio: the button asks the gateway; its answer starts the sound and
    // shows Mute; a refusal and an ended stream say why and switch it off.
    await page.evaluate(() => { window.__sent.length = 0; });
    out.audioCodec = await page.evaluate(async () => (await window.RemoHopAudio.unsupported()) || 'ok');
    await page.click('#d-audio');
    await sleep(300);
    out.audioAsked = await page.evaluate(() => window.__sent.filter(m => m.type === 'audio'));
    await deliver({type: 'audio', on: true, audio: {codec: 'opus', sample_rate: 48000, channels: 2, source: 'test'}});
    await sleep(100);
    const audioState = () => page.evaluate(() => ({on: window.remohopDesktop.audio().on, pressed: document.getElementById('d-audio').getAttribute('aria-pressed'),
      mute: !document.getElementById('d-mute').hidden, muted: window.remohopDesktop.audio().muted,
      toast: document.getElementById('d-toast').hidden ? '' : document.getElementById('d-toast').textContent}));
    out.audioOn = await audioState();
    await page.click('#d-mute');
    out.audioMuted = await audioState();
    await deliver({type: 'audio_end', error: 'the host stopped capturing'});
    out.audioEnded = await audioState();
    await page.click('#d-audio');
    await sleep(300);
    await deliver({type: 'audio', error: 'audio forwarding is switched off on this host'});
    out.audioRefused = await audioState();

    // Every string desktop.js uses is in English and Chinese.
    out.strings = await page.evaluate(async () => {
      const src = await (await fetch('/desktop.js')).text();
      const keys = [...new Set([...src.matchAll(/'(d\.[a-z0-9.]+)'/g)].map(m => m[1]))];
      const I = window.SDI18n;
      I.set('zh', {persist: false});
      return {missing: keys.filter(k => !I.has(k, 'en') || !I.has(k, 'zh')), count: keys.length,
              zhLock: I.t('d.plock'), zhLocked: I.t('d.locked')};
    });

    // Actual size on a HiDPI screen.
    await page.click('#d-fit');
    out.actual = await page.evaluate(() => document.getElementById('d-canvas').style.width);

    // A session that ends with technical detail.
    await deliver({type: 'end', error: 'desktop: host: this computer has no graphical desktop session to show. Details: DISPLAY is not set; X display :0: refused'});
    out.ended = await page.evaluate(() => {
      const m = document.getElementById('d-msg');
      const d = m.querySelector('details');
      return {text: m.firstChild ? m.firstChild.textContent : '', details: d ? d.querySelector('code').textContent : ''};
    });
  } catch (e) {
    out.fatal = String(e && e.stack || e);
  }
  out.errors = errors;
  console.log('RESULT ' + JSON.stringify(out));
  await browser.close();
})();
