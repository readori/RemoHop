#!/usr/bin/env python3
"""Regenerates site/public/ from site/src/. Standard library only.

The generated HTML is committed, so deploying needs no build step: point the
host at site/public. Run this after editing a fragment in src/, a document in
docs/, the page list below, SITE_URL or SOURCE_PUBLIC.

    python3 site/build.py

Besides the pages in src/, it renders the user guide (docs/zh, docs/en) and
the English reference documents in docs/ into /docs/<name>/ and
/en/docs/<name>/ with md.py.
"""
import hashlib
import html
import os
import pathlib
import re
import shutil
import struct
import sys

sys.dont_write_bytecode = True  # keep site/ free of __pycache__
import md  # noqa: E402  (site/md.py)

# The site's own domain. Used for canonical, hreflang, Open Graph, sitemap.xml
# and robots.txt. Change it here and run this script again.
SITE_URL = "https://remohop.com"
REPO = "https://github.com/readori/RemoHop"

# Pre-launch switch. The source stays private until the apps are in the
# stores, so the site must not link to the repository or call RemoHop open
# source yet. With False:
#   - <!--src-->...<!--/src--> blocks in src/*.html are dropped and
#     <!--nosrc-->...<!--/nosrc--> blocks are shown (True: the reverse);
#   - the footer has no "Source (GitHub)" link;
#   - links in docs/*.md to files outside the published docs (Go code,
#     packaging, CONTRIBUTING.md, ...) are printed as plain text, and the
#     PRELAUNCH_DOC_EDITS and PRELAUNCH_DOC_STUBS below apply;
#   - the build fails if any generated page still contains the repository URL.
# At launch: make the repository public, set this to True, run this script.
SOURCE_PUBLIC = True


def pick(public, prelaunch):
    return public if SOURCE_PUBLIC else prelaunch


EMAIL = "support@readori.com"
TELEGRAM = "https://t.me/remohop"
UPDATED = "2026-11-23"

HERE = pathlib.Path(__file__).resolve().parent
ROOT = HERE.parent
SRC = HERE / "src"
OUT = HERE / "public"
DOCS = ROOT / "docs"

# The stylesheet is cached for a year (see _headers), so its URL carries a
# content hash. Editing site.css therefore needs a rebuild.
CSS_VERSION = hashlib.sha256((OUT / "assets" / "site.css").read_bytes()).hexdigest()[:10]

# slug -> (zh title, zh description, en title, en description)
PAGES = {
    "": (
        "RemoHop：远程连上你的电脑",
        pick("RemoHop 是开源的点对点远程控制工具：9 位 ID 加临时密码，端到端加密的 QUIC 会话，shell、文件、端口转发、远程桌面和供 AI 代理调用的 MCP 服务器都在一个 remohop 程序里。",
             "RemoHop 是点对点的远程控制工具：9 位 ID 加临时密码，端到端加密的 QUIC 会话，shell、文件、端口转发、远程桌面和供 AI 代理调用的 MCP 服务器都在一个 remohop 程序里。"),
        "RemoHop: reach your computers from anywhere",
        pick("RemoHop is an open-source peer-to-peer remote control tool: a 9-digit ID and a temporary password, end-to-end encrypted QUIC sessions, and shell, files, port forwarding, remote desktop and an MCP server for AI agents in one remohop binary.",
             "RemoHop is a peer-to-peer remote control tool: a 9-digit ID and a temporary password, end-to-end encrypted QUIC sessions, and shell, files, port forwarding, remote desktop and an MCP server for AI agents in one remohop binary."),
    ),
    "features": (
        "功能与限制",
        "RemoHop 的常用命令、浏览器客户端、连接如何建立、平台支持、安装方式和已知限制。",
        "Features and limits",
        "RemoHop's common commands, the browser client, how a connection is made, platform support, installation and known limits.",
    ),
    "docs": (
        "快速开始与文档",
        pick("从源码编译 remohop，运行汇合服务器，启动被控机，配对并连接。附 RemoHop 全部使用指南和参考文档的目录。",
             "运行汇合服务器，启动被控机，配对并连接。附 RemoHop 全部使用指南和参考文档的目录。"),
        "Quick start and documentation",
        pick("Build remohop from source, run a rendezvous server, start a host, pair and connect. With the index of the RemoHop user guide and reference documents.",
             "Run a rendezvous server, start a host, pair and connect. With the index of the RemoHop user guide and reference documents."),
    ),
    "apps": (
        "Android 与 iOS 控制端应用",
        "RemoHop 的手机应用只是控制端：在手机上连接并操作你的电脑。iOS 正在准备 TestFlight，目前两个平台都没有商店版本。",
        "Android and iOS controller apps",
        "The RemoHop mobile apps are controllers only: connect to and operate your computers from a phone. TestFlight for iOS is in preparation; neither platform has a store release yet.",
    ),
    "security": (
        "安全模型",
        "RemoHop 保护什么、不保护什么：端到端加密、CPace 密码认证密钥交换、主机密钥固定、限速、被控端访问控制，以及浏览器网关等例外。",
        "Security model",
        "What RemoHop protects and what it does not: end-to-end encryption, the CPace password-authenticated key exchange, host key pinning, rate limits, host access control, and the exceptions such as the browser gateway.",
    ),
    "self-hosting": (
        "自建汇合服务器与公共服务器",
        "在一台小虚拟机上运行 remohop signal：端口、内存占用、中继、websocket 桥，以及面向公众提供中继时的限额、流量预算和运营者能看到的数据。",
        "Self-hosting the rendezvous server",
        "Run remohop signal on a small VM: ports, memory use, the relay, the websocket bridge, and the limits, traffic budget and visible metadata when you offer a relay to the public.",
    ),
    "privacy": (
        "隐私政策",
        "RemoHop 如何处理数据：会话端到端加密，应用不收集分析数据；汇合服务器只处理 IP 地址、ID 和连接时间等元数据，可选账号保存邮箱哈希和设备列表。",
        "Privacy policy",
        "How RemoHop handles data: sessions are end-to-end encrypted and the apps collect no analytics; a rendezvous server processes metadata such as IP addresses, IDs and connection times, and optional accounts store an email hash and a device list.",
    ),
    "terms": (
        "使用条款",
        "使用 RemoHop 软件、移动应用、本网站和 Readori 运营的服务器时适用的条款。",
        "Terms of use",
        "The terms that apply when you use the RemoHop software, the mobile apps, this website and servers operated by Readori.",
    ),
    "licensing": (
        "许可证：AGPL-3.0 与商业授权",
        pick("RemoHop 采用双许可：所有人都可按 GNU AGPL-3.0 使用；AGPL 不适合的场景可向 Readori 购买商业授权。",
             "RemoHop 采用双许可：源码将在各平台应用上架后以 GNU AGPL-3.0 公开；AGPL 不适合的场景可向 Readori 购买商业授权。"),
        "Licensing: AGPL-3.0 and commercial",
        pick("RemoHop is dual-licensed: everyone may use it under the GNU AGPL-3.0, and a commercial license is available from Readori where the AGPL does not fit.",
             "RemoHop is dual-licensed: the source will be published under the GNU AGPL-3.0 once the apps are out, and a commercial license is available from Readori where the AGPL does not fit."),
    ),
    "support": (
        "支持与联系",
        "通过 support@readori.com 联系 RemoHop：报告问题时需要附带的信息、排障文档入口，以及如何私下报告安全问题。",
        "Support and contact",
        "Contact RemoHop at support@readori.com: what to include in a report, where the troubleshooting guide is, and how to report a security issue privately.",
    ),
}

NAV = [
    ("features", "功能", "Features"),
    ("docs", "文档", "Docs"),
    ("apps", "移动应用", "Apps"),
    ("security", "安全", "Security"),
    ("self-hosting", "自建服务器", "Self-hosting"),
    ("support", "支持", "Support"),
]

T = {
    "zh": {
        "lang": "zh-CN", "prefix": "", "skip": "跳到正文", "nav": "主导航", "home": "RemoHop 首页",
        "other": ("English", "en", "/en"), "foot_nav": "页脚",
        "f_product": "产品", "f_legal": "条款", "f_contact": "联系",
        "privacy": "隐私政策", "terms": "使用条款", "licensing": "许可证", "source": "源码（GitHub）",
        "legal": pick("RemoHop 由 Readori 发布，以 GNU AGPL-3.0 开源，另提供商业授权。",
                      "RemoHop 由 Readori 发布。源码将在各平台应用上架后以 GNU AGPL-3.0 公开，另提供商业授权。")
        + "本网站不使用 Cookie、分析脚本或任何第三方资源。",
        "docs": "文档", "doc_src": "源文件（GitHub）",
        "sitename": "RemoHop", "locale": "zh_CN",
    },
    "en": {
        "lang": "en", "prefix": "/en", "skip": "Skip to content", "nav": "Primary", "home": "RemoHop home",
        "other": ("中文", "zh-CN", ""), "foot_nav": "Footer",
        "f_product": "Product", "f_legal": "Legal", "f_contact": "Contact",
        "privacy": "Privacy policy", "terms": "Terms of use", "licensing": "Licensing", "source": "Source (GitHub)",
        "legal": pick("RemoHop is published by Readori under the GNU AGPL-3.0, with a commercial license available.",
                      "RemoHop is published by Readori. The source will be published under the GNU AGPL-3.0 once the apps are out; a commercial license is available.")
        + " This website uses no cookies, no analytics and no third-party resources.",
        "docs": "Docs", "doc_src": "Source file (GitHub)",
        "sitename": "RemoHop", "locale": "en_US",
    },
}

# The RemoHop mark: two screen corners and an arrow, on the cobalt app tile.
LOGO = (
    '<svg viewBox="0 0 64 64" aria-hidden="true" focusable="false">'
    '<rect width="64" height="64" rx="13" fill="#2546d8"/>'
    '<path d="M13 31V17H34M30 47H51V33M24 32H42M35 25L42 32L35 39" fill="none" stroke="#f7f8ff" '
    'stroke-width="5.5" stroke-linecap="round" stroke-linejoin="round"/></svg>'
)



# ---- documentation ----
#
# The user guide: docs/zh/<name>.md -> /docs/<name>/ and docs/en/<name>.md ->
# /en/docs/<name>/. Each language's README.md is the guide's table of contents:
# it is not a page of its own but is rendered into the "Full documentation"
# section of /docs/ and /en/docs/ (the {{GUIDES}} placeholder in docs.html),
# and links to it go there. The reference documents below exist in English
# only and live under /en/docs/; the Chinese guide links to them there.
# Not published: docs/README.md (links to it go to the /docs/ index too) and
# docs/app-store.md (material for the App Store submission).
REFERENCE = {
    "security.md": "security",
    "troubleshooting.md": "troubleshooting",
    "agents.md": "agents",
    "net.md": "net",
    "public-server.md": "public-server",
    "ARCHITECTURE.md": "architecture",
    "ROADMAP.md": "roadmap",
}

# Pre-launch only (SOURCE_PUBLIC = False): guide pages that describe what
# needs a release or the source are replaced by a short note under the same
# title and URL.
PRELAUNCH_DOC_STUBS = {
    "install": {
        "zh": "<p>RemoHop 还没有发布版本。各平台应用上架后，这里会列出下载地址和安装方法；源码也会在那时以 AGPL-3.0 公开。</p>",
        "en": "<p>There is no RemoHop release yet. Once the apps are out, this page will list the downloads and how to install them, and the source will be published under the AGPL-3.0 at the same time.</p>",
    },
    "building": {
        "zh": "<p>源码将在各平台应用上架后以 AGPL-3.0 公开。届时这一页会说明如何编译 RemoHop 和运行测试。</p>",
        "en": "<p>The source will be published under the AGPL-3.0 once the apps are out. This page will then describe how to build RemoHop and run its tests.</p>",
    },
}
STUB_CONTACT = {
    "zh": '<p>在此之前有问题，请写信到 <a href="mailto:{0}">{0}</a>。</p>',
    "en": '<p>Questions in the meantime: <a href="mailto:{0}">{0}</a>.</p>',
}

# Pre-launch only: exact replacements in the Markdown before it is rendered,
# for passages that tell people to build from source, point into the
# repository, or call RemoHop open source today. Keys are paths below docs/
# without ".md". Each old text must occur exactly once, so the build fails
# (instead of silently publishing it) when a document changes under a rule.
PRELAUNCH_DOC_EDITS = [
    ("zh/README", "：安装脚本、软件包、手动验证下载、从源码编译。", "：尚未发布，上架后在这里提供下载。"),
    ("en/README", ": install scripts, packages, verifying a download by hand, building from source.",
     ": not released yet; downloads will be listed here once the apps are out."),
    ("zh/self-hosting", """也可以在构建时写进程序：

```bash
go build -ldflags "-X github.com/readori/RemoHop/internal/dial.DefaultSignal=HOST:PORT -X github.com/readori/RemoHop/internal/dial.DefaultSignalPin=KEY" -o remohop ./cmd/remohop
```

""", ""),
    ("en/self-hosting", """Or bake both into a build:

```bash
go build -ldflags "-X github.com/readori/RemoHop/internal/dial.DefaultSignal=HOST:PORT -X github.com/readori/RemoHop/internal/dial.DefaultSignalPin=KEY" -o remohop ./cmd/remohop
```

""", ""),
    ("zh/self-hosting", "- 也可以把桥写进构建：在上面的 `-ldflags` 里加上 `-X github.com/readori/RemoHop/internal/dial.DefaultSignalWS=wss://relay.example.com/remohop/v1/udp`。\n", ""),
    ("en/self-hosting", "- A build can carry the bridge too: add `-X github.com/readori/RemoHop/internal/dial.DefaultSignalWS=wss://relay.example.com/remohop/v1/udp` to the `-ldflags` above.\n", ""),
    ("zh/quick-support", "要把这个构建参数传给 `make quick`：`EXTRA_LDFLAGS=\"-X github.com/readori/RemoHop/internal/dial.DefaultSignal=HOST:21116\" make quick`。", ""),
    ("en/quick-support", " To pass the build setting to `make quick`: `EXTRA_LDFLAGS=\"-X github.com/readori/RemoHop/internal/dial.DefaultSignal=HOST:21116\" make quick`.", ""),
    ("zh/quick-support", "；签名和公证见 [packaging/quick/README.md](../../packaging/quick/README.md)。", "。"),
    ("en/quick-support", "; see [packaging/quick/README.md](../../packaging/quick/README.md) for signing and notarization.", "."),
    ("zh/mobile-apps", "仓库里有两个移动端应用：`mobile/android`（Android）和 `mobile/ios`（iOS）。", "RemoHop 有两个移动端应用：Android 版和 iOS 版。"),
    ("en/mobile-apps", "The repository has two mobile apps: `mobile/android` and `mobile/ios`.", "There are two mobile apps, for Android and iOS."),
    ("zh/mobile-apps", """- 目前没有商店发布版本，需要自己构建。
- Android：运行 `mobile/android/build.sh`（需要 Go、JDK 17+、Android SDK 和 NDK，`ANDROID_HOME` 要设置），默认生成 debug APK 并运行 JVM 单元测试。
- iOS：在 macOS 上先运行 `mobile/ios/build-xcframework.sh` 生成 `Remohopcore.xcframework`，再用 Xcode 构建 `mobile/ios/RemoHop.xcodeproj`。
""", "- 目前没有商店发布版本。\n"),
    ("en/mobile-apps", """- There are no store releases yet. Build them yourself.
- Android: run `mobile/android/build.sh` (needs Go, JDK 17+, the Android SDK and an NDK, with `ANDROID_HOME` set). By default it builds a debug APK and runs the JVM unit tests.
- iOS: on macOS, run `mobile/ios/build-xcframework.sh` to produce `Remohopcore.xcframework`, then build `mobile/ios/RemoHop.xcodeproj` in Xcode.
""", "- There are no store releases yet.\n"),
    ("troubleshooting", " Build Android with `mobile/android/build.sh` (Go, JDK 17+, `ANDROID_HOME`, an NDK) and iOS with `mobile/ios/build-xcframework.sh` on macOS, then Xcode.", ""),
    ("troubleshooting", "`-X github.com/readori/RemoHop/internal/dial.DefaultSignal=...`", "`-X .../internal/dial.DefaultSignal=...`"),
    ("zh/ai-agents", "完整的工具表见 [skills/README.md](../../skills/README.md)。", ""),
    ("en/ai-agents", " The tool table is in [skills/README.md](../../skills/README.md).", ""),
    ("zh/ai-agents", """`skills/` 目录包含六个 Agent Skills，教代理使用命令行：""", "六个 Agent Skills 教代理使用命令行："),
    ("zh/ai-agents", """。Claude Code 的安装方式（在仓库根目录执行）：

```bash
mkdir -p ~/.claude/skills
for s in skills/remohop-*; do ln -sfn "$PWD/$s" ~/.claude/skills/$(basename "$s"); done
```

其他代理的安装方式见 [skills/README.md](../../skills/README.md)。
""", "。它们会随源码一起公开。\n"),
    ("en/ai-agents", "`skills/` contains six Agent Skills that teach an agent to use the CLI:", "Six Agent Skills teach an agent to use the CLI:"),
    ("en/ai-agents", """. For Claude Code (run from the repository root):

```bash
mkdir -p ~/.claude/skills
for s in skills/remohop-*; do ln -sfn "$PWD/$s" ~/.claude/skills/$(basename "$s"); done
```

See [skills/README.md](../../skills/README.md) for other agents and the setup steps.
""", ". They will be published with the source.\n"),
    ("zh/platforms", "- **许可证：** AGPL-3.0，另提供商业授权", "- **许可证：** 源码将在各平台应用上架后以 AGPL-3.0 公开，另提供商业授权"),
    ("en/platforms", "- **License:** AGPL-3.0, with a commercial license available",
     "- **License:** the source will be published under the AGPL-3.0 once the apps are out; a commercial license is available"),
    ("ROADMAP", "RemoHop is open source (AGPL-3.0) first.",
     "RemoHop puts the open-source version (AGPL-3.0) first; the source will be published once the apps are out."),
]

# The first line of each guide page links to the other language; the site
# header already has that switch.
LANG_LINE = re.compile(r"^\[(?:中文|English)\]\([^)]*\)(?: · \[[^\]]*\]\([^)]*\))*[ \t]*\n", re.M)


def url(lang, slug):
    p = T[lang]["prefix"]
    return f"{p}/{slug}/" if slug else f"{p}/"


def gate(text):
    """Keeps or drops <!--src-->…<!--/src--> and <!--nosrc-->…<!--/nosrc-->
    blocks according to SOURCE_PUBLIC. A block that fills whole lines takes
    its line break with it. Blocks do not nest."""
    for name in ("src", "nosrc"):
        if text.count(f"<!--{name}-->") != text.count(f"<!--/{name}-->"):
            raise SystemExit(f"unbalanced <!--{name}--> markers")
    keep, drop = ("src", "nosrc") if SOURCE_PUBLIC else ("nosrc", "src")
    inner = rf"(?:(?!<!--/?{drop}-->).)*"
    text = re.sub(rf"^[ \t]*<!--{drop}-->{inner}<!--/{drop}-->[ \t]*\n", "", text, flags=re.S | re.M)
    text = re.sub(rf"<!--{drop}-->{inner}<!--/{drop}-->", "", text, flags=re.S)
    text = re.sub(rf"^[ \t]*<!--/?{keep}-->[ \t]*\n", "", text, flags=re.M)
    return re.sub(rf"<!--/?{keep}-->", "", text)


def fill(text, lang):
    text = gate(text)
    text = text.replace("{{REPO}}", REPO).replace("{{EMAIL}}", EMAIL).replace("{{TELEGRAM}}", TELEGRAM).replace("{{UPDATED}}", UPDATED)
    text = text.replace("{{SITE}}", SITE_URL)
    # {{L:slug}} is the page in this language, {{E:slug}} the English one
    # (for the reference documents, which exist in English only).
    text = re.sub(r"\{\{L:([a-z0-9/-]*)\}\}", lambda m: url(lang, m.group(1)), text)
    text = re.sub(r"\{\{E:([a-z0-9/-]*)\}\}", lambda m: url("en", m.group(1)), text)
    # Notes for the maintainers stay in the sources, not in the published pages.
    text = re.sub(r"\s*<!--\s*TODO\(owner\).*?-->", "", text, flags=re.S)
    # Scrollable code blocks must be reachable from the keyboard.
    return text.replace("<pre>", '<pre tabindex="0">')


def page(lang, slug, title, desc, body, *, status404=False, langs=("zh", "en"), other=None,
         section=None, raw=False, cls="prose"):
    """langs: the languages the page exists in (hreflang); other: the target
    of the language switch; section: the navigation entry to mark as the
    current section; raw: body is finished HTML, not a src/ fragment."""
    t = T[lang]
    is_home = slug == ""
    full_title = title if is_home else f"{title} | RemoHop"
    canonical = SITE_URL + url(lang, slug)
    head = [
        "<!doctype html>",
        f'<html lang="{t["lang"]}">',
        "<head>",
        '<meta charset="utf-8">',
        '<meta name="viewport" content="width=device-width, initial-scale=1">',
        '<meta name="color-scheme" content="light dark">',
        f"<title>{html.escape(full_title)}</title>",
        f'<meta name="description" content="{html.escape(desc)}">',
        f'<link rel="stylesheet" href="/assets/site.css?v={CSS_VERSION}">',
        '<link rel="icon" href="/favicon.svg" type="image/svg+xml">',
        '<link rel="icon" href="/favicon.ico" sizes="32x32">',
        '<link rel="apple-touch-icon" href="/apple-touch-icon.png">',
        '<link rel="manifest" href="/site.webmanifest">',
        '<meta name="theme-color" content="#f6f7fb" media="(prefers-color-scheme: light)">',
        '<meta name="theme-color" content="#0f1524" media="(prefers-color-scheme: dark)">',
    ]
    if status404:
        head.append('<meta name="robots" content="noindex">')
    else:
        head.append(f'<link rel="canonical" href="{canonical}">')
        if len(langs) > 1:
            head += [
                f'<link rel="alternate" hreflang="zh-CN" href="{SITE_URL}{url("zh", slug)}">',
                f'<link rel="alternate" hreflang="en" href="{SITE_URL}{url("en", slug)}">',
                f'<link rel="alternate" hreflang="x-default" href="{SITE_URL}{url("zh", slug)}">',
            ]
        head += [
            '<meta property="og:type" content="website">',
            '<meta property="og:site_name" content="RemoHop">',
            f'<meta property="og:locale" content="{t["locale"]}">',
            f'<meta property="og:title" content="{html.escape(full_title)}">',
            f'<meta property="og:description" content="{html.escape(desc)}">',
            f'<meta property="og:url" content="{canonical}">',
            f'<meta property="og:image" content="{SITE_URL}/assets/img/web-desktop-1120.png">',
            '<meta property="og:image:width" content="1120">',
            '<meta property="og:image:height" content="700">',
            '<meta name="twitter:card" content="summary_large_image">',
        ]
    head.append("</head>")

    nav = []
    for s, zh, en in NAV:
        cur = ' aria-current="page"' if s == slug else ' aria-current="true"' if s == section else ""
        nav.append(f'<li><a href="{url(lang, s)}"{cur}>{zh if lang == "zh" else en}</a></li>')
    oname, olang, oprefix = t["other"]
    if other:
        other_href = other
    else:
        other_href = "/" if status404 and not oprefix else (oprefix + "/" if status404 else url("en" if lang == "zh" else "zh", slug))
    home_cur = ' aria-current="page"' if is_home and not status404 else ""
    # The home page lays out its own full-width bands; every other page is
    # one column of prose.
    content = (body if raw else fill(body, lang)).strip()
    if not is_home:
        content = f'<div class="wrap {cls}">\n{content}\n</div>'

    product = [f'<li><a href="{url(lang, s)}">{zh if lang == "zh" else en}</a></li>' for s, zh, en in NAV[:5]]
    if SOURCE_PUBLIC:
        product.append(f'<li><a href="{REPO}">{t["source"]}</a></li>')

    body_html = f"""<body>
<a class="skip" href="#main">{t["skip"]}</a>
<header class="site-head">
<div class="wrap">
<a class="brand" href="{url(lang, "")}" aria-label="{t["home"]}"{home_cur}>{LOGO}<span>RemoHop</span></a>
<nav class="site-nav" aria-label="{t["nav"]}">
<ul>
{chr(10).join(nav)}
</ul>
</nav>
<p class="lang mt0"><a href="{other_href}" lang="{olang}" hreflang="{olang}">{oname}</a></p>
</div>
</header>
<main id="main" tabindex="-1"{' class="home"' if is_home and not status404 else ''}>
{content}
</main>
<footer class="site-foot">
<div class="wrap">
<nav class="cols" aria-label="{t["foot_nav"]}">
<div>
<h2>{t["f_product"]}</h2>
<ul>
{chr(10).join(product)}
</ul>
</div>
<div>
<h2>{t["f_legal"]}</h2>
<ul>
<li><a href="{url(lang, "privacy")}">{t["privacy"]}</a></li>
<li><a href="{url(lang, "terms")}">{t["terms"]}</a></li>
<li><a href="{url(lang, "licensing")}">{t["licensing"]}</a></li>
</ul>
</div>
<div>
<h2>{t["f_contact"]}</h2>
<ul>
<li><a href="{url(lang, "support")}">{NAV[5][1] if lang == "zh" else NAV[5][2]}</a></li>
<li><a href="mailto:{EMAIL}">{EMAIL}</a></li>
<li><a href="{TELEGRAM}" rel="noopener">Telegram</a></li>
</ul>
</div>
</nav>
<p class="legal">{t["legal"]}</p>
</div>
</footer>
</body>
</html>
"""
    return "\n".join(head) + "\n" + body_html


def summary(fragment):
    """The meta description of a document: its first paragraph, shortened."""
    m = re.search(r"<p>(.*?)</p>", fragment, re.S)
    text = re.sub(r"\s+", " ", md.plain(m.group(1))).strip() if m else ""
    if len(text) > 160:
        cut = text[:157]
        if " " in cut[100:]:
            cut = cut[: cut.rfind(" ")]
        text = cut.rstrip(" ,;:，、；：") + "…"
    return text


class Docs:
    def __init__(self):
        self.guides = sorted(p.stem for p in (DOCS / "zh").glob("*.md") if p.stem != "README")
        en = sorted(p.stem for p in (DOCS / "en").glob("*.md") if p.stem != "README")
        if self.guides != en:
            raise SystemExit(f"docs/zh and docs/en differ: {sorted(set(self.guides) ^ set(en))}")
        # repository path of a published document -> (language, slug)
        self.urls = {}
        for name in self.guides:
            for lang in ("zh", "en"):
                self.urls[f"docs/{lang}/{name}.md"] = (lang, f"docs/{name}")
        for f, s in REFERENCE.items():
            if not (DOCS / f).is_file():
                raise SystemExit(f"REFERENCE: docs/{f} does not exist")
            self.urls[f"docs/{f}"] = ("en", f"docs/{s}")
        taken = set(self.guides)
        for s in REFERENCE.values():
            if s in taken:
                raise SystemExit(f"REFERENCE: /en/docs/{s}/ collides with another page")
            taken.add(s)
        self.images = {}  # public file name -> source path
        self.edits_used = set()

    def source(self, rel):
        """The Markdown of docs/<rel>.md, with the pre-launch edits applied."""
        text = (DOCS / f"{rel}.md").read_text(encoding="utf-8")
        if SOURCE_PUBLIC:
            return text
        for k, (key, old, new) in enumerate(PRELAUNCH_DOC_EDITS):
            if key != rel:
                continue
            count = text.count(old)
            if count != 1:
                raise SystemExit(f"PRELAUNCH_DOC_EDITS: docs/{rel}.md contains the text {count} times: {old[:70]!r}")
            text = text.replace(old, new)
            self.edits_used.add(k)
        return text

    def link(self, dest, src, lang):
        if re.match(r"^[a-zA-Z][a-zA-Z0-9+.-]*:", dest) or dest.startswith("#"):
            return dest
        path, hashmark, frag = dest.partition("#")
        target = os.path.normpath(os.path.join(os.path.dirname(src), path)).replace(os.sep, "/")
        suffix = "#" + frag if hashmark else ""
        if target in self.urls:
            tl, ts = self.urls[target]
            return url(tl, ts) + suffix
        if target in ("docs/README.md", "docs/zh/README.md", "docs/en/README.md"):
            tl = {"docs/zh/README.md": "zh", "docs/en/README.md": "en"}.get(target, lang)
            return url(tl, "docs") + "#more"
        if target in ("README.md", "README_EN.md") and frag in ("许可证", "license"):
            return url("zh" if target == "README.md" else "en", "licensing")
        if target.startswith("../") or not (ROOT / target).exists():
            raise SystemExit(f"{src}: broken link {dest}")
        # Everything else is a file in the repository.
        return f"{REPO}/blob/main/{target}{suffix}" if SOURCE_PUBLIC else None

    def image(self, dest, src):
        target = os.path.normpath(os.path.join(os.path.dirname(src), dest)).replace(os.sep, "/")
        path = ROOT / target
        if not path.is_file() or path.suffix != ".png":
            raise SystemExit(f"{src}: image {dest} is missing or not a PNG")
        name = path.name
        if self.images.setdefault(name, path) != path:
            raise SystemExit(f"two document images are called {name}")
        data = path.read_bytes()
        width, height = struct.unpack(">II", data[16:24])
        # /assets/ is cached for a year, so the URL carries a content hash.
        return f"/assets/docs/{name}?v={hashlib.sha256(data).hexdigest()[:10]}", width, height

    def render(self, rel, lang, text, offset=0):
        src = f"docs/{rel}.md"
        r = md.Renderer(lambda d: self.link(d, src, lang), lambda d: self.image(d, src), heading_offset=offset)
        body = r.render(LANG_LINE.sub("", text, count=1))
        return body, r.title

    def index(self, lang):
        """The guide's table of contents, for the docs page."""
        text = self.source(f"{lang}/README")
        text = re.sub(r"\A# .*\n", "", text)
        return self.render(f"{lang}/README", lang, text, offset=1)[0]

    def pages(self):
        """Yields (lang, slug, title, description, body, languages, source path)."""
        for name in self.guides:
            for lang in ("zh", "en"):
                rel = f"{lang}/{name}"
                body, title = self.render(rel, lang, self.source(rel))
                if not SOURCE_PUBLIC and name in PRELAUNCH_DOC_STUBS:
                    body = PRELAUNCH_DOC_STUBS[name][lang] + STUB_CONTACT[lang].format(EMAIL)
                yield lang, f"docs/{name}", title, body, ("zh", "en"), f"docs/{rel}.md"
        for f, s in REFERENCE.items():
            rel = f[:-3]
            body, title = self.render(rel, "en", self.source(rel))
            yield "en", f"docs/{s}", title, body, ("en",), f"docs/{f}"

    def copy_images(self):
        dest = OUT / "assets" / "docs"
        dest.mkdir(parents=True, exist_ok=True)
        for old in dest.iterdir():
            if old.name not in self.images:
                old.unlink()
        for name, path in sorted(self.images.items()):
            data = path.read_bytes()
            target = dest / name
            if not target.exists() or target.read_bytes() != data:
                target.write_bytes(data)


def doc_page(lang, slug, title, body, langs, src):
    t = T[lang]
    crumbs = f'<p class="crumbs"><a href="{url(lang, "docs")}#more">{t["docs"]}</a></p>'
    parts = [crumbs, body]
    if SOURCE_PUBLIC:
        parts.append(f'<p class="doc-src small"><a href="{REPO}/blob/main/{src}">{t["doc_src"]}</a></p>')
    other = None if len(langs) > 1 else url("zh", "docs") + "#more"
    return page(lang, slug, title, summary(body), "\n".join(parts), langs=langs, other=other,
                section="docs", raw=True, cls="prose doc")


def main():
    docs = Docs()
    sitemap = []  # one {language: path} per page
    for slug, (zt, zd, et, ed) in PAGES.items():
        name = slug or "index"
        for lang, title, desc in (("zh", zt, zd), ("en", et, ed)):
            body = (SRC / lang / f"{name}.html").read_text(encoding="utf-8")
            if slug == "docs":
                if "{{GUIDES}}" not in body:
                    raise SystemExit(f"src/{lang}/docs.html has no {{{{GUIDES}}}} placeholder")
                body = body.replace("{{GUIDES}}", docs.index(lang))
            dest = OUT / T[lang]["prefix"].lstrip("/") / slug / "index.html"
            dest.parent.mkdir(parents=True, exist_ok=True)
            dest.write_text(page(lang, slug, title, desc, body), encoding="utf-8")
        sitemap.append({"zh": url("zh", slug), "en": url("en", slug)})

    written = set()
    for lang, slug, title, body, langs, src in docs.pages():
        dest = OUT / T[lang]["prefix"].lstrip("/") / slug / "index.html"
        dest.parent.mkdir(parents=True, exist_ok=True)
        dest.write_text(doc_page(lang, slug, title, body, langs, src), encoding="utf-8")
        written.add(dest.parent)
        if lang == langs[0]:
            sitemap.append({lg: url(lg, slug) for lg in langs})
    if not SOURCE_PUBLIC and len(docs.edits_used) != len(PRELAUNCH_DOC_EDITS):
        unused = [PRELAUNCH_DOC_EDITS[k][:2] for k in range(len(PRELAUNCH_DOC_EDITS)) if k not in docs.edits_used]
        raise SystemExit(f"PRELAUNCH_DOC_EDITS not applied: {unused}")
    # Remove pages of documents that no longer exist.
    for base in (OUT / "docs", OUT / "en" / "docs"):
        for d in sorted(base.iterdir()):
            if d.is_dir() and d not in written:
                shutil.rmtree(d)
    docs.copy_images()

    # Cloudflare Pages serves the nearest 404.html up the tree.
    (OUT / "404.html").write_text(
        page("zh", "404", "找不到这个页面", "这个地址下没有页面。",
             (SRC / "zh" / "404.html").read_text(encoding="utf-8"), status404=True), encoding="utf-8")
    (OUT / "en" / "404.html").write_text(
        page("en", "404", "Page not found", "There is no page at this address.",
             (SRC / "en" / "404.html").read_text(encoding="utf-8"), status404=True), encoding="utf-8")

    sm = ['<?xml version="1.0" encoding="UTF-8"?>',
          '<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9" xmlns:xhtml="http://www.w3.org/1999/xhtml">']
    for entry in sitemap:
        for lang, path in entry.items():
            sm.append("  <url>")
            sm.append(f"    <loc>{SITE_URL}{path}</loc>")
            sm.append(f"    <lastmod>{UPDATED}</lastmod>")
            if len(entry) > 1:
                sm.append(f'    <xhtml:link rel="alternate" hreflang="zh-CN" href="{SITE_URL}{entry["zh"]}"/>')
                sm.append(f'    <xhtml:link rel="alternate" hreflang="en" href="{SITE_URL}{entry["en"]}"/>')
                sm.append(f'    <xhtml:link rel="alternate" hreflang="x-default" href="{SITE_URL}{entry["zh"]}"/>')
            sm.append("  </url>")
    sm.append("</urlset>")
    (OUT / "sitemap.xml").write_text("\n".join(sm) + "\n", encoding="utf-8")
    (OUT / "robots.txt").write_text(f"User-agent: *\nAllow: /\n\nSitemap: {SITE_URL}/sitemap.xml\n", encoding="utf-8")

    # Before launch, nothing published may point at the private repository.
    if not SOURCE_PUBLIC:
        needle = REPO.split("://", 1)[1].lower()
        leaks = sorted(str(p.relative_to(OUT)) for p in OUT.rglob("*")
                       if p.suffix in (".html", ".xml", ".txt") and needle in p.read_text(encoding="utf-8").lower())
        if leaks:
            raise SystemExit(f"SOURCE_PUBLIC is False but these files mention {needle}: {leaks}")
    pages = sum(len(e) for e in sitemap) + 2
    print(f"wrote {pages} pages to {OUT} (SOURCE_PUBLIC = {SOURCE_PUBLIC})")


if __name__ == "__main__":
    main()
