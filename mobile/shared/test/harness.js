// Stand-ins for the gateway page's app.js (window.SD) and desktop.js (its
// desktop WebSocket and canvas listeners), for phone_test.mjs.
'use strict';
window.__leaked = 0;
window.__api = [];
window.SD = {
  cur: () => 'c1',
  errText: e => (e && e.message) || String(e),
  json: async (method, path, body, params) => {
    window.__api.push({method, path, body, params});
    if (path === 'clip/get') return {kind: 'text', text: 'remote text'};
    if (path === 'info') return {info: {os: 'linux'}};
    return {ok: true};
  },
};
const canvas = document.getElementById('d-canvas');
// desktop.js listens on the canvas; touch must never get here.
for (const t of ['pointerdown', 'pointermove', 'pointerup']) {
  canvas.addEventListener(t, e => { if (e.pointerType === 'touch') window.__leaked++; });
}
window.__openDesktop = () => {
  const s = new WebSocket('ws://' + location.host + '/TOKEN/api/v1/desktop/ws?t=abc&c=c1');
  window.__deskSock = s;
  return s;
};
window.__openDesktop();
