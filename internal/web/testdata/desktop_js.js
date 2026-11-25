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
