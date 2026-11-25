// RemoHop desktop app page. It talks only to the app's own loopback server
// (see internal/localui for the security model) and, through postMessage,
// to the console of each host, which runs in an iframe of the in-process
// gateway (internal/web, embed mode). Text from other machines is only ever
// placed with textContent.
(function () {
  'use strict';
  const $ = id => document.getElementById(id);
  const base = location.pathname.replace(/[^/]*$/, '');

  // ---- language ----
  const T = {
    en: {
      'tab.connect': 'Connect', 'tab.computer': 'This computer', 'tab.settings': 'Settings',
      'wiz.title': 'Welcome to RemoHop', 'wiz.lead': 'Connect to your computers, or let someone help you.',
      'wiz.server': 'Rendezvous server', 'wiz.incoming': 'Let others connect to this computer now (each connection asks me)', 'next': 'Next', 'back': 'Back', 'done': 'Done', 'save': 'Save', 'cancel': 'Cancel',
      'auto': 'Auto', 'copy': 'Copy', 'copied': 'Copied', 'allow': 'Allow', 'deny': 'Deny', 'saved': 'Saved',
      'srv.official': 'Official server', 'srv.custom': 'My own server', 'srv.skip': 'Skip for now', 'srv.none': 'No server',
      'srv.skip.sub': 'Connect by address (host:port) only',
      'help.server': 'It introduces computers to each other by ID. Connections are end-to-end encrypted; the server never sees them.',
      'c.target': 'Computer ID or address', 'c.connect': 'Connect', 'c.saved': 'Saved computers', 'c.none': 'No saved computers yet.',
      'help.saved': 'Computers you connected to or paired with. A paired computer needs no password.',
      'p.online': 'Online', 'p.offline': 'Offline', 'p.unknown': 'Unknown', 'p.paired': 'Paired', 'p.account': 'Account',
      'p.rename': 'Rename', 'p.wake': 'Wake up', 'p.mac': 'Wake-on-LAN address', 'p.forget': 'Forget', 'p.woke': 'Wake-up packet sent',
      'p.rename.t': 'Name for {t}', 'p.name': 'Name', 'p.mac.t': 'Wake-on-LAN for {t}', 'p.mac.l': 'MAC address (empty removes it)',
      'p.forget.q': 'Forget {t}? Its pairing is deleted.',
      'in.allow': 'Allow incoming connections', 'in.id': 'ID', 'in.pw': 'Temporary password', 'in.new': 'New',
      'in.off': 'Off: nobody can connect', 'in.starting': 'Connecting to the server…', 'in.ready': 'Ready: every connection asks you',
      'in.offline': 'Offline: trying again…', 'in.noserver': 'No server: reachable by address on this network',
      'in.elsewhere': 'Served by the RemoHop service (ID {id})', 'in.error': 'Could not start',
      'in.addr': 'Address: {a}', 'in.sessions': 'Connected now', 'in.discall': 'Disconnect everyone',
      'help.incoming': 'While this is on, someone with this ID and password can ask to connect. You decide each time what they may do.',
      's.view': 'View', 's.control': 'Control', 's.files': 'Files', 's.disc': 'Disconnect', 's.remembered': 'Remembered',
      'un.title': 'Unattended access', 'un.off': 'Off', 'un.on': 'On', 'un.running': 'Running · ID {id}', 'un.stopped': 'Installed, not running',
      'un.setup': 'Set up', 'un.disable': 'Turn off', 'un.busy': 'Working…', 'un.enable': 'Turn on',
      'un.lead': 'Choose a strong password. Anyone with this ID and password can connect without asking.',
      'un.pw': 'Password', 'un.pw2': 'Repeat', 'un.gen': 'Suggest a strong password', 'un.admin': 'Windows will ask for an administrator.',
      'un.mismatch': 'The passwords differ.', 'un.na': 'Not available on this system',
      'un.sessions': 'Connections', 'un.change': 'Change password', 'un.none': 'Nobody is connected through unattended access.',
      'un.pwdone': 'Password changed', 'un.newpw': 'New password',
      'help.un': 'Reach this computer when nobody is at it, from start-up, with a strong password you choose. It runs as a background service.',
      'tr.title': 'Remembered devices', 'tr.forget': 'Forget', 'tr.control': 'control', 'tr.files': 'files', 'tr.view': 'view only',
      'help.tr': 'These devices connect without asking, with the access you chose, while incoming connections are on.',
      'req.title': '{d} wants to connect', 'req.title.access': '{d} asks for more', 'req.new': 'New device', 'req.known': 'Connected before',
      'req.scam': 'Did someone call you saying they are from support, your bank or the police, and ask you to install this? Deny. That is a scam.',
      'req.may': 'They may see your screen, and also:', 'req.control': 'Control the mouse and keyboard', 'req.files': 'Transfer files and use the clipboard',
      'req.remember': 'Remember this device', 'req.left': 'Refused in {n} s', 'req.device': 'Device', 'req.key': 'Key', 'req.from': 'From',
      'req.asks': 'Asks to:', 'req.input': 'control the mouse and keyboard', 'req.file': 'transfer files', 'req.clip': 'use the clipboard',
      'req.unknown': 'Unnamed device',
      'set.lang': 'Language', 'set.theme': 'Theme', 'set.light': 'Light', 'set.dark': 'Dark', 'set.start': 'Start', 'set.autostart': 'Start RemoHop when I sign in',
      'about': 'RemoHop {v} · {e}', 'browser': 'browser window',
      'upd.title': 'RemoHop {v} is available', 'upd.how.download': 'You have {c}. Download the new version and replace this copy.',
      'upd.how.cmd': 'You have {c}. Upgrade with:', 'upd.how.package': "You have {c}. Upgrade it with your system's package manager.",
      'upd.how.nix': 'You have {c}. Upgrade it through Nix.', 'upd.get': 'Download', 'upd.notes': "What's new", 'upd.skip': 'Skip this version',
      'upd.h': 'Updates', 'upd.auto': 'Check for updates automatically (once a day)', 'upd.now': 'Check now', 'upd.checking': 'Checking…',
      'upd.latest': 'Up to date ({v}) · checked {t}', 'upd.avail': '{v} is available · checked {t}', 'upd.never': 'Not checked yet',
      'upd.fail': 'Could not check (no connection to remohop.com?)', 'upd.managed': 'Switched off by the administrator ({e})',
      'upd.dev': 'Development build ({v}): not compared with releases',
      'upd.privacy': 'Only the latest version number is read from remohop.com; nothing about this computer is sent, and nothing is installed without you.',
      'h.closeq': 'Disconnect from {t}?', 'h.popout': 'Open in a window', 'h.close': 'Disconnect and close',
      'err.no_server': 'Enter the server address.', 'err.bad_server': 'That is not a server address (like signal.example.com:21116).',
      'err.weak_password': 'Use at least 12 characters with three of: lower case, upper case, digits, symbols.',
      'err.elevation_cancelled': 'The administrator prompt was cancelled.', 'err.bad_target': 'That is not an ID or an address.',
      'err.wake_name': 'Give this computer a short name first.', 'err.bad_mac': 'That is not a MAC address.',
      'err.gone': 'That is no longer there.', 'err.failed': 'Something went wrong: {m}', 'err.offline': 'The app is not answering.'
    },
    zh: {
      'tab.connect': '连接', 'tab.computer': '这台电脑', 'tab.settings': '设置',
      'wiz.title': '欢迎使用 RemoHop', 'wiz.lead': '连接你的电脑，或者让别人帮你。',
      'wiz.server': '汇合服务器', 'wiz.incoming': '现在就允许别人连接这台电脑（每次连接都先问我）', 'next': '下一步', 'back': '上一步', 'done': '完成', 'save': '保存', 'cancel': '取消',
      'auto': '自动', 'copy': '复制', 'copied': '已复制', 'allow': '允许', 'deny': '拒绝', 'saved': '已保存',
      'srv.official': '官方服务器', 'srv.custom': '我自己的服务器', 'srv.skip': '暂时跳过', 'srv.none': '不用服务器',
      'srv.skip.sub': '只按地址（主机:端口）连接',
      'help.server': '它按 ID 为电脑牵线。连接是端到端加密的，服务器看不到内容。',
      'c.target': '电脑 ID 或地址', 'c.connect': '连接', 'c.saved': '已保存的电脑', 'c.none': '还没有保存的电脑。',
      'help.saved': '连接过或配对过的电脑。已配对的电脑不用再输密码。',
      'p.online': '在线', 'p.offline': '离线', 'p.unknown': '未知', 'p.paired': '已配对', 'p.account': '账号',
      'p.rename': '重命名', 'p.wake': '唤醒', 'p.mac': '网络唤醒地址', 'p.forget': '移除', 'p.woke': '已发送唤醒包',
      'p.rename.t': '为 {t} 命名', 'p.name': '名称', 'p.mac.t': '{t} 的网络唤醒', 'p.mac.l': 'MAC 地址（留空则删除）',
      'p.forget.q': '移除 {t}？它的配对会被删除。',
      'in.allow': '允许别人连接', 'in.id': 'ID', 'in.pw': '临时密码', 'in.new': '换一个',
      'in.off': '已关闭：谁都连不上', 'in.starting': '正在连接服务器…', 'in.ready': '已就绪：每次连接都会先问你',
      'in.offline': '离线：正在重试…', 'in.noserver': '没有服务器：同一网络内可按地址连接',
      'in.elsewhere': '由 RemoHop 服务提供（ID {id}）', 'in.error': '无法启动',
      'in.addr': '地址：{a}', 'in.sessions': '当前连接', 'in.discall': '全部断开',
      'help.incoming': '打开后，知道这个 ID 和密码的人可以请求连接。每次由你决定对方能做什么。',
      's.view': '查看', 's.control': '控制', 's.files': '文件', 's.disc': '断开', 's.remembered': '已记住',
      'un.title': '无人值守访问', 'un.off': '关闭', 'un.on': '开启', 'un.running': '运行中 · ID {id}', 'un.stopped': '已安装，未运行',
      'un.setup': '设置', 'un.disable': '关闭', 'un.busy': '处理中…', 'un.enable': '开启',
      'un.lead': '请设一个强密码。知道这个 ID 和密码的人无需你同意即可连接。',
      'un.pw': '密码', 'un.pw2': '再输一次', 'un.gen': '生成强密码', 'un.admin': 'Windows 会要求管理员授权。',
      'un.mismatch': '两次输入的密码不一致。', 'un.na': '此系统不支持',
      'un.sessions': '连接', 'un.change': '修改密码', 'un.none': '没有人通过无人值守访问连接。',
      'un.pwdone': '密码已修改', 'un.newpw': '新密码',
      'help.un': '没人在电脑前也能连上，开机即可用，使用你设定的强密码。它作为后台服务运行。',
      'tr.title': '已记住的设备', 'tr.forget': '移除', 'tr.control': '控制', 'tr.files': '文件', 'tr.view': '仅查看',
      'help.tr': '允许连接打开时，这些设备无需询问即可连接，权限为你当时的选择。',
      'req.title': '{d} 请求连接', 'req.title.access': '{d} 请求更多权限', 'req.new': '新设备', 'req.known': '以前连接过',
      'req.scam': '是不是有人打电话自称客服、银行或警察，让你安装这个软件？请拒绝，这是诈骗。',
      'req.may': '对方可以看到你的屏幕，另外还可以：', 'req.control': '控制鼠标和键盘', 'req.files': '传输文件、使用剪贴板',
      'req.remember': '记住这台设备', 'req.left': '{n} 秒后自动拒绝', 'req.device': '设备', 'req.key': '密钥', 'req.from': '来自',
      'req.asks': '请求：', 'req.input': '控制鼠标和键盘', 'req.file': '传输文件', 'req.clip': '使用剪贴板',
      'req.unknown': '未命名设备',
      'set.lang': '语言', 'set.theme': '外观', 'set.light': '浅色', 'set.dark': '深色', 'set.start': '启动', 'set.autostart': '登录时启动 RemoHop',
      'about': 'RemoHop {v} · {e}', 'browser': '浏览器窗口',
      'upd.title': 'RemoHop {v} 已发布', 'upd.how.download': '当前是 {c}。下载新版替换当前程序即可。',
      'upd.how.cmd': '当前是 {c}。用这条命令升级：', 'upd.how.package': '当前是 {c}。请用系统的包管理器升级。',
      'upd.how.nix': '当前是 {c}。请通过 Nix 升级。', 'upd.get': '下载', 'upd.notes': '更新内容', 'upd.skip': '跳过这个版本',
      'upd.h': '更新', 'upd.auto': '自动检查更新（每天一次）', 'upd.now': '立即检查', 'upd.checking': '正在检查…',
      'upd.latest': '已是最新版本（{v}）· {t} 检查', 'upd.avail': '{v} 已发布 · {t} 检查', 'upd.never': '还没有检查过',
      'upd.fail': '检查失败（连不上 remohop.com？）', 'upd.managed': '已被管理员关闭（{e}）',
      'upd.dev': '开发版本（{v}），不与正式版比较',
      'upd.privacy': '只从 remohop.com 读取最新的版本号，不发送这台电脑的任何信息，也不会在你不知情时安装任何东西。',
      'h.closeq': '断开与 {t} 的连接？', 'h.popout': '在新窗口中打开', 'h.close': '断开并关闭',
      'err.no_server': '请输入服务器地址。', 'err.bad_server': '这不是服务器地址（例如 signal.example.com:21116）。',
      'err.weak_password': '至少 12 个字符，并包含小写、大写、数字、符号中的三种。',
      'err.elevation_cancelled': '管理员授权已取消。', 'err.bad_target': '这不是 ID 或地址。',
      'err.wake_name': '请先给这台电脑起个短名称。', 'err.bad_mac': '这不是 MAC 地址。',
      'err.gone': '它已经不在了。', 'err.failed': '出错了：{m}', 'err.offline': '应用没有响应。'
    }
  };
  let lang = 'en';
  const sysLang = () => (navigator.language || 'en').toLowerCase().startsWith('zh') ? 'zh' : 'en';
  function t(k, v) {
    let s = (T[lang] && T[lang][k]) || T.en[k] || k;
    if (v) s = s.replace(/\{(\w+)\}/g, (m, n) => (v[n] !== undefined ? String(v[n]) : m));
    return s;
  }
  function applyLang(l) {
    lang = l || sysLang();
    document.documentElement.lang = lang === 'zh' ? 'zh-CN' : 'en';
    for (const el of document.querySelectorAll('[data-t]')) el.textContent = t(el.dataset.t);
    for (const el of document.querySelectorAll('[data-ta]')) { el.setAttribute('aria-label', t(el.dataset.ta)); el.title = t(el.dataset.ta); }
  }

  // ---- API ----
  let csrf = '';
  async function api(path, body) {
    const r = await fetch(base + 'api/' + path, {method: 'POST', headers: {'Content-Type': 'application/json', 'X-RemoHop-CSRF': csrf},
      body: JSON.stringify(body || {})});
    const j = await r.json().catch(() => ({}));
    if (!r.ok) { const e = new Error((j.error && j.error.message) || r.statusText); e.code = j.error && j.error.code; throw e; }
    return j;
  }
  function errText(e) {
    if (e && e.code && T.en['err.' + e.code]) return t('err.' + e.code);
    return t('err.failed', {m: (e && e.message) || '?'});
  }
  function snack(msg) {
    const s = $('snack'); s.textContent = msg; s.hidden = false;
    clearTimeout(snack.tm); snack.tm = setTimeout(() => { s.hidden = true; }, 2600);
  }

  // ---- help (ⓘ) ----
  document.addEventListener('click', ev => {
    const b = ev.target.closest('[data-help]'); if (!b) return;
    ev.preventDefault();
    const h = $(b.dataset.help); h.hidden = !h.hidden; b.setAttribute('aria-expanded', String(!h.hidden));
  });

  // ---- views and tabs ----
  let view = 'connect';
  const hosts = new Map(); // target -> {tab, viewEl, iframe, origin, title}
  function show(v) {
    view = v;
    for (const el of document.querySelectorAll('#main > .view')) el.hidden = el.id !== 'v-' + v;
    for (const h of hosts.values()) h.viewEl.hidden = v !== 'host:' + h.target;
    for (const b of document.querySelectorAll('#tabs .tab')) b.setAttribute('aria-selected', String(b.dataset.view === v));
    $('tab-settings').setAttribute('aria-pressed', String(v === 'settings'));
    if (S && S.update) renderUpdate(S.update);
    if (v === 'connect') loadPeers(false);
    const h = v.startsWith('host:') && hosts.get(v.slice(5));
    if (h) h.tab.scrollIntoView({block: 'nearest', inline: 'nearest'});
  }
  for (const b of document.querySelectorAll('[data-view]')) b.addEventListener('click', () => show(b.dataset.view === view && b.dataset.view === 'settings' ? 'connect' : b.dataset.view));

  // ---- state ----
  let S = null, S0theme;
  function render(s) {
    const first = !S;
    S = s;
    document.documentElement.toggleAttribute('data-theme', !!s.theme);
    if (s.theme) document.documentElement.dataset.theme = s.theme;
    // The computers' tabs follow the app's theme too.
    if (!first && s.theme !== S0theme) for (const h of hosts.values()) {
      try { h.iframe.contentWindow.postMessage({type: 'remohop-theme', theme: s.theme || ''}, h.origin); } catch (e) { /* not loaded yet */ }
    }
    S0theme = s.theme;
    if ((s.lang || sysLang()) !== lang || first) applyLang(s.lang);
    $('bar').hidden = !s.setup;
    if (!s.setup) { for (const el of document.querySelectorAll('#main > .view')) el.hidden = el.id !== 'v-wizard'; renderWizard(); return; }
    if (first || !$('v-wizard').hidden) { show(view); if (first) defaultView(); }
    renderComputer(s);
    renderSettings(s);
    renderRequests(s);
    if (s.smoke && s.smoke !== lastSmoke) { lastSmoke = s.smoke; smokeView(s.smoke); }
  }
  // The first tab: Connect when there are saved computers, else This
  // computer (someone who has nothing to connect to is here to be helped).
  async function defaultView() {
    await loadPeers(false);
    if (view === 'connect' && !peers.length && S.setup) show('computer');
  }
  let lastSmoke = '';
  function smokeView(v) {
    if (v.startsWith('open:')) openHost(v.slice(5)); else show(v);
  }

  // ---- wizard ----
  let wizLang = null;
  function renderWizard() {
    if (wizLang === null) {
      wizLang = S.lang || '';
      // A choice made before (in quick support, which shares it) is preselected.
      if (S.server.chosen) {
        const v = S.server.mode === 'none' ? '' : S.server.mode;
        for (const r of document.querySelectorAll('input[name=wsrv]')) r.checked = r.value === v;
        if (S.server.mode === 'custom') $('wiz-addr').value = S.server.addr;
      }
    }
    for (const b of $('wiz-lang').children) b.setAttribute('aria-pressed', String(b.dataset.lang === wizLang));
    $('wiz-lang').children[0].textContent = t('auto');
  }
  $('wiz-lang').addEventListener('click', ev => {
    const b = ev.target.closest('button'); if (!b) return;
    wizLang = b.dataset.lang; applyLang(wizLang); renderWizard();
  });
  $('wiz-next').addEventListener('click', () => { $('wiz-1').hidden = true; $('wiz-2').hidden = false; $('wiz-s2').className = 'on'; });
  $('wiz-back').addEventListener('click', () => { $('wiz-1').hidden = false; $('wiz-2').hidden = true; $('wiz-s2').className = ''; });
  $('wiz-addr').addEventListener('focus', () => { document.querySelector('input[name=wsrv][value=custom]').checked = true; });
  $('wiz-done').addEventListener('click', async () => {
    const mode = document.querySelector('input[name=wsrv]:checked').value;
    $('wiz-err').textContent = '';
    try {
      await api('setup', {mode, addr: $('wiz-addr').value, lang: wizLang});
      if ($('wiz-incoming').checked) { await api('incoming', {on: true}); view = 'computer'; } else view = 'connect';
    }
    catch (e) { $('wiz-err').textContent = errText(e); }
  });

  // ---- connect ----
  let peers = [];
  let peersBusy = false;
  async function loadPeers(refresh) {
    if (peersBusy || !S || !S.setup) return;
    peersBusy = true;
    try { const j = await api('peers', {refresh}); peers = j.peers || []; renderPeers(); }
    catch (e) { /* keep the old list */ } finally { peersBusy = false; }
  }
  setInterval(() => { if (view === 'connect') loadPeers(false); }, 30000);
  function peerTitle(p) { return p.alias || p.name || p.target; }
  function renderPeers() {
    const box = $('peers'); box.textContent = '';
    $('peers-empty').hidden = peers.length > 0;
    for (const p of peers) {
      const el = document.createElement('div'); el.className = 'peer';
      const av = document.createElement('div'); av.className = 'avatar'; av.textContent = (peerTitle(p)[0] || '?').toUpperCase();
      const pr = document.createElement('span'); pr.className = 'pres' + (p.presence === 'online' ? ' online' : ''); av.append(pr);
      const txt = document.createElement('div'); txt.className = 'grow';
      const a = document.createElement('div'); a.className = 't'; a.textContent = peerTitle(p);
      const b = document.createElement('div'); b.className = 's';
      const bits = [];
      if (p.alias || p.name) bits.push(p.target);
      bits.push(t(p.presence === 'online' ? 'p.online' : p.presence === 'offline' ? 'p.offline' : 'p.unknown'));
      if (p.login === 'pairing') bits.push(t('p.paired'));
      b.textContent = bits.join(' · ');
      txt.append(a, b);
      const go = document.createElement('button'); go.className = 'btn tonal sm'; go.textContent = t('c.connect');
      go.addEventListener('click', () => openHost(p.target, peerTitle(p)));
      const more = document.createElement('button'); more.className = 'icon-btn'; more.setAttribute('aria-label', '…');
      more.innerHTML = '<svg><use href="#i-more"/></svg>';
      more.addEventListener('click', () => peerMenu(p));
      el.append(av, txt, go, more);
      box.append(el);
    }
  }
  async function ask(title, label, value) {
    $('ask-title').textContent = title; $('ask-label').textContent = label; $('ask-input').value = value || ''; $('ask-err').textContent = '';
    const d = $('dlg-ask'); d.returnValue = ''; d.showModal(); $('ask-input').focus();
    return new Promise(res => d.addEventListener('close', () => res(d.returnValue === 'ok' ? $('ask-input').value : null), {once: true}));
  }
  function sheet(title, items) {
    const d = document.createElement('dialog');
    const f = document.createElement('form'); f.method = 'dialog'; f.className = 'dlg';
    const h = document.createElement('h2'); h.textContent = title; f.append(h);
    for (const it of items) {
      const b = document.createElement('button'); b.className = 'btn text'; b.value = it.id; b.textContent = it.label;
      b.classList.add('block'); f.append(b);
    }
    const acts = document.createElement('div'); acts.className = 'acts';
    const c = document.createElement('button'); c.className = 'btn outline'; c.value = ''; c.textContent = t('cancel'); acts.append(c); f.append(acts);
    d.append(f); document.body.append(d); d.showModal();
    return new Promise(res => d.addEventListener('close', () => { res(d.returnValue); d.remove(); }, {once: true}));
  }
  async function peerMenu(p) {
    const items = [{id: 'rename', label: t('p.rename')}];
    if (p.wake) items.push({id: 'wake', label: t('p.wake')});
    items.push({id: 'mac', label: t('p.mac')}, {id: 'forget', label: t('p.forget')});
    const a = await sheet(peerTitle(p), items);
    try {
      if (a === 'rename') { const v = await ask(t('p.rename.t', {t: p.target}), t('p.name'), p.alias); if (v !== null) await api('peer/alias', {target: p.target, alias: v}); }
      if (a === 'wake') { await api('peer/wake', {target: p.target}); snack(t('p.woke')); }
      if (a === 'mac') { const v = await ask(t('p.mac.t', {t: peerTitle(p)}), t('p.mac.l'), ''); if (v !== null) { await api('peer/mac', {target: p.target, mac: v}); snack(t('saved')); } }
      if (a === 'forget') { const ok = await sheet(t('p.forget.q', {t: peerTitle(p)}), [{id: 'yes', label: t('p.forget')}]); if (ok === 'yes') await api('peer/forget', {target: p.target}); }
      if (a) loadPeers(true);
    } catch (e) { snack(errText(e)); }
  }
  $('connect-form').addEventListener('submit', ev => {
    ev.preventDefault();
    const v = $('target').value.trim(); if (!v) return;
    $('c-err').textContent = '';
    const p = peers.find(x => x.target === v.replace(/\s+/g, '') || x.alias === v);
    openHost(p ? p.target : v, p ? peerTitle(p) : v);
  });

  // ---- host tabs ----
  async function openHost(target, title) {
    target = target.trim();
    if (/^[\d\s]+$/.test(target)) target = target.replace(/\s+/g, '');
    if (hosts.has(target)) { show('host:' + target); return; }
    let url;
    try { url = (await api('console', {target})).url; } catch (e) { $('c-err').textContent = errText(e); show('connect'); return; }
    const h = {target, title: title || target, origin: new URL(url).origin};
    const tab = document.createElement('div'); tab.className = 'tab host'; tab.setAttribute('role', 'tab'); tab.dataset.view = 'host:' + target;
    tab.tabIndex = 0;
    const dot = document.createElement('span'); dot.className = 'dot wait';
    const name = document.createElement('span'); name.className = 'name lbl'; name.textContent = h.title;
    const pop = document.createElement('button'); pop.className = 'x'; pop.title = t('h.popout'); pop.setAttribute('aria-label', t('h.popout'));
    pop.innerHTML = '<svg><use href="#i-popout"/></svg>';
    const x = document.createElement('button'); x.className = 'x'; x.title = t('h.close'); x.setAttribute('aria-label', t('h.close'));
    x.innerHTML = '<svg><use href="#i-close"/></svg>';
    tab.append(dot, name, pop, x);
    tab.addEventListener('click', ev => { if (!ev.target.closest('.x')) show('host:' + target); });
    tab.addEventListener('keydown', ev => { if (ev.key === 'Enter' || ev.key === ' ') show('host:' + target); });
    x.addEventListener('click', () => closeHost(target, true));
    pop.addEventListener('click', () => popOut(target));
    $('tabs').append(tab);
    const v = document.createElement('section'); v.className = 'view host'; v.setAttribute('role', 'tabpanel');
    const f = document.createElement('iframe');
    f.title = h.title; f.allow = 'fullscreen; clipboard-read; clipboard-write; autoplay';
    f.src = url;
    v.append(f); $('hostviews').append(v);
    Object.assign(h, {tab, dot, name, viewEl: v, iframe: f});
    hosts.set(target, h);
    show('host:' + target);
  }
  function closeHost(target, disconnect) {
    const h = hosts.get(target); if (!h) return;
    if (disconnect) {
      try { h.iframe.contentWindow.postMessage({type: 'remohop-disconnect'}, h.origin); } catch (e) { /* gone */ }
      api('console/close', {target}).catch(() => {});
    }
    h.tab.remove(); h.viewEl.remove(); hosts.delete(target);
    if (view === 'host:' + target) show(hosts.size ? 'host:' + [...hosts.keys()].pop() : 'connect');
  }
  // Popping out moves the host into a window of its own: the window joins
  // this page's gateway session (the console hands over its session id),
  // so the connection is not made again.
  const pendingPop = new Map();
  function popOut(target) {
    const h = hosts.get(target); if (!h) return;
    pendingPop.set(target, true);
    h.iframe.contentWindow.postMessage({type: 'remohop-sid-request'}, h.origin);
  }
  window.addEventListener('message', async ev => {
    const h = [...hosts.values()].find(x => x.iframe.contentWindow === ev.source);
    if (!h || ev.origin !== h.origin || !ev.data || typeof ev.data !== 'object') return;
    const m = ev.data;
    if (m.type === 'remohop-session') {
      // A page that loaded while the theme changed gets it now.
      if (h.theme !== (S && S.theme || '')) { h.theme = S && S.theme || ''; h.iframe.contentWindow.postMessage({type: 'remohop-theme', theme: h.theme}, h.origin); }
      h.dot.className = 'dot' + (m.state === 'connected' ? ' on' : m.state === 'closed' ? '' : ' wait');
      if (m.title && typeof m.title === 'string') { h.title = m.title.slice(0, 80); h.name.textContent = h.title; }
    } else if (m.type === 'remohop-closed') {
      closeHost(h.target, false);
    } else if (m.type === 'remohop-sid' && pendingPop.get(h.target) && typeof m.sid === 'string') {
      pendingPop.delete(h.target);
      try { await api('popout', {target: h.target, title: h.title, sid: m.sid}); closeHost(h.target, false); }
      catch (e) { snack(errText(e)); }
    }
  });

  // ---- this computer ----
  function renderComputer(s) {
    const i = s.incoming;
    $('incoming').checked = i.on;
    let cls = '', txt = '';
    switch (i.phase) {
      case 'off': txt = t('in.off'); break;
      case 'starting': cls = 'wait'; txt = t('in.starting'); break;
      case 'ready': cls = i.online ? 'ok' : 'wait'; txt = t(i.online ? 'in.ready' : 'in.offline'); break;
      case 'no_server': cls = 'ok'; txt = t('in.noserver'); break;
      case 'elsewhere': cls = 'ok'; txt = t('in.elsewhere', {id: i.other_id || '?'}); break;
      case 'error': cls = 'bad'; txt = t('in.error'); break;
    }
    $('in-status').className = 'status ' + cls; $('in-status-t').textContent = txt;
    const showCreds = i.on && (i.phase === 'ready' || i.phase === 'starting' || i.phase === 'no_server');
    $('creds').hidden = !showCreds;
    const id = $('in-id');
    id.textContent = i.id || (i.phase === 'no_server' ? '—' : '…'); id.classList.toggle('pending', !i.id);
    $('in-pw').textContent = i.password || '…';
    $('in-addr').hidden = !(i.addresses && i.addresses.length);
    if (i.addresses) $('in-addr').textContent = t('in.addr', {a: i.addresses.slice(0, 3).join(', ')});
    $('in-err').textContent = i.phase === 'error' ? (i.error || '') : '';
    // sessions
    const box = $('sessions'); box.textContent = '';
    $('sessions-box').hidden = !i.sessions.length;
    $('badge').hidden = !i.sessions.length;
    for (const x of i.sessions) {
      const el = document.createElement('div'); el.className = 'sess';
      const g = document.createElement('div'); g.className = 'grow';
      const a = document.createElement('div'); a.className = 't'; a.textContent = x.label;
      const b = document.createElement('div'); b.className = 'muted small';
      b.textContent = [x.from, fmtDur(x.seconds), x.trusted ? t('s.remembered') : ''].filter(Boolean).join(' · ');
      g.append(a, b);
      const chips = document.createElement('div'); chips.className = 'chips';
      const view = document.createElement('span'); view.className = 'chip'; view.setAttribute('aria-pressed', 'true'); view.textContent = t('s.view');
      chips.append(view);
      for (const [k, on] of [['control', x.control], ['files', x.files]]) {
        const c = document.createElement('button'); c.className = 'chip'; c.setAttribute('aria-pressed', String(on)); c.textContent = t('s.' + k);
        c.addEventListener('click', () => api('session', {id: x.id, control: k === 'control' ? !x.control : x.control, files: k === 'files' ? !x.files : x.files}).catch(e => snack(errText(e))));
        chips.append(c);
      }
      const d = document.createElement('button'); d.className = 'btn danger sm'; d.textContent = t('s.disc');
      d.addEventListener('click', () => api('disconnect', {id: x.id}).catch(e => snack(errText(e))));
      el.append(g, chips, d); box.append(el);
    }
    // unattended
    const u = s.unattended;
    let us = t('un.off'), sub = '', btn = t('un.setup');
    if (!u.available) { sub = t('un.na'); }
    if (u.installed) { us = t('un.on'); sub = u.running ? t('un.running', {id: u.id || '…'}) : t('un.stopped'); btn = t('un.disable'); }
    if (u.busy) btn = t('un.busy');
    $('un-state').textContent = us; $('un-sub').textContent = u.error ? errText({message: u.error}) : sub;
    $('un-btn').textContent = btn; $('un-btn').disabled = !u.available || !!u.busy;
    $('un-btn').dataset.on = u.installed ? '1' : '';
    $('un-manage').hidden = !u.installed || !!u.busy;
    $('un-admin2').hidden = !u.needs_admin;
    // trusted
    const tr = $('trusted'); tr.textContent = '';
    $('trusted-box').hidden = !s.trusted.length;
    for (const d of s.trusted) {
      const r = document.createElement('div'); r.className = 'list-row';
      const g = document.createElement('div'); g.className = 'grow';
      const a = document.createElement('div'); a.textContent = d.name || t('req.unknown');
      const b = document.createElement('div'); b.className = 'muted small';
      const acc = [d.control ? t('tr.control') : '', d.files ? t('tr.files') : ''].filter(Boolean).join(', ') || t('tr.view');
      b.textContent = acc + ' · ' + shortKey(d.identity);
      g.append(a, b);
      const f = document.createElement('button'); f.className = 'btn text sm'; f.textContent = t('tr.forget');
      f.addEventListener('click', () => api('forget-device', {identity: d.identity}).catch(e => snack(errText(e))));
      r.append(g, f); tr.append(r);
    }
  }
  function fmtDur(s) { const m = Math.floor(s / 60); return m < 60 ? m + ':' + String(s % 60).padStart(2, '0') : Math.floor(m / 60) + 'h' + String(m % 60).padStart(2, '0'); }
  function shortKey(k) { return (k || '').slice(0, 16); }
  $('incoming').addEventListener('change', ev => api('incoming', {on: ev.target.checked}).catch(e => { snack(errText(e)); }));
  $('regen').addEventListener('click', () => api('regenerate').catch(e => snack(errText(e))));
  $('disc-all').addEventListener('click', () => api('disconnect', {id: 0}).catch(e => snack(errText(e))));
  for (const b of document.querySelectorAll('[data-copy]')) b.addEventListener('click', async () => {
    const v = $(b.dataset.copy).textContent.trim();
    try { await navigator.clipboard.writeText(v); } catch (e) {
      const ta = document.createElement('textarea'); ta.value = v; document.body.append(ta); ta.select(); document.execCommand('copy'); ta.remove();
    }
    snack(t('copied'));
  });

  // unattended dialog
  $('un-btn').addEventListener('click', async () => {
    if ($('un-btn').dataset.on) {
      try { await api('unattended/disable'); } catch (e) { snack(errText(e)); }
      return;
    }
    pwMode = 'enable';
    $('un-pw1').value = $('un-pw2').value = ''; $('un-err').textContent = '';
    $('un-admin').hidden = S.unattended && !S.unattended.needs_admin;
    $('un-ok').textContent = t('un.enable');
    $('dlg-un').showModal();
  });
  let pwMode = 'enable';
  $('un-gen').addEventListener('click', () => {
    const a = 'ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789';
    const r = crypto.getRandomValues(new Uint8Array(16));
    let p = ''; for (const x of r) p += a[x % a.length];
    p = p.slice(0, 4) + '-' + p.slice(4, 8) + '-' + p.slice(8, 12) + '-' + p.slice(12) + '7';
    $('un-pw1').type = $('un-pw2').type = 'text';
    $('un-pw1').value = $('un-pw2').value = p;
  });
  $('un-form').addEventListener('submit', async ev => {
    if (ev.submitter && ev.submitter.value !== 'ok') return;
    ev.preventDefault();
    if ($('un-pw1').value !== $('un-pw2').value) { $('un-err').textContent = t('un.mismatch'); return; }
    $('un-ok').disabled = true;
    try {
      await api(pwMode === 'change' ? 'unattended/password' : 'unattended/enable', {password: $('un-pw1').value});
      $('dlg-un').close();
      if (pwMode === 'change') snack(t('un.pwdone'));
    }
    catch (e) { $('un-err').textContent = errText(e); } finally { $('un-ok').disabled = false; }
  });

  // The service's connections: on Windows each look and each change is an
  // administrator step (the service's state is administrators' only).
  async function loadSvc() {
    $('svc-err').textContent = '';
    const box = $('svc-list'); box.textContent = '…';
    try {
      const ss = (await api('unattended/sessions')).sessions || [];
      box.textContent = '';
      if (!ss.length) { const p = document.createElement('p'); p.className = 'muted'; p.textContent = t('un.none'); box.append(p); }
      for (const x of ss) {
        const r = document.createElement('div'); r.className = 'list-row';
        const g = document.createElement('div'); g.className = 'grow';
        const a = document.createElement('div'); a.textContent = x.device || x.peer;
        const b = document.createElement('div'); b.className = 'muted small'; b.textContent = [x.from, x.path].filter(Boolean).join(' · ');
        g.append(a, b);
        const d = document.createElement('button'); d.type = 'button'; d.className = 'btn text sm'; d.textContent = t('s.disc');
        d.addEventListener('click', async () => { try { await api('unattended/disconnect', {id: x.id}); loadSvc(); } catch (e) { $('svc-err').textContent = errText(e); } });
        r.append(g, d); box.append(r);
      }
      $('svc-all').hidden = !ss.length;
    } catch (e) { box.textContent = ''; $('svc-err').textContent = errText(e); }
  }
  $('un-sess').addEventListener('click', () => { $('dlg-svc').showModal(); loadSvc(); });
  $('svc-all').addEventListener('click', async () => { try { await api('unattended/disconnect', {id: 0}); loadSvc(); } catch (e) { $('svc-err').textContent = errText(e); } });
  $('un-pw').addEventListener('click', () => {
    pwMode = 'change';
    $('un-pw1').value = $('un-pw2').value = ''; $('un-err').textContent = '';
    $('un-admin').hidden = S.unattended && !S.unattended.needs_admin;
    $('un-ok').textContent = t('un.change');
    $('dlg-un').showModal();
  });

  // ---- connection requests ----
  let reqShown = 0, reqTimer = 0;
  function renderRequests(s) {
    const list = s.incoming.requests;
    const d = $('dlg-req');
    const cur = list.find(r => r.n === reqShown);
    if (cur) { updateLeft(cur); return; }
    if (reqShown && d.open) d.close('gone');
    reqShown = 0;
    if (!list.length) return;
    const r = list[0];
    reqShown = r.n;
    const who = r.device || r.name || t('req.unknown');
    const access = r.access && r.access.length;
    $('req-title').textContent = t(access ? 'req.title.access' : 'req.title', {d: who});
    const chips = $('req-chips'); chips.textContent = '';
    if (!access) {
      const c = document.createElement('span'); c.className = 'chip ' + (r.first_seen ? 'bad' : 'ok'); c.textContent = t(r.first_seen ? 'req.new' : 'req.known'); chips.append(c);
    }
    $('req-warn').hidden = !!access || !r.first_seen;
    const kv = $('req-kv'); kv.textContent = '';
    const add = (k, v) => { if (!v) return; const dt = document.createElement('dt'); dt.textContent = t(k); const dd = document.createElement('dd'); dd.textContent = v; kv.append(dt, dd); };
    add('req.device', who); add('req.key', shortKey(r.identity)); add('req.from', r.from);
    if (access) add('req.asks', r.access.map(f => t('req.' + f)).filter(Boolean).join(', '));
    $('req-choice').hidden = !!access; $('req-remember-row').hidden = !!access;
    // An unknown device gets view only unless the user ticks more.
    $('req-control').checked = false; $('req-files').checked = false; $('req-remember').checked = false;
    updateLeft(r);
    d.returnValue = '';
    if (!d.open) d.showModal();
    $('req-deny').focus();
  }
  function updateLeft(r) { $('req-left').textContent = t('req.left', {n: r.left}); }
  $('dlg-req').addEventListener('close', async () => {
    const n = reqShown, v = $('dlg-req').returnValue;
    if (!n || v === 'gone') return;
    reqShown = 0;
    try {
      await api('decide', {n, allow: v === 'allow', control: $('req-control').checked, files: $('req-files').checked, remember: $('req-remember').checked});
    } catch (e) { if (e.code !== 'gone') snack(errText(e)); }
  });
  $('dlg-req').addEventListener('cancel', ev => { ev.preventDefault(); $('dlg-req').close('deny'); });

  // ---- settings ----
  let srvEdited = false;
  function renderSettings(s) {
    if (!srvEdited) {
      for (const r of document.querySelectorAll('input[name=srv]')) r.checked = r.value === s.server.mode;
      $('srv-addr').value = s.server.mode === 'custom' ? s.server.addr : '';
    }
    for (const b of $('set-lang').children) b.setAttribute('aria-pressed', String(b.dataset.lang === (s.lang || '')));
    for (const b of $('set-theme').children) b.setAttribute('aria-pressed', String(b.dataset.theme === (s.theme || '')));
    $('autostart-box').hidden = !s.autostart.available;
    $('autostart').checked = s.autostart.on;
    $('about').textContent = t('about', {v: s.version, e: s.ui.native ? s.ui.engine : t('browser')});
    renderUpdate(s.update);
  }

  // ---- updates ----
  const isRelease = v => /^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$/.test(v || '');
  function renderUpdate(u) {
    if (!u) return;
    const when = u.checked ? new Date(u.checked).toLocaleString(lang === 'zh' ? 'zh-CN' : 'en', {dateStyle: 'medium', timeStyle: 'short'}) : '';
    let st;
    if (u.managed) st = t('upd.managed', {e: 'REMOHOP_NO_UPDATE_CHECK'});
    else if (u.checking) st = t('upd.checking');
    else if (!isRelease(u.current)) st = t('upd.dev', {v: u.current});
    else if (u.error) st = t('upd.fail');
    else if (!u.checked) st = t('upd.never');
    else st = u.available ? t('upd.avail', {v: u.latest, t: when}) : t('upd.latest', {v: u.current, t: when});
    $('upd-status').textContent = st;
    $('upd-auto').checked = u.auto && !u.managed; $('upd-auto').disabled = u.managed;
    $('upd-now').disabled = u.checking;
    const show = u.available && !u.skipped;
    $('upd').hidden = !show || view.startsWith('host:');
    if (!show) return;
    const m = u.method || {kind: 'download'};
    $('upd-title').textContent = t('upd.title', {v: u.latest});
    const how = m.command ? 'cmd' : (m.kind === 'package' || m.kind === 'nix') ? m.kind : 'download';
    $('upd-how').textContent = t('upd.how.' + how, {c: u.current});
    $('upd-cmd').hidden = $('upd-copy').hidden = !m.command;
    $('upd-cmd').textContent = m.command || '';
    $('upd-get').hidden = how !== 'download';
  }
  $('upd-auto').addEventListener('change', ev => api('update/auto', {on: ev.target.checked}).catch(e => snack(errText(e))));
  $('upd-now').addEventListener('click', () => api('update/check').catch(e => snack(errText(e))));
  $('upd-get').addEventListener('click', () => api('update/open', {target: 'download', lang}).catch(e => snack(errText(e))));
  $('upd-notes').addEventListener('click', () => api('update/open', {target: 'notes', lang}).catch(e => snack(errText(e))));
  $('upd-skip').addEventListener('click', () => api('update/skip', {version: S.update.latest}).catch(e => snack(errText(e))));
  for (const el of document.querySelectorAll('input[name=srv], #srv-addr')) el.addEventListener('input', () => { srvEdited = true; });
  $('srv-addr').addEventListener('focus', () => { document.querySelector('input[name=srv][value=custom]').checked = true; srvEdited = true; });
  $('srv-save').addEventListener('click', async () => {
    $('srv-err').textContent = '';
    try { await api('server', {mode: document.querySelector('input[name=srv]:checked').value, addr: $('srv-addr').value}); srvEdited = false; snack(t('saved')); }
    catch (e) { $('srv-err').textContent = errText(e); }
  });
  $('set-lang').addEventListener('click', ev => { const b = ev.target.closest('button'); if (b) api('lang', {lang: b.dataset.lang}).catch(e => snack(errText(e))); });
  $('set-theme').addEventListener('click', ev => { const b = ev.target.closest('button'); if (b) api('theme', {theme: b.dataset.theme}).catch(e => snack(errText(e))); });
  $('autostart').addEventListener('change', ev => api('autostart', {on: ev.target.checked}).catch(e => snack(errText(e))));

  // ---- start ----
  async function start() {
    applyLang('');
    try {
      const r = await fetch(base + 'api/hello', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: '{}'});
      const j = await r.json(); csrf = j.csrf; render(j.state);
    } catch (e) { snack(t('err.offline')); setTimeout(start, 2000); return; }
    const es = new EventSource(base + 'api/events');
    es.onmessage = ev => { try { render(JSON.parse(ev.data)); } catch (e) { /* ignore */ } };
    loadPeers(false);
  }
  start();
})();
