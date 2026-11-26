# RemoHop website

Static site for RemoHop: plain HTML and one stylesheet. No JavaScript, no web
fonts, no CDN, no analytics, no cookies.

```
site/
  public/      what gets deployed (commit it; no build step on the host)
    _headers   Cloudflare Pages response headers (CSP, HSTS, caching)
    assets/    site.css, icons, screenshots (AVIF, WebP, PNG fallback)
    en/        English pages; Chinese (zh-CN) is at the root
  src/zh, src/en   page bodies as HTML fragments
  build.py     wraps the fragments in the shared header/footer, renders the
               docs, writes sitemap.xml and robots.txt (standard library only)
  md.py        the small Markdown converter build.py uses for docs/*.md
  review/      screenshots from a layout check; not deployed
```

Pages, each in Chinese (`/…`) and English (`/en/…`): home, `features/`, `how-it-works/`, `docs/`,
`apps/`, `security/`, `self-hosting/`, `privacy/`, `terms/`, `licensing/`,
`support/`, plus `404.html`. The documentation is generated from the Markdown
in the repository's `docs/` (see below).

The home page: a dark hero (moving aurora and grid, the browser client with
the desktop app's connection request floating over it, a platform marquee),
a row of numbers, an animated picture of how a connection is made, the
feature cards, the two ways to use it, the downloads and the current status.
Everything else lives on `features/` (commands, the browser client, how a
connection is made, platforms, install, limits) and the other pages. The home
fragment lays out its own full-width bands, so `build.py` does not wrap it in
the prose column the other pages use.

Motion is CSS only (plus SMIL inside the home page's inline SVG): keyframes,
`@property` for the rotating rims and the counting numbers, and scroll-driven
animations (`animation-timeline: view()`) for the sections that slide in and
the terminal that types. Browsers without scroll-driven animations show
everything in place, and `prefers-reduced-motion: reduce` stops all of it.
Home-page classes that could clash with the How it works page carry an `h-`
prefix.

Colours are shared with the web client and the quick-support window: cobalt
`#2546d8`, ink `#141c30`, paper `#f6f7fb`, line `#dce1ec`; green, amber and
red only ever appear together with words. The icons (`favicon.*`,
`apple-touch-icon.png`, `assets/icon-*.png`) and the header mark are the
RemoHop mark: two screen corners and an arrow on a cobalt tile.

## Editing

Edit a fragment in `src/`, or `public/assets/site.css`, then run:

```bash
python3 site/build.py
python3 -m http.server -d site/public 8000   # preview; links are root-relative
```

The stylesheet URL carries a content hash, so rebuild after changing the CSS.
Links between pages are written `{{L:slug}}` and resolve to the right language
(`{{L:docs/quick-start}}` for a guide page); `{{E:slug}}` always means the
English page, for the reference documents that exist only in English.
Running the script twice gives identical output.

## Documentation pages

`build.py` renders the Markdown in `docs/` with `md.py` into site pages that
use the normal layout. Edit the Markdown, not the generated HTML.

| Source | URL |
|---|---|
| `docs/zh/<name>.md` (user guide) | `/docs/<name>/` |
| `docs/en/<name>.md` (user guide) | `/en/docs/<name>/` |
| `docs/security.md`, `troubleshooting.md`, `agents.md`, `net.md`, `public-server.md`, `ARCHITECTURE.md`, `ROADMAP.md` (`REFERENCE` in `build.py`) | `/en/docs/<lowercase name>/`, English only |
| `docs/zh/README.md`, `docs/en/README.md` (the guide's contents) | rendered into the "Full documentation" section of `/docs/` and `/en/docs/` (`{{GUIDES}}` in `docs.html`) |

`/docs/` itself stays the quick-start page and is the documentation index.
Links between the Markdown files become site links, and heading ids follow
GitHub's rules, so `#anchors` keep working. Links to `docs/README.md` go to
the index; links to `README.zh-CN.md#许可证`/`README.md#license` go to
`licensing/`; links to any other file in the repository become GitHub links,
or plain text before launch. Images (`docs/img/*.png`) are copied to
`/assets/docs/` with a content hash in the URL. `docs/app-store.md` is not
published. `md.py` supports what the docs use (headings, lists, tables, code,
links, images, blockquotes, emphasis); raw HTML in Markdown is shown as text.

## Before launch: `SOURCE_PUBLIC`

The source stays private until the apps are in the stores, and the public
GitHub repository is empty until then. `SOURCE_PUBLIC = False` in `build.py`
keeps the site from linking to it or calling RemoHop open source:

- In `src/*.html`, `<!--src-->…<!--/src-->` is published only when
  `SOURCE_PUBLIC` is True, and `<!--nosrc-->…<!--/nosrc-->` only when it is
  False. Blocks may be inline or whole lines; they do not nest. Use them for
  anything that links to the repository or tells people to build from source.
- The footer's "Source (GitHub)" link appears only when True; descriptions
  and the footer's licence line come from `pick(public, prelaunch)`.
- In the docs, links to repository files become plain text, the pages in
  `PRELAUNCH_DOC_STUBS` (install, building) are replaced by a short note, and
  `PRELAUNCH_DOC_EDITS` replaces passages that need the source (exact text;
  the build stops if a rule no longer matches its document).
- The build stops if any generated file still contains the repository URL.

At launch: make the repository public, set `SOURCE_PUBLIC = True`, run
`python3 site/build.py`, check the result and commit. The pre-launch rules can
then be deleted.

## Deploying on Cloudflare Pages

1. Workers & Pages → Create → Pages → connect the repository.
2. Framework preset: None. Build command: empty. Build output directory:
   `site/public`.
3. Add the custom domain, then set `SITE_URL` in `build.py` to it, run the
   script and commit. Canonical links, hreflang, Open Graph, `sitemap.xml` and
   `robots.txt` all use that value.

Without Git integration: `npx wrangler pages deploy site/public --project-name remohop`.

`_headers` applies the security headers on Pages. On another host, set the same
headers in its configuration. HSTS is sent as `max-age=31536000` without
`includeSubDomains` or `preload`; add those only when every subdomain is
HTTPS-only.

## Before going live

Search for `TODO(owner)` in `site/`. Open items:

- **Domain.** `SITE_URL` is `https://remohop.com`; the pages are only reachable
  there once the domain is registered and pointed at the host.
- **Source publication.** `SOURCE_PUBLIC` is False (see above). Flip it when
  the repository is public.
- **Downloads.** The home page's "Get RemoHop" section, the install section
  on `features/`, step 1 on `docs/` and the install guide page say there are
  no releases. Replace them with real links after the first release.
- **Privacy policy.** Public server hostname, log retention (written as 14
  days), support mail retention (written as 24 months), the hosting and mail
  providers, and the publisher's legal name and address.
- **Terms.** Governing law and venue, if wanted, after legal review.
- **App store links** on `apps/` once the apps are released.

## Screenshots

All screenshots are real captures, taken with Playwright's headless Chromium
in the light colour scheme. The terminal samples on `features/` are unedited
output of a real session.

**Desktop tab** (`web-desktop-v2-*`, on the home page, on `features/` and as
`og:image`). The page shows it about 34rem wide on a desktop and 22rem on a
phone, so it is captured at a small viewport (UI text stays readable when
shrunk) and a high scale factor (the files are true pixels):

1. Remote screen: `Xvfb :99 -screen 0 2560x1440x24 -dpi 192 -noreset`, then
   `xsetroot -solid '#2f3b55'` with a blank cursor bitmap (`-cursor`) so the
   pointer does not show over the background. Two xterms with
   `-fa "JetBrains Mono" -fs 17 -fg "#24292f" -b 14 -bw 3 -bd "#1b2438" +sb`:
   one at `-geometry 54x17+60+90` (`-bg "#fdf6e3"`) where `ls -l` and
   `cat Makefile` ran in a small Go project, one at `-geometry 38x5+1478+60`
   (`-bg "#eef3fb"`, cursor hidden with `printf '\e[?25l'`) running
   `watch -n 2 -t` on `date`, the load average and `df -h /`. The monitor
   window sits top right because the floating dialog covers the bottom right.
2. `DISPLAY=:99 remohop host --no-signal --listen 127.0.0.1:21118`; pair with
   `--name studio`; `remohop web --no-open`.
3. Open the page at a 1008×806 CSS pixel viewport, `deviceScaleFactor: 3`
   (below 1000 px the header wraps to two rows; at this height the stage is
   16:9, so the remote screen fills it). Choose the saved host, the Desktop
   tab, "Native size" and automatic frame rate (the HUD then reads 2560×1440
   H.264 at about 27 Mbit/s). Park the remote pointer in the bottom right
   corner, move the mouse off the canvas, and wait 20 s so the encoder has
   refined the still screen. Capture down to 20 px under the stage:
   1008×792 CSS pixels, 3024×2376 pixels.
4. Export 640, 1280, 1920 and 2640 pixel wide AVIF (Pillow, quality 70,
   4:4:4 chroma, speed 2) and WebP (quality 88, method 6) files plus a 1280
   pixel PNG, all Lanczos downscales of the capture. The `width`/`height`
   attributes are the CSS size, 1008×792.

**Connection request** (`request-{zh,en}-*`, floating on the home page). Run
`remohop app --browser --no-open --lang zh` (or `en`) with its own config
directory (`signal-none on`, `host-listen 127.0.0.1:21942`), finish the first
run wizard with "Skip for now" and incoming connections on, and read the
temporary password from "This computer". From a fresh config directory whose
`name` is `Laptop-2`, run `remohop --password PW 127.0.0.1:21942 info`; the
dialog then shows a new device with the scam warning. At a 700×900 viewport
and `deviceScaleFactor: 3`, wait until the countdown reads 20 s, hide
everything but the dialog (transparent page and backdrop; the context needs
`bypassCSP`), and take an element screenshot with `omitBackground`. The dialog
is 440 CSS pixels wide (446 tall in Chinese, 466 in English). Export 440, 880
and 1320 pixel wide AVIF and WebP files with alpha, as above, plus a 1320 pixel
PNG.

**Terminal tab** (`web-terminal-*`, on `features/`): captured from the same
`remohop web` session at 1120×700 CSS pixels and `deviceScaleFactor: 2`, with
an Xvfb display of 1280×720, and exported 640, 1120 and 2240 pixels wide as
AVIF and WebP plus a 1120 pixel PNG.

Images under `/assets/` are cached for a year, so a retaken image gets a new
file name.

## Guidance applied

From Chrome's [Modern Web Guidance](https://developer.chrome.com/docs/modern-web-guidance)
guides (HTML, CSS, dark mode, typography, accessibility, performance, security,
cross-document transitions, responsive tables) and its list of common mistakes:

- **Baseline first.** Widely available features are used directly. Newer ones
  either degrade safely with no fallback code (`text-wrap`, `@view-transition`,
  `light-dark()` behind an `@supports not` fallback) or are not used.
- **Dark mode.** `<meta name="color-scheme" content="light dark">` plus
  `color-scheme` on `:root`; colour tokens with `light-dark()`; a media-query
  fallback for browsers without it. No theme toggle: the page follows the system.
- **CSS.** Cascade layers declared up front, tokens as custom properties on
  `:root`, logical properties, container queries for the two-column blocks,
  `:has()` where it replaces a class, no `*` reset beyond `box-sizing`.
- **Typography.** System font stack with CJK fallbacks, `rem` sizes, unitless
  line height, a capped measure, `text-wrap: balance` on headings and `pretty`
  on body text, fluid headings with `clamp()` using font-relative bounds.
- **Motion.** The only animation is a short cross-fade between pages, and the
  `@view-transition` rule itself sits inside
  `prefers-reduced-motion: no-preference`. No smooth scrolling.
- **Accessibility.** Landmarks, one `h1` per page, ordered headings, a skip
  link to a focusable `main`, labelled navigation, `aria-current`, visible
  `:focus-visible` outlines, table headers with `scope`, scrollable tables and
  code blocks reachable by keyboard, alt text, `lang` on the page and on links
  to the other language, 24 px minimum targets, a `forced-colors` rule. Text
  and accent colours meet WCAG AA in both schemes.
- **Performance.** No render-blocking script, one small stylesheet, no web
  fonts, `width`/`height` on every image, `<picture>` with AVIF and WebP and
  `srcset`/`sizes`, lazy loading for the below-the-fold screenshots, `svh`
  instead of `vh`. HTML is 4 to 10 KB gzipped per page and the CSS about 5 KB.
- **Security.** A CSP that starts at `default-src 'none'` (possible because
  nothing is inline), `frame-ancestors 'none'`, `base-uri 'none'`, HSTS,
  `nosniff`, `Referrer-Policy`, `Permissions-Policy`, COOP. No third-party
  subresources, so no SRI is needed.
- **Privacy and SEO.** No cookies or trackers; a unique title and description
  per page, canonical and `hreflang` links (with `x-default`), a sitemap with
  language alternates, Open Graph tags, and `noindex` on preview deployments.

## Checking

Each page was loaded in headless Chrome at 320, 375 and 1280 px, in light and
dark, behind the CSP from `_headers`: no console errors, no CSP violations, no
horizontal page scroll, no inline scripts or styles, and no broken internal
links or anchors. Lighthouse has not been run yet; run it against the deployed
site.
