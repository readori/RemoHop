// RemoHop signal panel: a read-only dashboard of the rendezvous server.
// Plain JavaScript, no libraries; charts are inline SVG drawn here. The
// page polls the JSON API under /api/ every 12 seconds while it is visible.
// Every string is in T.en and T.zh. Nothing from the server is ever placed
// as HTML: text goes in with textContent.
'use strict';
(function () {
  const T = {
    en: {
      brandSub: 'Signal panel', server: 'Rendezvous server', secMonitor: 'Monitor', secService: 'Service', secOps: 'Operations',
      pOverview: 'Overview', pTrends: 'Trends', pRelay: 'Relay', pLimits: 'Limits', pHealth: 'Health',
      auto: 'Auto', light: 'Light', dark: 'Dark', refresh: 'Refresh', r24h: '24 hours', r7d: '7 days',
      leadOverview: 'The server at a glance: who is online, connections, the relay and its budget.',
      leadTrends: 'Sampled every minute: 24 hours at one-minute resolution, 7 days at 15 minutes.',
      leadRelay: 'Sessions the server relays when two peers cannot connect directly. It only ever passes ciphertext.',
      leadLimits: 'The abuse controls in force, and how close the server is to each of them right now.',
      leadHealth: 'Recent warnings and errors from the server\'s log, without addresses or IDs.',
      liveAgo: 'Live · updated {s}', liveNow: 'Live · updated just now', liveErr: 'Cannot reach the panel API', livePaused: 'Paused while hidden',
      errApi: 'The panel could not load its data ({e}). It tries again every few seconds.',
      version: 'Version', started: 'Started', uptime: 'Uptime', idsStored: 'IDs stored',
      heroUp: 'Up and serving for {d}', heroOk: 'No warnings in the last hour', heroWarn: '{n} warnings in the last hour', heroErr: '{n} errors in the last hour', heroWarn1: '1 warning in the last hour', heroErr1: '1 error in the last hour',
      viaLoopback: 'Local access over loopback (an SSH tunnel, say)', viaPassword: 'Signed in with the panel password',
      viaCloudflare: 'Signed in as {e} through Cloudflare Access', meLocal: 'Local access', meLoopback: 'Loopback, no login',
      mePassword: 'Panel password', meCloudflare: 'Cloudflare Access',
      featRelay: 'Relay', featAccounts: 'Accounts', featWS: 'Websocket bridge', on: 'on', off: 'off',
      kHosts: 'Hosts online', kHostsSub: 'of {n} IDs stored', kConns: 'Connections', kConnsSub: '{p} of the limit', kConnsWS: '{n} websocket bridges',
      kRelay: 'Relayed sessions now', kRelaySub: '{n} started today', kRelayOff: 'The relay is off', kLookups: 'Lookups today',
      kLookupsSub: '{i} introductions · {r} registrations',
      actTitle: 'Hosts online and connections', actSub: 'Last 24 hours, one point a minute', actSubPeak: 'Last 24 hours, the peak of every {d}',
      refTitle: 'Refusals today', refSub: 'What the limits turned away, by kind', refNone: 'Nothing refused today',
      gRate: 'Rate limited', gCaps: 'Connection caps', gRelay: 'Relay refused', gOther: 'Other limits', total: 'Total',
      relMonth: 'Relayed this month', relOf: 'of {b} budget', relNoBudget: 'No monthly budget (--relay-monthly-gb)',
      relOffT: 'The relay is off', relOffS: 'Start the server with --relay to relay sessions that cannot connect directly.',
      st_none: 'No budget', st_normal: 'Within budget', st_half_rate: 'Anonymous at half rate', st_anonymous_refused: 'Anonymous refused', st_all_refused: 'All new sessions refused',
      srvTitle: 'Server process', rss: 'Memory (RSS)', rssApprox: 'Memory (Go runtime)', heap: 'Heap in use', goroutines: 'Goroutines', memLimit: 'Memory limit',
      listenTitle: 'Listening', lUDP: 'Rendezvous (UDP)', lAlt: 'NAT detection (UDP)', lWS: 'Websocket bridge (TCP)', lPanel: 'This panel (TCP)',
      lRelay: 'Relay ports (UDP)', relayRandom: 'random high ports', scope_all: 'all interfaces', scope_loopback: 'loopback', scope_address: 'one address',
      accounts: 'Accounts', accountsSub: '{n} accounts · {d} member devices', accountsOff: 'Accounts are off',
      privacy: 'Counts and aggregates only. This panel never lists IP addresses, host IDs, account emails or keys, just as the server\'s own summaries hold counts, not addresses.',
      noData: 'Not recorded yet. The panel takes a sample every minute.', gapNote: 'Gaps: not recorded (the server was not running).',
      tHosts: 'Hosts online', tHostsSub: 'Registered hosts connected to the server', tConns: 'Connections', tConnsSub: 'Open QUIC connections, and websocket bridges',
      sConns: 'QUIC connections', sWS: 'Websocket bridges', tThroughput: 'Relay throughput', tThroughputSub: 'Average over each interval',
      tSessions: 'Relayed sessions', tSessionsSub: 'Sessions started', sStarted: 'Started',
      tRegs: 'Registrations', tRegsSub: 'Hosts registering, and new IDs given out', sRegs: 'Registrations', sNewIDs: 'New IDs',
      tLookups: 'Lookups and introductions', tLookupsSub: 'Connect requests, and introductions made', sLookups: 'Lookups', sIntros: 'Introductions',
      trRefusals: 'Refusals by kind', trRefSub: 'Stacked; totals for the range below', perInterval: 'per {d}', peak: 'Peak {v}',
      r_rate_register: 'Registrations', r_rate_new_id: 'New IDs', r_rate_connect: 'Connect requests', r_rate_miss: 'IDs not online (global)', r_rate_offer: 'Offers to one host',
      r_busy: 'Server full (max connections)', r_per_source: 'Per address', r_per_net: 'Per network (/48)', r_ids_full: 'ID table full',
      r_relay_per_source: 'Per-address cap', r_relay_mode: 'Accounts only', r_relay_deny: 'Deny list', r_relay_budget: 'Monthly budget', r_relay_full: 'All slots in use',
      r_ext_limited: 'Extension requests', r_ws_refused: 'Websocket bridges',
      byDay: 'Relayed per day', byDaySub: 'This month (UTC). Dashed: not recorded.', relRefused: 'Refused relay requests', relRefusedSub: 'Today and since the server started',
      today: 'today', sinceStart: 'since start', policy: 'Policy', policySub: 'What the relay allows, as the server applies it',
      relActive: 'Active sessions', relActiveSub: 'of {n} at most', relToday: 'Sessions today', relTodaySub: '{n} since the server started · {b} today',
      pMode: 'Relays for', modeAll: 'everyone', modeAccounts: 'account devices only', pRate: 'Per session', pAccRate: 'Account devices', pTotal: 'All sessions together',
      pMax: 'Concurrent sessions', pPerSource: 'Per client address', pHostCap: 'Per host address', pIdle: 'Idle timeout', pPorts: 'Port range',
      pDeny: 'Deny list', denyEntries: '{n} entries', denyNone: 'none', pBudget: 'Monthly budget', unlimited: 'no limit',
      closeness: 'How close the server is to each limit', closenessSub: 'Right now. For per-address limits, the busiest address (which one is not shown).',
      g_conns: 'Open connections', g_conns_source: 'Connections of the busiest address', g_conns_net: 'Connections of the busiest network (/48)',
      g_ids: 'IDs stored', g_ext_active: 'Extension requests in progress', g_relays: 'Relayed sessions', g_relay_source: 'Relayed sessions of the busiest client address',
      g_relay_host: 'Relayed sessions of the busiest host address', g_ws: 'Websocket bridges', g_ws_source: 'Websocket bridges of the busiest address',
      lvOk: 'Normal', lvBusy: 'Busy', lvNear: 'Near the limit',
      rates: 'Rate limits', ratesSub: 'Token buckets: a burst, then one more every interval', colLimit: 'Limit', colBurst: 'Burst', colRefill: 'Refill', colRefused: 'Refused, 24 h',
      every: '1 every {d}', counted: 'counted above',
      rl_register: 'Registrations per address', rl_new_id: 'New IDs per address', rl_new_id_net: 'New IDs per network', rl_new_id_all: 'New IDs, whole server',
      rl_connect: 'Connect requests per address', rl_connect_net: 'Connect requests per network', rl_miss: 'Requests for IDs not online, whole server',
      rl_offer: 'Offers delivered to one host', rl_ext: 'Extension requests per address',
      limConns: 'Connections and IDs', limExt: 'Extension requests', limWS: 'Websocket bridge',
      kMaxConns: 'Connections', kPerSource: 'Per address', kPerNet: 'Per network (/48)', kMaxIDs: 'IDs', kExpiry: 'Unused ID kept', kLifetime: 'Unregistered connection',
      kBurst: 'Burst per address', kRefill: 'Refill per second', kConc: 'At once, server', kConcSrc: 'At once, per address',
      kWSMax: 'Bridges', kWSPer: 'Per address', kWSRate: 'Per bridge', kWSTotal: 'All bridges', kWSIdle: 'Idle timeout', kWSRealIP: 'Client address from proxy header',
      yes: 'yes', no: 'no', wsOff: 'The websocket bridge is off (--ws-listen).', days: '{n} days',
      hErr: 'Errors, last hour', hWarn: 'Warnings, last hour', hErrSub: 'of the {n} records kept', hMemSub: 'heap {h}', hGoSub: 'running now',
      logTitle: 'Recent log records', logSub: 'The last {n} of each kind, newest first', logWarn: 'Warnings and errors', logInfo: 'Info',
      logNoneT: 'Nothing to report', logNoneS: 'Warnings and errors the server logs will appear here.', logNoneInfoS: 'Hosts coming and going, relay sessions and summaries appear here.',
      logOffT: 'Log records are not collected', logOffS: 'Start the server with --panel to keep recent records.',
      lv_error: 'Error', lv_warn: 'Warning', lv_info: 'Info',
      logPrivacy: 'Addresses, host IDs, keys and email addresses are removed from these records before they reach the panel. The full log stays in the system journal.',
      ago: '{n} ago', relayed: 'Relayed', notRecorded: 'Not recorded', relBytesToday: 'Relayed today', hLastHour: 'In the last 60 minutes',
    },
    zh: {
      brandSub: '汇合服务器面板', server: '汇合服务器', secMonitor: '监控', secService: '服务', secOps: '运维',
      pOverview: '概览', pTrends: '趋势', pRelay: '中继', pLimits: '限额', pHealth: '健康',
      auto: '自动', light: '浅色', dark: '深色', refresh: '刷新', r24h: '24 小时', r7d: '7 天',
      leadOverview: '服务器一览：在线主机、连接、中继及其流量预算。',
      leadTrends: '每分钟采样一次：最近 24 小时按分钟，最近 7 天按 15 分钟。',
      leadRelay: '两端无法直连时由服务器中继的会话。服务器只转发密文。',
      leadLimits: '当前生效的防滥用限额，以及服务器此刻离各项限额有多近。',
      leadHealth: '服务器日志中最近的警告和错误，已去除地址和 ID。',
      liveAgo: '实时 · {s}更新', liveNow: '实时 · 刚刚更新', liveErr: '无法访问面板接口', livePaused: '页面隐藏时暂停',
      errApi: '面板无法加载数据（{e}），几秒后会自动重试。',
      version: '版本', started: '启动于', uptime: '运行时长', idsStored: '已存 ID',
      heroUp: '已持续运行 {d}', heroOk: '最近一小时没有警告', heroWarn: '最近一小时 {n} 条警告', heroErr: '最近一小时 {n} 条错误', heroWarn1: '最近一小时 1 条警告', heroErr1: '最近一小时 1 条错误',
      viaLoopback: '经本机回环访问（例如 SSH 隧道）', viaPassword: '已用面板密码登录', viaCloudflare: '已通过 Cloudflare Access 以 {e} 登录',
      meLocal: '本机访问', meLoopback: '回环地址，无需登录', mePassword: '面板密码', meCloudflare: 'Cloudflare Access',
      featRelay: '中继', featAccounts: '账号', featWS: 'WebSocket 桥', on: '开', off: '关',
      kHosts: '在线主机', kHostsSub: '已存 {n} 个 ID', kConns: '连接', kConnsSub: '占上限 {p}', kConnsWS: '{n} 个 WebSocket 桥',
      kRelay: '当前中继会话', kRelaySub: '今天已开始 {n} 个', kRelayOff: '中继未开启', kLookups: '今日查询',
      kLookupsSub: '介绍 {i} 次 · 注册 {r} 次',
      actTitle: '在线主机与连接', actSub: '最近 24 小时，每分钟一个点', actSubPeak: '最近 24 小时，每 {d}取峰值',
      refTitle: '今日拒绝', refSub: '各项限额拒绝的请求，按类别', refNone: '今天没有拒绝',
      gRate: '频率限制', gCaps: '连接上限', gRelay: '中继拒绝', gOther: '其他限额', total: '合计',
      relMonth: '本月中继流量', relOf: '预算 {b}', relNoBudget: '未设置月度预算（--relay-monthly-gb）',
      relOffT: '中继未开启', relOffS: '用 --relay 启动服务器，即可为无法直连的会话提供中继。',
      st_none: '无预算', st_normal: '预算内', st_half_rate: '匿名会话半速', st_anonymous_refused: '拒绝匿名会话', st_all_refused: '拒绝所有新会话',
      srvTitle: '服务器进程', rss: '内存（RSS）', rssApprox: '内存（Go 运行时）', heap: '堆内存', goroutines: 'Goroutine', memLimit: '内存上限',
      listenTitle: '监听', lUDP: '汇合（UDP）', lAlt: 'NAT 检测（UDP）', lWS: 'WebSocket 桥（TCP）', lPanel: '本面板（TCP）',
      lRelay: '中继端口（UDP）', relayRandom: '随机高位端口', scope_all: '所有网卡', scope_loopback: '回环', scope_address: '指定地址',
      accounts: '账号', accountsSub: '{n} 个账号 · {d} 台成员设备', accountsOff: '账号功能未开启',
      privacy: '只显示计数和汇总。本面板从不列出 IP 地址、主机 ID、账号邮箱或密钥，与服务器自身的汇总一样只记数量、不记地址。',
      noData: '尚无记录。面板每分钟采样一次。', gapNote: '空缺：未记录（服务器当时未运行）。',
      tHosts: '在线主机', tHostsSub: '连接在服务器上的已注册主机', tConns: '连接', tConnsSub: '打开的 QUIC 连接及 WebSocket 桥',
      sConns: 'QUIC 连接', sWS: 'WebSocket 桥', tThroughput: '中继吞吐量', tThroughputSub: '每个区间的平均值',
      tSessions: '中继会话', tSessionsSub: '开始的会话数', sStarted: '开始',
      tRegs: '注册', tRegsSub: '主机注册次数，以及新分配的 ID', sRegs: '注册', sNewIDs: '新 ID',
      tLookups: '查询与介绍', tLookupsSub: '连接请求，及完成的介绍', sLookups: '查询', sIntros: '介绍',
      trRefusals: '按类别的拒绝', trRefSub: '堆叠显示；下方为该时间范围的合计', perInterval: '每 {d}', peak: '峰值 {v}',
      r_rate_register: '注册', r_rate_new_id: '新 ID', r_rate_connect: '连接请求', r_rate_miss: '不在线的 ID（全局）', r_rate_offer: '发往同一主机',
      r_busy: '服务器已满（最大连接）', r_per_source: '每地址', r_per_net: '每网段（/48）', r_ids_full: 'ID 表已满',
      r_relay_per_source: '每地址上限', r_relay_mode: '仅限账号', r_relay_deny: '拒绝名单', r_relay_budget: '月度预算', r_relay_full: '中继位已满',
      r_ext_limited: '扩展请求', r_ws_refused: 'WebSocket 桥',
      byDay: '每日中继流量', byDaySub: '本月（UTC）。虚线：未记录。', relRefused: '被拒绝的中继请求', relRefusedSub: '今天及服务器启动以来',
      today: '今天', sinceStart: '启动以来', policy: '策略', policySub: '服务器实际执行的中继规则',
      relActive: '活动会话', relActiveSub: '最多 {n} 个', relToday: '今日会话', relTodaySub: '启动以来 {n} 个 · 今天 {b}',
      pMode: '中继对象', modeAll: '所有人', modeAccounts: '仅账号设备', pRate: '每会话', pAccRate: '账号设备', pTotal: '全部会话合计',
      pMax: '并发会话', pPerSource: '每客户端地址', pHostCap: '每主机地址', pIdle: '空闲超时', pPorts: '端口范围',
      pDeny: '拒绝名单', denyEntries: '{n} 条', denyNone: '无', pBudget: '月度预算', unlimited: '不限',
      closeness: '服务器离各项限额有多近', closenessSub: '此刻的数值。按地址计的限额取最忙的地址（不显示是哪个）。',
      g_conns: '打开的连接', g_conns_source: '最忙地址的连接', g_conns_net: '最忙网段（/48）的连接',
      g_ids: '已存 ID', g_ext_active: '处理中的扩展请求', g_relays: '中继会话', g_relay_source: '最忙客户端地址的中继会话',
      g_relay_host: '最忙主机地址的中继会话', g_ws: 'WebSocket 桥', g_ws_source: '最忙地址的 WebSocket 桥',
      lvOk: '正常', lvBusy: '较忙', lvNear: '接近上限',
      rates: '频率限制', ratesSub: '令牌桶：先允许一批，之后每个间隔补充一个', colLimit: '限额', colBurst: '突发', colRefill: '补充', colRefused: '24 小时拒绝',
      every: '每 {d} 一个', counted: '计入上一行',
      rl_register: '每地址注册', rl_new_id: '每地址新 ID', rl_new_id_net: '每网段新 ID', rl_new_id_all: '全服务器新 ID',
      rl_connect: '每地址连接请求', rl_connect_net: '每网段连接请求', rl_miss: '查询不在线的 ID（全服务器）',
      rl_offer: '发往同一主机的邀请', rl_ext: '每地址扩展请求',
      limConns: '连接与 ID', limExt: '扩展请求', limWS: 'WebSocket 桥',
      kMaxConns: '连接', kPerSource: '每地址', kPerNet: '每网段（/48）', kMaxIDs: 'ID', kExpiry: '未使用 ID 保留', kLifetime: '未注册连接',
      kBurst: '每地址突发', kRefill: '每秒补充', kConc: '同时（全服务器）', kConcSrc: '同时（每地址）',
      kWSMax: '桥数量', kWSPer: '每地址', kWSRate: '每个桥', kWSTotal: '全部桥', kWSIdle: '空闲超时', kWSRealIP: '从代理头获取客户端地址',
      yes: '是', no: '否', wsOff: 'WebSocket 桥未开启（--ws-listen）。', days: '{n} 天',
      hErr: '最近一小时错误', hWarn: '最近一小时警告', hErrSub: '共保留 {n} 条', hMemSub: '堆 {h}', hGoSub: '当前运行中',
      logTitle: '最近的日志', logSub: '每类保留最近 {n} 条，最新在前', logWarn: '警告和错误', logInfo: '信息',
      logNoneT: '一切正常', logNoneS: '服务器记录的警告和错误会显示在这里。', logNoneInfoS: '主机上下线、中继会话和汇总会显示在这里。',
      logOffT: '未收集日志', logOffS: '用 --panel 启动服务器即可保留最近的日志。',
      lv_error: '错误', lv_warn: '警告', lv_info: '信息',
      logPrivacy: '这些记录在送到面板之前已去除地址、主机 ID、密钥和邮箱。完整日志仍在系统日志中。',
      ago: '{n}前', relayed: '中继流量', notRecorded: '未记录', relBytesToday: '今日中继流量', hLastHour: '最近 60 分钟',
    },
  };

  // ---- preferences, language, theme ----
  const pref = (k, d) => { try { return localStorage.getItem(k) || d; } catch (e) { return d; } };
  const setPref = (k, v) => { try { localStorage.setItem(k, v); } catch (e) { /* private mode */ } };
  let langPref = pref('remohop-panel-lang', 'auto');
  let themePref = pref('remohop-panel-theme', 'auto');
  let lang = 'en';
  function resolveLang() {
    if (langPref === 'zh' || langPref === 'en') return langPref;
    const l = (navigator.languages && navigator.languages[0]) || navigator.language || 'en';
    return /^zh\b/i.test(l) ? 'zh' : 'en';
  }
  function t(k, vars) {
    let s = (T[lang] && T[lang][k]) || T.en[k] || k;
    if (vars) for (const v in vars) s = s.split('{' + v + '}').join(String(vars[v]));
    return s;
  }
  function applyTheme() {
    if (themePref === 'auto') document.documentElement.removeAttribute('data-theme');
    else document.documentElement.setAttribute('data-theme', themePref);
  }
  function applyLang() {
    lang = resolveLang();
    document.documentElement.lang = lang === 'zh' ? 'zh-CN' : 'en';
    document.querySelectorAll('[data-t]').forEach(e => { e.textContent = t(e.dataset.t); });
    document.querySelectorAll('[data-ta]').forEach(e => { e.setAttribute('aria-label', t(e.dataset.ta)); e.title = t(e.dataset.ta); });
    document.title = (lang === 'zh' ? 'RemoHop 汇合服务器面板' : 'RemoHop signal panel');
  }
  function syncSegs() {
    document.querySelectorAll('.seg[data-set]').forEach(seg => {
      const cur = seg.dataset.set === 'theme' ? themePref : langPref;
      seg.querySelectorAll('button').forEach(b => b.setAttribute('aria-pressed', String(b.dataset.v === cur)));
    });
    $('range').querySelectorAll('button').forEach(b => b.setAttribute('aria-pressed', String(b.dataset.v === state.range)));
    $('log-filter').querySelectorAll('button').forEach(b => b.setAttribute('aria-pressed', String(b.dataset.v === state.logLevel)));
  }

  // ---- DOM helpers ----
  const $ = id => document.getElementById(id);
  const SVG = 'http://www.w3.org/2000/svg';
  function h(tag, cls, text) {
    const e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text !== undefined && text !== null) e.textContent = String(text);
    return e;
  }
  function s(tag, attrs, parent) {
    const e = document.createElementNS(SVG, tag);
    for (const k in attrs || {}) e.setAttribute(k, attrs[k]);
    if (parent) parent.appendChild(e);
    return e;
  }
  function icon(name, cls) {
    const e = s('svg', { class: 'i' + (cls ? ' ' + cls : '') });
    s('use', { href: '#i-' + name }, e);
    return e;
  }
  function clear(e) { while (e.firstChild) e.removeChild(e.firstChild); return e; }
  function append(parent, ...kids) { kids.forEach(k => { if (k) parent.appendChild(k); }); return parent; }

  // ---- formatting ----
  const nf = () => new Intl.NumberFormat(lang === 'zh' ? 'zh-CN' : 'en-US');
  function fmtNum(n) { return nf().format(Math.round(n || 0)); }
  function fmt1(n) { return new Intl.NumberFormat(lang === 'zh' ? 'zh-CN' : 'en-US', { maximumFractionDigits: n < 10 ? 1 : 0 }).format(n); }
  function fmtBytes(b) {
    b = b || 0;
    const u = ['B', 'kB', 'MB', 'GB', 'TB', 'PB'];
    let i = 0;
    while (b >= 1000 && i < u.length - 1) { b /= 1000; i++; }
    return (i === 0 ? fmtNum(b) : fmt1(b)) + ' ' + u[i];
  }
  function fmtBits(bps) {
    const u = ['bit/s', 'kbit/s', 'Mbit/s', 'Gbit/s'];
    let i = 0;
    bps = bps || 0;
    while (bps >= 1000 && i < u.length - 1) { bps /= 1000; i++; }
    return fmt1(bps) + ' ' + u[i];
  }
  function fmtDur(sec) {
    sec = Math.max(0, Math.floor(sec || 0));
    const d = Math.floor(sec / 86400), hh = Math.floor(sec % 86400 / 3600), m = Math.floor(sec % 3600 / 60);
    const zh = lang === 'zh';
    if (d > 0) return zh ? `${d} 天 ${hh} 小时` : `${d} d ${hh} h`;
    if (hh > 0) return zh ? `${hh} 小时 ${m} 分` : `${hh} h ${m} min`;
    if (m > 0) return zh ? `${m} 分钟` : `${m} min`;
    return zh ? `${sec} 秒` : `${sec} s`;
  }
  function fmtEvery(ms) {
    if (ms < 1000) return (lang === 'zh' ? `${ms} 毫秒` : `${ms} ms`);
    return fmtDur(ms / 1000).replace(/ 0 (min|分)$/, '');
  }
  const loc = () => (lang === 'zh' ? 'zh-CN' : 'en-GB');
  function fmtClock(sec) { return new Date(sec * 1000).toLocaleTimeString(loc(), { hour: '2-digit', minute: '2-digit' }); }
  function fmtDay(sec) { return new Date(sec * 1000).toLocaleDateString(loc(), { weekday: 'short', day: 'numeric', month: 'short' }); }
  function fmtStamp(sec, step) { return step >= 900 ? fmtDay(sec) + ' ' + fmtClock(sec) : fmtClock(sec); }
  function fmtPct(v) { return fmt1(v) + '%'; }

  // ---- state and data ----
  const state = { page: 'overview', range: pref('remohop-panel-range', '24h'), logLevel: 'warn', ov: null, health: null, hist: {},
    relay: null, limits: null, updated: 0, error: '' };
  async function getJSON(path) {
    const r = await fetch(path, { credentials: 'same-origin', cache: 'no-store', headers: { Accept: 'application/json' } });
    if (!r.ok) throw new Error(r.status + ' ' + r.statusText);
    return r.json();
  }
  let loading = false;
  async function load() {
    if (loading) return;
    loading = true;
    const page = state.page;
    try {
      const want = [getJSON('/api/overview'), getJSON('/api/health')];
      if (page === 'overview') want.push(getJSON('/api/history?range=24h'));
      if (page === 'trends') want.push(getJSON('/api/history?range=' + state.range));
      if (page === 'relay') want.push(getJSON('/api/relay'));
      if (page === 'limits') want.push(getJSON('/api/limits'));
      const res = await Promise.all(want);
      state.ov = res[0];
      state.health = res[1];
      if (page === 'overview') state.hist['24h'] = res[2];
      if (page === 'trends') state.hist[state.range] = res[2];
      if (page === 'relay') state.relay = res[2];
      if (page === 'limits') state.limits = res[2];
      state.updated = Date.now();
      state.error = '';
    } catch (e) {
      state.error = String(e && e.message || e);
    } finally {
      loading = false;
    }
    render();
  }

  // ---- charts ----
  const tip = () => $('tip');
  function showTip(ev, title, rows) {
    const el = clear(tip());
    el.appendChild(h('b', '', title));
    rows.forEach(r => {
      const row = h('div', 'r');
      const name = h('span');
      if (r.cls) name.appendChild(h('i', 'sw ' + r.cls));
      name.appendChild(document.createTextNode(r.label));
      append(row, name, h('span', 'num', r.value));
      el.appendChild(row);
    });
    el.hidden = false;
    const w = el.offsetWidth, hgt = el.offsetHeight;
    let x = ev.clientX + 14, y = ev.clientY - hgt - 10;
    if (x + w > window.innerWidth - 8) x = ev.clientX - w - 14;
    if (x < 8) x = 8;
    if (y < 8) y = ev.clientY + 16;
    el.style.left = x + 'px';
    el.style.top = y + 'px';
  }
  function hideTip() { tip().hidden = true; }

  // niceMax is the top of an axis with four steps of 1, 2, 2.5 or 5 times
  // a power of ten; counts get whole steps.
  function niceMax(v, whole) {
    if (!(v > 0)) return whole ? 4 : 1;
    const raw = v / 4, p = Math.pow(10, Math.floor(Math.log10(raw)));
    let step = 10 * p;
    for (const m of [1, 2, 2.5, 5, 10]) if (m * p >= raw) { step = m * p; break; }
    if (whole) step = Math.max(1, Math.ceil(step));
    return step * 4;
  }
  function legend(series) {
    if (series.length < 2) return null;
    const l = h('div', 'legend');
    series.forEach(se => { const it = h('span'); append(it, h('i', 'sw ' + se.cls), document.createTextNode(se.label)); l.appendChild(it); });
    return l;
  }

  // bucketize merges samples into intervals of `size` seconds, as the
  // server merges minutes into quarter hours: counters add up, gauges keep
  // their peak. A chart never draws more marks than its width can show.
  function bucketize(points, size) {
    const out = [];
    points.forEach(p => {
      const t = Math.floor(p.t / size) * size;
      let b = out[out.length - 1];
      if (!b || b.t !== t) { b = { t, hosts: 0, conns: 0, ws: 0, relays: 0, c: {}, n: 0 }; out.push(b); }
      b.n++;
      b.hosts = Math.max(b.hosts, p.hosts || 0); b.conns = Math.max(b.conns, p.conns || 0);
      b.ws = Math.max(b.ws, p.ws || 0); b.relays = Math.max(b.relays, p.relays || 0);
      for (const k in (p.c || {})) b.c[k] = (b.c[k] || 0) + p.c[k];
    });
    return out;
  }
  function bucketFor(step, range, width, bars) {
    const minPx = bars ? 4 : 2;
    for (const size of [60, 300, 600, 900, 1800, 3600, 7200, 10800, 21600, 43200, 86400]) {
      if (size >= step && size % step === 0 && width / (range / size) >= minPx) return size;
    }
    return 86400;
  }

  // timeChart draws series of samples over [now-range, now]. A slot with
  // no sample is a gap (not recorded), never a zero.
  function timeChart(host, o) {
    clear(host);
    const W = Math.max(host.clientWidth, 200), H = Math.max(host.clientHeight, 120);
    const bars = o.type === 'bars' || o.type === 'stack';
    const step = bucketFor(o.step, o.range, W - 60, bars), t1 = o.now, t0 = o.now - o.range;
    const raw = (o.points || []).filter(p => p.t + o.step > t0);
    const pts = step === o.step ? raw.map(p => Object.assign({}, p)) : bucketize(raw, step);
    // recorded: the seconds a point's samples cover (a bucket at the edge
    // of a gap covers fewer than its size), for averages over time.
    pts.forEach(p => { p.recorded = (p.n || 1) * o.step; });
    const svg = s('svg', { viewBox: `0 0 ${W} ${H}`, role: 'img' }, host);
    const fmt = o.fmt || fmtNum;
    let ymax = 0;
    pts.forEach(p => {
      if (o.type === 'stack') ymax = Math.max(ymax, o.series.reduce((a, se) => a + (se.get(p) || 0), 0));
      else o.series.forEach(se => { ymax = Math.max(ymax, se.get(p) || 0); });
    });
    const top = niceMax(ymax, o.whole !== false);
    const labW = Math.max(28, fmt(top).length * 6.4 + 8);
    const L = labW, R = 6, TP = 8, B = 22, PW = W - L - R, PH = H - TP - B;
    const x = tt => L + (tt - t0) / (t1 - t0) * PW;
    const y = v => TP + PH - (v / top) * PH;
    for (let i = 0; i <= 4; i++) {
      const v = top * i / 4, yy = Math.round(y(v)) + .5;
      s('line', { class: i === 0 ? 'base' : 'gl', x1: L, x2: W - R, y1: yy, y2: yy }, svg);
      if (!pts.length) continue; // no scale to speak of
      const tx = s('text', { x: L - 8, y: yy + 4, 'text-anchor': 'end' }, svg);
      tx.textContent = fmt(v);
    }
    // x ticks: whole hours (24 h) or midnights (7 d), as many as fit.
    const want = Math.max(2, Math.floor(PW / 110));
    const unit = o.range > 2 * 86400 ? 86400 : 3600;
    const tz = new Date().getTimezoneOffset() * 60;
    let every = unit;
    while ((t1 - t0) / every > want) every += unit;
    if (unit === 3600) { for (const e of [3600, 7200, 10800, 14400, 21600, 43200]) if ((t1 - t0) / e <= want) { every = e; break; } }
    const first = Math.ceil((t0 - tz) / every) * every + tz;
    for (let tt = first; tt <= t1; tt += every) {
      const xx = x(tt);
      if (xx < L + 16 || xx > W - R - 16) continue;
      const tx = s('text', { x: xx, y: H - 6, 'text-anchor': 'middle' }, svg);
      tx.textContent = unit === 86400 ? new Date(tt * 1000).toLocaleDateString(loc(), { weekday: 'short', day: 'numeric' }) : fmtClock(tt);
    }
    if (!pts.length) {
      const e = h('div', 'chart-empty', t('noData'));
      host.appendChild(e);
      return { step, peak: 0 };
    }
    const slot = PW * step / (t1 - t0);
    if (o.type === 'bars' || o.type === 'stack') {
      const bw = Math.max(1, slot - (slot > 4 ? 2 : 0));
      pts.forEach(p => {
        let base = 0;
        o.series.forEach((se, i) => {
          const v = se.get(p) || 0;
          if (v <= 0) return;
          const x0 = x(p.t) + (slot - bw) / 2;
          const yTop = y(base + v), yBot = y(base);
          const gap = o.type === 'stack' && base > 0 ? 1 : 0;
          s('rect', { class: 'bar ' + se.cls, x: x0.toFixed(1), y: yTop.toFixed(1), width: bw.toFixed(1), height: Math.max(.5, yBot - yTop - gap).toFixed(1),
            rx: Math.min(2, bw / 2) }, svg);
          if (o.type === 'stack') base += v;
          void i;
        });
      });
    } else {
      o.series.forEach(se => {
        // Split into runs of consecutive samples.
        const runs = [];
        let run = [];
        pts.forEach((p, i) => {
          if (i > 0 && p.t - pts[i - 1].t > step * 1.5) { runs.push(run); run = []; }
          run.push(p);
        });
        runs.push(run);
        runs.forEach(r => {
          const xy = r.map(p => [x(p.t + step / 2), y(se.get(p) || 0)]);
          if (xy.length === 1) {
            s('circle', { class: 'dot ' + se.cls, cx: xy[0][0].toFixed(1), cy: xy[0][1].toFixed(1), r: 3 }, svg);
            return;
          }
          const d = xy.map((q, i) => (i ? 'L' : 'M') + q[0].toFixed(1) + ' ' + q[1].toFixed(1)).join('');
          if (o.type === 'area') {
            s('path', { class: 'ar ' + se.cls, d: d + `L${xy[xy.length - 1][0].toFixed(1)} ${y(0)}L${xy[0][0].toFixed(1)} ${y(0)}Z` }, svg);
          }
          s('path', { class: 'ln ' + se.cls, d }, svg);
        });
      });
    }
    // Hover: a crosshair and the values of the nearest sample.
    const cross = s('line', { class: 'xh', y1: TP, y2: TP + PH, x1: 0, x2: 0, visibility: 'hidden' }, svg);
    const dots = bars ? [] : o.series.map(se => s('circle', { class: 'dot ' + se.cls, r: 4, cx: 0, cy: 0, visibility: 'hidden' }, svg));
    const hover = on => { cross.setAttribute('visibility', on ? 'visible' : 'hidden'); dots.forEach(d => d.setAttribute('visibility', on ? 'visible' : 'hidden')); };
    const hit = s('rect', { class: 'hit', x: L, y: TP, width: PW, height: PH }, svg);
    const move = ev => {
      const r = svg.getBoundingClientRect();
      const px = (ev.clientX - r.left) * (W / r.width);
      const tt = t0 + (px - L) / PW * (t1 - t0) - step / 2;
      let best = null, bd = Infinity;
      pts.forEach(p => { const d = Math.abs(p.t - tt); if (d < bd) { bd = d; best = p; } });
      if (!best || bd > step * 2) { hideTip(); hover(false); return; }
      const cx = bars ? x(best.t) + slot / 2 : x(best.t + step / 2);
      hover(true);
      cross.setAttribute('x1', cx); cross.setAttribute('x2', cx);
      dots.forEach((d, i) => { d.setAttribute('cx', cx); d.setAttribute('cy', y(o.series[i].get(best) || 0)); });
      const rows = o.series.map(se => ({ label: se.label, cls: se.cls, value: fmt(se.get(best) || 0) }));
      if (o.type === 'stack') rows.push({ label: t('total'), value: fmt(o.series.reduce((a, se) => a + (se.get(best) || 0), 0)) });
      showTip(ev, fmtStamp(best.t, step) + (step >= 900 ? '' : '–' + fmtClock(best.t + step)), rows);
    };
    hit.addEventListener('pointermove', move);
    hit.addEventListener('pointerdown', move);
    hit.addEventListener('pointerleave', () => { hideTip(); hover(false); });
    let peak = 0;
    pts.forEach(p => o.series.forEach(se => { peak = Math.max(peak, se.get(p) || 0); }));
    svg.setAttribute('aria-label', (o.aria || '') + (pts.length ? ' · ' + t('peak', { v: fmt(peak) }) : ''));
    return { step, peak };
  }

  function spark(host, values) {
    clear(host);
    const W = 120, H = 40;
    const svg = s('svg', { viewBox: `0 0 ${W} ${H}`, preserveAspectRatio: 'none', 'aria-hidden': 'true' }, host);
    if (values.length < 2) return;
    const top = Math.max(1, ...values);
    const xy = values.map((v, i) => [i / (values.length - 1) * W, H - 3 - (v / top) * (H - 6)]);
    const d = xy.map((q, i) => (i ? 'L' : 'M') + q[0].toFixed(1) + ' ' + q[1].toFixed(1)).join('');
    s('path', { class: 'a', d: d + `L${W} ${H}L0 ${H}Z` }, svg);
    s('path', { class: 'l', d, 'vector-effect': 'non-scaling-stroke' }, svg);
  }

  function dmeter(frac, cls) {
    const n = 40, gap = 2;
    const svg = s('svg', { class: 'dmeter', viewBox: `0 0 ${n * 10} 14`, preserveAspectRatio: 'none', 'aria-hidden': 'true' });
    const on = Math.min(n, Math.round(Math.max(0, frac) * n));
    for (let i = 0; i < n; i++) s('rect', { x: i * 10, y: 0, width: 10 - gap, height: 14, rx: 2, class: i < on ? 'on ' + (cls || '') : '' }, svg);
    return svg;
  }
  function barMeter(frac, cls) {
    const m = h('div', 'bar-m');
    const i = h('i', cls || '');
    i.style.width = Math.min(100, Math.max(0, frac * 100)).toFixed(1) + '%';
    if (frac <= 0) i.classList.add('zero');
    m.appendChild(i);
    return m;
  }

  function donut(host, parts, center) {
    clear(host);
    const wrap = h('div', 'donut');
    const svg = s('svg', { viewBox: '0 0 150 150', role: 'img' });
    const R = 58, C = 2 * Math.PI * R;
    s('circle', { class: 'track', cx: 75, cy: 75, r: R }, svg);
    const total = parts.reduce((a, p) => a + p.value, 0);
    let off = 0;
    const gap = parts.filter(p => p.value > 0).length > 1 ? 3 : 0;
    parts.forEach(p => {
      if (!p.value) return;
      const len = p.value / total * C;
      const seg = s('circle', { class: 'seg-a ' + p.cls, cx: 75, cy: 75, r: R, 'stroke-dasharray': `${Math.max(.5, len - gap)} ${C}`,
        'stroke-dashoffset': -off, transform: 'rotate(-90 75 75)' }, svg);
      seg.addEventListener('pointermove', ev => showTip(ev, p.label, [{ label: t('today'), value: fmtNum(p.value) + ' · ' + fmtPct(p.value / total * 100) }]));
      seg.addEventListener('pointerleave', hideTip);
      off += len;
    });
    const big = s('text', { x: 75, y: 78, 'text-anchor': 'middle', class: 'big' }, svg);
    big.textContent = fmtNum(total);
    const sm = s('text', { x: 75, y: 96, 'text-anchor': 'middle' }, svg);
    sm.textContent = center;
    svg.setAttribute('aria-label', center + ': ' + fmtNum(total));
    const ul = h('ul');
    parts.forEach(p => {
      const li = h('li');
      append(li, h('i', 'sw ' + p.cls), h('span', 'grow', p.label), h('b', '', fmtNum(p.value)));
      ul.appendChild(li);
    });
    append(wrap, svg, ul);
    host.appendChild(wrap);
  }

  function emptyState(ic, title, sub, ok) {
    const e = h('div', 'empty');
    const i = h('div', 'empty-ic' + (ok ? ' ok' : ''));
    i.appendChild(icon(ic));
    append(e, i, h('div', 'empty-t', title), h('div', '', sub));
    return e;
  }
  function cardHead(ic, title, sub, extra) {
    const hd = h('div', 'card-h');
    const tl = h('span', 'tile');
    tl.appendChild(icon(ic));
    const g = h('div', 'grow');
    append(g, h('h3', 'ct', title), sub ? h('p', 'cs', sub) : null);
    append(hd, tl, g, extra || null);
    return hd;
  }
  function chip(text, cls, dot) {
    const c = h('span', 'chip' + (cls ? ' ' + cls : ''));
    if (dot) c.appendChild(h('i', 'd'));
    c.appendChild(document.createTextNode(text));
    return c;
  }
  function statRow(k, v) {
    const r = h('div', 'stat-row');
    append(r, h('span', 'k', k), typeof v === 'object' ? v : h('span', 'v', v));
    return r;
  }

  // ---- refusal groups ----
  const GROUPS = [
    { key: 'rate', label: 'gRate', cls: 's1', reasons: ['rate_register', 'rate_new_id', 'rate_connect', 'rate_miss', 'rate_offer'] },
    { key: 'caps', label: 'gCaps', cls: 's2', reasons: ['busy', 'per_source', 'per_net', 'ids_full'] },
    { key: 'relay', label: 'gRelay', cls: 's3', reasons: ['relay_per_source', 'relay_mode', 'relay_deny', 'relay_budget', 'relay_full'] },
    { key: 'other', label: 'gOther', cls: 's4', reasons: ['ext_limited', 'ws_refused'] },
  ];
  const sumOf = (c, keys) => keys.reduce((a, k) => a + ((c && c[k]) || 0), 0);
  const cnt = (p, k) => (p.c && p.c[k]) || 0;

  // ---- shell ----
  const PAGES = {
    overview: { title: 'pOverview', lead: 'leadOverview', sec: 'secMonitor' },
    trends: { title: 'pTrends', lead: 'leadTrends', sec: 'secMonitor' },
    relay: { title: 'pRelay', lead: 'leadRelay', sec: 'secService' },
    limits: { title: 'pLimits', lead: 'leadLimits', sec: 'secService' },
    health: { title: 'pHealth', lead: 'leadHealth', sec: 'secOps' },
  };
  function healthLevel() {
    const hl = state.health;
    if (!hl) return 'none';
    if (hl.errors_1h > 0) return 'bad';
    if (hl.warnings_1h > 0) return 'warn';
    return 'ok';
  }
  function renderShell() {
    const p = PAGES[state.page];
    $('ph-title').textContent = t(p.title);
    $('ph-lead').textContent = t(p.lead);
    const now = new Date();
    $('ph-eyebrow').textContent = t(p.sec) + ' · ' + now.toLocaleDateString(loc(), { weekday: 'short', day: 'numeric', month: 'short' }) + ' · ' +
      now.toLocaleTimeString(loc(), { hour: '2-digit', minute: '2-digit' });
    document.querySelectorAll('.nav .tab').forEach(a => {
      if (a.dataset.page === state.page) a.setAttribute('aria-current', 'page'); else a.removeAttribute('aria-current');
    });
    document.querySelectorAll('.view').forEach(v => { v.hidden = v.dataset.page !== state.page; });
    $('range').hidden = state.page !== 'trends';
    const live = $('live');
    live.className = 'status';
    if (state.error) { live.classList.add('bad'); $('live-t').textContent = t('liveErr'); }
    else if (document.hidden) { $('live-t').textContent = t('livePaused'); }
    else if (state.updated) {
      live.classList.add('ok');
      const ago = Math.round((Date.now() - state.updated) / 1000);
      $('live-t').textContent = ago < 3 ? t('liveNow') : t('liveAgo', { s: t('ago', { n: fmtDur(ago) }) });
    }
    const err = $('err');
    err.hidden = !state.error;
    err.textContent = state.error ? t('errApi', { e: state.error }) : '';
    const ov = state.ov;
    if (!ov) return;
    $('srv-sub').textContent = (ov.version || '–') + ' · ' + fmtDur(ov.uptime_s);
    const lvl = healthLevel();
    $('srv-dot').className = 'pres ' + (lvl === 'none' ? 'ok' : lvl);
    const hb = $('nav-health');
    const n = state.health ? state.health.errors_1h + state.health.warnings_1h : 0;
    hb.hidden = !n;
    hb.className = 'count ' + (state.health && state.health.errors_1h ? 'bad' : 'warn');
    hb.textContent = fmtNum(n);
    const rb = $('nav-relay');
    rb.hidden = !(ov.relay && ov.relay.active);
    rb.className = 'count ok';
    rb.textContent = ov.relay ? fmtNum(ov.relay.active) : '';
    const v = ov.viewer || {};
    const meIc = $('me-ic');
    if (v.via === 'cloudflare') {
      $('me-t').textContent = v.email || t('meCloudflare');
      $('me-s').textContent = t('meCloudflare');
      meIc.setAttribute('href', '#i-cloud');
    } else if (v.via === 'password') {
      $('me-t').textContent = t('meLocal');
      $('me-s').textContent = t('mePassword');
      meIc.setAttribute('href', '#i-lock');
    } else {
      $('me-t').textContent = t('meLocal');
      $('me-s').textContent = t('meLoopback');
      meIc.setAttribute('href', '#i-term');
    }
  }

  // ---- overview ----
  function kpi(ic, icCls, label, value, sub, sparkVals, unit) {
    const c = h('div', 'card kpi');
    const hd = h('div', 'kpi-h');
    const i = h('span', 'kpi-ic ' + icCls);
    i.appendChild(icon(ic));
    append(hd, i, h('span', '', label));
    const row = h('div', 'kpi-row');
    const v = h('div', 'kpi-v', value);
    if (unit) v.appendChild(h('small', '', unit));
    row.appendChild(v);
    if (sparkVals) { const sp = h('div', 'spark'); row.appendChild(sp); spark(sp, sparkVals); }
    append(c, hd, row, h('div', 'kpi-s', sub));
    return c;
  }
  function listenScope(li) { return li ? ':' + li.port + ' · ' + t('scope_' + li.scope) : null; }

  function renderOverview() {
    const ov = state.ov;
    if (!ov) return;
    const today = ov.today || {}, live = ov.live || {}, recent = ov.recent || [];
    // Welcome band.
    const now = new Date(ov.now * 1000);
    $('hero-eyebrow').textContent = t('server') + ' · ' + now.toLocaleDateString(loc(), { weekday: 'long', day: 'numeric', month: 'long' });
    $('hero-t').textContent = t('heroUp', { d: fmtDur(ov.uptime_s) });
    const hs = clear($('hero-s'));
    const hl = state.health || {};
    if (hl.errors_1h) hs.appendChild(chip(t(hl.errors_1h === 1 ? 'heroErr1' : 'heroErr', { n: hl.errors_1h }), 'bad', true));
    else if (hl.warnings_1h) hs.appendChild(chip(t(hl.warnings_1h === 1 ? 'heroWarn1' : 'heroWarn', { n: hl.warnings_1h }), 'warn', true));
    else hs.appendChild(chip(t('heroOk'), 'ok', true));
    const v = ov.viewer || {};
    hs.appendChild(h('span', '', v.via === 'cloudflare' ? t('viaCloudflare', { e: v.email || '?' }) : v.via === 'password' ? t('viaPassword') : t('viaLoopback')));
    const chips = clear($('listen-chips'));
    const f = ov.features || {};
    [['featRelay', f.relay], ['featAccounts', f.accounts], ['featWS', f.ws]].forEach(([k, on]) => {
      chips.appendChild(chip(t(k) + ' · ' + t(on ? 'on' : 'off'), on ? 'acc' : '', on));
    });
    $('f-version').textContent = ov.version || '–';
    const st = new Date(ov.started * 1000);
    $('f-uptime').textContent = st.toDateString() === now.toDateString() ? fmtClock(ov.started) : st.toLocaleDateString(loc(), { day: 'numeric', month: 'short' });
    $('f-ids').textContent = fmtNum(live.ids);

    // KPI cards.
    const k = clear($('kpis'));
    const pctConns = live.max_conns ? live.conns / live.max_conns * 100 : 0;
    let connSub = t('kConnsSub', { p: fmtPct(pctConns) });
    if (f.ws) connSub += ' · ' + t('kConnsWS', { n: fmtNum(live.ws) });
    append(k,
      kpi('monitor', 'c1', t('kHosts'), fmtNum(live.hosts), t('kHostsSub', { n: fmtNum(live.ids) }), recent.map(p => p.hosts)),
      kpi('link', pctConns > 85 ? 'bad' : pctConns > 60 ? 'warn' : 'n', t('kConns'), fmtNum(live.conns), connSub, recent.map(p => p.conns)),
      f.relay ? kpi('relay', 'ok', t('kRelay'), fmtNum(live.relays), t('kRelaySub', { n: fmtNum(today.relay_sessions) }), recent.map(p => cnt(p, 'relay_bytes')))
        : kpi('relay', 'n', t('kRelay'), '–', t('kRelayOff')),
      kpi('search', 'n', t('kLookups'), fmtNum(today.lookups), t('kLookupsSub', { i: fmtNum(today.introductions), r: fmtNum(today.registrations) }),
        recent.map(p => cnt(p, 'lookups'))),
    );

    // Activity chart.
    const hist = state.hist['24h'];
    const host = $('ov-chart');
    const series = [{ label: t('kHosts'), cls: 's1', get: p => p.hosts }, { label: t('sConns'), cls: 's2', get: p => p.conns }];
    const lg = host.previousElementSibling && host.previousElementSibling.classList.contains('legend') ? host.previousElementSibling : null;
    if (lg) lg.remove();
    host.parentNode.insertBefore(legend(series), host);
    const ar = timeChart(host, { points: hist ? hist.points : [], step: 60, now: ov.now, range: 86400, series, type: 'line', aria: t('actTitle') });
    $('act-sub').textContent = ar.step > 60 ? t('actSubPeak', { d: fmtEvery(ar.step * 1000) }) : t('actSub');

    // Refusals today.
    donut($('ov-donut'), GROUPS.map(g => ({ label: t(g.label), cls: g.cls, value: sumOf(today, g.reasons) })), t('today'));

    // Relay this month.
    const rc = clear($('ov-relay'));
    rc.appendChild(cardHead('relay', t('relMonth'), null));
    if (!ov.relay) {
      rc.appendChild(emptyState('relay', t('relOffT'), t('relOffS')));
    } else {
      const r = ov.relay;
      const bv = h('div', 'big-v', fmtBytes(r.month_bytes));
      rc.appendChild(bv);
      if (r.budget_bytes > 0) {
        const frac = r.month_bytes / r.budget_bytes;
        rc.appendChild(dmeter(frac, frac >= 1 ? 'bad' : frac >= .8 ? 'warn' : ''));
        const row = h('div', 'kpi-h');
        append(row, h('span', 'sub grow', t('relOf', { b: fmtBytes(r.budget_bytes) }) + ' · ' + fmtPct(frac * 100)),
          chip(t('st_' + r.budget_state), r.budget_state === 'normal' ? 'ok' : r.budget_state === 'half_rate' ? 'warn' : 'bad', true));
        rc.appendChild(row);
      } else {
        rc.appendChild(h('div', 'sub', t('relNoBudget')));
      }
      const sl = h('div', 'stat-list mt16');
      append(sl, statRow(t('relActive'), fmtNum(r.active) + ' / ' + fmtNum(r.max)), statRow(t('relToday'), fmtNum(today.relay_sessions)),
        statRow(t('relBytesToday'), fmtBytes(today.relay_bytes)));
      rc.appendChild(sl);
    }

    // Server process.
    const mc = clear($('ov-mem'));
    const m = ov.memory || {};
    mc.appendChild(cardHead('chip', t('srvTitle'), null));
    const bv = h('div', 'big-v', fmtBytes(m.rss));
    mc.appendChild(bv);
    mc.appendChild(h('div', 'sub', m.rss_exact ? t('rss') : t('rssApprox')));
    if (m.limit) mc.appendChild(barMeter(m.rss / m.limit, m.rss / m.limit > .85 ? 'bad' : m.rss / m.limit > .6 ? 'warn' : ''));
    const ml = h('div', 'stat-list mt16');
    append(ml, statRow(t('heap'), fmtBytes(m.heap)), statRow(t('goroutines'), fmtNum(m.goroutines)), m.limit ? statRow(t('memLimit'), fmtBytes(m.limit)) : null);
    mc.appendChild(ml);

    // Listening and accounts.
    const ec = clear($('ov-extra'));
    ec.appendChild(cardHead('plug', t('listenTitle'), null));
    const ll = h('div', 'stat-list');
    const L = ov.listen || {};
    [['lUDP', L.udp], ['lAlt', L.alt], ['lWS', L.ws], ['lPanel', L.panel]].forEach(([key, li]) => {
      if (li) ll.appendChild(statRow(t(key), h('span', 'chip', listenScope(li))));
    });
    if (f.relay) ll.appendChild(statRow(t('lRelay'), h('span', 'chip', ov.relay_ports || t('relayRandom'))));
    if (ov.accounts) ll.appendChild(statRow(t('accounts'), t('accountsSub', { n: fmtNum(ov.accounts.accounts), d: fmtNum(ov.accounts.devices) })));
    else ll.appendChild(statRow(t('accounts'), h('span', 'chip', t('off'))));
    ec.appendChild(ll);
  }

  // ---- trends ----
  const TRENDS = [
    { id: 'hosts', title: 'tHosts', sub: 'tHostsSub', type: 'area', series: () => [{ label: t('tHosts'), cls: 's1', get: p => p.hosts }] },
    { id: 'conns', title: 'tConns', sub: 'tConnsSub', type: 'line', series: () => [{ label: t('sConns'), cls: 's1', get: p => p.conns }, { label: t('sWS'), cls: 's2', get: p => p.ws }] },
    { id: 'thr', counter: true, title: 'tThroughput', sub: 'tThroughputSub', type: 'area', fmt: v => fmtBits(v), whole: false,
      series: () => [{ label: t('tThroughput'), cls: 's1', get: p => cnt(p, 'relay_bytes') * 8 / (p.recorded || 60) }] },
    { id: 'sess', counter: true, title: 'tSessions', sub: 'tSessionsSub', type: 'bars', series: () => [{ label: t('sStarted'), cls: 's1', get: p => cnt(p, 'relay_sessions') }] },
    { id: 'regs', counter: true, title: 'tRegs', sub: 'tRegsSub', type: 'line', series: () => [{ label: t('sRegs'), cls: 's1', get: p => cnt(p, 'registrations') }, { label: t('sNewIDs'), cls: 's2', get: p => cnt(p, 'new_ids') }] },
    { id: 'look', counter: true, title: 'tLookups', sub: 'tLookupsSub', type: 'line', series: () => [{ label: t('sLookups'), cls: 's1', get: p => cnt(p, 'lookups') }, { label: t('sIntros'), cls: 's2', get: p => cnt(p, 'introductions') }] },
  ];
  function renderTrends() {
    const hist = state.hist[state.range];
    const grid = $('trend-grid');
    const range = state.range === '7d' ? 7 * 86400 : 86400;
    const step = hist ? hist.step : (state.range === '7d' ? 900 : 60);
    const now = hist ? hist.now : Math.floor(Date.now() / 1000);
    const pts = hist ? hist.points : [];
    if (grid.childElementCount !== TRENDS.length) {
      clear(grid);
      TRENDS.forEach(tr => {
        const c = h('div', 'card');
        c.dataset.trend = tr.id;
        grid.appendChild(c);
      });
    }
    TRENDS.forEach((tr, i) => {
      const c = clear(grid.children[i]);
      const series = tr.series();
      const fmt = tr.fmt || fmtNum;
      const pk = chip('');
      const head = cardHead(tr.id === 'thr' || tr.id === 'sess' ? 'relay' : tr.id === 'look' ? 'search' : tr.id === 'regs' ? 'key' : tr.id === 'conns' ? 'link' : 'monitor',
        t(tr.title), t(tr.sub), pk);
      c.appendChild(head);
      // A single series needs no legend (the title names it); its line
      // keeps the charts of a row level.
      c.appendChild(legend(series) || h('div', 'legend'));
      const ch = h('div', 'chart short');
      c.appendChild(ch);
      const r = timeChart(ch, { points: pts, step, now, range, series, type: tr.type, fmt, whole: tr.whole, aria: t(tr.title) });
      pk.hidden = !r.peak;
      pk.textContent = t('peak', { v: fmt(r.peak) });
      // Counts are per interval: say which.
      if (tr.counter) head.querySelector('.cs').textContent = t(tr.sub) + ' · ' + t('perInterval', { d: fmtEvery(r.step * 1000) });
    });
    // Refusals.
    const series = GROUPS.map(g => ({ label: t(g.label), cls: g.cls, get: p => sumOf(p.c, g.reasons) }));
    const host = $('tr-refusals');
    const prev = host.previousElementSibling;
    if (prev && prev.classList.contains('legend')) prev.remove();
    host.parentNode.insertBefore(legend(series), host);
    $('tr-ref-sub').textContent = t('trRefSub');
    const rr = timeChart(host, { points: pts, step, now, range, series, type: 'stack', aria: t('trRefusals') });
    $('tr-ref-sub').textContent = t('trRefSub') + ' · ' + t('perInterval', { d: fmtEvery(rr.step * 1000) });
    const totals = {};
    pts.forEach(p => { for (const k in (p.c || {})) totals[k] = (totals[k] || 0) + p.c[k]; });
    const rt = clear($('tr-reasons'));
    GROUPS.forEach(g => {
      const box = h('div', 'reason-group');
      const hd = h('h4');
      append(hd, h('i', 'sw ' + g.cls), h('span', '', t(g.label)), h('span', 'n', fmtNum(sumOf(totals, g.reasons))));
      box.appendChild(hd);
      g.reasons.forEach(r => {
        const row = h('div', 'reason');
        const n = totals[r] || 0;
        append(row, h('span', 'grow', t('r_' + r)), h('span', 'count' + (n ? ' warn' : ''), fmtNum(n)));
        box.appendChild(row);
      });
      rt.appendChild(box);
    });
  }

  // ---- relay ----
  function renderRelay() {
    const r = state.relay;
    if (!r) return;
    const off = $('relay-off');
    off.hidden = r.on;
    $('relay-on').hidden = !r.on;
    if (!r.on) {
      clear(off);
      const c = h('div', 'card');
      c.appendChild(emptyState('relay', t('relOffT'), t('relOffS')));
      off.appendChild(c);
      return;
    }
    const u = r.usage || {}, pol = r.policy || {};
    const k = clear($('relay-kpis'));
    // Month.
    const mc = h('div', 'card kpi');
    const mh = h('div', 'kpi-h');
    const mi = h('span', 'kpi-ic c1');
    mi.appendChild(icon('db'));
    append(mh, mi, h('span', '', t('relMonth')));
    mc.appendChild(mh);
    const mv = h('div', 'kpi-v mt16', fmtBytes(u.bytes));
    mc.appendChild(mv);
    if (u.budget > 0) {
      const frac = u.bytes / u.budget;
      mc.appendChild(dmeter(frac, frac >= 1 ? 'bad' : frac >= .8 ? 'warn' : ''));
      const row = h('div', 'kpi-h');
      append(row, h('span', 'sub grow', t('relOf', { b: fmtBytes(u.budget) }) + ' · ' + fmtPct(u.pct || 0)),
        chip(t('st_' + u.state), u.state === 'normal' ? 'ok' : u.state === 'half_rate' ? 'warn' : 'bad', true));
      mc.appendChild(row);
    } else {
      mc.appendChild(h('div', 'kpi-s', t('relNoBudget')));
    }
    // Active.
    const ac = h('div', 'card kpi');
    const ah = h('div', 'kpi-h');
    const ai = h('span', 'kpi-ic ok');
    ai.appendChild(icon('relay'));
    append(ah, ai, h('span', '', t('relActive')));
    ac.appendChild(ah);
    const av = h('div', 'kpi-v mt16', fmtNum(r.active));
    av.appendChild(h('small', '', '/ ' + fmtNum(pol.max_sessions)));
    ac.appendChild(av);
    const af = pol.max_sessions ? r.active / pol.max_sessions : 0;
    ac.appendChild(dmeter(af, af > .85 ? 'bad' : af > .6 ? 'warn' : ''));
    ac.appendChild(h('div', 'kpi-s', t('relActiveSub', { n: fmtNum(pol.max_sessions) })));
    // Today.
    const tc = kpi('clock', 'n', t('relToday'), fmtNum(r.sessions.today), t('relTodaySub', { n: fmtNum(r.sessions.total), b: fmtBytes(r.bytes.today) }));
    append(k, mc, ac, tc);

    // Per day.
    const days = r.days || [];
    const host = $('rel-days');
    $('rel-days-sub').textContent = t('byDaySub');
    let peak = 0;
    days.forEach(d => { peak = Math.max(peak, d.bytes); });
    const pk = $('rel-peak');
    pk.hidden = !peak;
    pk.textContent = t('peak', { v: fmtBytes(peak) });
    dayBars(host, days);

    // Refusals.
    const rr = clear($('rel-reasons'));
    const sl = h('div', 'stat-list');
    ['per_source', 'mode', 'deny', 'budget', 'full'].forEach(key => {
      const td = (r.refused.today || {})[key] || 0, tot = (r.refused.total || {})[key] || 0;
      const v = h('span', 'v');
      append(v, h('span', 'count' + (td ? ' warn' : ''), fmtNum(td) + ' ' + t('today')), document.createTextNode(' '), h('span', 'count', fmtNum(tot) + ' ' + t('sinceStart')));
      sl.appendChild(statRow(t('r_relay_' + key), v));
    });
    rr.appendChild(sl);

    // Policy.
    const pl = clear($('rel-policy'));
    const kv = (key, val, sub) => {
      const d = h('div');
      const dd = h('dd', '', val);
      if (sub) dd.appendChild(h('span', 'sub', sub));
      append(d, h('dt', '', t(key)), dd);
      pl.appendChild(d);
    };
    kv('pMode', t(pol.mode === 'accounts' ? 'modeAccounts' : 'modeAll'));
    kv('pRate', fmtBits(pol.rate_kbps * 1000));
    if (pol.account_kbps !== pol.rate_kbps || pol.mode === 'accounts') kv('pAccRate', fmtBits(pol.account_kbps * 1000));
    kv('pTotal', fmtBits(pol.total_kbps * 1000));
    kv('pMax', fmtNum(pol.max_sessions));
    kv('pPerSource', pol.per_source ? fmtNum(pol.per_source) : t('unlimited'));
    kv('pHostCap', pol.host_cap ? fmtNum(pol.host_cap) : t('unlimited'));
    kv('pIdle', fmtDur(pol.idle_s));
    kv('pPorts', pol.ports || t('relayRandom'));
    kv('pDeny', pol.deny_list ? t('denyEntries', { n: fmtNum(pol.deny_entries || 0) }) : t('denyNone'));
    kv('pBudget', pol.monthly_budget_bytes ? fmtBytes(pol.monthly_budget_bytes) : t('unlimited'));
  }

  function dayBars(host, days) {
    clear(host);
    const W = Math.max(host.clientWidth, 200), H = Math.max(host.clientHeight, 120);
    const svg = s('svg', { viewBox: `0 0 ${W} ${H}`, role: 'img', 'aria-label': t('byDay') }, host);
    let ymax = 0;
    days.forEach(d => { ymax = Math.max(ymax, d.bytes); });
    const top = niceMax(ymax, false);
    const L = Math.max(40, fmtBytes(top).length * 6.4 + 8), R = 6, TP = 8, B = 22, PW = W - L - R, PH = H - TP - B;
    const y = v => TP + PH - v / top * PH;
    for (let i = 0; i <= 4; i++) {
      const v = top * i / 4, yy = Math.round(y(v)) + .5;
      s('line', { class: i === 0 ? 'base' : 'gl', x1: L, x2: W - R, y1: yy, y2: yy }, svg);
      const tx = s('text', { x: L - 8, y: yy + 4, 'text-anchor': 'end' }, svg);
      tx.textContent = fmtBytes(v);
    }
    let n = days.length;
    if (days.length) {
      const d0 = new Date(days[0].day + 'T00:00:00Z');
      n = new Date(Date.UTC(d0.getUTCFullYear(), d0.getUTCMonth() + 1, 0)).getUTCDate();
    }
    const slot = PW / n, bw = Math.max(2, slot - Math.min(6, slot * .3));
    days.forEach((d, i) => {
      const x0 = L + i * slot + (slot - bw) / 2;
      let r;
      if (!d.recorded) {
        r = s('rect', { class: 'bar nd', x: x0.toFixed(1), y: TP + PH - 18, width: bw.toFixed(1), height: 18, rx: 3 }, svg);
      } else {
        const yy = y(d.bytes);
        r = s('rect', { class: 'bar s1', x: x0.toFixed(1), y: yy.toFixed(1), width: bw.toFixed(1), height: Math.max(1, TP + PH - yy).toFixed(1), rx: Math.min(3, bw / 2) }, svg);
      }
      const day = Number(d.day.slice(8));
      if (i === 0 || day % 5 === 0) {
        const tx = s('text', { x: (x0 + bw / 2).toFixed(1), y: H - 6, 'text-anchor': 'middle' }, svg);
        tx.textContent = String(day);
      }
      r.addEventListener('pointermove', ev => showTip(ev, d.day, d.recorded ? [{ label: t('relayed'), value: fmtBytes(d.bytes) },
        { label: t('tSessions'), value: fmtNum(d.sessions) }] : [{ label: t('notRecorded'), value: '' }]));
      r.addEventListener('pointerleave', hideTip);
    });
  }

  // ---- limits ----
  function level(frac) { return frac > .85 ? ['bad', 'lvNear'] : frac > .6 ? ['warn', 'lvBusy'] : ['ok', 'lvOk']; }
  function renderLimits() {
    const l = state.limits;
    if (!l) return;
    const g = clear($('gauges'));
    l.gauges.forEach(gg => {
      const frac = gg.max ? gg.value / gg.max : 0;
      const [cls, lv] = level(frac);
      const box = h('div', 'gauge');
      const hd = h('div', 'gauge-h');
      append(hd, h('span', 'k', t('g_' + gg.key)), h('span', 'v', fmtNum(gg.value) + ' / ' + fmtNum(gg.max)), chip(t(lv), cls, true));
      append(box, hd, barMeter(frac, cls === 'ok' ? '' : cls), h('div', 'sub', fmtPct(frac * 100)));
      g.appendChild(box);
    });
    const rt = clear($('rates'));
    const th = h('div', 'tr th');
    append(th, h('span', '', t('colLimit')), h('span', '', t('colBurst')), h('span', '', t('colRefill')), h('span', '', t('colRefused')));
    rt.appendChild(th);
    const shared = ['new_id_net', 'new_id_all', 'connect_net'];
    l.rates.forEach(r => {
      const row = h('div', 'tr');
      const nm = h('div');
      append(nm, h('div', 't', t('rl_' + r.key)));
      const ref = shared.includes(r.key) ? h('span', 's', t('counted')) : h('span', 'count' + (r.refused_24h ? ' warn' : ''), fmtNum(r.refused_24h));
      const refCell = h('div');
      refCell.appendChild(ref);
      append(row, nm, h('span', 'n', fmtNum(r.burst)), h('span', 'n', t('every', { d: fmtEvery(r.every_ms) })), refCell);
      rt.appendChild(row);
    });
    const kvs = (id, rows) => {
      const dl = clear($(id));
      rows.forEach(([k, v]) => append(dl, h('dt', '', t(k)), h('dd', '', v)));
    };
    const lm = l.limits;
    kvs('lim-conns', [['kMaxConns', fmtNum(lm.max_conns)], ['kPerSource', fmtNum(lm.max_conns_per_source)], ['kPerNet', fmtNum(lm.max_conns_per_net)],
      ['kMaxIDs', fmtNum(lm.max_ids)], ['kExpiry', t('days', { n: lm.id_expiry_days })], ['kLifetime', fmtDur(lm.client_lifetime_s)]]);
    kvs('lim-ext', [['kBurst', fmtNum(l.ext.burst)], ['kRefill', fmtNum(l.ext.refill_per_second)], ['kConc', fmtNum(l.ext.max_concurrent)],
      ['kConcSrc', fmtNum(l.ext.per_source_concurrent)]]);
    if (l.ws) {
      kvs('lim-ws', [['kWSMax', fmtNum(l.ws.max_conns)], ['kWSPer', fmtNum(l.ws.max_conns_per_source)], ['kWSRate', fmtBits(l.ws.rate * 8)],
        ['kWSTotal', fmtBits(l.ws.total_rate * 8)], ['kWSIdle', fmtDur(l.ws.idle_s)], ['kWSRealIP', t(l.ws.real_ip_header ? 'yes' : 'no')]]);
    } else {
      const dl = clear($('lim-ws'));
      dl.appendChild(h('dt', '', t('wsOff')));
      dl.appendChild(h('dd', '', ''));
    }
  }

  // ---- health ----
  function renderHealth() {
    const hl = state.health, ov = state.ov;
    if (!hl || !ov) return;
    const m = ov.memory || {};
    const k = clear($('health-kpis'));
    append(k,
      kpi('alert', hl.errors_1h ? 'bad' : 'n', t('hErr'), fmtNum(hl.errors_1h), t('hLastHour')),
      kpi('alert', hl.warnings_1h ? 'warn' : 'n', t('hWarn'), fmtNum(hl.warnings_1h), t('hLastHour')),
      kpi('chip', 'c1', m.rss_exact ? t('rss') : t('rssApprox'), fmtBytes(m.rss), t('hMemSub', { h: fmtBytes(m.heap) })),
      kpi('pulse', 'n', t('goroutines'), fmtNum(m.goroutines), t('hGoSub')),
    );
    $('log-sub').textContent = t('logSub', { n: hl.keep || 200 });
    const box = clear($('logs'));
    if (!hl.logging) {
      box.appendChild(emptyState('pulse', t('logOffT'), t('logOffS')));
      return;
    }
    const recs = (state.logLevel === 'info' ? hl.info : hl.warnings) || [];
    if (!recs.length) {
      box.appendChild(emptyState('check', t('logNoneT'), state.logLevel === 'info' ? t('logNoneInfoS') : t('logNoneS'), true));
      return;
    }
    const today = new Date().toDateString();
    recs.slice().reverse().forEach(r => {
      const row = h('div', 'log');
      const d = new Date(r.t);
      const tm = h('time', '', (d.toDateString() === today ? '' : d.toLocaleDateString(loc(), { day: 'numeric', month: 'short' }) + ' ') +
        d.toLocaleTimeString(loc(), { hour: '2-digit', minute: '2-digit', second: '2-digit' }));
      tm.setAttribute('datetime', d.toISOString());
      const lv = chip(t('lv_' + r.level), r.level === 'error' ? 'bad' : r.level === 'warn' ? 'warn' : '', r.level !== 'info');
      const body = h('div', 'body');
      body.appendChild(h('div', 'msg', r.msg));
      if (r.attrs && r.attrs.length) {
        const at = h('div', 'attrs');
        r.attrs.forEach(a => at.appendChild(h('span', '', a.k + '=' + a.v)));
        body.appendChild(at);
      }
      append(row, tm, lv, body);
      box.appendChild(row);
    });
  }

  // ---- render, routing, polling ----
  function render() {
    renderShell();
    syncSegs();
    if (state.page === 'overview') renderOverview();
    if (state.page === 'trends') renderTrends();
    if (state.page === 'relay') renderRelay();
    if (state.page === 'limits') renderLimits();
    if (state.page === 'health') renderHealth();
  }
  function route() {
    const p = (location.hash || '').replace(/^#/, '');
    state.page = PAGES[p] ? p : 'overview';
    hideTip();
    render();
    load();
  }
  let timer = null;
  function schedule() {
    clearInterval(timer);
    timer = setInterval(() => { if (!document.hidden) load(); else renderShell(); }, 12000);
  }
  let resizeT = null;

  document.addEventListener('DOMContentLoaded', () => {
    applyTheme();
    applyLang();
    document.querySelectorAll('.seg[data-set]').forEach(seg => seg.addEventListener('click', ev => {
      const b = ev.target.closest('button');
      if (!b) return;
      if (seg.dataset.set === 'theme') { themePref = b.dataset.v; setPref('remohop-panel-theme', themePref); applyTheme(); }
      else { langPref = b.dataset.v; setPref('remohop-panel-lang', langPref); applyLang(); }
      render();
    }));
    $('range').addEventListener('click', ev => {
      const b = ev.target.closest('button');
      if (!b) return;
      state.range = b.dataset.v;
      setPref('remohop-panel-range', state.range);
      render();
      load();
    });
    $('log-filter').addEventListener('click', ev => {
      const b = ev.target.closest('button');
      if (!b) return;
      state.logLevel = b.dataset.v;
      render();
    });
    $('refresh').addEventListener('click', () => {
      const b = $('refresh');
      b.classList.remove('spin');
      void b.offsetWidth;
      b.classList.add('spin');
      load();
    });
    window.addEventListener('hashchange', route);
    window.addEventListener('resize', () => { clearTimeout(resizeT); resizeT = setTimeout(render, 120); });
    document.addEventListener('visibilitychange', () => { if (!document.hidden) load(); else renderShell(); });
    try { window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', render); } catch (e) { /* old browsers */ }
    setInterval(renderShell, 5000);
    route();
    schedule();
  });
})();
