// Renders the RemoHop Google Play phone screenshots (posters) from the app's CI captures.
// Same background, type and headlines as the App Store artwork: ../../../ios/store-art/build.js.
// Usage: PPTR=puppeteer-core CHROME=/path/to/chrome node build.js
// Captures: src/<locale>/<n>-<screen>.webp or .png (the remohop-play-screenshots artifact of
// sodesk-android.yml with SCREENSHOTS=only; 1080x1920, 1920x1080 for the landscape desktop).
// Output: out/<locale>/<n>-<scene>.jpg, 1242x2208 (9:16).
const fs = require("fs");
const path = require("path");
const { T, page, render } = require("../../../ios/store-art/build.js");

const ROOT = __dirname;
const SRC = process.env.SRC || path.join(ROOT, "src");
const OUT = process.env.OUT || path.join(ROOT, "out");
const LOCALES = { en: "en-US", zh: "zh-CN" };

// Play order. [scene, capture, landscape capture or null]; the headline is the iPhone poster's
// for the same scene (Settings takes the privacy one: identity key, no account, server of your choice).
const SCENES = [
  ["desktop", "4-desktop", "8-desktopwide"],
  ["terminal", "2-terminal", null],
  ["files", "3-files", null],
  ["connect", "1-connect", null],
  ["clipboard", "6-clipboard", null],
  ["info", "7-info", null],
  ["settings", "5-settings", null],
];
const HEADLINE = { settings: "privacy" };

function capture(lang, name) {
  for (const ext of [".webp", ".png"]) {
    const f = path.join(SRC, LOCALES[lang], name + ext);
    if (fs.existsSync(f)) return "file:///" + f.replace(/\\/g, "/");
  }
  return null;
}

const W = 1242, H = 2208;
const FRAME = `
.pixel{position:absolute;background:#05070d;border-radius:var(--r);padding:var(--b);
  box-shadow:0 calc(var(--w)*.05) calc(var(--w)*.14) rgba(3,6,20,.55),0 0 0 calc(var(--w)*.005) rgba(255,255,255,.18) inset;}
.pixel img{display:block;width:100%;border-radius:calc(var(--r) - var(--b));}
.pixel .cam{position:absolute;left:50%;top:calc(var(--b) + var(--s)*.0125);transform:translate(-50%,0);
  width:calc(var(--s)*.03);height:calc(var(--s)*.03);border-radius:50%;background:#000;box-shadow:0 0 0 2px #111}
.pixel.land .cam{left:calc(var(--b) + var(--s)*.0125);top:50%;transform:translate(0,-50%)}
`;

// A Pixel-style frame around a capture: w = the frame's outer width, a = the capture's h/w;
// the camera sits in the middle of the status bar (on the left in landscape).
function pixel(src, x, y, w, a, land = false) {
  const b = Math.round(w * 0.028), r = Math.round(w * (land ? 0.055 : 0.075));
  const sw = w - 2 * b, s = land ? sw * a : sw; // s: the capture's short side as drawn
  return `<div class="pixel${land ? " land" : ""}" style="--w:${w}px;--b:${b}px;--r:${r}px;--s:${s}px;left:${x}px;top:${y}px;width:${w}px">
    <img src="${src}"><div class="cam"></div></div>`;
}
const frameH = (w, a) => Math.round((w - 2 * Math.round(w * 0.028)) * a) + 2 * Math.round(w * 0.028);

function poster(lang, scene, shot, wide) {
  const [, title, sub] = T[lang].phone.find(p => p[0] === (HEADLINE[scene] || scene));
  const text = `<div style="position:absolute;left:90px;right:90px;top:150px;text-align:center">
      <h1 style="font-size:${lang === "zh" ? 100 : 104}px">${title}</h1>
      <p class="sub" style="font-size:44px;margin-top:34px">${sub}</p>
    </div>`;
  let body;
  if (wide) {
    // Both ways round: the desktop tab upright behind, full screen sideways in front.
    const pw = 820, lw = 1150, lh = frameH(lw, 9 / 16);
    body = pixel(shot, (W - pw) / 2, 880, pw, 16 / 9) +
      `<div style="position:absolute;left:0;right:0;top:650px;height:${lh}px;transform:rotate(-4deg)">${pixel(wide, (W - lw) / 2, 0, lw, 9 / 16, true)}</div>`;
  } else {
    const pw = 880, ph = frameH(pw, 16 / 9);
    body = pixel(shot, (W - pw) / 2, H - ph - 80, pw, 16 / 9);
  }
  return page(W, H, lang, `<style>${FRAME}</style>${text}${body}`);
}

(async () => {
  const jobs = [];
  for (const lang of ["en", "zh"]) {
    let n = 0;
    for (const [scene, name, wideName] of SCENES) {
      const shot = capture(lang, name);
      if (!shot) { console.log(`${LOCALES[lang]}: no ${name} capture, ${scene} poster skipped`); continue; }
      const wide = wideName && capture(lang, wideName);
      jobs.push([path.join(OUT, LOCALES[lang], `${++n}-${scene}.jpg`), W, H, poster(lang, scene, shot, wide)]);
    }
  }
  await render(jobs, OUT);
})();
