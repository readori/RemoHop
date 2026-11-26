// i18n.js: the web client's two languages (English, Simplified Chinese).
// Loaded before the other scripts. Static text carries data-i18n (text),
// data-i18n-rich (text with <strong>, <em> and <code> from this table
// only), or data-i18n-<attr> (placeholder, aria-label, title, alt); text
// set from scripts goes through say()/sayAttr(), which record the key so a
// language switch re-renders it in place, without reloading the session.
//
// The language is, in order: ?lang= in the address (the mobile apps pass
// their setting this way), the choice saved by the header switch, the
// browser's language (Chinese for any zh-*, otherwise English).
//
// Host-supplied text only ever arrives as a variable and is set with
// textContent.
'use strict';
(function () {
  const D = {
    en: {
      'title': 'RemoHop web client',
      'skip': 'Skip to content',
      'brand.sub': 'web client',
      'lang.group': 'Language',
      'raw': '{s}',
      'pair2': '{a}\n{b}',
      'join': '{a}{b}',

      // header and session
      'host.label': 'Connected host',
      'host.newconn': 'Connect another',
      'host.disconnect': 'Disconnect',
      'host.logout': 'Sign out',
      'loading': 'Loading…',
      'gw.unreachable': 'Cannot reach the gateway: {msg}',
      'gw.lost': 'The gateway is not answering.',
      'gw.retry': 'Retry',
      'gw.ended.login': 'Your gateway session ended. Log in again.',
      'gw.ended.reload': 'This page was away for more than 15 minutes (the computer slept, or the browser paused the tab), so the gateway closed its connections. Reload to start again: computers this gateway is paired with connect without a password.',
      'gw.reload': 'Reload',
      'sess.ended': 'The session with {target} ended.',
      'sess.reconnecting': 'The connection to {target} dropped. Reconnecting…',
      'sess.reapproval': 'Reconnecting to {target}: waiting for the host\'s owner to approve…',
      'sess.lost': 'The connection to {target} was lost and could not be re-established: {msg} Connect again.',
      'sess.label': 'Connected to',
      'sess.alias': '{alias} ({target})',
      'sess.firstkey': 'First connection to this host: its identity key ({fp}) is now pinned.',

      // login
      'login.h': 'Gateway login',
      'login.hint': 'This gateway requires its own password (the one in its <code>--password-file</code>), not a host password.',
      'login.pass': 'Gateway password',
      'login.btn': 'Log in',

      // connect
      'connect.h': 'Connect to a host',
      'connect.lead': 'Enter the host\'s 9-digit ID, a name you paired, or its address.',
      'connect.target': 'Host ID, name or host:port',
      'connect.pass': 'Host password',
      'connect.passhint': 'Leave empty to use what this device remembers (a pairing or a saved password).',
      'connect.pair': 'Remember this computer (no password next time)',
      'connect.pair.hint': 'Pairs with it when it allows that; otherwise saves its password on this device.',
      'connect.pwsaved': 'Connected. This computer does not pair, so its password is saved on this device and used next time (until it changes).',
      'err.saved_password_rejected': 'The saved password no longer works (the computer may have a new one). Enter its password.',
      'peer.opt.saved': '{label} (password saved)', 'peer.showpw': 'Show saved password', 'peer.forgetpw': 'Delete saved password',
      'peer.pw.title': 'Saved password of {label}', 'peer.pw.copy': 'Copy', 'peer.forgetpw.done': 'Saved password deleted.',
      'pw.show': 'Show password', 'pw.hide': 'Hide password',
      'connect.btn': 'Connect',
      'connect.connecting': 'Connecting…',
      'connect.dialing': 'Connecting… {notice}',
      'connect.waiting': 'Waiting for the host\'s owner to approve this connection… (you can cancel)',
      'connect.canceled': 'Canceled.',
      'connect.failed': 'Connection failed.',
      'peers.h': 'Remembered: no password needed',
      'peers.recent.h': 'Recent',
      'peer.ready': 'Connects without a password',
      'peer.needspw': 'Needs the password',
      'peer.sub.alias': '{target} · {state}',
      'connect.pairfailed': 'Connected, but this computer could not be remembered. Its password will be needed next time.',
      'connect.pairrefused': 'This computer does not allow remembering it: you will need its password next time.',
      'peers.type': '(type below)',
      'peer.opt.paired': '{label} (paired)',
      'peer.opt.key': '{label} (key)',
      'peer.paired': 'paired',
      'peer.key': 'authorised by key',
      'peer.saved': 'saved',
      'peer.tag.paired': 'Paired',
      'peer.tag.key': 'Key',
      'peer.aria.ready': '{label}: connect without a password',
      'peer.aria.form': '{label}: enter its password',
      'peer.more': 'Actions for {label}',
      'peer.rename': 'Rename',
      'peer.rename.title': 'Rename {label}',
      'peer.rename.msg': 'A name for {target}. Leave it empty to show its ID or address.',
      'peer.rename.input': 'Name',
      'peer.forget': 'Forget this computer',
      'peer.forget.title': 'Forget {label}?',
      'peer.forget.msg': 'Its saved pairing and key are removed, and any open session to it ends. To connect again you will need its password.',
      'peer.forget.ok': 'Forget',
      'peer.connect': 'Connect',
      'disc.title': 'Disconnect from {host}?',
      'disc.msg': 'The desktop, terminal sessions and file transfers with this host end.',
      'disc.ok': 'Disconnect',

      'reason.bad_password': 'Wrong password.',
      'reason.host_locked_out': 'The host has locked this gateway out after too many wrong passwords. Wait a while, then try again.',
      'reason.denied': 'The host\'s owner denied the connection, or nobody answered the approval request in time.',
      'reason.host_busy': 'The host is busy with other logins. Try again in a moment.',
      'reason.pairing_rejected': 'The pairing saved on this gateway is no longer accepted by the host. Connect with the password.',
      'reason.key_rejected': 'The host does not accept this gateway\'s key.',
      'reason.password_login_disabled': 'The host does not allow password logins.',
      'reason.password_required': 'Enter this computer\'s password.',
      'reason.unknown_id': 'No host with that ID exists.',
      'reason.host_offline': 'That host is not online.',
      'reason.host_unreachable': 'The host did not answer.',
      'reason.rendezvous_rate_limited': 'The rendezvous server is limiting requests from this gateway. Try again in a moment.',
      'reason.no_path': 'No direct path to the host could be found.',
      'reason.host_key_changed': 'The host\'s identity key changed since it was first seen. This could be an impersonation attempt.',
      'reason.too_many_sessions': 'This gateway is at its limit of host sessions.',
      'reason.timeout': 'Timed out.',
      'reason.no_rendezvous': 'A 9-digit ID is looked up on a rendezvous server, a meeting point that introduces this gateway to your computers, and none is set up. Connect by the computer\'s address (host:port) instead, or set your server once on the computer running this page (remohop config set signal HOST[:PORT]), then try again.',
      'reason.rendezvous_unreachable': 'The rendezvous server cannot be reached. Check its address and the network, or connect by address (host:port).',
      'reason.signal_wrong_protocol': 'The address set as the rendezvous server is not a RemoHop rendezvous server. Check that setting, or connect to the host by its address (host:port).',
      'reason.signal_is_host': 'The address set as the rendezvous server is a RemoHop host, not a rendezvous server. Connect to that host by its address (host:port), and fix the rendezvous server setting.',
      'reason.remote_error': 'Could not connect to the host.',
      'reason.bad_target': 'Enter a 9-digit ID, a saved name, or an address such as 192.168.1.5:21118.',
      'connect.details': 'Details',

      // connection path (the gateway's own words, kept in English)
      'path.title': 'How the gateway reaches the host',
      'path.lan': 'lan',
      'path.direct': 'direct',
      'path.punched': 'punched',
      'path.relayed': 'relayed',
      'path.paired': ' · paired',
      'path.relayed.title': 'Relayed through the rendezvous server (still end-to-end encrypted, but slower)',
      'path.via': 'Connection path: {path}',

      // tabs
      'tabs.label': 'Features',
      'tab.desktop': 'Desktop',
      'tab.terminal': 'Terminal',
      'tab.files': 'Files',
      'tab.clipboard': 'Clipboard',
      'tab.info': 'Info',
      'refused': 'The host refused this: {msg}',

      // desktop
      'd.controls': 'Desktop controls',
      'd.viewonly': 'View only',
      'd.fit': 'Fit',
      'd.actual': '100%',
      'd.fs': 'Fullscreen',
      'd.exitfs': 'Exit full screen',
      'd.refresh': 'Refresh',
      'd.refresh.title': 'Ask the host for a fresh key frame',
      'd.cad.title': 'Send Ctrl+Alt+Del',
      'd.quality': 'Quality',
      'd.q.native': 'Native size',
      'd.q.max': 'Max {p}p',
      'd.monitor': 'Monitor',
      'd.monitor.all': 'All monitors',
      'd.monitor.n': 'Monitor {n} ({w}×{h})',
      'd.stats': 'Stats',
      'd.reconnect': 'Reconnect',
      'd.canvas': 'Remote desktop. Keyboard and mouse go to the remote machine while this has focus.',
      'd.connecting': 'Connecting…',
      'd.nocodec': 'This browser has no WebCodecs video decoder for AV1 or H.264. Use a recent Chrome, Edge, Firefox or Safari over HTTPS or localhost.',
      'd.disconnected': 'Disconnected from the gateway.',
      'd.refused': 'The host refused the desktop: {err}',
      'd.ended': 'Desktop ended: {err}',
      'd.paused': 'Remote screen unavailable: {s}',
      'd.closed': 'closed',
      'd.cannot': 'This browser cannot decode {codec}.',
      'd.noinput': 'The host has input switched off.',
      'd.askctl': 'Request control',
      'd.asking': 'Asking the host\u2019s owner for control\u2026',
      'd.askfailed': 'Control was not given: {msg}',
      'd.hud.latency': 'latency',
      'd.hud.na': 'n/a',
      'd.hud.rtt': 'rtt',
      'd.hud.host': 'host {fps} fps, dropped {n}',
      'd.hud.restarts': 'restarts {n}',
      'd.hud.skipped': 'skipped here {n}',
      'd.hud.encode': 'capture+encode',
      'd.hud.viewonly': 'view only',
      'd.hud.input': 'input on',

      // terminal
      't.controls': 'Terminal controls',
      't.session': 'Session',
      't.new': 'New session',
      't.kill': 'Kill session',
      't.copy': 'Copy',
      't.copy.title': 'Copy the selected text (Ctrl+Shift+C)',
      't.paste': 'Paste',
      't.paste.title': 'Paste (Ctrl+V or Ctrl+Shift+V)',
      't.smaller': 'Smaller text',
      't.bigger': 'Bigger text',
      't.keys': 'Extra keys',
      't.up': 'Up',
      't.down': 'Down',
      't.left': 'Left',
      't.right': 'Right',
      't.input': 'Terminal input',
      't.attached': '{name} (attached)',
      't.detached': '{name} (detached)',
      't.connecting': 'Connecting…',
      't.st.new': 'Session {name} (new)',
      't.st.reattached': 'Session {name} (re-attached)',
      't.st.exit': 'The shell exited (code {code}).',
      't.st.exitsig': 'The shell exited (code {code}, {signal}).',
      't.st.detached': 'Another client took over this session ({reason}).',
      't.st.refused': 'Refused by the host.',
      't.st.error': 'Error.',
      't.st.closed': 'Disconnected. Choose a session to attach again.',
      't.st.none': 'No sessions. Use New session.',
      't.st.noclip': 'Your browser blocked clipboard access; press Ctrl+V in the terminal.',
      't.kill.title': 'Kill session',
      't.kill.msg': 'End session "{name}" and its shell?',
      't.kill.ok': 'Kill',

      // files
      'f.controls': 'File controls',
      'f.up': 'Up',
      'f.path': 'Path',
      'f.go': 'Go',
      'f.refresh': 'Refresh',
      'f.mkdir': 'New folder',
      'f.upload': 'Upload…',
      'f.caption': 'Remote directory listing',
      'f.name': 'Name',
      'f.size': 'Size',
      'f.mtime': 'Modified',
      'f.actions': 'Actions',
      'f.empty': 'This folder is empty.',
      'f.drop': 'Drop files here to upload them to this folder. An existing file is never replaced without asking.',
      'f.uploads': 'Uploads',
      'f.download': 'Download',
      'f.download.aria': 'Download {name}',
      'f.rename': 'Rename',
      'f.rename.aria': 'Rename {name}',
      'f.delete': 'Delete',
      'f.delete.aria': 'Delete {name}',
      'f.rename.msg': 'New name for "{name}"',
      'f.rename.input': 'New name',
      'f.delete.dir': 'Delete the folder "{name}" and everything in it? This cannot be undone.',
      'f.delete.file': 'Delete "{name}"? This cannot be undone.',
      'f.notempty.title': 'Folder is not empty',
      'f.notempty.msg': 'Delete "{name}" including all its contents?',
      'f.notempty.ok': 'Delete everything',
      'f.mkdir.input': 'Folder name',
      'f.create': 'Create',
      'f.exists.title': 'File exists',
      'f.exists.msg': '"{name}" already exists in this folder.',
      'f.skip': 'Skip',
      'f.keep': 'Keep both',
      'f.replace': 'Replace',
      'f.up.item': '{name} ({size})',
      'f.up.aria': 'Upload progress for {name}',
      'f.up.running': 'Uploading…',
      'f.up.done': 'Done.',
      'f.up.renamed': 'Done, saved as "{name}" because that name was taken.',
      'f.up.failed': 'Failed: {msg}',
      'f.up.refused': 'Failed: the host refused: {msg}',
      'f.up.neterr': 'Failed: network error',

      // clipboard
      'c.remote.h': 'Remote clipboard',
      'c.fetch': 'Fetch remote clipboard',
      'c.copy': 'Copy to this device',
      'c.remote': 'Remote clipboard text',
      'c.remote.ph': 'Nothing fetched yet.',
      'c.send.h': 'Send text to the remote clipboard',
      'c.local': 'Text to send',
      'c.local.ph': 'Type or paste text to put on the remote clipboard.',
      'c.paste': 'Paste from this device',
      'c.send': 'Send to remote',
      'c.fetched': 'Fetched {n} characters.',
      'c.image': 'The remote clipboard holds an image ({size}); only text is shown here.',
      'c.empty': 'The remote clipboard is empty.',
      'c.nothing': 'Nothing to send.',
      'c.sent': 'Sent {n} characters to the remote clipboard.',
      'c.copied': 'Copied to this device.',
      'c.selected': 'Selected; press Ctrl+C to copy.',
      'c.blocked': 'Your browser blocked clipboard access; paste with Ctrl+V.',

      // info
      'i.refresh': 'Refresh',
      'i.shot': 'Take screenshot',
      'i.img': 'Screenshot of the remote screen',
      'i.taken': 'Taken {time}, {size}',
      'i.host': 'Host',
      'i.system': 'System',
      'i.osarch': 'OS / arch',
      'i.kernel': 'Kernel',
      'i.cpu': 'CPU',
      'i.cpu.v': '{model} ({n} cores)',
      'i.memory': 'Memory',
      'i.disk': 'Disk (/)',
      'i.free': '{free} free of {total}',
      'i.uptime': 'Uptime',
      'i.uptime.v': '{h} h {m} min',
      'i.load': 'Load',
      'i.user': 'User',
      'i.home': 'Home',
      'i.shell': 'Shell',
      'i.time': 'Local time',
      'i.display': 'Display',
      'i.yes': 'yes',
      'i.no': 'no',
      'i.conn': 'Connection',
      'i.conn.auth': ', login by {auth}',
      'i.conn.key': ', host key {fp}',
      'auth.password': 'password',
      'auth.token': 'token',
      'auth.key': 'key',

      // dialogs
      'dlg.all': 'Apply to all remaining files',
      'cancel': 'Cancel',
      'ok': 'OK',
      'save': 'Save',

      'foot.summary': '<strong>The gateway sees everything you do here.</strong> How this works',
      'foot.how': '<strong>How this works.</strong> A browser cannot hole-punch or speak RemoHop\'s QUIC session, so this page talks to a <em>gateway</em> ' +
        '(<code>remohop web</code>) and the gateway speaks the normal P2P session to the host. That session is end-to-end encrypted between the gateway and the host, ' +
        'but <strong>the gateway sees everything</strong>: the screen, every keystroke, files, the clipboard and any password you type here. ' +
        'Use only a gateway you control. Exposing one beyond localhost requires TLS and its own login; anyone who logs in to a gateway can use its saved pairings.',
    },
    zh: {
      'title': 'RemoHop 网页客户端',
      'skip': '跳到主要内容',
      'brand.sub': '网页客户端',
      'lang.group': '语言',

      'host.label': '已连接的被控端',
      'host.newconn': '连接其他被控端',
      'host.disconnect': '断开',
      'host.logout': '退出登录',
      'loading': '正在加载…',
      'gw.unreachable': '无法连接网关：{msg}',
      'gw.lost': '网关没有响应。',
      'gw.retry': '重试',
      'gw.ended.login': '网关会话已结束，请重新登录。',
      'gw.ended.reload': '本页面离开超过 15 分钟（电脑睡眠了，或浏览器暂停了这个标签页），网关已关闭它的连接。请刷新以重新开始：已与此网关配对的电脑无需密码即可连接。',
      'gw.reload': '刷新',
      'sess.ended': '与 {target} 的会话已结束。',
      'sess.reconnecting': '与 {target} 的连接已断开，正在重新连接…',
      'sess.reapproval': '正在重新连接 {target}：等待被控端的主人批准…',
      'sess.lost': '与 {target} 的连接已断开，无法重新建立：{msg} 请重新连接。',
      'sess.label': '已连接到',
      'sess.alias': '{alias}（{target}）',
      'sess.firstkey': '首次连接此被控端：已固定其身份密钥（{fp}）。',

      'login.h': '登录网关',
      'login.hint': '此网关需要它自己的密码（即 <code>--password-file</code> 中的密码），而不是被控端的密码。',
      'login.pass': '网关密码',
      'login.btn': '登录',

      'connect.h': '连接被控端',
      'connect.lead': '输入被控端的 9 位 ID、配对时起的名称或它的地址。',
      'connect.target': '被控端 ID、名称或 host:port',
      'connect.pass': '被控端密码',
      'connect.passhint': '留空则使用本机记住的配对或密码。',
      'connect.pair': '记住这台电脑（下次不用输入密码）',
      'connect.pair.hint': '对方允许时会配对；不允许时把密码保存在本机。',
      'connect.pwsaved': '已连接。这台电脑不支持配对，已把密码保存在本机，下次自动使用（对方换密码前一直有效）。',
      'err.saved_password_rejected': '保存的密码已失效（对方可能换了新密码），请重新输入。',
      'peer.opt.saved': '{label}（已保存密码）', 'peer.showpw': '查看保存的密码', 'peer.forgetpw': '删除保存的密码',
      'peer.pw.title': '{label} 的保存密码', 'peer.pw.copy': '复制', 'peer.forgetpw.done': '已删除保存的密码。',
      'pw.show': '显示密码', 'pw.hide': '隐藏密码',
      'connect.btn': '连接',
      'connect.connecting': '正在连接…',
      'connect.dialing': '正在连接… {notice}',
      'connect.waiting': '正在等待被控端的主人批准此连接…（可以取消）',
      'connect.canceled': '已取消。',
      'connect.failed': '连接失败。',
      'peers.h': '已记住：无需密码',
      'peers.recent.h': '最近连接',
      'peer.ready': '无需密码即可连接',
      'peer.needspw': '需要输入密码',
      'peer.sub.alias': '{target} · {state}',
      'connect.pairfailed': '已连接，但未能记住这台电脑，下次仍需输入密码。',
      'connect.pairrefused': '这台电脑不允许记住登录，下次仍需输入密码。',
      'peers.type': '（在下方输入）',
      'peer.opt.paired': '{label}（已配对）',
      'peer.opt.key': '{label}（密钥）',
      'peer.paired': '已配对',
      'peer.key': '已通过密钥授权',
      'peer.saved': '已保存',
      'peer.tag.paired': '已配对',
      'peer.tag.key': '密钥',
      'peer.aria.ready': '{label}：无需密码，直接连接',
      'peer.aria.form': '{label}：需要输入密码',
      'peer.more': '{label} 的操作',
      'peer.rename': '重命名',
      'peer.rename.title': '重命名 {label}',
      'peer.rename.msg': '为 {target} 起个名字。留空则显示它的 ID 或地址。',
      'peer.rename.input': '名称',
      'peer.forget': '移除这台电脑',
      'peer.forget.title': '移除 {label}？',
      'peer.forget.msg': '它的配对信息和密钥会被删除，与它的连接也会断开。以后再连接时需要输入它的密码。',
      'peer.forget.ok': '移除',
      'peer.connect': '连接',
      'disc.title': '断开与 {host} 的连接？',
      'disc.msg': '与这台被控端的远程桌面、终端会话和文件传输都会结束。',
      'disc.ok': '断开连接',

      'reason.bad_password': '密码错误。',
      'reason.host_locked_out': '密码错误次数过多，被控端已暂时拒绝此网关。请稍后再试。',
      'reason.denied': '被控端的主人拒绝了连接，或没有人及时响应确认请求。',
      'reason.host_busy': '被控端正在处理其他登录，请稍后再试。',
      'reason.pairing_rejected': '被控端已不再接受此网关上保存的配对，请用密码连接。',
      'reason.key_rejected': '被控端不接受此网关的密钥。',
      'reason.password_login_disabled': '被控端不允许用密码登录。',
      'reason.password_required': '请输入这台电脑的密码。',
      'reason.unknown_id': '没有这个 ID 的被控端。',
      'reason.host_offline': '该被控端不在线。',
      'reason.host_unreachable': '被控端没有响应。',
      'reason.rendezvous_rate_limited': '汇合服务器正在限制此网关的请求，请稍后再试。',
      'reason.no_path': '找不到通往被控端的直连路径。',
      'reason.host_key_changed': '被控端的身份密钥与首次连接时不同，可能有人在冒充它。',
      'reason.too_many_sessions': '此网关的被控端会话数已达上限。',
      'reason.timeout': '连接超时。',
      'reason.no_rendezvous': '9 位 ID 要通过汇合服务器查找（它是把本网关介绍给你的电脑的“碰头点”），而目前还没有设置。请改用电脑的地址（host:port）连接，或在运行本页面的电脑上设置一次服务器（remohop config set signal HOST[:PORT]），然后重试。',
      'reason.rendezvous_unreachable': '无法连接汇合服务器。请检查服务器地址和网络，或直接用地址（host:port）连接。',
      'reason.signal_wrong_protocol': '设置的汇合服务器地址不是 RemoHop 汇合服务器。请检查该设置，或直接用地址（host:port）连接被控端。',
      'reason.signal_is_host': '设置的汇合服务器地址是一台 RemoHop 被控端，不是汇合服务器。请直接用该地址（host:port）连接它，并修改汇合服务器设置。',
      'reason.remote_error': '无法连接到被控端。',
      'reason.bad_target': '请输入 9 位 ID、已保存的名称，或类似 192.168.1.5:21118 的地址。',
      'connect.details': '详细信息',

      'path.title': '网关与被控端之间的连接方式',
      'path.lan': '局域网直连',
      'path.direct': '直连',
      'path.punched': '打洞直连',
      'path.relayed': '中继',
      'path.relayed (websocket)': '中继（WebSocket）',
      'path.unknown': '未知',
      'path.paired': ' · 已配对',
      'path.relayed.title': '经汇合服务器中继（仍为端到端加密，但速度较慢）',
      'path.via': '连接方式：{path}',

      'tabs.label': '功能',
      'tab.desktop': '远程桌面',
      'tab.terminal': '终端',
      'tab.files': '文件',
      'tab.clipboard': '剪贴板',
      'tab.info': '信息',
      'refused': '被控端拒绝了此操作：{msg}',

      'd.controls': '远程桌面控制',
      'd.viewonly': '仅查看',
      'd.fit': '适应窗口',
      'd.actual': '原始大小',
      'd.fs': '全屏',
      'd.exitfs': '退出全屏',
      'd.refresh': '刷新画面',
      'd.refresh.title': '请被控端重新发送一帧完整画面',
      'd.cad.title': '发送 Ctrl+Alt+Del',
      'd.quality': '画质',
      'd.q.native': '原始分辨率',
      'd.q.max': '最高 {p}p',
      'd.monitor': '显示器',
      'd.monitor.all': '全部显示器',
      'd.monitor.n': '显示器 {n}（{w}×{h}）',
      'd.stats': '统计',
      'd.reconnect': '重新连接',
      'd.canvas': '远程桌面。获得焦点时，键盘和鼠标操作会发送到远程电脑。',
      'd.connecting': '正在连接…',
      'd.nocodec': '此浏览器没有能解码 AV1 或 H.264 的 WebCodecs 视频解码器。请通过 HTTPS 或 localhost 使用较新版本的 Chrome、Edge、Firefox 或 Safari。',
      'd.disconnected': '与网关的连接已断开。',
      'd.refused': '被控端拒绝了远程桌面：{err}',
      'd.ended': '远程桌面已结束：{err}',
      'd.paused': '远程屏幕暂不可用：{s}',
      'd.closed': '已关闭',
      'd.cannot': '此浏览器无法解码 {codec}。',
      'd.noinput': '被控端已关闭键鼠控制。',
      'd.askctl': '请求控制',
      'd.asking': '正在向被控端的主人请求控制…',
      'd.askfailed': '未获得控制权：{msg}',
      'd.hud.latency': '延迟',
      'd.hud.na': '无',
      'd.hud.rtt': '往返',
      'd.hud.host': '被控端 {fps} fps，丢弃 {n}',
      'd.hud.restarts': '编码器重启 {n}',
      'd.hud.skipped': '本端跳过 {n}',
      'd.hud.encode': '采集+编码',
      'd.hud.viewonly': '仅查看',
      'd.hud.input': '可控制',

      't.controls': '终端控制',
      't.session': '会话',
      't.new': '新建会话',
      't.kill': '结束会话',
      't.copy': '复制',
      't.copy.title': '复制选中的文本（Ctrl+Shift+C）',
      't.paste': '粘贴',
      't.paste.title': '粘贴（Ctrl+V 或 Ctrl+Shift+V）',
      't.smaller': '缩小字号',
      't.bigger': '放大字号',
      't.keys': '附加按键',
      't.up': '上',
      't.down': '下',
      't.left': '左',
      't.right': '右',
      't.input': '终端输入',
      't.attached': '{name}（使用中）',
      't.detached': '{name}（空闲）',
      't.connecting': '正在连接…',
      't.st.new': '会话 {name}（新建）',
      't.st.reattached': '会话 {name}（已重新接入）',
      't.st.exit': 'Shell 已退出（代码 {code}）。',
      't.st.exitsig': 'Shell 已退出（代码 {code}，{signal}）。',
      't.st.detached': '另一个客户端接管了此会话（{reason}）。',
      't.st.refused': '被控端拒绝了。',
      't.st.error': '出错了。',
      't.st.closed': '连接已断开。选择一个会话即可重新接入。',
      't.st.none': '没有会话。请点“新建会话”。',
      't.st.noclip': '浏览器阻止了剪贴板访问，请在终端里按 Ctrl+V。',
      't.kill.title': '结束会话',
      't.kill.msg': '结束会话“{name}”及其 Shell？',
      't.kill.ok': '结束',

      'f.controls': '文件操作',
      'f.up': '上一级',
      'f.path': '路径',
      'f.go': '转到',
      'f.refresh': '刷新',
      'f.mkdir': '新建文件夹',
      'f.upload': '上传…',
      'f.caption': '远程目录列表',
      'f.name': '名称',
      'f.size': '大小',
      'f.mtime': '修改时间',
      'f.actions': '操作',
      'f.empty': '此文件夹是空的。',
      'f.drop': '把文件拖到这里，即可上传到此文件夹。同名文件不会在未经询问的情况下被替换。',
      'f.uploads': '上传',
      'f.download': '下载',
      'f.download.aria': '下载 {name}',
      'f.rename': '重命名',
      'f.rename.aria': '重命名 {name}',
      'f.delete': '删除',
      'f.delete.aria': '删除 {name}',
      'f.rename.msg': '“{name}”的新名称',
      'f.rename.input': '新名称',
      'f.delete.dir': '删除文件夹“{name}”及其中的所有内容？此操作无法撤销。',
      'f.delete.file': '删除“{name}”？此操作无法撤销。',
      'f.notempty.title': '文件夹不是空的',
      'f.notempty.msg': '删除“{name}”及其中的全部内容？',
      'f.notempty.ok': '全部删除',
      'f.mkdir.input': '文件夹名称',
      'f.create': '创建',
      'f.exists.title': '文件已存在',
      'f.exists.msg': '此文件夹中已有“{name}”。',
      'f.skip': '跳过',
      'f.keep': '保留两者',
      'f.replace': '替换',
      'f.up.item': '{name}（{size}）',
      'f.up.aria': '{name} 的上传进度',
      'f.up.running': '正在上传…',
      'f.up.done': '已完成。',
      'f.up.renamed': '已完成。原名称已被占用，另存为“{name}”。',
      'f.up.failed': '失败：{msg}',
      'f.up.refused': '失败：被控端拒绝：{msg}',
      'f.up.neterr': '失败：网络错误',

      'c.remote.h': '远程剪贴板',
      'c.fetch': '读取远程剪贴板',
      'c.copy': '复制到本设备',
      'c.remote': '远程剪贴板中的文本',
      'c.remote.ph': '尚未读取。',
      'c.send.h': '发送文本到远程剪贴板',
      'c.local': '要发送的文本',
      'c.local.ph': '输入或粘贴要放到远程剪贴板的文本。',
      'c.paste': '从本设备粘贴',
      'c.send': '发送到远程',
      'c.fetched': '已读取 {n} 个字符。',
      'c.image': '远程剪贴板里是一张图片（{size}），这里只显示文本。',
      'c.empty': '远程剪贴板是空的。',
      'c.nothing': '没有要发送的内容。',
      'c.sent': '已将 {n} 个字符发送到远程剪贴板。',
      'c.copied': '已复制到本设备。',
      'c.selected': '已选中，按 Ctrl+C 复制。',
      'c.blocked': '浏览器阻止了剪贴板访问，请用 Ctrl+V 粘贴。',

      'i.refresh': '刷新',
      'i.shot': '截图',
      'i.img': '远程屏幕截图',
      'i.taken': '截取于 {time}，{size}',
      'i.host': '主机名',
      'i.system': '系统',
      'i.osarch': '操作系统 / 架构',
      'i.kernel': '内核',
      'i.cpu': 'CPU',
      'i.cpu.v': '{model}（{n} 核）',
      'i.memory': '内存',
      'i.disk': '磁盘（/）',
      'i.free': '可用 {free}，共 {total}',
      'i.uptime': '已运行',
      'i.uptime.v': '{h} 小时 {m} 分钟',
      'i.load': '负载',
      'i.user': '用户',
      'i.home': '主目录',
      'i.shell': 'Shell',
      'i.time': '本地时间',
      'i.display': '图形界面',
      'i.yes': '有',
      'i.no': '无',
      'i.conn': '连接',
      'i.conn.auth': '，登录方式：{auth}',
      'i.conn.key': '，被控端密钥 {fp}',
      'auth.password': '密码',
      'auth.token': '配对令牌',
      'auth.key': '密钥',

      'dlg.all': '其余文件也照此处理',
      'cancel': '取消',
      'ok': '确定',
      'save': '保存',

      'foot.summary': '<strong>网关能看到你在这里的一切操作。</strong>工作原理',
      'foot.how': '<strong>工作原理：</strong>浏览器无法打洞，也无法直接建立 RemoHop 的 QUIC 会话，所以本页面连接的是<em>网关</em>' +
        '（<code>remohop web</code>），再由网关与被控端建立常规的 P2P 会话。该会话在网关与被控端之间端到端加密，' +
        '但<strong>网关能看到一切</strong>：屏幕画面、每一次按键、文件、剪贴板，以及你在这里输入的任何密码。' +
        '请只使用你自己掌控的网关。要让网关在本机以外可以访问，必须启用 TLS 并设置网关自己的登录密码；任何登录网关的人都能使用其中保存的配对。',

      // API error codes (English shows the gateway's own message instead)
      'err.bad_password': '密码错误。',
      'err.locked_out': '登录失败次数过多，请稍后再试。',
      'err.login_required': '请先登录。',
      'err.bad_csrf': '网关会话已失效，请刷新页面。',
      'err.bad_alias': '名称需以字母或汉字开头，只能包含字母、汉字、数字、单个空格以及 . _ -，最多 63 个字符。',
      'err.bad_origin': '已拒绝跨站请求。',
      'err.bad_target': '请输入 9 位 ID、已保存的名称或 host:port。',
      'err.busy': '正在进行的请求太多，请稍后再试。',
      'err.canceled': '已取消。',
      'err.disconnected': '与该被控端的连接已断开（会话已结束或因空闲被关闭），请重新连接。',
      'err.too_many_sessions': '此网关的被控端会话数已达上限，请先断开一个或稍后再试。',
      'err.too_many_browsers': '此网关上的浏览器会话太多。',
      'err.password_required': '请输入被控端的密码。',
      'err.not_found': '找不到该文件或文件夹。',
      'err.not_a_file': '只能下载普通文件。',
      'err.not_empty': '文件夹不是空的。',
      'err.exists': '同名的文件或文件夹已存在。',
      'err.permission_denied': '没有权限。',
      'err.no_space': '被控端的磁盘空间不足。',
      'err.bad_name': '文件名无效。',
      'err.timeout': '超时。',
      'err.gateway_unreachable': '网关没有响应，请重试。',
      'err.reconnecting': '与被控端的连接已断开，正在重新连接…',
      'err.signal_is_host': '设置的汇合服务器地址是一台 RemoHop 被控端，不是汇合服务器。请直接用该地址（host:port）连接它，并修改汇合服务器设置。',
      'err.signal_wrong_protocol': '设置的汇合服务器地址不是 RemoHop 汇合服务器，请检查该设置。',
    },
  };

  const STORE = 'remohop.lang';
  const norm = l => { l = String(l || '').toLowerCase(); return l.startsWith('zh') ? 'zh' : l.startsWith('en') ? 'en' : ''; };
  function fromQuery() { try { return norm(new URLSearchParams(location.search).get('lang')); } catch (e) { return ''; } }
  function saved() { try { return norm(localStorage.getItem(STORE)); } catch (e) { return ''; } }
  const browser = () => norm((navigator.languages && navigator.languages[0]) || navigator.language) === 'zh' ? 'zh' : 'en';
  let lang = fromQuery() || saved() || browser();

  const has = (key, l) => Object.prototype.hasOwnProperty.call(D[l || lang] || {}, key);
  // A variable is a string or number, or {k, v, fb}: another key (with its
  // own variables), shown as fb where the current language lacks it.
  function val(x) {
    if (x === null || x === undefined) return '';
    if (typeof x !== 'object') return String(x);
    if (x.fb !== undefined && !has(x.k)) return String(x.fb);
    return t(x.k, x.v);
  }
  function t(key, vars) {
    const s = has(key) ? D[lang][key] : has(key, 'en') ? D.en[key] : key;
    return s.replace(/\{(\w+)\}/g, (m, k) => vars && Object.prototype.hasOwnProperty.call(vars, k) ? val(vars[k]) : m);
  }

  // rich builds a dictionary string with <strong>, <em> and <code> as
  // elements; anything else stays text.
  function rich(el, s) {
    el.textContent = '';
    const stack = [el];
    for (const part of s.split(/(<\/?(?:strong|em|code)>)/)) {
      const m = /^<(\/?)(strong|em|code)>$/.exec(part);
      if (m && !m[1]) { const e = document.createElement(m[2]); stack[stack.length - 1].appendChild(e); stack.push(e); }
      else if (m) { if (stack.length > 1) stack.pop(); }
      else if (part) stack[stack.length - 1].appendChild(document.createTextNode(part));
    }
  }

  const ATTRS = ['placeholder', 'aria-label', 'title', 'alt'];
  function vars(el, name) {
    const v = el.getAttribute(name + '-vars');
    if (!v) return undefined;
    try { return JSON.parse(v); } catch (e) { return undefined; }
  }
  function render(el) {
    if (el.hasAttribute('data-i18n')) el.textContent = t(el.getAttribute('data-i18n'), vars(el, 'data-i18n'));
    if (el.hasAttribute('data-i18n-rich')) rich(el, t(el.getAttribute('data-i18n-rich')));
    for (const a of ATTRS) {
      const n = 'data-i18n-' + a;
      if (el.hasAttribute(n)) el.setAttribute(a, t(el.getAttribute(n), vars(el, n)));
    }
  }
  function apply(root) {
    root = root || document;
    const sel = '[data-i18n],[data-i18n-rich],' + ATTRS.map(a => '[data-i18n-' + a + ']').join(',');
    if (root.nodeType === 1 && root.matches(sel)) render(root);
    for (const el of root.querySelectorAll(sel)) render(el);
    if (root === document) {
      document.documentElement.lang = lang === 'zh' ? 'zh-CN' : 'en';
      document.title = t('title');
      for (const b of document.querySelectorAll('#langsw button[data-lang]')) b.setAttribute('aria-pressed', String(b.dataset.lang === lang));
    }
  }

  // say sets el's text from key (null: clear it) and remembers it for a
  // language switch; plain sets text that is not translated.
  function bind(el, name, key, v) {
    el.setAttribute(name, key);
    if (v) el.setAttribute(name + '-vars', JSON.stringify(v)); else el.removeAttribute(name + '-vars');
  }
  function unbind(el, name) { el.removeAttribute(name); el.removeAttribute(name + '-vars'); }
  function say(el, key, v) {
    if (!el) return;
    if (!key) { unbind(el, 'data-i18n'); el.textContent = ''; return; }
    bind(el, 'data-i18n', key, v);
    el.textContent = t(key, v);
  }
  function plain(el, text) { if (!el) return; unbind(el, 'data-i18n'); el.textContent = text || ''; }
  function sayAttr(el, attr, key, v) {
    if (!el) return;
    bind(el, 'data-i18n-' + attr, key, v);
    el.setAttribute(attr, t(key, v));
  }

  function set(l, opts) {
    l = norm(l);
    if (!l) return;
    if (!(opts && opts.persist === false)) { try { localStorage.setItem(STORE, l); } catch (e) { /* not remembered */ } }
    if (l === lang) return;
    lang = l;
    // A ?lang= in the address (the apps' first load) follows, so that a
    // reload of the page keeps the new language.
    try {
      const u = new URL(location.href);
      if (u.searchParams.has('lang')) { u.searchParams.set('lang', l); history.replaceState(history.state, '', u.toString()); }
    } catch (e) { /* keep the address */ }
    apply(document);
    window.dispatchEvent(new CustomEvent('remohop:lang', {detail: {lang}}));
  }
  // add merges more strings (the mobile apps' injected script has its own).
  function add(more) { for (const l of Object.keys(more)) D[l] = Object.assign(D[l] || {}, more[l]); }

  window.SDI18n = {
    t, has, say, plain, sayAttr, apply, set, add,
    lang: () => lang,
    // For toLocale*String: the browser's own format unless it disagrees with the page.
    locale: () => lang === 'zh' ? 'zh-CN' : browser() === 'en' ? undefined : 'en',
  };

  for (const b of document.querySelectorAll('#langsw button[data-lang]')) b.addEventListener('click', () => set(b.dataset.lang));
  apply(document);
})();
