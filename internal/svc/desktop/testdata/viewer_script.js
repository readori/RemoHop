// Runs one check against the viewer page in headless Chromium (Playwright)
// and prints "RESULT <json>". Run by viewer_script_linux_test.go with
// VIEWER_URL and CHECK (the name of a check below); every message the page
// sends to the viewer is recorded in window.__sent.
'use strict';
const {chromium} = require('playwright');

const sleep = ms => new Promise(r => setTimeout(r, ms));

async function until(page, what, fn, arg, timeout = 30000) {
  const t0 = Date.now();
  for (;;) {
    const v = await page.evaluate(fn, arg);
    if (v) return v;
    if (Date.now() - t0 > timeout) throw new Error('timeout waiting for ' + what);
    await sleep(50);
  }
}

const checks = {
  // The colours drawn on the canvas at the points given in POINTS
  // ([[x, y], ...] in stream pixels), once the picture has settled.
  async colors(page) {
    await until(page, 'video', () => window.remohop.stats().decoded > 30);
    await sleep(1500);
    const points = JSON.parse(process.env.POINTS);
    return page.evaluate(points => {
      const c = document.getElementById('screen');
      const g = c.getContext('2d');
      return {width: c.width, height: c.height, codec: window.remohop.stats().hello.codec,
              px: points.map(([x, y]) => Array.from(g.getImageData(x, y, 1, 1).data.slice(0, 3)))};
    }, points);
  },

  // Mouse buttons pressed and released in an overlapping order, then a
  // cancelled pointer: what the page sent.
  async chord(page) {
    await until(page, 'input', () => { const s = window.remohop.stats(); return s.decoded > 5 && s.hello && s.hello.input; });
    const box = await page.locator('#screen').boundingBox();
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
    await page.mouse.down({button: 'left'});
    await page.mouse.down({button: 'right'});
    await page.mouse.up({button: 'left'});
    await page.mouse.up({button: 'right'});
    await sleep(200);
    const chord = await page.evaluate(() => window.__sent.filter(m => m.type === 'button' || m.type === 'release_all'));
    await page.evaluate(() => { window.__sent.length = 0; });
    await page.mouse.down({button: 'left'});
    await page.evaluate(() => {
      const c = document.getElementById('screen');
      c.dispatchEvent(new PointerEvent('pointercancel', {pointerId: 1, bubbles: true}));
    });
    await sleep(200);
    const cancel = await page.evaluate(() => window.__sent.filter(m => m.type === 'button' || m.type === 'release_all'));
    await page.mouse.up({button: 'left'});
    return {chord, cancel};
  },

  // A key held down: Playwright marks a second keydown of a held key as a
  // repeat, as browsers do for the keyboard's autorepeat.
  async repeat(page) {
    await until(page, 'input', () => { const s = window.remohop.stats(); return s.decoded > 5 && s.hello && s.hello.input; });
    await page.locator('#screen').focus();
    await page.keyboard.down('ArrowLeft');
    await page.keyboard.down('ArrowLeft');
    await page.keyboard.down('ArrowLeft');
    await page.keyboard.up('ArrowLeft');
    await page.keyboard.down('Shift');
    await page.keyboard.down('Shift');
    await page.keyboard.up('Shift');
    await sleep(200);
    return page.evaluate(() => window.__sent.filter(m => m.type === 'key'));
  },
};

(async () => {
  const browser = await chromium.launch({channel: process.env.BROWSER_CHANNEL || undefined, args: ['--no-sandbox']});
  const page = await browser.newPage({viewport: {width: 1000, height: 600}});
  const errors = [];
  page.on('pageerror', e => errors.push(String(e)));
  await page.addInitScript(() => {
    window.__sent = [];
    const orig = WebSocket.prototype.send;
    WebSocket.prototype.send = function (d) {
      if (typeof d === 'string') { try { window.__sent.push(JSON.parse(d)); } catch (e) { /* not JSON */ } }
      return orig.call(this, d);
    };
  });
  let out;
  try {
    await page.goto(process.env.VIEWER_URL);
    out = {result: await checks[process.env.CHECK](page)};
  } catch (e) {
    out = {fatal: String(e && e.stack || e)};
  }
  out.errors = errors;
  console.log('RESULT ' + JSON.stringify(out));
  await browser.close();
})();
