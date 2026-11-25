// The "this computer is being controlled" banner: a small window that stays
// on top while anyone is connected, with who it is and a way to end it.
(function () {
  'use strict';
  const base = location.pathname.replace(/[^/]*$/, '');
  const W = {
    en: {one: '{n} is connected to this computer', many: '{c} devices are connected to this computer', stop: 'Disconnect'},
    zh: {one: '{n} 正连接到这台电脑', many: '{c} 台设备正连接到这台电脑', stop: '断开'}
  };
  let csrf = '';
  function render(s) {
    const lang = s.lang || ((navigator.language || '').toLowerCase().startsWith('zh') ? 'zh' : 'en');
    const w = W[lang];
    document.documentElement.lang = lang === 'zh' ? 'zh-CN' : 'en';
    if (s.theme) document.documentElement.dataset.theme = s.theme; else delete document.documentElement.dataset.theme;
    const ss = s.incoming.sessions;
    document.getElementById('ov-t').textContent = ss.length === 1 ? w.one.replace('{n}', ss[0].label) : w.many.replace('{c}', ss.length);
    document.getElementById('ov-stop').textContent = w.stop;
  }
  document.getElementById('ov-stop').addEventListener('click', () => {
    fetch(base + 'api/disconnect', {method: 'POST', headers: {'Content-Type': 'application/json', 'X-RemoHop-CSRF': csrf}, body: '{"id":0}'});
  });
  fetch(base + 'api/hello', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: '{}'})
    .then(r => r.json()).then(j => {
      csrf = j.csrf; render(j.state);
      const es = new EventSource(base + 'api/events');
      es.onmessage = ev => { try { render(JSON.parse(ev.data)); } catch (e) { /* ignore */ } };
    });
})();
