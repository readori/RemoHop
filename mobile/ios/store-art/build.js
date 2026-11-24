// Renders the RemoHop App Store artwork from the real-device screenshots in ./src.
// Usage: node build.js   (needs puppeteer-core; set PPTR to its node_modules folder)
// Screens are src/<locale>-<screen>.webp (real-device screenshots). Output: out/<locale>/*.jpg.
const fs = require("fs");
const path = require("path");
const PPTR = process.env.PPTR || "puppeteer-core";
const puppeteer = require(PPTR);

const ROOT = __dirname;
const SRC = process.env.SRC || path.join(ROOT, "src");
const OUT = process.env.OUT || path.join(ROOT, "out");
const CHROME = process.env.CHROME || "C:/Program Files/Google/Chrome/Application/chrome.exe";
const url = f => "file:///" + path.join(SRC, f).replace(/\\/g, "/");

// Screens: src file per language.
const FILE = { connect: "connect-live" };
const SHOT = new Proxy({}, { get: (_, key) => ({ en: `en-${FILE[key] || key}.webp`, zh: `zh-${FILE[key] || key}.webp` }) });

const T = {
  en: {
    tagline: "Your computers,<br>one tap away",
    subtitle: "P2P remote desktop &amp; terminal",
    chips: ["Remote desktop", "Terminal", "Files", "Clipboard"],
    trust: ["End-to-end encrypted", "Peer-to-peer", "No account"],
    phone: [
      ["desktop", "Your computer,<br>in your pocket", "Remote desktop with keyboard, mouse and special keys"],
      ["terminal", "A real terminal,<br>anywhere", "Sessions stay open, so you can pick up where you left off"],
      ["files", "Your files,<br>at hand", "Browse, upload and organise files on your computer"],
      ["connect", "Pair once,<br>skip the password", "Connect by ID or address. No account needed."],
      ["clipboard", "Share the<br>clipboard", "Send text to your computer, or fetch what it copied"],
      ["info", "Your machine<br>at a glance", "System, memory, disk, uptime and connection details"],
      ["privacy", "Private<br>by design", "End-to-end encrypted and peer-to-peer. No analytics, ads or tracking."],
    ],
    pad: [
      [["desktop", "terminal"], "Screen and terminal,<br>wherever you are", "Remote desktop and real shell sessions on your own computers"],
      [["files", "clipboard"], "Files and clipboard", "Move files and text between your device and your computer"],
      [["connect", "info"], "Pair once, connect by ID", "Saved computers connect without a password. No account needed."],
      [["privacy", "settings"], "End-to-end encrypted", "Peer-to-peer sessions. Keys and settings stay on your device."],
    ],
  },
  zh: {
    tagline: "你的电脑，<br>一触即达",
    subtitle: "点对点远程桌面与终端",
    chips: ["远程桌面", "终端", "文件", "剪贴板"],
    trust: ["端到端加密", "点对点直连", "无需注册账号"],
    phone: [
      ["desktop", "电脑装进口袋", "远程桌面，支持键盘、鼠标和常用按键"],
      ["terminal", "随时随地，<br>真正的终端", "会话保持在线，回来接着用"],
      ["files", "文件随手可取", "浏览、上传、整理电脑上的文件"],
      ["connect", "配对一次，<br>以后免密码", "输入 ID 或地址即可连接，无需注册账号"],
      ["clipboard", "剪贴板双向互通", "把文字发到电脑，或读取电脑的剪贴板"],
      ["info", "主机状态一目了然", "系统、内存、磁盘、运行时间和连接方式"],
      ["privacy", "隐私，从设计开始", "端到端加密、点对点直连，没有统计、广告或跟踪"],
    ],
    pad: [
      [["desktop", "terminal"], "屏幕和终端，随时在手", "在手机和平板上操作你自己的电脑"],
      [["files", "clipboard"], "文件与剪贴板", "在设备和电脑之间传文件、传文字"],
      [["connect", "info"], "配对一次，按 ID 直连", "保存过的电脑免密码连接，无需注册账号"],
      [["privacy", "settings"], "端到端加密", "点对点会话，密钥和设置只保存在本设备上"],
    ],
  },
};

const FONT = `'Inter','Noto Sans SC','PingFang SC','Microsoft YaHei UI',sans-serif`;
const css = (W, H) => `
@import url('https://fonts.googleapis.com/css2?family=Inter:wght@500;600;700;800&family=Noto+Sans+SC:wght@500;700;900&display=block');
*{margin:0;padding:0;box-sizing:border-box}
html,body{width:${W}px;height:${H}px;overflow:hidden}
body{font-family:${FONT};color:#fff;position:relative;
  background:
    radial-gradient(ellipse 70% 45% at 85% 8%, rgba(120,150,255,.55), transparent 70%),
    radial-gradient(ellipse 60% 40% at 5% 95%, rgba(37,70,216,.55), transparent 70%),
    linear-gradient(165deg,#2b4fe0 0%,#1d36b0 38%,#121c4a 72%,#0b1022 100%);}
body:before{content:"";position:absolute;inset:0;
  background-image:radial-gradient(rgba(255,255,255,.07) 1.5px, transparent 1.6px);
  background-size:${Math.round(W / 40)}px ${Math.round(W / 40)}px;
  mask-image:linear-gradient(180deg,rgba(0,0,0,.9),transparent 60%);}
.phone{position:absolute;background:#05070d;border-radius:var(--r);padding:var(--b);
  box-shadow:0 calc(var(--w)*.06) calc(var(--w)*.16) rgba(3,6,20,.55),0 0 0 calc(var(--w)*.006) rgba(255,255,255,.18) inset;}
.phone img{display:block;width:100%;border-radius:calc(var(--r) - var(--b));}
.phone .island{position:absolute;left:50%;top:calc(var(--b) + var(--w)*.025);transform:translateX(-50%);
  width:calc(var(--w)*.3);height:calc(var(--w)*.085);background:#000;border-radius:999px}
h1{font-weight:800;letter-spacing:-.02em;line-height:1.08}
.zh h1{letter-spacing:0;line-height:1.2;font-weight:900}
p.sub{color:rgba(255,255,255,.82);font-weight:500;line-height:1.35}
.chip{display:inline-block;border-radius:999px;background:rgba(255,255,255,.14);border:1px solid rgba(255,255,255,.25);font-weight:600}
`;

// Phone frame: w = outer width; the screenshot keeps its own aspect ratio.
function phone(file, x, y, w, extra = "") {
  const b = Math.round(w * 0.035), r = Math.round(w * 0.16);
  return `<div class="phone" style="--w:${w}px;--b:${b}px;--r:${r}px;left:${x}px;top:${y}px;width:${w}px;${extra}">
    <img src="${url(file)}"><div class="island"></div></div>`;
}
const ASPECT = 2000 / 923; // screenshot h/w
const phoneH = w => (w - 2 * Math.round(w * 0.035)) * ASPECT + 2 * Math.round(w * 0.035);

function page(W, H, lang, body) {
  return `<!doctype html><html lang="${lang === "zh" ? "zh-Hans" : "en"}"><head><meta charset="utf-8"><style>${css(W, H)}</style></head>
  <body class="${lang}">${body}</body></html>`;
}

function iphone(lang, [key, title, sub]) {
  const W = 1320, H = 2868, pw = 960, ph = phoneH(pw);
  const top = H - ph - 90;
  return page(W, H, lang, `
    <div style="position:absolute;left:110px;right:110px;top:170px;text-align:center">
      <h1 style="font-size:${lang === "zh" ? 112 : 118}px">${title}</h1>
      <p class="sub" style="font-size:50px;margin-top:40px">${sub}</p>
    </div>
    ${phone(SHOT[key][lang], (W - pw) / 2, top, pw)}`);
}

function ipad(lang, [[a, b], title, sub]) {
  const W = 2064, H = 2752, pw = 860, ph = phoneH(pw), gap = 110;
  const x0 = (W - 2 * pw - gap) / 2, top = H - ph - 110;
  return page(W, H, lang, `
    <div style="position:absolute;left:160px;right:160px;top:190px;text-align:center">
      <h1 style="font-size:132px">${title}</h1>
      <p class="sub" style="font-size:60px;margin-top:44px">${sub}</p>
    </div>
    ${phone(SHOT[a][lang], x0, top + 60, pw)}
    ${phone(SHOT[b][lang], x0 + pw + gap, top - 60, pw)}`);
}

function brandBlock(lang, iconSize, nameSize) {
  return `<div style="display:flex;align-items:center;gap:${iconSize * 0.32}px">
    <img src="${url("icon.png")}" style="width:${iconSize}px;height:${iconSize}px;border-radius:${iconSize * 0.2237}px;box-shadow:0 ${iconSize * .08}px ${iconSize * .25}px rgba(0,0,0,.35)">
    <div style="font-size:${nameSize}px;font-weight:800;letter-spacing:-.01em">RemoHop</div></div>`;
}

function header(lang) {
  const W = 5244, H = 2950, t = T[lang];
  const chips = t.chips.map(c => `<span class="chip" style="font-size:64px;padding:26px 56px;margin:0 28px 28px 0">${c}</span>`).join("");
  const trust = t.trust.join("&nbsp;&nbsp;·&nbsp;&nbsp;");
  const pw = 1080, ph = phoneH(pw), cy = (H - ph) / 2;
  return page(W, H, lang, `
    <div style="position:absolute;left:300px;top:0;bottom:0;width:2350px;display:flex;flex-direction:column;justify-content:center">
      ${brandBlock(lang, 260, 120)}
      <h1 style="font-size:${lang === "zh" ? 240 : 250}px;margin-top:110px">${t.tagline}</h1>
      <p class="sub" style="font-size:96px;margin-top:70px">${t.subtitle}</p>
      <div style="margin-top:100px">${chips}</div>
      <p class="sub" style="font-size:66px;margin-top:40px;color:rgba(255,255,255,.7)">${trust}</p>
    </div>
    ${phone(SHOT.terminal[lang], 2780, cy + 230, pw, "transform:rotate(-6deg)")}
    ${phone(SHOT.files[lang], 4060, cy + 230, pw, "transform:rotate(6deg)")}
    ${phone(SHOT.desktop[lang], 3400, cy - 40, pw * 1.06, "z-index:2")}`);
}

function search(lang) {
  const W = 3840, H = 2560, t = T[lang];
  const pw = 880, ph = phoneH(pw), gap = 170, x0 = (W - 3 * pw - 2 * gap) / 2, top = H - ph + 260;
  return page(W, H, lang, `
    <div style="position:absolute;left:0;right:0;top:150px;display:flex;flex-direction:column;align-items:center;text-align:center">
      ${brandBlock(lang, 200, 104)}
      <h1 style="font-size:${lang === "zh" ? 150 : 156}px;margin-top:70px">${t.tagline.replace("<br>", " ")}</h1>
      <p class="sub" style="font-size:70px;margin-top:40px">${t.subtitle}&nbsp;&nbsp;·&nbsp;&nbsp;${t.trust.join("&nbsp;&nbsp;·&nbsp;&nbsp;")}</p>
    </div>
    ${phone(SHOT.connect[lang], x0, top + 80, pw)}
    ${phone(SHOT.desktop[lang], x0 + pw + gap, top - 40, pw)}
    ${phone(SHOT.terminal[lang], x0 + 2 * (pw + gap), top + 80, pw)}`);
}

(async () => {
  const jobs = [];
  for (const lang of ["zh", "en"]) {
    const d = path.join(OUT, lang === "zh" ? "zh-Hans" : "en-US");
    jobs.push([path.join(d, "Header_5244x2950.jpg"), 5244, 2950, header(lang)]);
    jobs.push([path.join(d, "SearchResults_3840x2560.jpg"), 3840, 2560, search(lang)]);
    T[lang].phone.forEach((s, i) => jobs.push([path.join(d, `iPhone_1320x2868_${i + 1}_${s[0]}.jpg`), 1320, 2868, iphone(lang, s)]));
    T[lang].pad.forEach((s, i) => jobs.push([path.join(d, `iPad_2064x2752_${i + 1}_${s[0].join("-")}.jpg`), 2064, 2752, ipad(lang, s)]));
  }
  const browser = await puppeteer.launch({ executablePath: CHROME, headless: true, args: ["--allow-file-access-from-files", "--hide-scrollbars"] });
  const pg = await browser.newPage();
  for (const [file, W, H, html] of jobs) {
    fs.mkdirSync(path.dirname(file), { recursive: true });
    const tmp = path.join(OUT, "_page.html");
    fs.writeFileSync(tmp, html);
    await pg.setViewport({ width: W, height: H, deviceScaleFactor: 1 });
    await pg.goto("file:///" + tmp.replace(/\\/g, "/"), { waitUntil: "networkidle0", timeout: 60000 });
    await pg.evaluate(() => document.fonts.ready);
    await pg.screenshot({ path: file, type: "jpeg", quality: 92, clip: { x: 0, y: 0, width: W, height: H } });
    console.log("wrote", path.relative(ROOT, file));
  }
  fs.unlinkSync(path.join(OUT, "_page.html"));
  await browser.close();
})();
