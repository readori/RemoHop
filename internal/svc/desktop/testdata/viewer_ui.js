// Drives the viewer page in headless Chromium (Playwright) and prints one
// JSON object with what it saw. Run by viewer_browser_linux_test.go with
// VIEWER_URL, FLIP_URL (a test server that flashes a patch of the remote
// screen and answers with the time it did, microseconds since the epoch)
// and AUDIO=1 when the host has an audio source.
'use strict';
const {chromium} = require('playwright');

const sleep = ms => new Promise(r => setTimeout(r, ms));
const out = {errors: []};

async function until(page, what, fn, arg, timeout = 30000) {
  const t0 = Date.now();
  for (;;) {
    const v = await page.evaluate(fn, arg);
    if (v) return v;
    if (Date.now() - t0 > timeout) throw new Error('timeout waiting for ' + what);
    await sleep(100);
  }
}

(async () => {
  const browser = await chromium.launch({
    channel: process.env.BROWSER_CHANNEL || undefined,
    args: ['--no-sandbox', '--autoplay-policy=no-user-gesture-required'],
  });
  const page = await browser.newPage({viewport: {width: 1000, height: 500}});
  page.on('console', m => { if (m.type() === 'error') out.errors.push(m.text()); });
  page.on('pageerror', e => out.errors.push(String(e)));
  try {
    await page.goto(process.env.VIEWER_URL);

    // 1. Video and the monitor menu.
    await until(page, 'video', () => window.remohop.stats().decoded > 20);
    out.video = await page.evaluate(() => {
      const s = window.remohop.stats();
      return {codec: s.hello.codec, width: s.hello.width, height: s.hello.height, all: !!s.hello.all_displays, decoded: s.decoded};
    });
    out.menu = await page.evaluate(() => {
      const sel = document.getElementById('sdisp');
      return {hidden: sel.classList.contains('hidden'), value: sel.value,
              options: [...sel.options].map(o => ({value: o.value, text: o.textContent}))};
    });
    const width = () => page.evaluate(() => document.getElementById('screen').width);
    const pick = async value => {
      await page.selectOption('#sdisp', String(value));
      return value;
    };
    const switched = {};
    const monitor2 = out.menu.options.find(o => /400×240/.test(o.text));
    if (monitor2) {
      await pick(monitor2.value);
      await until(page, 'monitor 2', () => document.getElementById('screen').width === 400);
      const before = await page.evaluate(() => window.remohop.stats().decoded);
      await until(page, 'monitor 2 frames', b => window.remohop.stats().decoded > b + 5, before);
      switched.monitor2 = {width: await width(), select: await page.evaluate(() => document.getElementById('sdisp').value)};
      await pick(-1);
      await until(page, 'all monitors', () => document.getElementById('screen').width === 800);
      switched.all = {width: await width(), select: await page.evaluate(() => document.getElementById('sdisp').value)};
    }
    out.switched = switched;

    // 2. Audio.
    if (process.env.AUDIO === '1') {
      await page.click('#baudio');
      await until(page, 'audio', () => window.remohop.audio().decoded > 40, null, 20000);
      await sleep(1500);
      out.audio = await page.evaluate(() => window.remohop.audio());
      out.audioHUD = await page.evaluate(() => document.getElementById('baudio').textContent);
      await page.click('#bmute');
      out.muted = await page.evaluate(() => window.remohop.audio().muted);
      await page.click('#bmute');
      out.unmuted = await page.evaluate(() => !window.remohop.audio().muted);
      const sk = await page.evaluate(() => window.remohop.audio().skewMs);
      out.skewMs = sk;
      await page.click('#baudio');
      out.audioOff = await page.evaluate(() => !window.remohop.audio().on);
    } else {
      await page.click('#baudio');
      await page.waitForSelector('#toast:not(.hidden)', {timeout: 20000});
      out.audioToast = await page.textContent('#toast');
    }

    // 3. Upload: a drop of a file, and the Upload button's file chooser.
    await page.evaluate(() => {
      const dt = new DataTransfer();
      dt.items.add(new File([new Uint8Array(3 << 20).fill(65)], 'dropped.bin', {type: 'application/octet-stream'}));
      dt.items.add(new File(['hello from the browser\n'], 'hello.txt', {type: 'text/plain'}));
      const fire = type => window.dispatchEvent(new DragEvent(type, {dataTransfer: dt, bubbles: true, cancelable: true}));
      fire('dragenter'); fire('dragover');
      window.__dropShown = !document.getElementById('drop').classList.contains('hidden');
      fire('drop');
    });
    out.dropOverlayShown = await page.evaluate(() => window.__dropShown);
    await page.setInputFiles('#fileinput', {name: 'picked.txt', mimeType: 'text/plain', buffer: Buffer.from('picked with the button\n')});
    await until(page, 'uploads', () => document.querySelectorAll('#uploads .up.ok').length >= 3, null, 30000);
    out.uploads = await page.evaluate(() => [...document.querySelectorAll('#uploads .up')].map(e => e.textContent));
    out.dropOverlayHidden = await page.evaluate(() => document.getElementById('drop').classList.contains('hidden'));

    // 4. Capture-to-display latency, measured from outside: the test server
    // flashes a patch of the remote screen black or white; a requestAnimationFrame
    // loop in the page notes when the canvas shows the change. The pixel read
    // is off-centre: the host draws the pointer, black, at the middle of the
    // screen.
    await page.evaluate(() => {
      const c = document.getElementById('screen');
      const g = c.getContext('2d');
      window.__changes = [];
      let prev = null;
      const loop = () => {
        const d = g.getImageData((c.width >> 1) - 40, (c.height >> 1) - 40, 1, 1).data;
        const v = d[0] > 128 ? 1 : 0;
        if (prev !== null && v !== prev) window.__changes.push((performance.timeOrigin + performance.now()) * 1000);
        prev = v;
        requestAnimationFrame(loop);
      };
      requestAnimationFrame(loop);
    });
    await sleep(500);
    const flips = [];
    for (let i = 0; i < 40; i++) {
      const r = await fetch(process.env.FLIP_URL);
      flips.push((await r.json()).t);
      await sleep(300);
    }
    await sleep(500);
    const changes = await page.evaluate(() => window.__changes);
    const lat = [];
    for (const t0 of flips) {
      const c = changes.find(c => c >= t0);
      if (c !== undefined) lat.push((c - t0) / 1000);
    }
    lat.sort((a, b) => a - b);
    const pct = q => lat[Math.max(0, Math.min(lat.length - 1, Math.ceil(q * lat.length) - 1))];
    out.flips = {sent: flips.length, seen: lat.length,
                 mean: lat.reduce((a, b) => a + b, 0) / (lat.length || 1), p50: pct(0.5), p95: pct(0.95), max: lat[lat.length - 1]};
    out.viewerLatency = await page.evaluate(() => window.remohop.stats().latency);
    out.fps = await page.evaluate(() => window.remohop.stats().fps);
  } catch (e) {
    out.fatal = String(e && e.stack || e);
  }
  await browser.close();
  console.log('RESULT ' + JSON.stringify(out));
})();
