// Drives the quick-support page in headless Chromium. Run by
// TestPageInBrowser (browser_test.go), which serves the page with a scripted
// controller and passes:
//   argv[2]  the page URL (with the access token)
//   argv[3]  the scenario endpoint: POST <endpoint>?step=NAME changes what
//            the controller reports, GET <endpoint>?log=1 returns its call log
//   argv[4]  a foreign origin serving /attack.html (the cross-site test)
//   argv[5]  directory for screenshots, or ""
const { chromium } = require('playwright');
const path = require('path');

const [url, scenario, foreign, shots] = process.argv.slice(2);
const problems = [];

async function step(name) {
  const r = await fetch(scenario + '?step=' + name, { method: 'POST' });
  if (!r.ok) throw new Error('scenario step ' + name + ': ' + r.status);
}
async function log() { return (await fetch(scenario + '?log=1')).text(); }
function check(ok, what) { if (!ok) throw new Error('check failed: ' + what); }
async function waitLog(page, text) {
  for (let i = 0; i < 100; i++) {
    if ((await log()).includes(text)) return;
    await page.waitForTimeout(50);
  }
  throw new Error('the controller never saw "' + text + '"; log: ' + (await log()));
}

async function open(browser, lang, opts) {
  const ctx = await browser.newContext(Object.assign({ viewport: { width: 480, height: 820 }, deviceScaleFactor: 1, locale: 'en-US' }, opts || {}));
  const page = await ctx.newPage();
  page.on('console', (m) => {
    // A refused action (HTTP 409) is logged by the browser; the page shows it.
    if (m.type() === 'error' && !m.text().includes('409')) problems.push('console: ' + m.text());
  });
  page.on('pageerror', (e) => problems.push('pageerror: ' + e.message));
  page.on('dialog', (d) => { problems.push('dialog: ' + d.message()); d.dismiss(); });
  await page.goto(url + (lang ? '?lang=' + lang : ''));
  await page.waitForSelector('#status.ok, #status.wait, #status.live, #status.bad, #setup:not([hidden]), #message:not([hidden])');
  return page;
}

async function shot(page, name) {
  if (!shots) return;
  await page.mouse.move(0, 0);
  await page.screenshot({ path: path.join(shots, name + '.png'), fullPage: true });
}

(async () => {
  const browser = await chromium.launch();
  try {
    // --- English: the whole flow -------------------------------------
    let page = await open(browser, 'en');
    check(await page.title() === 'RemoHop Quick Support', 'title');
    check(await page.textContent('#id') === '123 456 789', 'ID shown');
    check(await page.textContent('#pw') === 'abcd-efgh', 'password shown');
    check((await page.textContent('#statustext')).startsWith('Ready'), 'ready status');
    check(await page.isChecked('#opt-input') && await page.isChecked('#opt-files') && !(await page.isChecked('#opt-advanced')), 'default options');
    const idSize = await page.$eval('#id', (e) => parseFloat(getComputedStyle(e).fontSize));
    check(idSize >= 30, 'the ID is shown large (' + idSize + 'px)');
    check(await page.$eval('#id', (e) => e.scrollWidth <= e.clientWidth), 'the ID fits its box');
    await shot(page, 'quick-ready-en');

    await page.click('#regen');
    await page.waitForFunction(() => document.getElementById('pw').textContent === 'wxyz-2345');

    // A connection request: named, with the key, Allow not clickable at once.
    await step('request');
    await page.waitForSelector('.ask');
    check((await page.textContent('.ask h2')) === 'helper-laptop wants to connect', 'request title');
    check((await page.textContent('.ask')).includes('3f9a:01c2:77de:b0a4:5e16'), 'device key shown');
    check((await page.textContent('.ask')).includes('203.0.113.5'), 'address shown');
    check(await page.isDisabled('.ask .allow'), 'Allow starts disabled');
    check((await page.title()).startsWith('●'), 'title flags the request');
    check((await page.textContent('#statustext')).includes('asking to connect'), 'status says someone is asking');
    await page.waitForSelector('.ask .allow:not([disabled])');
    check(/Refused automatically in \d+ s/.test(await page.textContent('.ask .left')), 'countdown');
    await shot(page, 'quick-request-en');
    await page.click('.ask .allow');
    await waitLog(page, 'decide 7 true');
    await page.waitForSelector('.ask', { state: 'detached' });
    await page.waitForSelector('#sessions li');
    check((await page.textContent('#sessions li .name')) === 'helper-laptop', 'session listed');
    check((await page.textContent('#statustext')).includes('shared with 1 person'), 'sharing status');
    await shot(page, 'quick-connected-en');

    // Text from the other side is text, whatever it contains.
    await step('hostile');
    await page.waitForSelector('.ask');
    check((await page.textContent('.ask h2')).includes('<img src=x onerror=alert(1)>'), 'hostile name shown literally');
    check((await page.$$('.ask img, .ask script, .ask a')).length === 0, 'no markup from a name');
    await page.waitForSelector('.ask .allow:not([disabled])');
    await page.click('.ask .deny');
    await waitLog(page, 'decide 8 false');
    await page.waitForSelector('.ask', { state: 'detached' });
    check((await page.$$('#sessions li')).length === 1, 'denied request opened no session');

    // An action of a granted session asks differently.
    await step('action');
    await page.waitForSelector('.ask');
    check((await page.textContent('.ask h2')) === 'ci-agent asks for your permission', 'action request title');
    check((await page.textContent('.ask')).includes('exec: git push'), 'action shown');
    await page.waitForSelector('.ask .allow:not([disabled])');
    await page.click('.ask .deny');
    await page.waitForSelector('.ask', { state: 'detached' });

    await page.uncheck('#opt-input');
    await waitLog(page, 'option input false');
    await page.check('#opt-advanced');
    await waitLog(page, 'option advanced true');

    await page.click('#sessions li button');
    await waitLog(page, 'disconnect 7');
    await page.waitForSelector('#sessions li', { state: 'detached' });
    check(await page.isVisible('#nosessions'), 'nobody connected again');

    // Details in plain language.
    await page.click('summary');
    const net = await page.textContent('#net');
    check(net.includes('rendezvous.example.net') && net.includes('end-to-end encrypted') && net.includes('60 seconds'), 'connection details: ' + net);

    // Server lost.
    await step('offline');
    await page.waitForSelector('#status.bad');
    check((await page.textContent('#statustext')).includes('Lost contact'), 'offline status');
    await step('online');
    await page.waitForSelector('#status.ok');

    // --- Chinese ------------------------------------------------------
    await page.click('#lang');
    check(await page.title() === 'RemoHop 快速协助', 'Chinese title');
    check((await page.textContent('#stop')) === '停止共享', 'Chinese stop button');
    check(await page.getAttribute('html', 'lang') === 'zh-CN', 'lang attribute');
    await page.close();

    await step('reset');
    page = await open(browser, 'zh');
    check((await page.textContent('#statustext')).startsWith('已就绪'), 'Chinese status');
    await shot(page, 'quick-ready-zh');
    await step('request');
    await page.waitForSelector('.ask .allow:not([disabled])');
    check((await page.textContent('.ask h2')) === 'helper-laptop 请求连接', 'Chinese request');
    await shot(page, 'quick-request-zh');
    await page.click('.ask .allow');
    await page.waitForSelector('#sessions li');
    await shot(page, 'quick-connected-zh');
    await page.close();

    // The choice made with the switch is kept by the process: a new page
    // (as after a restart, on another port, with empty storage) opens in
    // it, whatever the browser's language.
    await step('reset');
    page = await open(browser, '', { locale: 'en-US' });
    check(await page.title() === 'RemoHop 快速协助', 'the language picked in the window is kept by the process');
    check(await page.getAttribute('#lang', 'aria-label') === '语言：切换到英文', 'the switch has a Chinese accessible name');
    await page.click('#lang');
    check(await page.title() === 'RemoHop Quick Support', 'switched back to English');
    await page.close();
    page = await open(browser, '', { locale: 'zh-CN' });
    check(await page.title() === 'RemoHop Quick Support', 'the server-side language (en) wins over the browser locale');
    await page.close();

    // Dark colour scheme renders too.
    page = await open(browser, 'en', { colorScheme: 'dark' });
    await shot(page, 'quick-ready-dark-en');
    await page.close();

    // --- Other phases ---------------------------------------------------
    await step('connecting');
    page = await open(browser, 'en');
    check((await page.textContent('#id')).includes('Getting an ID'), 'no ID yet');
    check((await page.textContent('#statustext')).includes('Still trying to reach the server rendezvous.example.net'), 'slow registration explained');
    await shot(page, 'quick-connecting-en');
    await step('setup');
    await page.waitForSelector('#setup:not([hidden])');
    check(!(await page.isVisible('#ready')), 'no credentials in setup');
    // The same choice as the desktop app's wizard, the official server first.
    check(await page.isChecked('input[name=mode][value=official]'), 'the official server is the default choice');
    check((await page.textContent('#setup')).includes('Run by RemoHop'), 'the official server is described');
    check(!(await page.textContent('#setup')).includes('signal.remohop.com'), 'its address is not shown');
    check(!(await page.textContent('#setup')).includes('has no server of its own'), 'no "RemoHop has no server" text');
    await shot(page, 'quick-setup-en');
    await step('failconfigure');
    // Typing in the address field picks "my helper gave me an address".
    await page.fill('#server', 'bad address');
    check(await page.isChecked('input[name=mode][value=custom]'), 'typing an address picks that choice');
    await page.click('#setupform button');
    await page.waitForFunction(() => document.getElementById('setuperr').textContent.includes('not a server address'));
    // In Chinese the choice and the refusal are in Chinese.
    const zhPage = await open(browser, 'zh');
    check((await zhPage.textContent('#setup')).includes('使用官方服务器'), 'Chinese: the official server choice');
    await shot(zhPage, 'quick-setup-zh');
    await zhPage.click('input[name=mode][value=custom]');
    await zhPage.fill('#server', 'bad address');
    await zhPage.click('#setupform button');
    await zhPage.waitForFunction(() => document.getElementById('setuperr').textContent.includes('不是有效的服务器地址'));
    await zhPage.close();
    await step('okconfigure');
    await page.fill('#server', 'support.example.net');
    await page.click('#setupform button');
    await waitLog(page, 'configure support.example.net');
    await page.waitForSelector('#ready:not([hidden])');
    // The default choice and "no server" reach the controller as such.
    await step('setup');
    await page.waitForSelector('#setup:not([hidden])');
    await page.click('input[name=mode][value=official]');
    await page.click('#setupform button');
    await waitLog(page, 'configure official');
    await step('setup');
    await page.waitForSelector('#setup:not([hidden])');
    await page.click('input[name=mode][value=none]');
    await page.click('#setupform button');
    await waitLog(page, 'configure none');
    await page.waitForSelector('#ready:not([hidden])');
    await step('running');
    await page.waitForSelector('#message:not([hidden])');
    check((await page.textContent('#msgtitle')).includes('already running'), 'already-running message');
    check((await page.textContent('#msgdetail')).includes('987 654 321'), 'other host ID shown');
    check(!(await page.isVisible('#stop')), 'nothing to stop');
    await shot(page, 'quick-already-running-en');
    await page.close();

    // --- Cross-site: another origin on this machine cannot act -------------
    await step('reset');
    const ctx = await browser.newContext();
    const attacker = await ctx.newPage();
    await attacker.goto(foreign + '/attack.html');
    const results = await attacker.evaluate(async (target) => {
      const out = {};
      const tryFetch = async (name, path, init) => {
        try { const r = await fetch(target + path, init); out[name] = r.type === 'opaque' ? 'opaque' : r.status; }
        catch (e) { out[name] = 'blocked'; }
      };
      await tryFetch('read', 'api/state', {});
      await tryFetch('json', 'api/stop', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' });
      await tryFetch('simple', 'api/stop', { method: 'POST', mode: 'no-cors', headers: { 'Content-Type': 'text/plain' }, body: '{}' });
      await tryFetch('hello', 'api/hello', { method: 'POST', mode: 'no-cors', body: '{}' });
      out.frame = await new Promise((resolve) => {
        const f = document.createElement('iframe');
        f.src = target;
        f.onload = () => { try { resolve(f.contentDocument ? 'readable' : 'blank'); } catch (e) { resolve('cross'); } };
        f.onerror = () => resolve('error');
        document.body.appendChild(f);
        setTimeout(() => resolve('timeout'), 3000);
      });
      out.events = await new Promise((resolve) => {
        const es = new EventSource(target + 'api/events');
        es.onmessage = () => { es.close(); resolve('data'); };
        es.onerror = () => { es.close(); resolve('blocked'); };
        setTimeout(() => resolve('timeout'), 3000);
      });
      return out;
    }, url);
    check(results.read === 'blocked', 'cross-site read: ' + JSON.stringify(results));
    check(results.json === 'blocked', 'cross-site JSON POST: ' + JSON.stringify(results));
    check(results.events === 'blocked', 'cross-site event stream: ' + JSON.stringify(results));
    check(results.frame !== 'readable', 'framing: ' + JSON.stringify(results));
    await attacker.waitForTimeout(300);
    check(!(await log()).includes('stop'), 'a cross-site request stopped sharing; log: ' + (await log()));
    await ctx.close();

    // --- Stop sharing, and closing the window ---------------------------------
    page = await open(browser, 'en');
    await page.click('#stop');
    await waitLog(page, 'stop');
    await page.waitForSelector('#message:not([hidden])');
    check((await page.textContent('#msgtitle')) === 'Sharing has stopped', 'stopped message');
    check(!(await page.isVisible('#ready')), 'credentials hidden once stopped');
    await shot(page, 'quick-stopped-en');
    await page.close().catch(() => {});

    await step('reset');
    page = await open(browser, 'en');
    await page.close({ runBeforeUnload: true });
    await new Promise((r) => setTimeout(r, 500));

    if (problems.length) throw new Error('page problems:\n' + problems.join('\n'));
    console.log('PAGE TEST OK');
  } finally {
    await browser.close();
  }
})().catch((e) => { console.error(e.stack || String(e)); process.exit(1); });
