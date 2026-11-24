// files.js: the Files tab. Names from the host go to the DOM with textContent
// only; operations use the raw name from the listing.
'use strict';
(function () {
  const $ = id => document.getElementById(id);
  const SD = window.SD;
  let dir = '', sep = '/', entries = [], conn = null;

  const join = (d, n) => (d.endsWith('/') || d.endsWith('\\')) ? d + n : d + sep + n;

  // Only the newest listing is shown: an older request that answers late
  // (opening the tab lists the home directory) must not replace it.
  let listSeq = 0;
  async function list(path) {
    const seq = ++listSeq;
    SD.showErr('f-err');
    try {
      const j = await SD.json('GET', 'files/list', undefined, {c: conn, path: path || ''});
      if (seq !== listSeq) return;
      dir = j.path; sep = j.sep; entries = j.entries; $('f-path').value = dir;
      $('f-up').disabled = !j.parent; $('f-up').dataset.parent = j.parent;
      render();
    } catch (e) { if (seq === listSeq) SD.showErr('f-err', e); }
  }

  // btn makes a row button labelled by key, with an aria-label (key + the
  // file's name) that says which file it acts on.
  function btn(key, fn, aria, name) {
    const b = document.createElement('button');
    b.type = 'button'; SD.say(b, key);
    if (aria) SD.sayAttr(b, 'aria-label', aria, {name});
    b.addEventListener('click', fn);
    return b;
  }

  function render() {
    const body = $('f-body');
    body.textContent = '';
    $('f-empty').hidden = entries.length > 0;
    for (const e of entries) {
      const tr = document.createElement('tr');
      const name = document.createElement('td'); name.className = 'name';
      const label = (e.dir ? '\u{1F4C1} ' : '') + e.display + (e.link ? ' → ' + e.target : '');
      if (e.dir && !e.bad) {
        const b = document.createElement('button'); b.type = 'button'; b.className = 'link';
        b.textContent = label; b.addEventListener('click', () => list(join(dir, e.name)));
        name.appendChild(b);
      } else name.textContent = label;
      const size = document.createElement('td'); size.className = 'num'; size.textContent = e.dir ? '' : SD.fmtSize(e.size);
      const mt = document.createElement('td'); mt.className = 'mtime'; mt.textContent = e.mtime ? new Date(e.mtime).toLocaleString(SD.i18n.locale()) : '';
      const act = document.createElement('td'); act.className = 'act';
      if (!e.bad) {
        if (!e.dir) act.appendChild(btn('f.download', () => download(e), 'f.download.aria', e.display));
        act.appendChild(btn('f.rename', () => rename(e), 'f.rename.aria', e.display));
        act.appendChild(btn('f.delete', () => del(e), 'f.delete.aria', e.display));
      }
      tr.append(name, size, mt, act);
      body.appendChild(tr);
    }
  }

  async function download(e) {
    SD.showErr('f-err');
    try {
      const t = await SD.ticket('dl');
      const a = document.createElement('a');
      a.href = SD.url('files/download', {t, c: conn, path: join(dir, e.name)}).toString();
      a.download = '';
      document.body.appendChild(a); a.click(); a.remove();
    } catch (er) { SD.showErr('f-err', er); }
  }

  async function rename(e) {
    const t = SD.t;
    const r = await SD.ask({title: t('f.rename'), message: t('f.rename.msg', {name: e.display}), input: {value: e.name, label: t('f.rename.input')},
      buttons: [{label: t('cancel'), value: false}, {label: t('f.rename'), value: true, primary: true}]});
    if (r.value !== true || !r.text || r.text === e.name) return;
    try { await SD.json('POST', 'files/rename', {c: conn, from: join(dir, e.name), to: join(dir, r.text)}); await list(dir); }
    catch (er) { SD.showErr('f-err', er); }
  }

  async function del(e) {
    let recursive = false;
    const t = SD.t;
    const msg = t(e.dir ? 'f.delete.dir' : 'f.delete.file', {name: e.display});
    if (!await SD.confirm(t('f.delete'), msg, t('f.delete'))) return;
    try {
      try { await SD.json('POST', 'files/delete', {c: conn, path: join(dir, e.name)}); }
      catch (er) {
        if (e.dir && er.code === 'not_empty') {
          if (!await SD.confirm(t('f.notempty.title'), t('f.notempty.msg', {name: e.display}), t('f.notempty.ok'))) return;
          recursive = true;
          await SD.json('POST', 'files/delete', {c: conn, path: join(dir, e.name), recursive});
        } else throw er;
      }
      await list(dir);
    } catch (er) { SD.showErr('f-err', er); }
  }

  async function mkdir() {
    const t = SD.t;
    const r = await SD.ask({title: t('f.mkdir'), input: {value: '', label: t('f.mkdir.input')},
      buttons: [{label: t('cancel'), value: false}, {label: t('f.create'), value: true, primary: true}]});
    if (r.value !== true || !r.text) return;
    try { await SD.json('POST', 'files/mkdir', {c: conn, path: join(dir, r.text)}); await list(dir); }
    catch (er) { SD.showErr('f-err', er); }
  }

  // ---- uploads ----
  let queue = [], running = false, policyAll = null;
  function enqueue(files) {
    for (const f of files) queue.push(f);
    if (!running) pump();
  }
  async function pump() {
    running = true; policyAll = null;
    while (queue.length) {
      const f = queue.shift();
      const clash = entries.some(e => e.name === f.name);
      let overwrite = false;
      if (clash) {
        let choice = policyAll;
        if (!choice) {
          const t = SD.t;
          const r = await SD.ask({title: t('f.exists.title'), message: t('f.exists.msg', {name: f.name}), all: queue.length > 0,
            buttons: [{label: t('f.skip'), value: 'skip'}, {label: t('f.keep'), value: 'rename', primary: true}, {label: t('f.replace'), value: 'replace'}]});
          choice = r.value || 'skip';
          if (r.all) policyAll = choice;
        }
        if (choice === 'skip') continue;
        overwrite = choice === 'replace';
      }
      await upload(f, overwrite);
    }
    running = false;
    await list(dir);
  }
  function upload(f, overwrite) {
    return new Promise(resolve => {
      const li = document.createElement('li');
      const t = document.createElement('div'); SD.say(t, 'f.up.item', {name: f.name, size: SD.fmtSize(f.size)});
      const p = document.createElement('progress'); p.max = Math.max(1, f.size); p.value = 0;
      SD.sayAttr(p, 'aria-label', 'f.up.aria', {name: f.name});
      const s = document.createElement('div'); s.className = 'hint'; SD.say(s, 'f.up.running');
      li.append(t, p, s); $('f-uploads').appendChild(li);
      const xhr = new XMLHttpRequest();
      xhr.open('POST', SD.url('files/upload', {c: conn, dir, name: f.name, overwrite: overwrite ? '1' : '0'}));
      const h = SD.headers();
      for (const k of Object.keys(h)) xhr.setRequestHeader(k, h[k]);
      xhr.setRequestHeader('Content-Type', 'application/octet-stream');
      xhr.upload.onprogress = ev => { p.value = ev.loaded; };
      xhr.onload = () => {
        let j = {}; try { j = JSON.parse(xhr.responseText); } catch (e) { /* ignore */ }
        if (xhr.status === 200) {
          p.value = p.max;
          if (j.renamed) SD.say(s, 'f.up.renamed', {name: j.name}); else SD.say(s, 'f.up.done');
        } else {
          const er = j.error || {};
          if (er.code === 'feature_off' || er.code === 'host_refused') SD.say(s, 'f.up.refused', {msg: er.message || xhr.statusText});
          else SD.say(s, 'f.up.failed', {msg: er.code ? SD.errVar(er) : (er.message || xhr.statusText)});
          s.className = 'err';
        }
        resolve();
      };
      xhr.onerror = () => { SD.say(s, 'f.up.neterr'); s.className = 'err'; resolve(); };
      xhr.send(f);
    });
  }

  $('f-up').addEventListener('click', () => list($('f-up').dataset.parent));
  $('f-go').addEventListener('click', () => list($('f-path').value));
  $('f-path').addEventListener('keydown', e => { if (e.key === 'Enter') list($('f-path').value); });
  $('f-refresh').addEventListener('click', () => list(dir));
  $('f-mkdir').addEventListener('click', mkdir);
  $('f-upload').addEventListener('click', () => $('f-file').click());
  $('f-file').addEventListener('change', () => { enqueue(Array.from($('f-file').files)); $('f-file').value = ''; });
  const drop = $('f-drop');
  for (const ev of ['dragenter', 'dragover']) drop.addEventListener(ev, e => { e.preventDefault(); drop.classList.add('over'); });
  for (const ev of ['dragleave', 'drop']) drop.addEventListener(ev, e => { e.preventDefault(); drop.classList.remove('over'); });
  drop.addEventListener('drop', e => { if (e.dataTransfer && e.dataTransfer.files.length) enqueue(Array.from(e.dataTransfer.files)); });

  // Dates follow the page's language.
  addEventListener('remohop:lang', () => { if (entries.length) render(); });

  SD.tabs.files = {
    show(c) {
      if (c !== conn) { conn = c; dir = ''; entries = []; render(); }
      conn = c;
      list(dir);
    },
  };
})();
