// RemoHop quick support: the page served by `remohop quick` on 127.0.0.1.
// Everything shown comes from the state the local process streams; text
// from the other side (names) is only ever set as textContent.
(function () {
  'use strict';

  var T = {
    en: {
      title: 'RemoHop Quick Support',
      other_lang: '中文',
      lang_label: 'Language: switch to Chinese',
      your_id: 'Your ID',
      password: 'Password',
      copy: 'Copy',
      copied: 'Copied',
      copy_manual: 'Selected: press Ctrl+C (⌘C on a Mac) to copy',
      new_password: 'Create a new password',
      creds_hint: 'Give the ID and the password to the person helping you, and to nobody else. You will be asked before anyone can connect.',
      waiting_id: 'Getting an ID…',
      connected_title: 'Who is connected',
      nobody: 'Nobody is connected.',
      disconnect: 'Disconnect',
      allow_title: 'A helper you allow may',
      opt_screen: 'See your screen',
      opt_input: 'Use your mouse and keyboard',
      opt_files: 'Transfer files and use the clipboard',
      opt_advanced: 'Run commands and reach your network',
      opt_advanced_hint: 'Terminal, port forwarding, VPN, serial and USB devices. Leave this off unless your helper asks for it and you trust them.',
      details: 'Connection details',
      stop: 'Stop sharing',
      close_hint: 'Closing this window also stops sharing.',
      st_connecting: 'Connecting to the server…',
      st_slow: 'Still trying to reach the server {server}. Check that this computer is connected to the internet.',
      st_ready: 'Ready. Waiting for your helper to connect.',
      st_offline: 'Lost contact with the server; trying again. New helpers cannot connect until it is back.',
      st_offline_live: 'Lost contact with the server; trying again. The people already connected are not affected.',
      st_live_1: 'Your screen is being shared with 1 person.',
      st_live_n: 'Your screen is being shared with {n} people.',
      st_asking: 'Someone is asking to connect. Answer below.',
      st_setup: 'Not started yet.',
      st_error: 'RemoHop could not start.',
      st_stopped: 'Sharing has stopped.',
      ask_title: '{who} wants to connect',
      ask_someone: 'A device',
      ask_claims: 'Calls itself',
      ask_known: 'Known here as',
      ask_key: 'Device key',
      ask_from: 'Address',
      ask_hint: 'Allow only if you are expecting this person right now. The name is whatever the other side typed; if you are unsure, ask them to read you their device key.',
      ask_left: 'Refused automatically in {s} s.',
      ask_action_title: '{who} asks for your permission',
      ask_action: 'Wants to',
      ask_action_hint: 'This helper is already connected with limited rights and needs your OK for this one action.',
      allow: 'Allow',
      deny: 'Deny',
      via_lan: 'same network',
      via_direct: 'direct connection',
      via_relay: 'through the relay',
      since_s: 'connected {s} s ago',
      since_m: 'connected {m} min ago',
      net_server: 'Registered with the server {server}.',
      net_noserver: 'Not registered with the server {server} right now.',
      net_direct_relay: 'Helpers connect straight to this computer when the networks allow it. When they do not, the connection goes through the server\'s relay if it offers one. It stays end-to-end encrypted either way: the server cannot see your screen.',
      net_direct_only: 'Helpers connect straight to this computer. Relayed connections are switched off on this computer, so a helper on a restrictive network may not get through.',
      net_ws: 'This network only lets web traffic out, so helpers will come through the server\'s relay. The connection stays end-to-end encrypted: the server cannot see your screen.',
      net_approve: 'The password alone lets nobody in: every connection waits for you to press Allow, and is refused after {s} seconds without an answer.',
      net_device: 'This computer\'s name: {name}',
      setup_title: 'One step before you start',
      setup_text: 'Your helper reaches this computer through a small meeting-point server (a "rendezvous server") that they or their company run; RemoHop has no server of its own. Ask your helper for its address, which looks like support.example.com, and type it here. You only do this once.',
      setup_why: 'Why is this needed?',
      setup_why_text: 'The server only introduces the two computers to each other, so your helper can find this one by its ID. It cannot see your screen: the connection is end-to-end encrypted. If your helper is on the same network as you, they can also connect to this computer\'s address directly; they will tell you if so.',
      err_no_server: 'Enter the server address.',
      err_bad_server: '"{server}" is not a server address. It looks like support.example.com or support.example.com:21116.',
      setup_label: 'Server address',
      setup_go: 'Continue',
      err_running_title: 'RemoHop is already running on this computer',
      err_running_text: 'It runs in the background (as a service or in a terminal), so quick support cannot start a second copy.',
      err_running_id: 'Its ID is {id}. Whoever set it up knows its password.',
      err_start_title: 'RemoHop could not start',
      err_start_text: 'Close this window and try again. If it keeps happening, send the text below to the person helping you.',
      stopped_title: 'Sharing has stopped',
      stopped_text: 'Nobody can connect to this computer any more. You can close this window.',
      gone_title: 'RemoHop is no longer running',
      gone_text: 'Sharing has stopped. You can close this window. To share again, open RemoHop again.',
      title_asking: '● Connection request',
      failed: 'That did not work: {msg}'
    },
    zh: {
      title: 'RemoHop 快速协助',
      other_lang: 'English',
      lang_label: '语言：切换到英文',
      your_id: '本机 ID',
      password: '密码',
      copy: '复制',
      copied: '已复制',
      copy_manual: '已选中：按 Ctrl+C（Mac 上按 ⌘C）复制',
      new_password: '换一个新密码',
      creds_hint: '把 ID 和密码告诉帮助你的人，不要告诉其他人。任何人连接之前，都会先征得你的同意。',
      waiting_id: '正在获取 ID…',
      connected_title: '当前连接',
      nobody: '没有人连接。',
      disconnect: '断开',
      allow_title: '你允许的协助者可以',
      opt_screen: '看到你的屏幕',
      opt_input: '使用你的鼠标和键盘',
      opt_files: '传输文件、使用剪贴板',
      opt_advanced: '运行命令、访问你的网络',
      opt_advanced_hint: '终端、端口转发、VPN、串口和 USB 设备。除非协助者提出需要并且你信任对方，否则不要打开。',
      details: '连接详情',
      stop: '停止共享',
      close_hint: '关闭此窗口也会停止共享。',
      st_connecting: '正在连接服务器…',
      st_slow: '仍在尝试连接服务器 {server}。请检查这台电脑是否已联网。',
      st_ready: '已就绪，等待协助者连接。',
      st_offline: '与服务器的连接中断，正在重试。恢复之前，新的协助者无法连接。',
      st_offline_live: '与服务器的连接中断，正在重试。已连接的人不受影响。',
      st_live_1: '你的屏幕正在共享给 1 个人。',
      st_live_n: '你的屏幕正在共享给 {n} 个人。',
      st_asking: '有人请求连接，请在下方答复。',
      st_setup: '尚未开始。',
      st_error: 'RemoHop 无法启动。',
      st_stopped: '共享已停止。',
      ask_title: '{who} 请求连接',
      ask_someone: '一台设备',
      ask_claims: '自称',
      ask_known: '本机记录的名称',
      ask_key: '设备密钥',
      ask_from: '地址',
      ask_hint: '只有当你此刻正在等这个人连接时才允许。名称是对方自己填写的；如果不确定，请让对方念出他的设备密钥进行核对。',
      ask_left: '{s} 秒后自动拒绝。',
      ask_action_title: '{who} 请求你的许可',
      ask_action: '想要执行',
      ask_action_hint: '该协助者已经连接，但权限受限，这一项操作需要你的同意。',
      allow: '允许',
      deny: '拒绝',
      via_lan: '同一网络',
      via_direct: '直接连接',
      via_relay: '经中继',
      since_s: '已连接 {s} 秒',
      since_m: '已连接 {m} 分钟',
      net_server: '已在服务器 {server} 注册。',
      net_noserver: '当前未在服务器 {server} 注册。',
      net_direct_relay: '网络允许时，协助者会直接连接到这台电脑；不允许时，如果服务器提供中继，连接会经由中继。无论哪种方式都是端到端加密的，服务器看不到你的屏幕。',
      net_direct_only: '协助者直接连接到这台电脑。这台电脑已关闭中继连接，因此处在受限网络中的协助者可能无法连接。',
      net_ws: '当前网络只允许网页流量出站，协助者将经由服务器的中继连接。连接仍是端到端加密的，服务器看不到你的屏幕。',
      net_approve: '只有密码进不来：每个连接都要等你点“允许”，{s} 秒内没有答复就会被拒绝。',
      net_device: '这台电脑的名称：{name}',
      setup_title: '开始之前还差一步',
      setup_text: '协助者要通过一台小小的“碰头点”服务器（汇合服务器）才能找到这台电脑，这台服务器由协助者或其公司运行；RemoHop 本身不提供服务器。请向协助者索取它的地址（类似 support.example.com），填在这里。只需填写一次。',
      setup_label: '服务器地址',
      setup_go: '继续',
      setup_why: '为什么需要它？',
      setup_why_text: '这台服务器只负责把两台电脑介绍给对方，让协助者能凭 ID 找到你的电脑。它看不到你的屏幕：连接是端到端加密的。如果协助者和你在同一个网络中，他们也可以直接连接这台电脑的地址；需要时他们会告诉你。',
      err_running_title: 'RemoHop 已经在这台电脑上运行',
      err_running_text: '它正在后台运行（作为系统服务或在终端里），因此快速协助无法再启动一份。',
      err_running_id: '它的 ID 是 {id}。密码请询问为你设置它的人。',
      err_start_title: 'RemoHop 无法启动',
      err_start_text: '请关闭此窗口后重试。如果问题反复出现，请把下面的文字发给帮助你的人。',
      stopped_title: '共享已停止',
      stopped_text: '现在没有人能连接这台电脑了，可以关闭此窗口。',
      gone_title: 'RemoHop 已不在运行',
      gone_text: '共享已停止，可以关闭此窗口。如需再次共享，请重新打开 RemoHop。',
      title_asking: '● 连接请求',
      failed: '操作没有成功：{msg}',
      err_no_server: '请填写服务器地址。',
      err_bad_server: '“{server}”不是有效的服务器地址，应类似 support.example.com 或 support.example.com:21116。'
    }
  };

  var $ = function (id) { return document.getElementById(id); };
  var lang = 'en', csrf = '', state = null, gone = false, goneTimer = 0, es = null;
  var askEls = {};       // request n -> element
  var sessionSig = '';

  function t(key, vars) {
    var s = (T[lang] && T[lang][key]) || T.en[key] || key;
    return s.replace(/\{(\w+)\}/g, function (_, k) { return vars && vars[k] != null ? String(vars[k]) : ''; });
  }

  function stored() { try { return localStorage.getItem('remohop-quick-lang'); } catch (e) { return null; } }

  // ?lang= (tests), then what this computer last picked (the process saves
  // it, since this page's storage does not outlive the run's port), then
  // this browser's own memory, then the browser's language.
  function pickLang(server) {
    var q = new URLSearchParams(location.search).get('lang');
    var cand = [q, server, stored()].concat(navigator.languages || [navigator.language || '']);
    for (var i = 0; i < cand.length; i++) {
      var c = String(cand[i] || '').toLowerCase();
      if (c.indexOf('zh') === 0) return 'zh';
      if (c.indexOf('en') === 0) return 'en';
    }
    return 'en';
  }

  function applyLang() {
    document.documentElement.lang = lang === 'zh' ? 'zh-CN' : 'en';
    var els = document.querySelectorAll('[data-t]');
    for (var i = 0; i < els.length; i++) els[i].textContent = t(els[i].getAttribute('data-t'));
    $('lang').textContent = t('other_lang');
    $('lang').setAttribute('lang', lang === 'zh' ? 'en' : 'zh-CN');
    $('lang').setAttribute('aria-label', t('lang_label'));
    for (var n in askEls) { askEls[n].remove(); }
    askEls = {}; sessionSig = '';
    render();
  }

  function api(path, body) {
    return fetch('api/' + path, {
      method: 'POST', cache: 'no-store', keepalive: path === 'bye',
      headers: { 'Content-Type': 'application/json', 'X-RemoHop-CSRF': csrf },
      body: JSON.stringify(body || {})
    }).then(function (r) {
      return r.json().then(function (j) {
        if (!r.ok) {
          var e = new Error((j.error && j.error.message) || ('HTTP ' + r.status));
          e.code = j.error && j.error.code;
          throw e;
        }
        return j;
      });
    });
  }

  function act(path, body) {
    $('actionerr').textContent = '';
    return api(path, body).catch(function (e) { $('actionerr').textContent = t('failed', { msg: e.message }); });
  }

  function el(tag, cls, text) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text != null) e.textContent = text;
    return e;
  }

  function who(x) { return x.name || x.device || t('ask_someone'); }

  function renderRequests(list) {
    var seen = {};
    list.forEach(function (r) {
      seen[r.n] = true;
      var card = askEls[r.n];
      if (!card) {
        card = el('div', 'ask');
        card.setAttribute('role', 'group');
        card.appendChild(el('h2', '', t(r.action ? 'ask_action_title' : 'ask_title', { who: r.grant || who(r) })));
        var dl = el('dl');
        var row = function (k, v, mono) {
          if (!v) return;
          dl.appendChild(el('dt', '', t(k)));
          dl.appendChild(el('dd', mono ? 'mono' : '', v));
        };
        row('ask_action', r.action, true);
        row('ask_known', r.name);
        row('ask_claims', r.device);
        row('ask_key', r.identity, true);
        row('ask_from', r.from, true);
        card.appendChild(dl);
        card.appendChild(el('p', 'hint', t(r.action ? 'ask_action_hint' : 'ask_hint')));
        var buttons = el('div', 'buttons');
        var deny = el('button', 'deny', t('deny'));
        var allow = el('button', 'primary allow', t('allow'));
        deny.type = allow.type = 'button';
        // A card that appears under the pointer must not turn a click
        // meant for something else into "Allow".
        allow.disabled = true;
        setTimeout(function () { allow.disabled = false; }, 800);
        var answer = function (ok) {
          deny.disabled = allow.disabled = true;
          act('decide', { n: r.n, allow: ok });
        };
        deny.addEventListener('click', function () { answer(false); });
        allow.addEventListener('click', function () { answer(true); });
        buttons.appendChild(deny);
        buttons.appendChild(allow);
        card.appendChild(buttons);
        card.appendChild(el('p', 'left'));
        $('requests').appendChild(card);
        askEls[r.n] = card;
      }
      card.querySelector('.left').textContent = t('ask_left', { s: r.left });
    });
    for (var n in askEls) {
      if (!seen[n]) { askEls[n].remove(); delete askEls[n]; }
    }
  }

  function via(path) {
    if (path === 'lan') return t('via_lan');
    if (String(path).indexOf('relayed') === 0) return t('via_relay');
    return t('via_direct');
  }

  function renderSessions(list) {
    var sig = list.map(function (s) { return s.id; }).join(',');
    var ul = $('sessions');
    if (sig !== sessionSig) {
      sessionSig = sig;
      ul.textContent = '';
      list.forEach(function (s) {
        var li = el('li');
        li.dataset.id = s.id;
        var w = el('div', 'who');
        w.appendChild(el('div', 'name', who(s)));
        w.appendChild(el('div', 'meta mono', s.identity));
        w.appendChild(el('div', 'meta dur'));
        li.appendChild(w);
        var b = el('button', 'small danger', t('disconnect'));
        b.type = 'button';
        b.addEventListener('click', function () { b.disabled = true; act('disconnect', { id: s.id }); });
        li.appendChild(b);
        ul.appendChild(li);
      });
    }
    list.forEach(function (s) {
      var li = ul.querySelector('li[data-id="' + s.id + '"]');
      if (!li) return;
      var d = s.seconds < 60 ? t('since_s', { s: s.seconds }) : t('since_m', { m: Math.floor(s.seconds / 60) });
      li.querySelector('.dur').textContent = [s.from, via(s.path), d].filter(Boolean).join(' · ');
    });
    $('nosessions').hidden = list.length > 0;
  }

  function setStatus(cls, text) {
    $('status').className = 'status ' + cls;
    $('statustext').textContent = text;
  }

  function message(title, text, detail) {
    $('msgtitle').textContent = title;
    $('msgtext').textContent = text;
    $('msgdetail').textContent = detail || '';
    $('message').hidden = false;
  }

  function render() {
    var s = state;
    if (!s) return;
    if (gone) s = { phase: 'gone', requests: [], sessions: [], options: {} };
    var running = s.phase === 'ready' || s.phase === 'connecting';
    $('ready').hidden = !running;
    $('setup').hidden = s.phase !== 'setup';
    $('message').hidden = true;
    $('stop').hidden = $('closehint').hidden = !(running || s.phase === 'setup');
    $('version').textContent = s.version ? 'RemoHop ' + s.version : '';
    renderRequests(s.requests || []);
    renderSessions(s.sessions || []);
    document.title = (s.requests && s.requests.length ? t('title_asking') + ' · ' : '') + t('title');

    if (s.phase === 'gone') {
      setStatus('bad', t('st_stopped'));
      message(t('gone_title'), t('gone_text'));
      return;
    }
    if (s.phase === 'stopped') {
      setStatus('', t('st_stopped'));
      message(t('stopped_title'), t('stopped_text'));
      return;
    }
    if (s.phase === 'error') {
      setStatus('bad', t('st_error'));
      if (s.error_code === 'already_running') {
        message(t('err_running_title'), t('err_running_text'), s.other_id ? t('err_running_id', { id: s.other_id }) : '');
      } else {
        message(t('err_start_title'), t('err_start_text'), s.error);
      }
      return;
    }
    if (s.phase === 'setup') {
      setStatus('', t('st_setup'));
      return;
    }

    var id = $('id'), pw = $('pw');
    id.textContent = s.id || t('waiting_id');
    id.classList.toggle('pending', !s.id);
    pw.textContent = s.password || '';
    $('copyid').disabled = !s.id;
    $('opt-input').checked = !!s.options.input;
    $('opt-files').checked = !!s.options.files;
    $('opt-advanced').checked = !!s.options.advanced;

    var n = s.sessions.length;
    if (s.requests.length) setStatus('wait', t('st_asking'));
    else if (s.phase === 'connecting') setStatus('wait', s.waited > 12 ? t('st_slow', { server: s.server }) : t('st_connecting'));
    else if (!s.online && s.server) setStatus('bad', t(n ? 'st_offline_live' : 'st_offline'));
    else if (n) setStatus('live', n === 1 ? t('st_live_1') : t('st_live_n', { n: n }));
    else setStatus('ok', t('st_ready'));

    var net = $('net');
    net.textContent = '';
    var add = function (text) { net.appendChild(el('li', '', text)); };
    if (s.server) add(t(s.online ? 'net_server' : 'net_noserver', { server: s.server }));
    if (s.transport === 'websocket') add(t('net_ws'));
    else add(t(s.relay ? 'net_direct_relay' : 'net_direct_only'));
    add(t('net_approve', { s: s.ask_timeout }));
    if (s.device) add(t('net_device', { name: s.device }));
  }

  function update(s) {
    var was = state && state.phase;
    state = s;
    render();
    if (s.phase === 'stopped' && was !== 'stopped') {
      if (es) es.close();
      // Works where the window was opened for this page alone (the app
      // window); elsewhere the message stays.
      setTimeout(function () { try { window.close(); } catch (e) { /* stays open */ } }, 1500);
    }
  }

  function listen() {
    es = new EventSource('api/events');
    es.onmessage = function (ev) {
      clearTimeout(goneTimer); goneTimer = 0;
      if (gone) { gone = false; }
      try { update(JSON.parse(ev.data)); } catch (e) { /* ignore a torn event */ }
    };
    es.onerror = function () {
      // EventSource retries by itself; say so only when it stays down.
      if (!goneTimer && !(state && state.phase === 'stopped')) {
        goneTimer = setTimeout(function () { gone = true; render(); }, 4000);
      }
    };
  }

  // Copy: the clipboard API, else the older copy command, else the text is
  // selected and the button says how to copy it (never nothing at all).
  function copy(btn, el) {
    var text = el.textContent;
    var say = function (k) {
      btn.textContent = t(k);
      setTimeout(function () { btn.textContent = t('copy'); }, k === 'copied' ? 1500 : 4000);
    };
    var select = function () {
      var r = document.createRange();
      r.selectNodeContents(el);
      var sel = window.getSelection();
      sel.removeAllRanges(); sel.addRange(r);
      return sel;
    };
    var legacy = function () {
      var ok = false;
      try { select(); ok = document.execCommand('copy'); } catch (e) { ok = false; }
      if (ok) { window.getSelection().removeAllRanges(); say('copied'); } else { select(); say('copy_manual'); }
    };
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(function () { say('copied'); }, legacy);
    } else legacy();
  }

  $('copyid').addEventListener('click', function () { copy(this, $('id')); });
  $('copypw').addEventListener('click', function () { copy(this, $('pw')); });
  $('regen').addEventListener('click', function () { act('regenerate'); });
  $('stop').addEventListener('click', function () { $('stop').disabled = true; act('stop'); });
  ['input', 'files', 'advanced'].forEach(function (name) {
    $('opt-' + name).addEventListener('change', function () { act('option', { name: name, on: this.checked }); });
  });
  $('setupform').addEventListener('submit', function (ev) {
    ev.preventDefault();
    $('setuperr').textContent = '';
    var server = $('server').value.trim();
    api('configure', { server: server }).catch(function (e) {
      // English shows the process's own words; Chinese has its own.
      // In this page's words in either language; the process's own text
      // only for a case it has no words for.
      var k = 'err_' + e.code;
      $('setuperr').textContent = T[lang][k] ? t(k, { server: server }) : e.message;
    });
  });
  $('lang').addEventListener('click', function () {
    lang = lang === 'zh' ? 'en' : 'zh';
    try { localStorage.setItem('remohop-quick-lang', lang); } catch (e) { /* not remembered */ }
    // The process keeps it for the next start and its own dialogs.
    if (csrf) api('lang', { lang: lang }).catch(function () {});
    applyLang();
  });
  // Closing the window is how most people will stop sharing: tell the
  // process, which stops unless a page comes back within moments (reload).
  window.addEventListener('pagehide', function () {
    if (csrf && state && state.phase !== 'stopped' && !gone) api('bye').catch(function () {});
  });

  fetch('api/hello', { method: 'POST', cache: 'no-store', headers: { 'Content-Type': 'application/json' }, body: '{}' })
    .then(function (r) { return r.json(); })
    .then(function (h) {
      csrf = h.csrf;
      lang = pickLang(h.lang);
      state = h.state;
      applyLang();
      listen();
    })
    .catch(function () {
      lang = pickLang('');
      state = { phase: 'gone' }; gone = true;
      applyLang();
    });
})();
