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
      'tab.home': 'Home', 'tab.sessions': 'Sessions', 'nav.main': 'Main menu', 'nav.open': 'Open computers',
      'nav.collapse': 'Collapse the sidebar', 'nav.expand': 'Expand the sidebar',
      'me.title': 'This computer', 'me.off': 'Not reachable', 'me.on': 'Reachable', 'wiz.eyebrow': 'First start',
      'home.lead': 'This computer, who is connected to it, and the computers you reach.',
      'in.offnote': "Switch on to show this computer's ID and a password to give to whoever helps you.",
      'qc.title': 'Quick connect', 'qc.recent': 'Recently seen', 'qc.all': 'All saved computers', 'qc.none': 'Computers you connect to appear here.',
      'kpi.live': 'Connected now', 'kpi.live.s0': 'Nobody is connected', 'kpi.live.s': 'Control {c} · Files {f}',
      'kpi.queue': 'Needs you', 'kpi.queue.s0': 'Nobody is waiting', 'kpi.queue.s': 'Refused in {n} s without an answer',
      'kpi.saved': 'Saved computers', 'kpi.saved.s': '{n} online now', 'kpi.saved.s0': 'None saved yet',
      'link.sessions': 'Sessions', 'link.queue': 'Approval queue', 'link.connect': 'Address book',
      'act.title': 'Activity', 'act.sub': 'Devices connected to this computer in the last hour', 'act.ago': '60 min ago', 'act.now': 'now',
      'act.since': 'recorded since {t}', 'act.none': 'Nobody has connected in the last hour.', 'act.tip': '{t}: {n} connected', 'act.nodata': '{t}: not recorded',
      'act.peak': 'Peak {n}',
      'c.eyebrow': 'Address book', 'c.lead': 'Type an ID, a name you gave a computer, or host:port.',
      'c.none.sub': "The computers you connect to or pair with, and your account's devices, are listed here.",
      'f.all': 'All', 'c.search': 'Search saved computers', 'c.refresh': 'Check who is online', 'c.nomatch': 'No saved computer matches.',
      'col.name': 'Computer', 'col.platform': 'System', 'col.seen': 'Last seen online', 'col.id': 'ID', 'col.key': 'Key fingerprint',
      'lan.title': 'On this network', 'lan.looking': 'Looking for computers on this network…', 'lan.none': 'No computer on this network answered.',
      'lan.none.sub': 'A computer appears here while RemoHop runs on it with incoming connections on.',
      'lan.saved': 'Saved as {t}', 'lan.conflict': 'Key changed', 'lan.err': 'Could not look at this network: {m}',
      'lan.conflict.tip': 'Its key is not the one saved for {t}: it may be another computer pretending to be it.',
      'help.lan': 'Computers on this network running RemoHop with incoming connections on, found without a server. Connect uses their address; their key is checked when you connect, as always.',
      'sess.eyebrow': 'Sessions and approvals', 'sess.lead': 'Who is waiting, who is connected, and who may come back without asking.',
      'q.title': 'Needs you', 'q.none': 'Nothing needs you', 'q.none.sub': 'When someone asks to connect, the question appears in front of every window and waits here.',
      'q.foot': 'Unanswered requests are refused after {n} s · Esc denies', 'q.open': 'Answer in the dialog', 'q.next': 'Waiting its turn',
      'sess.none': 'Nobody is connected to this computer.', 'un.svc': 'Connections through unattended access are listed apart.',
      'out.title': 'Computers open here', 'out.none': 'No computer is open.', 'out.none.sub': 'Computers you open from Connect show here and in the sidebar.',
      'out.show': 'Show', 'out.connected': 'Connected', 'out.wait': 'Connecting…', 'out.closed': 'Not connected',
      'tr.none': 'No remembered devices.', 'tr.none.sub': 'Tick "Remember this device" when you allow one, and it is let in next time without asking.',
      'req.eyebrow': 'Connection request', 'set.eyebrow': 'Preferences', 'set.lead': 'How RemoHop works on this computer.',
      'wiz.title': 'Welcome to RemoHop', 'wiz.lead': 'Connect to your computers, or let someone help you.',
      'wiz.server': 'Rendezvous server', 'wiz.incoming': 'Let others connect to this computer now (each connection asks me)', 'next': 'Next', 'back': 'Back', 'done': 'Done', 'save': 'Save', 'cancel': 'Cancel',
      'auto': 'Auto', 'copy': 'Copy', 'copied': 'Copied', 'allow': 'Allow', 'deny': 'Deny', 'saved': 'Saved',
      'srv.official': 'Official server', 'srv.official.sub': 'Run by RemoHop, nothing to set up', 'srv.custom': 'My own server', 'srv.none': 'No server',
      'srv.skip.sub': 'Only on the same network, by address',
      'help.server': 'It introduces computers to each other by ID. Connections are end-to-end encrypted; the server never sees them.',
      'c.target': 'Computer ID or address', 'c.connect': 'Connect', 'c.saved': 'Saved computers', 'c.none': 'No saved computers yet.',
      'help.saved': 'Computers you connected to or paired with. A paired computer needs no password.',
      'p.online': 'Online', 'p.offline': 'Offline', 'p.unknown': 'Unknown', 'p.paired': 'Paired', 'p.account': 'Account',
      'p.savedpw': 'Password saved', 'p.showpw': 'Show saved password', 'p.forgetpw': 'Delete saved password', 'p.pw.t': 'Saved password of {t}',
      'p.forgetpw.done': 'Saved password deleted', 'close': 'Close', 'pw.show': 'Show password', 'pw.hide': 'Hide password',
      'p.rename': 'Rename', 'p.wake': 'Wake up', 'p.mac': 'Wake-on-LAN address', 'p.forget': 'Forget', 'p.woke': 'Wake-up packet sent',
      'p.rename.t': 'Name for {t}', 'p.name': 'Name', 'p.mac.t': 'Wake-on-LAN for {t}', 'p.mac.l': 'MAC address (empty removes it)',
      'p.forget.q': 'Forget {t}? Its pairing is deleted.', 'p.more': 'More for {t}',
      'in.allow': 'Allow incoming connections', 'in.id': 'ID', 'in.pw': 'Temporary password', 'in.new': 'New', 'in.pwfixed': 'My own password', 'in.setpw': 'Password settings', 'pwm.title': 'Password for incoming connections', 'pwm.temp': 'A new random password', 'pwm.temp.sub': 'Made each time RemoHop starts; "New" replaces it whenever you like', 'pwm.fixed': 'My own password', 'pwm.fixed.sub': 'Stays the same until you change it; at least 8 characters, with letters and digits', 'pwm.note': 'Either way, every connection still asks you first.', 'pwm.saved': 'Password settings saved',
      'in.off': 'Off: nobody can connect', 'in.starting': 'Connecting to the server…', 'in.ready': 'Ready: every connection asks you',
      'in.offline': 'Offline: trying again…', 'in.noserver': 'No server: reachable by address on this network',
      'in.unreach': "Can't reach {s}. Check the address in Settings › Rendezvous server.", 'in.official': 'the official server', 'in.fix': 'Rendezvous server settings',
      'in.elsewhere': 'Served by the RemoHop service (ID {id})', 'in.error': 'Could not start',
      'in.addr': 'Address: {a}', 'in.sessions': 'Connected now', 'in.discall': 'Disconnect everyone',
      'ff.missing': 'Screen sharing needs ffmpeg, which is not installed on this computer. Whoever connects can still use files and commands.',
      'ff.install': 'Install screen sharing component (ffmpeg, about {mb} MB)', 'ff.from': 'ffmpeg {v} from {s}. RemoHop checks the download against a fingerprint built into it before using it.',
      'ff.downloading': 'Downloading… {p}%', 'ff.verifying': 'Checking the download…', 'ff.installing': 'Installing…',
      'ff.failed': 'The install did not work: {m}', 'ff.off': 'Downloading it is switched off on this computer ({b}).',
      'ff.linux': "Install it with your system's package manager, for example: sudo apt install ffmpeg",
      'help.incoming': 'While this is on, someone with this ID and password can ask to connect. You decide each time what they may do.',
      's.view': 'View', 's.control': 'Control', 's.files': 'Files', 's.disc': 'Disconnect', 's.remembered': 'Remembered',
      'un.title': 'Unattended access', 'un.creds': 'Unattended ID and password', 'un.credsoff': 'Unattended access is off. Turn it on from Home.', 'un.admin3': 'Show asks Windows for an administrator.', 'un.runok': 'Running', 'un.idl': 'Unattended ID', 'un.pwl': 'Unattended password', 'un.getid': 'Show ID', 'un.nopw': 'The service has no password set.', 'un.off': 'Off', 'un.on': 'On', 'un.running': 'Running · ID {id}', 'un.stopped': 'Installed, not running',
      'un.setup': 'Set up', 'un.disable': 'Turn off', 'un.busy': 'Working…', 'un.wait': 'Turning on… Windows asks for administrator permission first; then it takes a few seconds.', 'un.enable': 'Turn on',
      'un.lead': 'Choose a strong password. Anyone with this ID and password can connect without asking.',
      'un.pw': 'Password', 'un.pw2': 'Repeat', 'un.gen': 'Suggest a strong password', 'un.admin': 'Windows will ask for an administrator.',
      'un.mismatch': 'The passwords differ.', 'un.na': 'Not available on this system',
      'un.sessions': 'Connections', 'un.change': 'Change password', 'un.none': 'Nobody is connected through unattended access.',
      'un.pwdone': 'Password changed', 'un.newpw': 'New password',
      'help.un': 'Reach this computer when nobody is at it, from start-up, with a strong password you choose. It runs as a background service.',
      'tr.title': 'Remembered devices', 'tr.rename': 'Rename this device', 'about.h': 'About', 'about.privacy': 'Privacy policy', 'about.terms': 'Terms of use', 'about.licensing': 'Licenses', 'about.support': 'Help and support', 'about.telegram': 'Telegram: @remohop', 'tr.forget': 'Forget', 'tr.control': 'control', 'tr.files': 'files', 'tr.view': 'view only',
      'help.tr': 'These devices connect without asking, with the access you chose, while incoming connections are on.',
      'req.title': '{d} wants to connect', 'req.title.access': '{d} asks for more', 'req.new': 'New device', 'req.known': 'Connected before',
      'req.scam': 'Did someone call you saying they are from support, your bank or the police, and ask you to install this? Deny. That is a scam.',
      'req.may': 'They may see your screen, and also:', 'req.control': 'Control the mouse and keyboard', 'req.files': 'Transfer files and use the clipboard',
      'req.remember': 'Remember this device', 'req.left': 'Refused in {n} s', 'req.device': 'Device', 'req.key': 'Key', 'req.from': 'From',
      'req.asks': 'Asks to:', 'req.input': 'control the mouse and keyboard', 'req.file': 'transfer files', 'req.clip': 'use the clipboard',
      'req.unknown': 'Unnamed device', 'req.title.action': '{d} asks to act under grant {g}', 'req.action': 'Wants to:',
      'set.lang': 'Language', 'set.theme': 'Theme', 'set.light': 'Light', 'set.dark': 'Dark', 'set.start': 'Start automatically', 'set.autostart': 'Start RemoHop when the computer starts (in the tray)',
      'about': '{v} · {e}', 'about.app': 'About RemoHop', 'set.g.conn': 'Connection', 'set.g.general': 'General', 'set.g.support': 'Support', 'upd.version': 'Version', 'browser': 'browser window',
      'upd.title': 'RemoHop {v} is available', 'upd.how.download': 'You have {c}. Download the new version and replace this copy.',
      'upd.how.cmd': 'You have {c}. Upgrade with:', 'upd.how.package': "You have {c}. Upgrade it with your system's package manager.",
      'upd.how.nix': 'You have {c}. Upgrade it through Nix.', 'upd.get': 'Download', 'upd.install': 'Upgrade now', 'upd.how.install': 'You have {c}. Upgrade in place: RemoHop restarts by itself.',
      'upd.st.verifying': 'Checking the release signature…', 'upd.st.downloading': 'Downloading… {p}', 'upd.st.installing': 'Installing…', 'upd.st.restarting': 'Restarting…',
      'upd.failed': 'The upgrade did not work: {m}', 'err.busy_sessions': 'Someone is connected to this computer. Disconnect them first, then upgrade.', 'upd.notes': "What's new", 'upd.skip': 'Skip this version',
      'upd.h': 'Updates', 'diag.h': 'Diagnostic logs', 'diag.label': 'Save a diagnostic report', 'diag.export': 'Export', 'diag.saved': 'Saved: {p}',
      'diag.help': 'A text file with the app\'s version, the system, its state and its recent log, to send to support when something does not work. Passwords, tokens and keys are removed; IDs, names and addresses stay. It is saved in your Downloads folder and goes nowhere unless you send it.', 'upd.auto': 'Check for updates automatically (once a day)', 'upd.now': 'Check now', 'upd.checking': 'Checking…',
      'upd.latest': 'Up to date ({v}) · checked {t}', 'upd.avail': '{v} is available · checked {t}', 'upd.never': 'Not checked yet',
      'upd.fail': 'Could not check (no connection to remohop.com?)', 'upd.managed': 'Switched off by the administrator ({e})',
      'upd.dev': 'Development build ({v}): not compared with releases',
      'upd.privacy': 'Only the latest version number is read from remohop.com; nothing about this computer is sent, and nothing is installed without you.',
      'h.closeq': 'Disconnect from {t}?', 'h.popout': 'Open in a window', 'h.close': 'Disconnect and close',
      'err.no_server': 'Enter the server address.', 'err.bad_server': 'That is not a server address (like signal.example.com:21116).',
      'err.weak_password': 'Use at least 12 characters with three of: lower case, upper case, digits, symbols.', 'err.weak_own_password': 'Use at least 8 characters, with at least two of: letters, digits, other characters.', 'in.show': 'Show', 'in.hide': 'Hide',
      'err.elevation_cancelled': 'The administrator prompt was cancelled.', 'err.bad_target': 'That is not an ID or an address.',
      'err.wake_name': 'Give this computer a short name first.', 'err.bad_mac': 'That is not a MAC address.',
      'err.gone': 'That is no longer there.', 'err.failed': 'Something went wrong: {m}', 'err.offline': 'The app is not answering.'
    },
    zh: {
      'tab.connect': '连接', 'tab.computer': '这台电脑', 'tab.settings': '设置',
      'tab.home': '首页', 'tab.sessions': '会话', 'nav.main': '主菜单', 'nav.open': '已打开的电脑',
      'nav.collapse': '收起侧栏', 'nav.expand': '展开侧栏',
      'me.title': '这台电脑', 'me.off': '不可连接', 'me.on': '可连接', 'wiz.eyebrow': '首次启动',
      'home.lead': '这台电脑、谁连着它，以及你要连接的电脑。',
      'in.offnote': '打开后会显示这台电脑的 ID 和密码，把它们告诉帮你的人。',
      'qc.title': '快速连接', 'qc.recent': '最近在线', 'qc.all': '全部已保存的电脑', 'qc.none': '连接过的电脑会出现在这里。',
      'kpi.live': '当前连接', 'kpi.live.s0': '没有人连接', 'kpi.live.s': '控制 {c} · 文件 {f}',
      'kpi.queue': '待你处理', 'kpi.queue.s0': '没有人在等待', 'kpi.queue.s': '{n} 秒内不回应将自动拒绝',
      'kpi.saved': '已保存的电脑', 'kpi.saved.s': '{n} 台在线', 'kpi.saved.s0': '还没有保存',
      'link.sessions': '会话', 'link.queue': '审批队列', 'link.connect': '通讯录',
      'act.title': '活动', 'act.sub': '最近一小时连接到这台电脑的设备数', 'act.ago': '60 分钟前', 'act.now': '现在',
      'act.since': '自 {t} 起记录', 'act.none': '最近一小时没有人连接。', 'act.tip': '{t}：{n} 个连接', 'act.nodata': '{t}：未记录',
      'act.peak': '峰值 {n}',
      'c.eyebrow': '通讯录', 'c.lead': '输入 ID、你给电脑起的名字，或 主机:端口。',
      'c.none.sub': '你连接过、配对过的电脑，以及账号下的设备，都会列在这里。',
      'f.all': '全部', 'c.search': '搜索已保存的电脑', 'c.refresh': '刷新在线状态', 'c.nomatch': '没有匹配的电脑。',
      'col.name': '电脑', 'col.platform': '系统', 'col.seen': '最近在线', 'col.id': 'ID', 'col.key': '密钥指纹',
      'lan.title': '本网络中的电脑', 'lan.looking': '正在查找本网络中的电脑…', 'lan.none': '本网络中没有电脑应答。',
      'lan.none.sub': '电脑上运行 RemoHop 并允许连接时，会出现在这里。',
      'lan.saved': '已保存为 {t}', 'lan.conflict': '密钥已变', 'lan.err': '无法查找本网络：{m}',
      'lan.conflict.tip': '它的密钥与为 {t} 保存的不同：可能是另一台电脑在冒充它。',
      'help.lan': '本网络中运行 RemoHop 并允许连接的电脑，无需服务器即可找到。“连接”使用它的地址；连接时照常核对它的密钥。',
      'sess.eyebrow': '会话与审批', 'sess.lead': '谁在等待、谁已连接，以及谁可以不经询问再次连接。',
      'q.title': '待你处理', 'q.none': '没有待处理的请求', 'q.none.sub': '有人请求连接时，问题会出现在所有窗口最前面，并在这里排队。',
      'q.foot': '无人回应的请求 {n} 秒后自动拒绝 · Esc 拒绝', 'q.open': '请在对话框中回答', 'q.next': '排队中',
      'sess.none': '没有人连接这台电脑。', 'un.svc': '通过无人值守访问的连接单独列出。',
      'out.title': '在这里打开的电脑', 'out.none': '没有打开的电脑。', 'out.none.sub': '从“连接”打开的电脑会显示在这里和侧栏中。',
      'out.show': '显示', 'out.connected': '已连接', 'out.wait': '正在连接…', 'out.closed': '未连接',
      'tr.none': '没有已记住的设备。', 'tr.none.sub': '允许连接时勾选“记住这台设备”，下次它无需询问即可连接。',
      'req.eyebrow': '连接请求', 'set.eyebrow': '偏好设置', 'set.lead': 'RemoHop 在这台电脑上的工作方式。',
      'wiz.title': '欢迎使用 RemoHop', 'wiz.lead': '连接你的电脑，或者让别人帮你。',
      'wiz.server': '汇合服务器', 'wiz.incoming': '现在就允许别人连接这台电脑（每次连接都先问我）', 'next': '下一步', 'back': '上一步', 'done': '完成', 'save': '保存', 'cancel': '取消',
      'auto': '自动', 'copy': '复制', 'copied': '已复制', 'allow': '允许', 'deny': '拒绝', 'saved': '已保存',
      'srv.official': '官方服务器', 'srv.official.sub': '由 RemoHop 运营，无需设置', 'srv.custom': '我自己的服务器', 'srv.none': '不用服务器',
      'srv.skip.sub': '仅限同一网络，按地址连接',
      'help.server': '它按 ID 为电脑牵线。连接是端到端加密的，服务器看不到内容。',
      'c.target': '电脑 ID 或地址', 'c.connect': '连接', 'c.saved': '已保存的电脑', 'c.none': '还没有保存的电脑。',
      'help.saved': '连接过或配对过的电脑。已配对的电脑不用再输密码。',
      'p.online': '在线', 'p.offline': '离线', 'p.unknown': '未知', 'p.paired': '已配对', 'p.account': '账号',
      'p.savedpw': '已保存密码', 'p.showpw': '查看保存的密码', 'p.forgetpw': '删除保存的密码', 'p.pw.t': '{t} 的保存密码',
      'p.forgetpw.done': '已删除保存的密码', 'close': '关闭', 'pw.show': '显示密码', 'pw.hide': '隐藏密码',
      'p.rename': '重命名', 'p.wake': '唤醒', 'p.mac': '网络唤醒地址', 'p.forget': '移除', 'p.woke': '已发送唤醒包',
      'p.rename.t': '为 {t} 命名', 'p.name': '名称', 'p.mac.t': '{t} 的网络唤醒', 'p.mac.l': 'MAC 地址（留空则删除）',
      'p.forget.q': '移除 {t}？它的配对会被删除。', 'p.more': '{t} 的更多操作',
      'in.allow': '允许别人连接', 'in.id': 'ID', 'in.pw': '临时密码', 'in.new': '换一个', 'in.pwfixed': '固定密码', 'in.setpw': '密码设置', 'pwm.title': '连接密码', 'pwm.temp': '随机密码', 'pwm.temp.sub': '每次启动 RemoHop 时自动生成，随时可点“换一个”', 'pwm.fixed': '我自己的固定密码', 'pwm.fixed.sub': '一直不变，直到你修改；至少 8 位，包含字母和数字', 'pwm.note': '无论哪种，每次有人连接都会先问你。', 'pwm.saved': '密码设置已保存',
      'in.off': '已关闭：谁都连不上', 'in.starting': '正在连接服务器…', 'in.ready': '已就绪：每次连接都会先问你',
      'in.offline': '离线：正在重试…', 'in.noserver': '没有服务器：同一网络内可按地址连接',
      'in.unreach': '无法连接 {s}。请在 设置 › 汇合服务器 中检查地址。', 'in.official': '官方服务器', 'in.fix': '汇合服务器设置',
      'in.elsewhere': '由 RemoHop 服务提供（ID {id}）', 'in.error': '无法启动',
      'in.addr': '地址：{a}', 'in.sessions': '当前连接', 'in.discall': '全部断开',
      'ff.missing': '屏幕共享需要 ffmpeg，但这台电脑上没有安装。连接进来的人仍然可以使用文件和命令。',
      'ff.install': '安装屏幕共享组件（ffmpeg，约 {mb} MB）', 'ff.from': 'ffmpeg {v}，来自 {s}。RemoHop 使用前会用内置的指纹校验下载的文件。',
      'ff.downloading': '正在下载… {p}%', 'ff.verifying': '正在校验下载的文件…', 'ff.installing': '正在安装…',
      'ff.failed': '安装没有成功：{m}', 'ff.off': '这台电脑禁止了下载（{b}）。',
      'ff.linux': '请用系统的包管理器安装，例如：sudo apt install ffmpeg',
      'help.incoming': '打开后，知道这个 ID 和密码的人可以请求连接。每次由你决定对方能做什么。',
      's.view': '查看', 's.control': '控制', 's.files': '文件', 's.disc': '断开', 's.remembered': '已记住',
      'un.title': '无人值守访问', 'un.creds': '无人值守 ID 与密码', 'un.credsoff': '无人值守访问未开启，请先在“首页”打开开关。', 'un.admin3': '点“显示”时 Windows 会要求管理员授权。', 'un.runok': '运行中', 'un.idl': '无人值守 ID', 'un.pwl': '无人值守密码', 'un.getid': '查看 ID', 'un.nopw': '服务还没有设置密码。', 'un.off': '未开启', 'un.on': '已开启', 'un.running': '运行中 · ID {id}', 'un.stopped': '已安装，未运行',
      'un.setup': '设置', 'un.disable': '关闭', 'un.busy': '处理中…', 'un.wait': '正在开启… Windows 会先请求管理员权限，确认后几秒内完成。', 'un.enable': '开启',
      'un.lead': '请设一个强密码。知道这个 ID 和密码的人无需你同意即可连接。',
      'un.pw': '密码', 'un.pw2': '再输一次', 'un.gen': '生成强密码', 'un.admin': 'Windows 会要求管理员授权。',
      'un.mismatch': '两次输入的密码不一致。', 'un.na': '此系统不支持',
      'un.sessions': '连接', 'un.change': '修改密码', 'un.none': '没有人通过无人值守访问连接。',
      'un.pwdone': '密码已修改', 'un.newpw': '新密码',
      'help.un': '没人在电脑前也能连上，开机即可用，使用你设定的强密码。它作为后台服务运行。',
      'tr.title': '已记住的设备', 'tr.rename': '重命名这台设备', 'about.h': '关于', 'about.privacy': '隐私政策', 'about.terms': '使用条款', 'about.licensing': '许可证', 'about.support': '帮助与支持', 'about.telegram': 'Telegram：@remohop', 'tr.forget': '移除', 'tr.control': '控制', 'tr.files': '文件', 'tr.view': '仅查看',
      'help.tr': '允许连接打开时，这些设备无需询问即可连接，权限为你当时的选择。',
      'req.title': '{d} 请求连接', 'req.title.access': '{d} 请求更多权限', 'req.new': '新设备', 'req.known': '以前连接过',
      'req.scam': '是不是有人打电话自称客服、银行或警察，让你安装这个软件？请拒绝，这是诈骗。',
      'req.may': '对方可以看到你的屏幕，另外还可以：', 'req.control': '控制鼠标和键盘', 'req.files': '传输文件、使用剪贴板',
      'req.remember': '记住这台设备', 'req.left': '{n} 秒后自动拒绝', 'req.device': '设备', 'req.key': '密钥', 'req.from': '来自',
      'req.asks': '请求：', 'req.input': '控制鼠标和键盘', 'req.file': '传输文件', 'req.clip': '使用剪贴板',
      'req.unknown': '未命名设备', 'req.title.action': '{d} 请求在授权 {g} 下执行操作', 'req.action': '请求：',
      'set.lang': '语言', 'set.theme': '外观', 'set.light': '浅色', 'set.dark': '深色', 'set.start': '开机自动启动', 'set.autostart': '开机后自动启动 RemoHop（在托盘中运行）',
      'about': '{v} · {e}', 'about.app': '关于 RemoHop', 'set.g.conn': '连接', 'set.g.general': '通用', 'set.g.support': '支持', 'upd.version': '版本', 'browser': '浏览器窗口',
      'upd.title': 'RemoHop {v} 已发布', 'upd.how.download': '当前是 {c}。下载新版替换当前程序即可。',
      'upd.how.cmd': '当前是 {c}。用这条命令升级：', 'upd.how.package': '当前是 {c}。请用系统的包管理器升级。',
      'upd.how.nix': '当前是 {c}。请通过 Nix 升级。', 'upd.get': '下载', 'upd.install': '立即升级', 'upd.how.install': '当前是 {c}。可以直接升级，完成后 RemoHop 会自动重启。',
      'upd.st.verifying': '正在校验发布签名…', 'upd.st.downloading': '正在下载… {p}', 'upd.st.installing': '正在安装…', 'upd.st.restarting': '正在重启…',
      'upd.failed': '升级没有成功：{m}', 'err.busy_sessions': '有人正连接着这台电脑。请先断开，再升级。', 'upd.notes': '更新内容', 'upd.skip': '跳过这个版本',
      'upd.h': '更新', 'diag.h': '诊断日志', 'diag.label': '保存诊断报告', 'diag.export': '导出', 'diag.saved': '已保存：{p}',
      'diag.help': '一个文本文件，包含应用版本、系统、运行状态和最近的日志，遇到问题时发给支持团队。密码、令牌和密钥会被去掉，ID、名称和地址会保留。文件保存在“下载”文件夹里，你不发送就不会离开这台电脑。', 'upd.auto': '自动检查更新（每天一次）', 'upd.now': '立即检查', 'upd.checking': '正在检查…',
      'upd.latest': '已是最新版本（{v}）· {t} 检查', 'upd.avail': '{v} 已发布 · {t} 检查', 'upd.never': '还没有检查过',
      'upd.fail': '检查失败（连不上 remohop.com？）', 'upd.managed': '已被管理员关闭（{e}）',
      'upd.dev': '开发版本（{v}），不与正式版比较',
      'upd.privacy': '只从 remohop.com 读取最新的版本号，不发送这台电脑的任何信息，也不会在你不知情时安装任何东西。',
      'h.closeq': '断开与 {t} 的连接？', 'h.popout': '在新窗口中打开', 'h.close': '断开并关闭',
      'err.no_server': '请输入服务器地址。', 'err.bad_server': '这不是服务器地址（例如 signal.example.com:21116）。',
      'err.weak_password': '至少 12 个字符，并包含小写、大写、数字、符号中的三种。', 'err.weak_own_password': '至少 8 位，且包含字母、数字、其他字符中的两种。', 'in.show': '显示', 'in.hide': '隐藏',
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
    for (const el of document.querySelectorAll('[data-tp]')) el.placeholder = t(el.dataset.tp);
    // Fixed places in the sidebar keep their name as a tooltip for the
    // icon rail.
    for (const el of document.querySelectorAll('.side .tab:not(.host)')) el.title = el.querySelector('.lbl').textContent;
    sideLabel();
    homeDate();
    if (S && S.setup) { renderPeers(); renderLAN(); renderOut(); renderActivity(); }
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

  // ---- views and the sidebar ----
  // "computer" is Home: this computer's ID and password, its sessions in
  // short, quick connect and the activity of the last hour.
  let view = 'computer';
  const hosts = new Map(); // target -> {tab, viewEl, iframe, origin, title}
  function show(v) {
    view = v;
    for (const el of document.querySelectorAll('#main > .view')) el.hidden = el.id !== 'v-' + v;
    for (const h of hosts.values()) h.viewEl.hidden = v !== 'host:' + h.target;
    for (const b of document.querySelectorAll('.side .tab')) {
      if (b.dataset.view === v) b.setAttribute('aria-current', 'page'); else b.removeAttribute('aria-current');
    }
    document.body.classList.toggle('on-host', v.startsWith('host:'));
    if (S && S.update) renderUpdate(S.update);
    if (v === 'connect' || v === 'computer') loadPeers(false);
    if (v === 'connect') loadLAN();
    const h = v.startsWith('host:') && hosts.get(v.slice(5));
    if (h) h.tab.scrollIntoView({block: 'nearest', inline: 'nearest'});
    else { const el = $('v-' + v); if (el) el.scrollTop = 0; }
  }
  for (const b of document.querySelectorAll('[data-view]')) b.addEventListener('click', () => show(b.dataset.view));

  // The sidebar folds into an icon rail: by choice (remembered on this
  // computer), and always in narrow windows (style.css).
  let railed = false;
  try { railed = localStorage.getItem('remohop.rail') === '1'; } catch (e) { /* no storage */ }
  function sideLabel() {
    const b = $('side-toggle'); if (!b) return;
    document.body.classList.toggle('rail', railed);
    b.setAttribute('aria-pressed', String(railed));
    const k = railed ? 'nav.expand' : 'nav.collapse';
    b.setAttribute('aria-label', t(k)); b.title = t(k);
  }
  $('side-toggle').addEventListener('click', () => {
    railed = !railed; sideLabel();
    try { localStorage.setItem('remohop.rail', railed ? '1' : '0'); } catch (e) { /* no storage */ }
  });
  // Home's date line.
  function homeDate() {
    const d = new Date();
    $('home-date').textContent = d.toLocaleDateString(lang === 'zh' ? 'zh-CN' : 'en', {weekday: 'long', month: 'long', day: 'numeric'}) +
      ' · ' + d.toLocaleTimeString(lang === 'zh' ? 'zh-CN' : 'en', {hour: '2-digit', minute: '2-digit'});
  }
  setInterval(() => { homeDate(); if (S && S.setup) { sampleActivity(); renderActivity(); renderPeerTimes(); } }, 30000);
  // Settings' index scrolls to its group.
  for (const b of document.querySelectorAll('#set-index [data-sec]')) b.addEventListener('click', () => {
    const el = $(b.dataset.sec); el.scrollIntoView({block: 'start', behavior: reduceMotion() ? 'auto' : 'smooth'});
  });
  const reduceMotion = () => window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches;

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
    // The app opens on Home, which has this computer's ID and password for
    // someone who is here to be helped and quick connect for everyone
    // else; right after the first-run wizard it opens where that left off.
    if (first || !$('v-wizard').hidden) show(view);
    renderComputer(s);
    renderSettings(s);
    renderRequests(s);
    renderQueue(s);
    sampleActivity(); renderActivity();
    if (s.smoke && s.smoke !== lastSmoke) { lastSmoke = s.smoke; smokeView(s.smoke); }
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
      // The new state may have arrived before the answer did.
      if (S && S.setup) show(view);
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
  setInterval(() => { if (view === 'connect' || view === 'computer') loadPeers(false); }, 30000);
  function peerTitle(p) { return p.alias || p.name || p.target; }
  // An ID is shown in groups of three, as this computer's own.
  const fmtID = s => /^\d{9}$/.test(s || '') ? s.replace(/(\d{3})(?=\d)/g, '$1 ') : (s || '');
  const PLATFORMS = {windows: 'Windows', darwin: 'macOS', macos: 'macOS', linux: 'Linux', android: 'Android', ios: 'iOS', freebsd: 'FreeBSD'};
  const platformName = p => p ? (PLATFORMS[String(p).toLowerCase().split('/')[0]] || p) : '—';
  function ago(iso) {
    if (!iso) return '—';
    const d = new Date(iso); if (isNaN(d)) return '—';
    const s = Math.round((d.getTime() - Date.now()) / 1000);
    try {
      const rtf = new Intl.RelativeTimeFormat(lang === 'zh' ? 'zh-CN' : 'en', {numeric: 'auto'});
      const a = Math.abs(s);
      if (a < 60) return rtf.format(0, 'second');
      if (a < 3600) return rtf.format(Math.round(s / 60), 'minute');
      if (a < 86400) return rtf.format(Math.round(s / 3600), 'hour');
      if (a < 86400 * 45) return rtf.format(Math.round(s / 86400), 'day');
    } catch (e) { /* old engine: a date */ }
    return d.toLocaleDateString(lang === 'zh' ? 'zh-CN' : 'en', {dateStyle: 'medium'});
  }
  // avatar is a computer's initial with its presence dot.
  function avatar(title, online) {
    const av = document.createElement('div'); av.className = 'avatar'; av.textContent = (title[0] || '?').toUpperCase();
    const pr = document.createElement('span'); pr.className = 'pres' + (online ? ' online' : ''); av.append(pr);
    return av;
  }
  function icon(name) {
    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg'); svg.setAttribute('class', 'i');
    const use = document.createElementNS('http://www.w3.org/2000/svg', 'use'); use.setAttribute('href', '#' + name);
    svg.append(use); return svg;
  }
  let peerFilter = '';
  function peerMatches(p) {
    if (peerFilter === 'online' && p.presence !== 'online') return false;
    if (peerFilter === 'paired' && p.login !== 'pairing') return false;
    const q = $('peer-q').value.trim().toLowerCase().replace(/\s+/g, '');
    if (!q) return true;
    return [p.alias, p.name, p.target].some(x => x && x.toLowerCase().replace(/\s+/g, '').includes(q));
  }
  function renderPeers() {
    const box = $('peers'); box.textContent = '';
    $('peers-empty').hidden = peers.length > 0;
    const shown = peers.filter(peerMatches);
    $('peers-nomatch').hidden = !peers.length || shown.length > 0;
    $('peers-head').hidden = !shown.length;
    for (const p of shown) {
      const el = document.createElement('div'); el.className = 'peer';
      const av = avatar(peerTitle(p), p.presence === 'online');
      const txt = document.createElement('div'); txt.className = 'grow';
      const a = document.createElement('div'); a.className = 't'; a.textContent = peerTitle(p);
      const b = document.createElement('div'); b.className = 's';
      const bits = [];
      if (p.alias || p.name) bits.push(fmtID(p.target));
      bits.push(t(p.presence === 'online' ? 'p.online' : p.presence === 'offline' ? 'p.offline' : 'p.unknown'));
      if (p.login === 'pairing') bits.push(t('p.paired'));
      if (p.login === 'saved_password') bits.push(t('p.savedpw'));
      if (p.account) bits.push(t('p.account'));
      b.textContent = bits.join(' · ');
      txt.append(a, b);
      const name = document.createElement('div'); name.className = 'pc-name'; name.append(av, txt);
      const plat = document.createElement('div'); plat.className = 'pc-col'; plat.textContent = platformName(p.platform);
      const seen = document.createElement('div'); seen.className = 'pc-col seen'; seen.dataset.at = p.last_seen || '';
      seen.textContent = p.presence === 'online' ? t('p.online') : ago(p.last_seen);
      const acts = document.createElement('div'); acts.className = 'pc-acts';
      const go = document.createElement('button'); go.className = 'btn tonal sm'; go.textContent = t('c.connect');
      go.addEventListener('click', () => openHost(p.target, peerTitle(p)));
      const more = document.createElement('button'); more.className = 'icon-btn'; more.setAttribute('aria-label', t('p.more', {t: peerTitle(p)})); more.title = '…';
      more.append(icon('i-more'));
      more.addEventListener('click', () => peerMenu(p));
      acts.append(go, more);
      el.append(name, plat, seen, acts);
      box.append(el);
    }
    // counts: the sidebar, the section, Home
    const online = peers.filter(p => p.presence === 'online').length;
    $('nav-peers').hidden = !peers.length; $('nav-peers').textContent = peers.length;
    $('peers-count').hidden = !peers.length; $('peers-count').textContent = peers.length;
    $('k-saved').textContent = peers.length;
    $('k-saved-s').textContent = peers.length ? t('kpi.saved.s', {n: online}) : t('kpi.saved.s0');
    // Home: the computers seen most recently, online ones first.
    const rec = $('recent'); rec.textContent = '';
    const order = [...peers].sort((x, y) => (y.presence === 'online') - (x.presence === 'online') ||
      (Date.parse(y.last_seen || 0) || 0) - (Date.parse(x.last_seen || 0) || 0));
    $('recent-empty').hidden = peers.length > 0; $('recent-l').hidden = !peers.length;
    for (const p of order.slice(0, 4)) {
      const r = document.createElement('button'); r.type = 'button'; r.className = 'rec';
      const txt = document.createElement('span'); txt.className = 'grow';
      const a = document.createElement('span'); a.className = 't'; a.textContent = peerTitle(p);
      const b = document.createElement('span'); b.className = 's mono'; b.textContent = fmtID(p.target);
      txt.append(a, b);
      const st = document.createElement('span'); st.className = 'rec-st'; st.textContent = p.presence === 'online' ? t('p.online') : ago(p.last_seen);
      r.append(avatar(peerTitle(p), p.presence === 'online'), txt, st, icon('i-chev'));
      r.title = t('c.connect') + ' · ' + peerTitle(p);
      r.addEventListener('click', () => openHost(p.target, peerTitle(p)));
      rec.append(r);
    }
  }
  // The "last seen" times age without a new list.
  function renderPeerTimes() {
    for (const el of document.querySelectorAll('#peers .seen[data-at]')) if (el.dataset.at && el.textContent !== t('p.online')) el.textContent = ago(el.dataset.at);
  }
  $('peer-filter').addEventListener('click', ev => {
    const b = ev.target.closest('button'); if (!b) return;
    peerFilter = b.dataset.f;
    for (const x of $('peer-filter').children) x.setAttribute('aria-pressed', String(x === b));
    renderPeers();
  });
  $('peer-q').addEventListener('input', renderPeers);
  $('peers-refresh').addEventListener('click', () => loadPeers(true));
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
  // A saved password, shown so the user can check it, with Copy.
  function showSecret(title, value) {
    const d = document.createElement('dialog');
    const f = document.createElement('form'); f.method = 'dialog'; f.className = 'dlg';
    const h = document.createElement('h2'); h.textContent = title;
    const v = document.createElement('div'); v.className = 'secret mono'; v.textContent = value;
    const acts = document.createElement('div'); acts.className = 'acts';
    const cp = document.createElement('button'); cp.type = 'button'; cp.className = 'btn tonal'; cp.textContent = t('copy');
    cp.addEventListener('click', async () => {
      try { await navigator.clipboard.writeText(value); } catch (e) { /* shown anyway */ }
      snack(t('copied'));
    });
    const c = document.createElement('button'); c.className = 'btn'; c.value = ''; c.textContent = t('close');
    acts.append(cp, c); f.append(h, v, acts); d.append(f); document.body.append(d); d.showModal();
    return new Promise(res => d.addEventListener('close', () => { d.remove(); res(); }, {once: true}));
  }
  async function peerMenu(p) {
    const items = [{id: 'rename', label: t('p.rename')}];
    if (p.wake) items.push({id: 'wake', label: t('p.wake')});
    if (p.login === 'saved_password') items.push({id: 'showpw', label: t('p.showpw')}, {id: 'forgetpw', label: t('p.forgetpw')});
    items.push({id: 'mac', label: t('p.mac')}, {id: 'forget', label: t('p.forget')});
    const a = await sheet(peerTitle(p), items);
    try {
      if (a === 'rename') { const v = await ask(t('p.rename.t', {t: p.target}), t('p.name'), p.alias); if (v !== null) await api('peer/alias', {target: p.target, alias: v}); }
      if (a === 'wake') { await api('peer/wake', {target: p.target}); snack(t('p.woke')); }
      if (a === 'mac') { const v = await ask(t('p.mac.t', {t: peerTitle(p)}), t('p.mac.l'), ''); if (v !== null) { await api('peer/mac', {target: p.target, mac: v}); snack(t('saved')); } }
      if (a === 'showpw') { const r = await api('peer/password', {target: p.target}); await showSecret(t('p.pw.t', {t: peerTitle(p)}), r.password); return; }
      if (a === 'forgetpw') { await api('peer/password/forget', {target: p.target}); snack(t('p.forgetpw.done')); }
      if (a === 'forget') { const ok = await sheet(t('p.forget.q', {t: peerTitle(p)}), [{id: 'yes', label: t('p.forget')}]); if (ok === 'yes') await api('peer/forget', {target: p.target}); }
      if (a) loadPeers(true);
    } catch (e) { snack(errText(e)); }
  }
  // ---- on this network ----
  // The computers that answered discovery on the local network, looked
  // for every few seconds while Connect is shown (and not when the window
  // is hidden). Connect uses the address; what a computer says about
  // itself is only a hint, its key is checked when connecting.
  let lanHosts = null, lanErr = '', lanBusy = false;
  async function loadLAN() {
    if (lanBusy || !S || !S.setup || view !== 'connect' || document.hidden) return;
    lanBusy = true;
    try { const j = await api('lan', {}); lanHosts = j.hosts || []; lanErr = j.error || ''; renderLAN(); }
    catch (e) { /* keep the old list */ } finally { lanBusy = false; }
  }
  setInterval(loadLAN, 4000);
  document.addEventListener('visibilitychange', loadLAN);
  function renderLAN() {
    const box = $('lan'); box.textContent = '';
    const list = lanHosts || [];
    $('lan-count').hidden = !list.length; $('lan-count').textContent = list.length;
    $('lan-head').hidden = !list.length;
    $('lan-empty').hidden = list.length > 0;
    $('lan-empty-t').textContent = lanErr ? t('lan.err', {m: lanErr}) : t(lanHosts === null ? 'lan.looking' : 'lan.none');
    for (const h of list) {
      const title = h.name || h.addr;
      const el = document.createElement('div'); el.className = 'peer';
      const txt = document.createElement('div'); txt.className = 'grow';
      const a = document.createElement('div'); a.className = 't'; a.textContent = title;
      const b = document.createElement('div'); b.className = 's';
      const bits = [h.addr];
      if (h.saved) bits.push(t('lan.saved', {t: fmtID(h.saved)}));
      if (h.paired) bits.push(t('p.paired'));
      b.textContent = bits.join(' · ');
      txt.append(a, b);
      const name = document.createElement('div'); name.className = 'pc-name'; name.append(avatar(title, true), txt);
      const id = document.createElement('div'); id.className = 'pc-col mono'; id.textContent = h.id ? fmtID(h.id) : '—';
      const key = document.createElement('div'); key.className = 'pc-col mono';
      if (h.conflict) {
        const c = document.createElement('span'); c.className = 'chip bad'; c.textContent = t('lan.conflict');
        c.title = t('lan.conflict.tip', {t: fmtID(h.conflict)});
        key.append(c);
      } else { key.textContent = h.fingerprint; key.title = h.fingerprint; }
      const acts = document.createElement('div'); acts.className = 'pc-acts';
      const go = document.createElement('button'); go.className = 'btn tonal sm'; go.textContent = t('c.connect');
      go.setAttribute('aria-label', t('c.connect') + ' · ' + title);
      go.addEventListener('click', () => openHost(h.addr, title));
      acts.append(go);
      el.append(name, id, key, acts);
      box.append(el);
    }
  }

  // Connect (and Home's quick connect): an ID, a saved name or host:port.
  function connectTo(input) {
    const v = input.value.trim(); if (!v) return;
    $('c-err').textContent = '';
    const p = peers.find(x => x.target === v.replace(/\s+/g, '') || x.alias === v);
    openHost(p ? p.target : v, p ? peerTitle(p) : v);
  }
  $('connect-form').addEventListener('submit', ev => { ev.preventDefault(); connectTo($('target')); });
  $('qc-form').addEventListener('submit', ev => { ev.preventDefault(); connectTo($('qc-target')); });

  // ---- host tabs ----
  async function openHost(target, title) {
    target = target.trim();
    if (/^[\d\s]+$/.test(target)) target = target.replace(/\s+/g, '');
    if (hosts.has(target)) { show('host:' + target); return; }
    let url;
    try { url = (await api('console', {target})).url; } catch (e) { $('c-err').textContent = errText(e); show('connect'); return; }
    const h = {target, title: title || target, origin: new URL(url).origin, state: 'wait'};
    // A computer open in this window: a closable row of the sidebar (an
    // avatar with its connection dot in the icon rail).
    const tab = document.createElement('div'); tab.className = 'tab host'; tab.setAttribute('role', 'button'); tab.dataset.view = 'host:' + target;
    tab.tabIndex = 0;
    const hav = document.createElement('span'); hav.className = 'hav';
    const letter = document.createElement('span'); letter.className = 'hav-l'; letter.textContent = (h.title[0] || '?').toUpperCase();
    const dot = document.createElement('span'); dot.className = 'dot wait';
    hav.append(letter, dot);
    const name = document.createElement('span'); name.className = 'name lbl'; name.textContent = h.title;
    const pop = document.createElement('button'); pop.className = 'x pop'; pop.title = t('h.popout'); pop.setAttribute('aria-label', t('h.popout'));
    pop.append(icon('i-popout'));
    const x = document.createElement('button'); x.className = 'x close'; x.title = t('h.close'); x.setAttribute('aria-label', t('h.close'));
    x.append(icon('i-close'));
    tab.append(hav, name, pop, x);
    tab.title = h.title;
    tab.addEventListener('click', ev => { if (!ev.target.closest('.x')) show('host:' + target); });
    tab.addEventListener('keydown', ev => { if (ev.target === tab && (ev.key === 'Enter' || ev.key === ' ')) { ev.preventDefault(); show('host:' + target); } });
    x.addEventListener('click', () => closeHost(target, true));
    pop.addEventListener('click', () => popOut(target));
    $('host-tabs').append(tab);
    const v = document.createElement('section'); v.className = 'view host'; v.setAttribute('aria-label', h.title);
    const f = document.createElement('iframe');
    f.title = h.title; f.allow = 'fullscreen; clipboard-read; clipboard-write; autoplay';
    f.src = url;
    v.append(f); $('hostviews').append(v);
    Object.assign(h, {tab, dot, name, letter, viewEl: v, iframe: f});
    hosts.set(target, h);
    renderOut();
    show('host:' + target);
  }
  function closeHost(target, disconnect) {
    const h = hosts.get(target); if (!h) return;
    if (disconnect) {
      try { h.iframe.contentWindow.postMessage({type: 'remohop-disconnect'}, h.origin); } catch (e) { /* gone */ }
      api('console/close', {target}).catch(() => {});
    }
    h.tab.remove(); h.viewEl.remove(); hosts.delete(target);
    renderOut();
    if (view === 'host:' + target) show(hosts.size ? 'host:' + [...hosts.keys()].pop() : 'connect');
  }
  // The computers open in this window, on the Sessions page.
  function renderOut() {
    $('host-sec').hidden = !hosts.size;
    $('out-count').hidden = !hosts.size; $('out-count').textContent = hosts.size;
    $('out-empty').hidden = hosts.size > 0;
    const box = $('out-list'); box.textContent = '';
    for (const h of hosts.values()) {
      const r = document.createElement('div'); r.className = 'list-row';
      const av = avatar(h.title, h.state === 'connected');
      const g = document.createElement('div'); g.className = 'grow';
      const a = document.createElement('div'); a.className = 't'; a.textContent = h.title;
      const b = document.createElement('div'); b.className = 'muted small';
      b.textContent = [fmtID(h.target) !== h.title ? fmtID(h.target) : '', t(h.state === 'connected' ? 'out.connected' : h.state === 'closed' ? 'out.closed' : 'out.wait')].filter(Boolean).join(' · ');
      g.append(a, b);
      const sh = document.createElement('button'); sh.type = 'button'; sh.className = 'btn tonal sm'; sh.textContent = t('out.show');
      sh.addEventListener('click', () => show('host:' + h.target));
      const pop = document.createElement('button'); pop.type = 'button'; pop.className = 'icon-btn'; pop.title = t('h.popout'); pop.setAttribute('aria-label', t('h.popout'));
      pop.append(icon('i-popout')); pop.addEventListener('click', () => popOut(h.target));
      const x = document.createElement('button'); x.type = 'button'; x.className = 'btn text sm'; x.textContent = t('s.disc');
      x.addEventListener('click', () => closeHost(h.target, true));
      r.append(av, g, sh, pop, x); box.append(r);
    }
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
      h.state = m.state === 'connected' ? 'connected' : m.state === 'closed' ? 'closed' : 'wait';
      if (m.title && typeof m.title === 'string') {
        h.title = m.title.slice(0, 80); h.name.textContent = h.title; h.tab.title = h.title;
        h.letter.textContent = (h.title[0] || '?').toUpperCase(); h.viewEl.setAttribute('aria-label', h.title);
      }
      renderOut();
    } else if (m.type === 'remohop-closed') {
      closeHost(h.target, false);
    } else if (m.type === 'remohop-sid' && pendingPop.get(h.target) && typeof m.sid === 'string') {
      pendingPop.delete(h.target);
      try { await api('popout', {target: h.target, title: h.title, sid: m.sid}); closeHost(h.target, false); }
      catch (e) { snack(errText(e)); }
    }
  });

  // ---- this computer ----
  // Screen sharing without ffmpeg: say so while others may connect, and
  // offer to install it where RemoHop can (Windows, macOS).
  function renderFFmpeg(ff, on) {
    $('ff').hidden = !(on && ff.missing) && !ff.stage;
    const btn = $('ff-install');
    btn.hidden = !ff.can_install; btn.disabled = !!ff.stage;
    $('ff-install-t').textContent = t('ff.install', {mb: ff.size_mb});
    let st = '';
    if (ff.stage) st = t('ff.' + ff.stage, {p: ff.percent});
    else if (ff.error) st = t('ff.failed', {m: ff.error});
    $('ff-status').textContent = st;
    $('ff-row').hidden = btn.hidden && !st;
    let note = '';
    if (ff.can_install) note = t('ff.from', {v: ff.version, s: ff.source});
    else if (ff.disabled) note = t('ff.off', {b: ff.disabled});
    else if (S && S.ui && S.ui.platform === 'linux') note = t('ff.linux');
    $('ff-note').textContent = note; $('ff-note').hidden = !note;
  }
  $('ff-install').addEventListener('click', () => api('ffmpeg/install').catch(e => snack(errText(e))));

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
    // A server that does not answer within about 10 s is likely a wrong
    // address: say so, and lead to the setting.
    const unreach = i.on && i.phase === 'starting' && i.waited >= 10 && s.server.mode !== 'none';
    if (unreach) { cls = 'bad'; txt = t('in.unreach', {s: s.server.mode === 'custom' ? s.server.addr : t('in.official')}); }
    $('in-fix').hidden = !unreach;
    $('in-status').className = 'status ' + cls; $('in-status-t').textContent = txt;
    const showCreds = i.on && (i.phase === 'ready' || i.phase === 'starting' || i.phase === 'no_server');
    $('creds').hidden = !showCreds;
    $('in-offnote').hidden = i.on;
    // The sidebar's foot: this computer, reachable or not, and its ID.
    $('me-pres').className = 'pres' + (cls === 'ok' ? ' online' : cls === 'wait' ? ' wait' : cls === 'bad' ? ' bad' : '');
    $('me-s').textContent = showCreds && i.id ? i.id : t(i.on && cls !== 'bad' ? 'me.on' : 'me.off');
    $('me').title = t('me.title') + ' · ' + txt;
    const id = $('in-id');
    id.textContent = i.id || (i.phase === 'no_server' ? '—' : '…'); id.classList.toggle('pending', !i.id);
    const fixed = i.password_mode === 'fixed';
    $('in-pw-l').textContent = t(fixed ? 'in.pwfixed' : 'in.pw');
    $('in-pw').dataset.value = i.password || '';
    $('in-pw').textContent = !i.password ? '…' : fixed && !pwShown ? '••••••••' : i.password;
    $('regen').hidden = fixed; $('pw-reveal').hidden = !fixed;
    $('pw-reveal').setAttribute('aria-pressed', String(pwShown));
    $('pw-reveal').querySelector('span').textContent = t(pwShown ? 'in.hide' : 'in.show');
    $('in-addr').hidden = !(i.addresses && i.addresses.length);
    if (i.addresses) $('in-addr').textContent = t('in.addr', {a: i.addresses.slice(0, 3).join(', ')});
    $('in-err').textContent = i.phase === 'error' ? (i.error || '') : '';
    renderFFmpeg(s.ffmpeg || {}, i.on);
    // sessions
    const box = $('sessions'); box.textContent = '';
    $('sessions-box').hidden = !i.sessions.length;
    $('sessions-empty').hidden = i.sessions.length > 0;
    $('badge').hidden = !i.sessions.length;
    $('live-count').hidden = !i.sessions.length; $('live-count').textContent = i.sessions.length;
    $('k-live').textContent = i.sessions.length;
    $('k-live-s').textContent = i.sessions.length ? t('kpi.live.s', {c: i.sessions.filter(x => x.control).length, f: i.sessions.filter(x => x.files).length}) : t('kpi.live.s0');
    $('k-live-dot').classList.toggle('on', i.sessions.length > 0);
    for (const x of i.sessions) {
      const el = document.createElement('div'); el.className = 'sess';
      el.append(avatar(x.label || '?', true));
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
    let us = t('un.off'), sub = '', ucls = 'bad';
    if (!u.available) { sub = t('un.na'); ucls = ''; }
    if (u.installed) { us = t('un.on'); sub = u.running ? t('un.runok') : t('un.stopped'); ucls = u.running ? 'ok' : 'wait'; }
    if (u.busy) { sub = t('un.busy'); ucls = 'wait'; }
    $('un-status').className = 'status ' + ucls;
    $('un-state').textContent = us; $('un-sub').textContent = u.error ? errText({message: u.error}) : sub;
    $('un-switch').checked = !!u.installed; $('un-switch').disabled = !u.available || !!u.busy;
    $('un-open-st').className = 'status ' + ucls; $('un-open-t').textContent = us;
    $('un-manage').hidden = !u.installed || !!u.busy;
    $('svc-row').hidden = !u.installed;
    $('un-admin2').hidden = !u.needs_admin;
    // The service's own ID and password (another ID than this app's on
    // Windows). The password shows only when asked for, and hides again.
    $('un-creds').hidden = !u.installed; $('un-creds-off').hidden = !!u.installed;
    $('un-admin3').hidden = !u.installed || !u.needs_admin;
    if (!u.installed) unPw = null;
    $('un-id').textContent = u.id || '—'; $('un-id').dataset.value = u.id || '';
    $('un-idcopy').hidden = !u.id; $('un-idget').hidden = !!u.id;
    $('un-pwv').textContent = unPw === null ? '••••••••' : unPw; $('un-pwv').dataset.value = unPw || '';
    $('un-pwcopy').hidden = !unPw;
    $('un-reveal').setAttribute('aria-pressed', String(unPw !== null));
    $('un-reveal').querySelector('span').textContent = t(unPw !== null ? 'in.hide' : 'in.show');
    // trusted
    const tr = $('trusted'); tr.textContent = '';
    $('trusted-box').hidden = !s.trusted.length;
    $('trusted-empty').hidden = s.trusted.length > 0;
    for (const d of s.trusted) {
      const r = document.createElement('div'); r.className = 'item';
      r.append(tile('', 'i-device'));
      const g = document.createElement('div'); g.className = 'lbl';
      const a = document.createElement('div'); a.textContent = d.name || t('req.unknown');
      const b = document.createElement('div'); b.className = 'muted small mono';
      b.textContent = keyText(d.identity);
      g.append(a, b);
      // The access it gets next time: the same chips as Connected now.
      const chips = document.createElement('div'); chips.className = 'chips';
      const view = document.createElement('span'); view.className = 'chip'; view.setAttribute('aria-pressed', 'true'); view.textContent = t('s.view');
      chips.append(view);
      for (const [k, on] of [['control', d.control], ['files', d.files]]) {
        const c = document.createElement('button'); c.className = 'chip'; c.setAttribute('aria-pressed', String(on)); c.textContent = t('s.' + k);
        c.addEventListener('click', () => api('edit-device', {identity: d.identity, alias: d.name || '', control: k === 'control' ? !d.control : d.control, files: k === 'files' ? !d.files : d.files}).catch(e => snack(errText(e))));
        chips.append(c);
      }
      const rn = document.createElement('button'); rn.className = 'btn text sm'; rn.textContent = t('p.rename');
      rn.addEventListener('click', async () => {
        const v = await ask(t('tr.rename'), t('p.name'), d.name || '');
        if (v !== null && v.trim()) api('edit-device', {identity: d.identity, alias: v.trim(), control: d.control, files: d.files}).catch(e => snack(errText(e)));
      });
      const f = document.createElement('button'); f.className = 'btn text sm'; f.textContent = t('tr.forget');
      f.addEventListener('click', () => api('forget-device', {identity: d.identity}).catch(e => snack(errText(e))));
      const acts = document.createElement('div'); acts.className = 'tr-acts'; acts.append(chips, rn, f);
      r.append(g, acts); tr.append(r);
    }
  }
  // tile is a list row's coloured icon (style.css .tile).
  function tile(colour, name) {
    const sp = document.createElement('span'); sp.className = ('tile ' + colour).trim();
    sp.append(icon(name)); return sp;
  }
  function fmtDur(s) { const m = Math.floor(s / 60); return m < 60 ? m + ':' + String(s % 60).padStart(2, '0') : Math.floor(m / 60) + 'h' + String(m % 60).padStart(2, '0'); }
  // A key fingerprint is short enough to show whole; cutting it would
  // leave half a group that matches nothing the other side reads out.
  function keyText(k) { return k || ''; }
  $('incoming').addEventListener('change', ev => api('incoming', {on: ev.target.checked}).catch(e => { snack(errText(e)); }));
  $('regen').addEventListener('click', () => api('regenerate').catch(e => snack(errText(e))));
  // The user's own password shows as dots until "Show password".
  let pwShown = false;
  let unPw = null, unPwTimer = 0; // the unattended password, while shown
  $('pw-reveal').addEventListener('click', () => { pwShown = !pwShown; if (S) render(S); });
  $('pw-set').addEventListener('click', () => {
    const fixed = S.incoming.password_mode === 'fixed';
    for (const r of document.querySelectorAll('input[name=pwmode]')) r.checked = r.value === (fixed ? 'fixed' : 'temporary');
    $('pw-1').value = $('pw-2').value = ''; $('pw-err').textContent = '';
    $('dlg-pw').showModal();
  });
  for (const id of ['pw-1', 'pw-2']) $(id).addEventListener('focus', () => { document.querySelector('input[name=pwmode][value=fixed]').checked = true; });
  $('pw-form').addEventListener('submit', async ev => {
    if (ev.submitter && ev.submitter.value !== 'ok') return;
    ev.preventDefault();
    const fixed = document.querySelector('input[name=pwmode]:checked').value === 'fixed';
    if (fixed && $('pw-1').value !== $('pw-2').value) { $('pw-err').textContent = t('un.mismatch'); return; }
    if (fixed && !$('pw-1').value && S.incoming.password_mode === 'fixed') { $('dlg-pw').close(); return; } // unchanged
    try { await api('own-password', {password: fixed ? $('pw-1').value : ''}); $('dlg-pw').close(); pwShown = false; snack(t('pwm.saved')); }
    catch (e) { $('pw-err').textContent = errText(e); }
  });
  $('disc-all').addEventListener('click', () => api('disconnect', {id: 0}).catch(e => snack(errText(e))));
  for (const b of document.querySelectorAll('[data-copy]')) b.addEventListener('click', async () => {
    const src = $(b.dataset.copy), v = (src.dataset.value || src.textContent).trim();
    try { await navigator.clipboard.writeText(v); } catch (e) {
      const ta = document.createElement('textarea'); ta.value = v; document.body.append(ta); ta.select(); document.execCommand('copy'); ta.remove();
    }
    snack(t('copied'));
  });

  // unattended ID and password
  $('un-reveal').addEventListener('click', async () => {
    clearTimeout(unPwTimer);
    if (unPw !== null) { unPw = null; render(S); return; }
    try {
      const r = await api('unattended/reveal');
      unPw = (r && r.password) || '';
      if (!unPw) snack(t('un.nopw'));
      unPwTimer = setTimeout(() => { unPw = null; if (S) render(S); }, 60000);
    } catch (e) { snack(errText(e)); }
    if (S) render(S);
  });
  $('un-idget').addEventListener('click', async () => {
    try { await api('unattended/sessions'); } catch (e) { snack(errText(e)); }
  });
  // unattended dialog
  $('un-open').addEventListener('click', () => $('dlg-uncreds').showModal());
  $('dlg-uncreds').addEventListener('close', () => { clearTimeout(unPwTimer); unPw = null; if (S) render(S); });
  // The switch shows the service's state; flipping it asks for that state,
  // and the next render shows what came of it.
  $('un-switch').addEventListener('change', async ev => {
    const on = ev.target.checked;
    ev.target.checked = !on;
    if (!on) {
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
    $('un-ok').disabled = true; $('un-err').textContent = ''; $('un-wait').hidden = false;
    const label = $('un-ok').textContent; $('un-ok').textContent = t('un.busy');
    try {
      await api(pwMode === 'change' ? 'unattended/password' : 'unattended/enable', {password: $('un-pw1').value});
      $('dlg-un').close();
      if (pwMode === 'change') snack(t('un.pwdone'));
    }
    catch (e) { $('un-err').textContent = errText(e); } finally { $('un-ok').disabled = false; $('un-ok').textContent = label; $('un-wait').hidden = true; }
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
  for (const id of ['un-sess', 'un-sess2']) $(id).addEventListener('click', () => { $('dlg-svc').showModal(); loadSvc(); });
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
    // An action of a session on a grant is answered like a request for
    // more access: allow or deny, nothing to choose.
    const access = (r.access && r.access.length) || r.action;
    $('req-title').textContent = r.action ? t('req.title.action', {d: who, g: r.grant}) : t(access ? 'req.title.access' : 'req.title', {d: who});
    const chips = $('req-chips'); chips.textContent = '';
    if (!access) {
      const c = document.createElement('span'); c.className = 'chip ' + (r.first_seen ? 'bad' : 'ok'); c.textContent = t(r.first_seen ? 'req.new' : 'req.known'); chips.append(c);
    }
    $('req-warn').hidden = !!access || !r.first_seen;
    const kv = $('req-kv'); kv.textContent = '';
    const add = (k, v) => { if (!v) return; const dt = document.createElement('dt'); dt.textContent = t(k); const dd = document.createElement('dd'); dd.textContent = v; kv.append(dt, dd); };
    add('req.device', who); add('req.key', keyText(r.identity)); add('req.from', r.from);
    if (r.action) add('req.action', r.action);
    else if (access) add('req.asks', r.access.map(f => t('req.' + f)).filter(Boolean).join(', '));
    $('req-choice').hidden = !!access; $('req-remember-row').hidden = !!access;
    // An unknown device gets view only unless the user ticks more.
    $('req-control').checked = false; $('req-files').checked = false; $('req-remember').checked = false;
    updateLeft(r);
    d.returnValue = '';
    if (!d.open) d.showModal();
    $('req-deny').focus();
  }
  function updateLeft(r) { $('req-left').textContent = t('req.left', {n: r.left}); }

  // ---- the approval queue (Sessions) ----
  // Every request waiting for an answer, the first one also in the dialog,
  // with the time it has left as a bar of cells.
  function renderQueue(s) {
    const list = s.incoming.requests, total = s.ask_timeout || 60;
    for (const id of ['q-count', 'nav-queue']) { $(id).hidden = !list.length; $(id).textContent = list.length; }
    $('queue-empty').hidden = list.length > 0;
    $('k-queue').textContent = list.length;
    $('k-queue-dot').classList.toggle('on', list.length > 0);
    $('k-queue-s').textContent = list.length ? t('kpi.queue.s', {n: Math.min(...list.map(r => r.left))}) : t('kpi.queue.s0');
    $('q-foot').textContent = t('q.foot', {n: total});
    const box = $('queue'); box.textContent = '';
    list.forEach((r, k) => {
      const who = r.device || r.name || t('req.unknown');
      const access = (r.access && r.access.length) || r.action;
      const c = document.createElement('article'); c.className = 'qcard' + (k === 0 ? ' first' : '');
      const top = document.createElement('div'); top.className = 'qtop mono';
      const n = document.createElement('span'); n.textContent = 'REQ-' + String(r.n).padStart(3, '0');
      const chip = document.createElement('span');
      chip.className = 'chip ' + (access ? 'warn' : r.first_seen ? 'bad' : 'ok');
      chip.textContent = access ? t('req.asks').replace(/[:：]\s*$/, '') : t(r.first_seen ? 'req.new' : 'req.known');
      const left = document.createElement('span'); left.className = 'qleft'; left.textContent = r.left + ' s';
      top.append(n, chip, left);
      const h = document.createElement('div'); h.className = 'qt';
      h.textContent = r.action ? t('req.title.action', {d: who, g: r.grant}) : t(access ? 'req.title.access' : 'req.title', {d: who});
      const sub = document.createElement('div'); sub.className = 'muted small';
      sub.textContent = r.action ? r.action : access ? r.access.map(f => t('req.' + f)).filter(Boolean).join(', ') : [r.from, keyText(r.identity)].filter(Boolean).join(' · ');
      const bar = document.createElement('div'); bar.className = 'runbar'; bar.setAttribute('aria-hidden', 'true');
      const cells = 24, on = Math.max(0, Math.min(cells, Math.round(r.left / total * cells)));
      for (let j = 0; j < cells; j++) { const i = document.createElement('i'); if (j < on) i.className = r.left <= 10 ? 'on low' : 'on'; bar.append(i); }
      const foot = document.createElement('div'); foot.className = 'qfoot small';
      foot.textContent = t(k === 0 ? 'q.open' : 'q.next');
      c.append(top, h, sub, bar, foot);
      box.append(c);
    });
  }

  // ---- activity: devices connected to this computer, last hour ----
  // Only what this window saw (from when it opened) or can tell from the
  // sessions that are still open (each knows how long it has lasted): a
  // minute before that is drawn as "not recorded", never as zero.
  // Kept per minute, drawn as 30 columns of two minutes each.
  const ACT_MIN = 60, ACT_COLS = 30, ACT_ROWS = 5;
  const act = new Map(); // minute number -> most devices connected in it
  let actFrom = Math.floor(Date.now() / 60000);
  function sampleActivity() {
    if (!S || !S.incoming) return;
    const now = Math.floor(Date.now() / 60000);
    const ss = S.incoming.sessions || [];
    act.set(now, Math.max(act.get(now) || 0, ss.length));
    for (const x of ss) {
      const from = Math.max(now - ACT_MIN + 1, Math.floor((Date.now() - (x.seconds || 0) * 1000) / 60000));
      if (from < actFrom) actFrom = from;
    }
    // Minutes since a still-open session began had at least the sessions
    // that were open then.
    for (let m = now - ACT_MIN + 1; m < now; m++) {
      const n = ss.filter(x => Date.now() - (x.seconds || 0) * 1000 <= (m + 1) * 60000).length;
      if (n) act.set(m, Math.max(act.get(m) || 0, n));
    }
    for (const m of act.keys()) if (m <= now - ACT_MIN) act.delete(m);
  }
  function renderActivity() {
    const box = $('act-chart'); if (!box) return;
    const now = Math.floor(Date.now() / 60000), rows = ACT_ROWS, step = 12, r = 4.6, per = ACT_MIN / ACT_COLS;
    const ns = 'http://www.w3.org/2000/svg';
    const svg = document.createElementNS(ns, 'svg');
    svg.setAttribute('class', 'dm'); svg.setAttribute('viewBox', `0 0 ${ACT_COLS * step} ${rows * step}`);
    svg.setAttribute('role', 'img');
    const hm = m => new Date(m * 60000).toLocaleTimeString(lang === 'zh' ? 'zh-CN' : 'en', {hour: '2-digit', minute: '2-digit'});
    let peak = 0, any = false;
    for (let c = 0; c < ACT_COLS; c++) {
      const m0 = now - ACT_MIN + 1 + c * per, m1 = m0 + per - 1;
      const known = m1 >= actFrom;
      let n = 0; for (let m = m0; m <= m1; m++) n = Math.max(n, act.get(m) || 0);
      peak = Math.max(peak, n); if (n) any = true;
      const g = document.createElementNS(ns, 'g');
      const tip = document.createElementNS(ns, 'title');
      const when = hm(m0) + '–' + hm(m1 + 1);
      tip.textContent = known ? t('act.tip', {t: when, n}) : t('act.nodata', {t: when});
      g.append(tip);
      for (let k = 0; k < rows; k++) {
        const rect = document.createElementNS(ns, 'rect');
        const level = rows - k; // rows from the top: 5 ... 1
        rect.setAttribute('x', c * step + (step - 2 * r) / 2); rect.setAttribute('y', k * step + (step - 2 * r) / 2);
        rect.setAttribute('width', 2 * r); rect.setAttribute('height', 2 * r); rect.setAttribute('rx', 2);
        rect.setAttribute('class', !known ? 'nd' : n >= level ? 'v' + Math.min(n, 4) : 'z');
        g.append(rect);
      }
      svg.append(g);
    }
    const label = t('act.title') + ': ' + (any ? t('act.peak', {n: peak}) : t('act.none'));
    svg.setAttribute('aria-label', label);
    box.textContent = ''; box.append(svg);
    $('act-none').hidden = any;
    $('act-peak').hidden = !any; $('act-peak').textContent = t('act.peak', {n: peak});
    $('act-since').textContent = actFrom > now - ACT_MIN + 1 ? t('act.since', {t: hm(actFrom)}) : '';
  }
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
    $('srv-val').textContent = s.server.mode === 'custom' ? s.server.addr : s.server.mode === 'none' ? t('srv.none') : t('srv.official');
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
    const how = m.command ? 'cmd' : (m.kind === 'package' || m.kind === 'nix') ? m.kind : u.can_install ? 'install' : 'download';
    if (u.stage) $('upd-how').textContent = t('upd.st.' + u.stage, {p: u.percent >= 0 ? u.percent + '%' : ''});
    else if (u.install_error) $('upd-how').textContent = t('upd.failed', {m: u.install_error});
    else $('upd-how').textContent = t('upd.how.' + how, {c: u.current});
    $('upd-cmd').hidden = $('upd-copy').hidden = !m.command;
    $('upd-cmd').textContent = m.command || '';
    $('upd-install').hidden = !u.can_install;
    $('upd-install').disabled = !!u.stage;
    $('upd-get').hidden = how !== 'download' && how !== 'install';
    $('upd-get').className = u.can_install ? 'btn sm text' : 'btn sm';
    $('upd-skip').disabled = !!u.stage;
  }
  $('diag-export').addEventListener('click', async () => {
    $('diag-export').disabled = true;
    try { const r = await api('diagnostics/export'); snack(t('diag.saved', {p: r.path})); }
    catch (e) { snack(errText(e)); } finally { $('diag-export').disabled = false; }
  });
  for (const b of document.querySelectorAll('[data-page]')) b.addEventListener('click', () => api('open-page', {target: b.dataset.page, lang}).catch(e => snack(errText(e))));
  $('upd-auto').addEventListener('change', ev => api('update/auto', {on: ev.target.checked}).catch(e => snack(errText(e))));
  $('upd-now').addEventListener('click', () => api('update/check').catch(e => snack(errText(e))));
  $('upd-install').addEventListener('click', () => api('update/install').catch(e => snack(errText(e))));
  $('upd-get').addEventListener('click', () => api('update/open', {target: 'download', lang}).catch(e => snack(errText(e))));
  $('upd-notes').addEventListener('click', () => api('update/open', {target: 'notes', lang}).catch(e => snack(errText(e))));
  $('upd-skip').addEventListener('click', () => api('update/skip', {version: S.update.latest}).catch(e => snack(errText(e))));
  for (const el of document.querySelectorAll('input[name=srv], #srv-addr')) el.addEventListener('input', () => { srvEdited = true; });
  $('srv-addr').addEventListener('focus', () => { document.querySelector('input[name=srv][value=custom]').checked = true; srvEdited = true; });
  $('in-fix').addEventListener('click', () => {
    show('settings');
    $('srv-panel').hidden = false; $('srv-row').setAttribute('aria-expanded', 'true');
    const custom = document.querySelector('input[name=srv][value=custom]');
    (custom && custom.checked ? $('srv-addr') : $('srv-row')).focus();
  });
  $('srv-row').addEventListener('click', () => {
    const open = $('srv-panel').hidden;
    $('srv-panel').hidden = !open; $('srv-row').setAttribute('aria-expanded', String(open));
  });
  $('srv-save').addEventListener('click', async () => {
    $('srv-err').textContent = '';
    try { await api('server', {mode: document.querySelector('input[name=srv]:checked').value, addr: $('srv-addr').value}); srvEdited = false; snack(t('saved')); $('srv-panel').hidden = true; $('srv-row').setAttribute('aria-expanded', 'false'); }
    catch (e) { $('srv-err').textContent = errText(e); }
  });
  $('set-lang').addEventListener('click', ev => { const b = ev.target.closest('button'); if (b) api('lang', {lang: b.dataset.lang}).catch(e => snack(errText(e))); });
  $('set-theme').addEventListener('click', ev => { const b = ev.target.closest('button'); if (b) api('theme', {theme: b.dataset.theme}).catch(e => snack(errText(e))); });
  $('autostart').addEventListener('change', ev => api('autostart', {on: ev.target.checked}).catch(e => snack(errText(e))));

  // ---- show password ----
  // Each password field has an eye button to show what was typed.
  function addEye(input) {
    const wrap = document.createElement('span'); wrap.className = 'pwwrap';
    input.parentNode.insertBefore(wrap, input); wrap.append(input);
    const b = document.createElement('button'); b.type = 'button'; b.className = 'icon-btn pweye';
    const set = shown => {
      input.type = shown ? 'text' : 'password';
      b.textContent = ''; b.append(icon(shown ? 'i-eye-off' : 'i-eye'));
      b.setAttribute('aria-pressed', String(shown));
      b.setAttribute('aria-label', t(shown ? 'pw.hide' : 'pw.show')); b.title = t(shown ? 'pw.hide' : 'pw.show');
    };
    b.addEventListener('click', () => { set(input.type === 'password'); input.focus(); });
    wrap.append(b); set(false);
    return set;
  }
  const eyes = [...document.querySelectorAll('input[type=password]')].map(addEye);
  // A dialog that closes leaves no password shown.
  for (const d of document.querySelectorAll('dialog')) d.addEventListener('close', () => eyes.forEach(set => set(false)));

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
