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

  // A decoder that accepts its configuration and then fails on every frame
  // in hardware (FAIL=hw) or always (FAIL=all).
  async decodefail(page) {
    await sleep(+process.env.WAIT || 4000);
    return page.evaluate(() => ({stats: window.remohop.stats(), toast: document.getElementById('toast').textContent,
                                 msg: document.getElementById('msg').classList.contains('hidden') ? '' : document.getElementById('msgtext').textContent,
                                 sent: window.__sent.filter(m => m.type === 'keyframe' || m.type === 'config')}));
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

  // The remote pointer drawn by the page: its CSS cursor, the picture
  // around the remote pointer (which must not show it), the pointer drawn
  // where the host says once the test moves it on the host, and again when
  // the mouse leaves the picture. With NOCURSOR=1 the page's caps leave
  // the pointer out (as an older page's do) and the picture shows it.
  async cursor(page) {
    await until(page, 'video', () => { const s = window.remohop.stats(); return s.decoded > 5 && s.hello && s.hello.input; });
    const box = await page.locator('#screen').boundingBox();
    const at = (fx, fy) => page.mouse.move(box.x + box.width * fx, box.y + box.height * fy);
    await at(0.6, 0.6);
    await at(0.625, 0.625); // 400,300 of 640x480
    // The picture around the remote pointer: the largest difference from
    // the colour the test painted, in levels.
    const around = () => page.evaluate(() => {
      const c = document.getElementById('screen');
      const s = window.remohop.cursor().state || {x: 400, y: 300};
      const d = c.getContext('2d').getImageData(Math.max(0, s.x - 12), Math.max(0, s.y - 12), 24, 24).data;
      let worst = 0;
      for (let i = 0; i < d.length; i += 4) {
        worst = Math.max(worst, Math.abs(d[i] - 0x30), Math.abs(d[i + 1] - 0x60), Math.abs(d[i + 2] - 0xa0));
      }
      return worst;
    });
    if (process.env.NOCURSOR) {
      await sleep(1500);
      return {first: await page.evaluate(() => window.remohop.cursor()), worst: await around()};
    }
    await until(page, 'the pointer shape', () => {
      const c = window.remohop.cursor();
      return c.on && c.state && c.state.visible && c.decoded > 0 && c.css.startsWith('url(');
    });
    await sleep(500);
    const first = await page.evaluate(() => window.remohop.cursor());
    const worst = await around();
    await until(page, 'the pointer drawn where the host moved it', () => { const c = window.remohop.cursor(); return c.overlay && c.warped; }, null, 20000);
    const warped = await page.evaluate(() => window.remohop.cursor());
    await at(0.5, 0.5);
    await until(page, 'the overlay gone after moving the mouse', () => !window.remohop.cursor().overlay);
    await page.mouse.move(2, 2);
    await until(page, 'the pointer drawn while the mouse is outside', () => !!window.remohop.cursor().overlay);
    const outside = await page.evaluate(() => window.remohop.cursor());
    return {first, worst, warped, outside, box};
  },

  // Mouse lock with a fake Pointer Lock (PLOCK=1; headless browsers have
  // none). The test hides the remote pointer first. Ctrl+Alt+Shift+M locks
  // (asking for raw movement) and is not passed on; movement goes as
  // move_by and the host moves its pointer by it; buttons go without a
  // position; the shortcut releases; with Auto lock a click locks and is
  // not passed on.
  async plock(page) {
    await until(page, 'input', () => { const s = window.remohop.stats(); return s.decoded > 5 && s.hello && s.hello.input && s.hello.relative; });
    await until(page, 'the hidden pointer', () => { const c = window.remohop.cursor(); return c.on && c.state && !c.state.visible; });
    const box = await page.locator('#screen').boundingBox();
    const cx = box.x + box.width / 2, cy = box.y + box.height / 2;
    await page.mouse.move(cx - 5, cy - 5);
    await page.mouse.move(cx, cy);
    const suggestion = await until(page, 'the lock suggested', () => {
      const t = document.getElementById('toast');
      return !t.classList.contains('hidden') && t.textContent.includes('hid its pointer') && t.textContent;
    });
    const css = await page.evaluate(() => window.remohop.cursor().css);
    await page.evaluate(() => { window.__sent.length = 0; });
    await page.keyboard.press('Control+Alt+Shift+KeyM');
    await until(page, 'locked', () => window.remohop.cursor().locked);
    const before = await page.evaluate(() => window.remohop.cursor().state);
    await page.evaluate(() => document.getElementById('screen').dispatchEvent(
      new PointerEvent('pointermove', {bubbles: true, pointerId: 1, movementX: 3, movementY: -2})));
    await until(page, 'the host pointer moved by 3,-2', b => {
      const s = window.remohop.cursor().state;
      return s.x === b.x + 3 && s.y === b.y - 2;
    }, before);
    await page.mouse.down();
    await page.mouse.up();
    await sleep(200);
    const locked = await page.evaluate(() => window.__sent.filter(m => ['key', 'move', 'move_by', 'button'].includes(m.type)));
    await page.keyboard.press('Control+Alt+Shift+KeyM');
    await until(page, 'unlocked', () => !window.remohop.cursor().locked);
    await page.click('#bplauto');
    await page.evaluate(() => { window.__sent.length = 0; window.__lockReq.length = 0; });
    await page.mouse.move(cx, cy);
    await page.mouse.down();
    await until(page, 'locked by a click', () => window.remohop.cursor().locked);
    await page.mouse.up();
    await sleep(200);
    return {suggestion, css, locked, req: await page.evaluate(() => window.__lockReq),
            auto: await page.evaluate(() => window.__sent.filter(m => m.type === 'button')),
            first: await page.evaluate(() => window.__firstReq)};
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
  if (process.env.NOCURSOR) {
    await page.addInitScript(() => {
      const orig = WebSocket.prototype.send;
      WebSocket.prototype.send = function (d) {
        if (typeof d === 'string' && d.includes('"caps"')) { const m = JSON.parse(d); delete m.cursor; d = JSON.stringify(m); }
        return orig.call(this, d);
      };
    });
  }
  if (process.env.PLOCK) {
    await page.addInitScript(() => {
      let locked = null;
      window.__lockReq = [];
      const changed = () => setTimeout(() => document.dispatchEvent(new Event('pointerlockchange')), 0);
      Object.defineProperty(Document.prototype, 'pointerLockElement', {get() { return locked; }, configurable: true});
      Element.prototype.requestPointerLock = function (opts) {
        window.__lockReq.push(opts || null);
        if (!window.__firstReq) window.__firstReq = opts || null;
        locked = this;
        changed();
        return Promise.resolve();
      };
      Document.prototype.exitPointerLock = function () { locked = null; changed(); };
    });
  }
  if (process.env.FAIL) {
    await page.addInitScript(mode => {
      const Real = window.VideoDecoder;
      window.VideoDecoder = class extends Real {
        constructor(init) { super(init); this.fakeError = init.error; }
        configure(c) { this.hw = c.hardwareAcceleration; return super.configure(c); }
        decode(chunk) {
          if (mode === 'all' || this.hw !== 'prefer-software') {
            setTimeout(() => this.fakeError(new DOMException('fake decoder failure')), 0);
            return;
          }
          return super.decode(chunk);
        }
      };
    }, process.env.FAIL);
  }
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
