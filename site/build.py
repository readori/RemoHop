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
import json
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


# Release switch: False until the first desktop release (v1.0.0) is published on
# GitHub Releases. With False:
#   - <!--rel-->...<!--/rel--> blocks in src/*.html are dropped and
#     <!--norel-->...<!--/norel--> blocks are shown (True: the reverse);
#   - the download page names the release files but does not link them (the
#     links would be dead) and says that the first release is being prepared;
#   - RELEASE_DOC_EDITS below are not applied;
#   - the build fails if a page links into the (empty) releases.
# After the tag is published: set this to True, run
#     python3 site/build.py --release-docs
# once (it applies RELEASE_DOC_EDITS to the Markdown files in the repository, so
# README.md and docs/ say the same as the site), then run this script and commit.
RELEASED = True

# The newest published release. build.py writes it to /version.json, the file
# the desktop app reads once a day to say that a newer version exists
# (internal/update). Set it when a release is published, in the same change
# that runs --release-docs. CI compares it with the repository's newest vX.Y.Z
# tag (REMOHOP_LATEST_TAG), so a stale value fails the "website is up to
# date" check instead of leaving people on an old version.
LATEST_RELEASE = "v1.0.8"

# The mobile apps in the App Store and on Google Play, one switch per store.
# Same mechanism, with <!--ios-->...<!--/ios-->, <!--play-->...<!--/play--> and
# their <!--noios-->/<!--noplay--> opposites; <!--store--> blocks need both
# stores live and <!--nostore--> blocks show while either is not. While a switch
# is False its store address appears nowhere (the build checks it).
IOS_LIVE = True     # RemoHop 1.0.0 approved and released on the App Store
PLAY_LIVE = False   # in Google Play review
STORES_LIVE = IOS_LIVE and PLAY_LIVE
APP_STORE = "https://apps.apple.com/app/id6838609056"
GOOGLE_PLAY = "https://play.google.com/store/apps/details?id=com.remohop.app"


EMAIL = "info@remohop.com"  # general contact
SUPPORT_EMAIL = "support@remohop.com"  # support page and store support lines
PRIVACY_EMAIL = "privacy@remohop.com"  # privacy policy
TELEGRAM = "https://t.me/remohop"
UPDATED = "2026-11-26"

HERE = pathlib.Path(__file__).resolve().parent
ROOT = HERE.parent
SRC = HERE / "src"
OUT = HERE / "public"
DOCS = ROOT / "docs"

# The stylesheet is cached for a year (see _headers), so its URL carries a
# content hash. Editing site.css therefore needs a rebuild.
CSS_VERSION = hashlib.sha256((OUT / "assets" / "site.css").read_bytes()).hexdigest()[:10]

# Team edition prices, in US dollars, used by the pricing, Teams and home
# pages as {{P:name}}. Change a price here and run this script.
PRICES = {
    "user_month": "3",       # Teams, per user per month, billed yearly
    "user_year": "36",       # the same, per user per year
    "free_users": "5",       # Teams Free: up to this many users
    "free_devices": "50",    # Teams Free: up to this many devices
    "devices_per_user": "20",  # Teams: devices included per user
    "enterprise_users": "500",  # Enterprise: more than this many users
}

# slug -> (zh title, zh description, en title, en description)
PAGES = {
    "": (
        "RemoHop：给人和 AI 代理用的远程控制，权限由你划定",
        pick("RemoHop 是开源的点对点远程控制工具。内置 MCP 服务器（29 个工具），AI 代理可以在远程机器上执行命令、传文件、看屏幕，权限由被控机上限定命令、目录和时限、可要求批准和录制的授权决定。远程桌面、终端、文件、端口转发和组网都在一个 remohop 程序里，端到端加密，默认直连。",
             "RemoHop 是点对点的远程控制工具。内置 MCP 服务器（29 个工具），AI 代理可以在远程机器上执行命令、传文件、看屏幕，权限由被控机上限定命令、目录和时限、可要求批准和录制的授权决定。远程桌面、终端、文件、端口转发和组网都在一个 remohop 程序里，端到端加密，默认直连。"),
        "RemoHop: remote access for people and AI agents, within limits you set",
        pick("RemoHop is an open-source peer-to-peer remote control tool. Its built-in MCP server (29 tools) lets AI agents use remote machines under grants that limit commands, folders and time, and can require approval and recording. Remote desktop, shell, files, port forwarding and a mesh network in one remohop binary, end to end encrypted and direct by default.",
             "RemoHop is a peer-to-peer remote control tool. Its built-in MCP server (29 tools) lets AI agents use remote machines under grants that limit commands, folders and time, and can require approval and recording. Remote desktop, shell, files, port forwarding and a mesh network in one remohop binary, end to end encrypted and direct by default."),
    ),
    "features": (
        "功能与限制",
        "RemoHop 的常用命令、浏览器客户端、连接如何建立、平台支持、安装方式和已知限制。",
        "Features and limits",
        "RemoHop's common commands, the browser client, how a connection is made, platform support, installation and known limits.",
    ),
    "how-it-works": (
        "工作原理：P2P 打洞与端到端加密",
        "RemoHop 怎样把两台设备直接连起来：汇合服务器的职责，局域网、打洞、路由器端口映射、端口预测、中继和 WebSocket 桥这几条路径按什么顺序尝试，默认端口，六步连接流程，以及 QUIC、TLS 1.3 和 CPace 怎样保护会话。",
        "How it works: P2P hole punching and end-to-end encryption",
        "How RemoHop connects two devices directly: what the rendezvous server does, the order in which it tries the paths (LAN, hole punching, router port mapping, port prediction, relay, websocket bridge), the default ports, the six connection steps, and how QUIC, TLS 1.3 and CPace protect the session.",
    ),
    "docs": (
        "快速开始与文档",
        pick("从源码编译 remohop，运行汇合服务器，启动被控机，配对并连接。附 RemoHop 全部使用指南和参考文档的目录。",
             "运行汇合服务器，启动被控机，配对并连接。附 RemoHop 全部使用指南和参考文档的目录。"),
        "Quick start and documentation",
        pick("Build remohop from source, run a rendezvous server, start a host, pair and connect. With the index of the RemoHop user guide and reference documents.",
             "Run a rendezvous server, start a host, pair and connect. With the index of the RemoHop user guide and reference documents."),
    ),
    "download": (
        "下载",
        "下载 RemoHop：给被协助的人用的快速协助程序（Windows、macOS、Linux），命令行程序 remohop 的压缩包、软件包和安装脚本，包管理器，手机应用，以及如何校验下载。",
        "Download",
        "Download RemoHop: the quick-support app for the person being helped (Windows, macOS, Linux), the remohop command-line archives, packages and install scripts, package managers, the phone apps, and how to verify a download.",
    ),
    "apps": (
        "Android 与 iOS 应用",
        "RemoHop 的手机应用：在手机或平板上连接并操作你的电脑，断线自动重连；手机本身也能被远程，Android 经你允许可被控制，iPhone 和 iPad 可以共享屏幕（只能观看）。",
        "Android and iOS apps",
        "The RemoHop phone apps: connect to and operate your computers from a phone or tablet, with sessions that reconnect by themselves. The phone can be a host too: an Android phone can be controlled if you allow it, an iPhone or iPad can share its screen (view only).",
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
        "使用 RemoHop 软件、移动应用、本网站和 RemoHop 运营的服务器时适用的条款。",
        "Terms of use",
        "The terms that apply when you use the RemoHop software, the mobile apps, this website and servers operated by the RemoHop project.",
    ),
    "licensing": (
        "许可证：AGPL-3.0 与商业授权",
        pick("RemoHop 采用双许可：所有人都可按 GNU AGPL-3.0 使用；AGPL 不适合的场景可向 RemoHop 项目购买商业授权。",
             "RemoHop 采用双许可：源码将在各平台应用上架后以 GNU AGPL-3.0 公开；AGPL 不适合的场景可向 RemoHop 项目购买商业授权。"),
        "Licensing: AGPL-3.0 and commercial",
        pick("RemoHop is dual-licensed: everyone may use it under the GNU AGPL-3.0, and a commercial license is available from the RemoHop project where the AGPL does not fit.",
             "RemoHop is dual-licensed: the source will be published under the GNU AGPL-3.0 once the apps are out, and a commercial license is available from the RemoHop project where the AGPL does not fit."),
    ),
    "teams": (
        "RemoHop 团队版：团队的策略、审批与审计",
        "RemoHop 团队版是自托管的商业版：人员与设备分组，按策略授权，OIDC 或 SAML 登录、SCIM 同步，管理员审批，集中审计和录像，团队成员与策略记在由管理员密钥签名的日志里，团队服务器无法伪造。",
        "RemoHop Teams: policies, approvals and audit for a team",
        "RemoHop Teams is the self-hosted commercial edition: people and devices in groups, policies that become grants on each host, OIDC or SAML sign-in, SCIM provisioning, admin approvals, central audit and recordings, and a membership log signed by admin keys that the team server cannot forge.",
    ),
    "pricing": (
        "价格",
        f"RemoHop 的价格：开源版任何用途都免费；团队版 {PRICES['free_users']} 人以内免费，之后每用户每月 {PRICES['user_month']} 美元（按年付费），自托管，离线许可证文件；超过 {PRICES['enterprise_users']} 人请联系我们。",
        "Pricing",
        f"RemoHop pricing: the open-source edition is free for any use; the team edition is free for up to {PRICES['free_users']} users, then ${PRICES['user_month']} per user per month billed yearly, self-hosted with an offline license file; more than {PRICES['enterprise_users']} users, contact us.",
    ),
    "support": (
        "支持与联系",
        "通过 support@remohop.com 联系 RemoHop：报告问题时需要附带的信息、排障文档入口，以及如何私下报告安全问题。",
        "Support and contact",
        "Contact RemoHop at support@remohop.com: what to include in a report, where the troubleshooting guide is, and how to report a security issue privately.",
    ),
}

# Header navigation: (slug, zh, en). Apps and Self-hosting are in the footer.
NAV = [
    ("features", "功能", "Features"),
    ("how-it-works", "工作原理", "How it works"),
    ("security", "安全", "Security"),
    ("teams", "团队版", "Teams"),
    ("pricing", "价格", "Pricing"),
    ("docs", "文档", "Docs"),
    ("download", "下载", "Download"),
    ("support", "支持", "Support"),
]

# Footer columns, by T key of the heading: (slug, zh, en). "#src" is the
# repository link, shown only when SOURCE_PUBLIC.
FOOT = [
    ("f_product", [
        ("features", "功能", "Features"),
        ("how-it-works", "工作原理", "How it works"),
        ("security", "安全", "Security"),
        ("download", "下载", "Download"),
        ("apps", "移动应用", "Apps"),
        ("self-hosting", "自建服务器", "Self-hosting"),
        ("docs", "文档", "Docs"),
        ("#src", "源码（GitHub）", "Source (GitHub)"),
    ]),
    ("f_editions", [
        ("pricing", "价格", "Pricing"),
        ("teams", "RemoHop 团队版", "RemoHop Teams"),
        ("licensing", "许可证", "Licensing"),
    ]),
    ("f_legal", [
        ("privacy", "隐私政策", "Privacy policy"),
        ("terms", "使用条款", "Terms of use"),
    ]),
]

T = {
    "zh": {
        "lang": "zh-CN", "prefix": "", "skip": "跳到正文", "nav": "主导航", "home": "RemoHop 首页",
        "other": ("English", "en", "/en"), "foot_nav": "页脚",
        "f_product": "产品", "f_editions": "版本", "f_legal": "条款", "f_contact": "联系",
        "legal": pick("RemoHop 是独立的开源项目，以 GNU AGPL-3.0 开源，另提供商业授权。",
                      "RemoHop 是独立的开源项目。源码将在各平台应用上架后以 GNU AGPL-3.0 公开，另提供商业授权。")
        + "本网站不使用 Cookie、分析脚本或任何第三方资源。",
        "docs": "文档", "doc_src": "源文件（GitHub）",
        "sitename": "RemoHop", "locale": "zh_CN",
    },
    "en": {
        "lang": "en", "prefix": "/en", "skip": "Skip to content", "nav": "Primary", "home": "RemoHop home",
        "other": ("中文", "zh-CN", ""), "foot_nav": "Footer",
        "f_product": "Product", "f_editions": "Editions", "f_legal": "Legal", "f_contact": "Contact",
        "legal": pick("RemoHop is an independent open-source project, published under the GNU AGPL-3.0, with a commercial license available.",
                      "RemoHop is an independent open-source project. The source will be published under the GNU AGPL-3.0 once the apps are out; a commercial license is available.")
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



# ---- download page ----
#
# /download/ and /en/download/: src/<lang>/download.html with the placeholders
# {{DL:quick}}, {{DL:cli}}, {{DL:channels}} and {{DL:mobile}}, filled from the
# data below. Release asset names carry no version (packaging/README.md, "What a
# release contains"), so .../releases/latest/download/NAME always is the newest.
DOWNLOAD = REPO + "/releases/latest/download/"

# The double-click programs, one row per system: (platform, CPU, the desktop
# app's file, quick support's file); zh and en texts as pairs. Both are the
# same signed program under two names (cmd/remohop-quick/launch.go). The page
# shows only the buttons; how to open each one is in docs/*/install.md and in
# the app.
APP_ASSETS = [
    (("Windows 10、11", "Windows 10, 11"), "x64", "RemoHop-windows-amd64.zip", "RemoHop-QuickSupport-windows-amd64.zip"),
    (("Windows 11", "Windows 11"), "ARM64", "RemoHop-windows-arm64.zip", "RemoHop-QuickSupport-windows-arm64.zip"),
    (("macOS 12 及以上", "macOS 12 or later"), ("Apple 芯片和 Intel", "Apple silicon and Intel"), "RemoHop-macos.dmg", "RemoHop-QuickSupport-macos.zip"),
    (("Linux", "Linux"), "x86-64", "remohop-app_linux_amd64.tar.gz", "remohop-quick_linux_amd64.tar.gz"),
    (("Linux", "Linux"), "ARM64", "remohop-app_linux_arm64.tar.gz", "remohop-quick_linux_arm64.tar.gz"),
]
# The command-line archives: (platform, CPU, file). No notes under the files:
# the site carries no fine print (site/README.md, "No fine print").
CLI_ASSETS = [
    (("Linux", "Linux"), "x86-64", "remohop_linux_amd64.tar.gz"),
    (("Linux", "Linux"), "ARM64", "remohop_linux_arm64.tar.gz"),
    (("macOS", "macOS"), ("通用（Apple 芯片和 Intel）", "universal (Apple silicon and Intel)"), "remohop_darwin_universal.tar.gz"),
    (("Windows", "Windows"), "x64", "remohop_windows_amd64.zip"),
    (("Windows", "Windows"), "ARM64", "remohop_windows_arm64.zip"),
]

# Package managers, stores and images. Each entry is one channel; set "live" to
# True once it is published (packaging/README.md, "Package-manager manifests"),
# and run this script. Until then the page shows "coming soon" instead of the
# command. "url" (optional) is the channel's page, linked when live.
CHANNELS = [
    {"name": "Homebrew", "live": True, "os": "macOS · Linux", "what": ("命令行程序", "command line"),
     "cmd": "brew install readori/tap/remohop"},
    {"name": "Homebrew Cask", "live": True, "os": "macOS", "what": ("RemoHop 应用", "the RemoHop app"),
     "cmd": "brew install --cask readori/tap/remohop-quick"},
    {"name": "Scoop", "live": True, "os": "Windows", "what": ("命令行程序", "command line"),
     "cmd": "scoop bucket add readori https://github.com/readori/scoop-bucket\nscoop install remohop"},
    {"name": "winget", "live": False, "os": "Windows", "what": ("命令行程序", "command line"),
     "cmd": "winget install RemoHop.RemoHop"},
    {"name": "Microsoft Store", "live": False, "os": "Windows 10, 11", "what": ("快速协助", "quick support"),
     "url": "", "store": ("在 Microsoft Store 里搜索 RemoHop。", "Search for RemoHop in the Microsoft Store.")},
    {"name": "AUR", "live": False, "os": "Arch Linux", "what": ("命令行程序", "command line"),
     "cmd": "yay -S remohop-bin"},
    {"name": "Docker", "live": True, "os": "linux/amd64 · linux/arm64", "what": ("汇合服务器", "rendezvous server"),
     "cmd": "docker run -d --name remohop-signal --restart unless-stopped \\\n"
            "  -p 21116:21116/udp -p 21117:21117/udp -v remohop-signal:/data \\\n"
            "  ghcr.io/readori/remohop-signal:latest"},
]

DL_TEXT = {
    "zh": {"platform": "系统", "file": "文件", "app": "桌面应用", "quick": "快速协助（免安装）", "soon": "即将推出", "live": "可用",
           "unreleased": "发布后可下载", "store_soon": "即将上架", "open": "打开"},
    "en": {"platform": "System", "file": "File", "app": "Desktop app", "quick": "Quick Support (no install)", "soon": "Coming soon", "live": "Available",
           "unreleased": "available after the release", "store_soon": "Coming soon", "open": "Open"},
}


def asset_table(lang, rows, label):
    t = DL_TEXT[lang]
    i = 0 if lang == "zh" else 1
    out = [f'<div class="table-wrap dl" tabindex="0" role="region" aria-label="{html.escape(label)}">', "<table>",
           f'<thead><tr><th scope="col">{t["platform"]}</th><th scope="col">{t["file"]}</th></tr></thead>', "<tbody>"]
    for plat, cpu, name in rows:
        cpu = cpu[i] if isinstance(cpu, tuple) else cpu
        f = f'<a href="{DOWNLOAD}{name}"><code>{name}</code></a>' if RELEASED else f"<code>{name}</code>"
        out.append(f'<tr><td>{plat[i]}<br><span class="small">{cpu}</span></td><td class="file">{f}</td></tr>')
    out += ["</tbody>", "</table>", "</div>"]
    return "\n".join(out)


def app_table(lang):
    """The two double-click downloads per system, as buttons."""
    t = DL_TEXT[lang]
    i = 0 if lang == "zh" else 1
    label = "RemoHop 下载" if i == 0 else "RemoHop downloads"
    out = [f'<div class="table-wrap dl" tabindex="0" role="region" aria-label="{label}">', "<table>",
           f'<thead><tr><th scope="col">{t["platform"]}</th><th scope="col">{t["app"]}</th><th scope="col">{t["quick"]}</th></tr></thead>', "<tbody>"]

    def cell(name, text, primary):
        cls = "btn primary" if primary else "btn"
        b = f'<a class="{cls}" href="{DOWNLOAD}{name}" title="{name}">{text}</a>' if RELEASED else f'<span class="{cls}">{text}</span>'
        return f'<td class="file">{b}</td>'
    for plat, cpu, app, quick in APP_ASSETS:
        cpu = cpu[i] if isinstance(cpu, tuple) else cpu
        out.append(f'<tr><td>{plat[i]}<br><span class="small">{cpu}</span></td>'
                   + cell(app, "RemoHop", True) + cell(quick, t["quick"], False) + "</tr>")
    out += ["</tbody>", "</table>", "</div>"]
    return "\n".join(out)


def channel_cards(lang):
    t = DL_TEXT[lang]
    i = 0 if lang == "zh" else 1
    out = ['<div class="cards">']
    for c in CHANNELS:
        live = c["live"] and RELEASED
        part = [f'<article class="panel">', f'<p class="chip">{t["live"]}</p>' if live else f'<p class="chip wait">{t["soon"]}</p>',
                f'<h3>{html.escape(c["name"])}</h3>', f'<p class="platforms">{html.escape(c["os"])} · {c["what"][i]}</p>']
        if live and c.get("cmd"):
            part.append(f'<pre><code>{html.escape(c["cmd"])}</code></pre>')
        elif live and c.get("store"):
            part.append(f"<p>{c['store'][i]}</p>")
            if c.get("url"):
                part.append(f'<p class="links"><a href="{html.escape(c["url"])}">{t["open"]}</a></p>')
        part.append("</article>")
        out.append("\n".join(part))
    out.append("</div>")
    return "\n".join(out)


def download_parts(lang):
    zh = lang == "zh"
    return {
        "quick": app_table(lang),
        "cli": asset_table(lang, CLI_ASSETS, "命令行程序下载" if zh else "Command-line downloads"),
        "channels": channel_cards(lang),
    }



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

# Statements in the repository's Markdown that change at release time:
# (switch, file, text now, text after). switch is "rel" (RELEASED) or "store"
# (STORES_LIVE), "ios" (IOS_LIVE) or "play" (PLAY_LIVE). When the switch is on, the site renders docs/ with the new
# text, and `python3 site/build.py --release-docs` writes it into the files
# (README.md and README.zh-CN.md are not on the site, so only that command
# changes them). Each old text must occur exactly once, unless the new text is
# already there (the command was run), so a document that changes under a rule
# fails the build instead of keeping a stale statement.
RELEASE_DOC_EDITS = [
    ("rel", "README.md", "- No release has been published yet; build from source. Windows binaries are not code-signed yet.",
     "- Releases are on [GitHub Releases](https://github.com/readori/RemoHop/releases), with every download listed at [remohop.com/en/download](https://remohop.com/en/download/); see [Install](docs/en/install.md). Windows binaries are not code-signed yet."),
    ("rel", "README.zh-CN.md", "- 目前还没有发布版本，请从源码编译。Windows 二进制尚未做代码签名。",
     "- 发布版本在 [GitHub Releases](https://github.com/readori/RemoHop/releases)，全部下载见 [remohop.com/download](https://remohop.com/download/)；安装方法见[安装](docs/zh/install.md)。Windows 二进制尚未做代码签名。"),
    ("ios", "README.md", "| Mobile | Android and iOS controller apps (no releases yet) |",
     "| Mobile | Android and iOS controller apps ([App Store](https://apps.apple.com/app/id6838609056); Google Play coming soon) |"),
    ("play", "README.md", "([App Store](https://apps.apple.com/app/id6838609056); Google Play coming soon)",
     "([App Store](https://apps.apple.com/app/id6838609056), [Google Play](https://play.google.com/store/apps/details?id=com.remohop.app))"),
    ("ios", "README.zh-CN.md", "| 移动端 | Android 和 iOS 控制端应用（尚无发布版本） |",
     "| 移动端 | Android 和 iOS 控制端应用（[App Store](https://apps.apple.com/app/id6838609056)；Google Play 即将上架） |"),
    ("play", "README.zh-CN.md", "（[App Store](https://apps.apple.com/app/id6838609056)；Google Play 即将上架）",
     "（[App Store](https://apps.apple.com/app/id6838609056)、[Google Play](https://play.google.com/store/apps/details?id=com.remohop.app)）"),
    ("rel", "docs/en/install.md",
     "Releases are built by the release workflow on every `v*` tag. No release has been published yet, so the download commands below work only from the first release; until then, [build from source](#from-source).",
     "Releases are built by the release workflow on every `v*` tag and published on [GitHub Releases](https://github.com/readori/RemoHop/releases); the commands below always fetch the latest one. You can also [build from source](#from-source)."),
    ("rel", "docs/zh/install.md",
     "每次推送 `v*` 标签，发布工作流都会构建发布版本。目前还没有发布过任何版本，下面的下载命令要等首个发布版本之后才能用；在此之前请[从源码编译](#从源码编译)。",
     "每次推送 `v*` 标签，发布工作流都会构建发布版本并发布到 [GitHub Releases](https://github.com/readori/RemoHop/releases)；下面的命令总是下载最新版本。也可以[从源码编译](#从源码编译)。"),
    ("rel", "docs/en/platforms.md", "The `v*` tag release workflow has not run yet, no release has been published, and Windows binaries are unsigned.",
     "Releases are published on GitHub Releases by the `v*` tag workflow; the macOS binary and `RemoHop.app` are signed and notarized, Windows binaries are unsigned."),
    ("rel", "docs/zh/platforms.md", "推送 `v*` 标签触发的发布工作流还没有运行过，尚无已发布的版本，Windows 二进制也未签名。",
     "发布版本由推送 `v*` 标签触发的发布工作流发布到 GitHub Releases；macOS 二进制和 `RemoHop.app` 已签名并经过公证，Windows 二进制未签名。"),
    ("rel", "docs/security.md", "- There are no published releases yet. Windows binaries are not code-signed; the release workflow signs and notarizes the macOS binary.",
     "- Windows binaries are not code-signed; the release workflow signs and notarizes the macOS binary and `RemoHop.app`."),
    ("rel", "docs/en/quick-support.md", "`RemoHop.exe` and `RemoHop.app` are the same program as `remohop`;",
     "Every release has them as downloads (`RemoHop-QuickSupport-windows-amd64.zip` and `-arm64.zip`, `RemoHop-QuickSupport-macos.zip`, `remohop-quick_linux_*.tar.gz`; see [Install](install.md#quick-support)). `RemoHop.exe` and `RemoHop.app` are the same program as `remohop`;"),
    ("rel", "docs/zh/quick-support.md", "`RemoHop.exe` 和 `RemoHop.app` 与 `remohop` 是同一个程序；",
     "每个发布版本都提供下载（`RemoHop-QuickSupport-windows-amd64.zip` 和 `-arm64.zip`、`RemoHop-QuickSupport-macos.zip`、`remohop-quick_linux_*.tar.gz`，见[安装](install.md#快速协助)）。`RemoHop.exe` 和 `RemoHop.app` 与 `remohop` 是同一个程序；"),
    ("rel", "docs/en/quick-support.md", "`RemoHop.app` built here is unsigned;",
     "The `RemoHop-macos.dmg` download is signed with a Developer ID, notarized and stapled; a `RemoHop.app` you build yourself is unsigned;"),
    ("rel", "docs/zh/quick-support.md", "这里构建出的 `RemoHop.app` 没有签名；",
     "下载的 `RemoHop-macos.dmg` 已用 Developer ID 签名、经过公证并附带公证票据；自己构建的 `RemoHop.app` 没有签名；"),
    ("ios", "docs/en/mobile-apps.md", "- Both apps have been submitted for review and are coming to the App Store and Google Play; until they are approved, build them yourself.",
     "- Get the iOS app from the [App Store](https://apps.apple.com/app/id6838609056). The Android app has been submitted to Google Play for review; until it is approved, build it yourself."),
    ("play", "docs/en/mobile-apps.md", "- Get the iOS app from the [App Store](https://apps.apple.com/app/id6838609056). The Android app has been submitted to Google Play for review; until it is approved, build it yourself.",
     "- Get them from the [App Store](https://apps.apple.com/app/id6838609056) and [Google Play](https://play.google.com/store/apps/details?id=com.remohop.app), or build them yourself."),
    ("ios", "docs/zh/mobile-apps.md", "- 两个应用都已提交审核，即将上架 App Store 和 Google Play；审核通过之前，需要自己构建。",
     "- iOS 应用可以从 [App Store](https://apps.apple.com/app/id6838609056) 安装。Android 应用已提交 Google Play 审核，审核通过之前需要自己构建。"),
    ("play", "docs/zh/mobile-apps.md", "- iOS 应用可以从 [App Store](https://apps.apple.com/app/id6838609056) 安装。Android 应用已提交 Google Play 审核，审核通过之前需要自己构建。",
     "- 可以从 [App Store](https://apps.apple.com/app/id6838609056) 和 [Google Play](https://play.google.com/store/apps/details?id=com.remohop.app) 安装，也可以自己构建。"),
]


def superseded(k, text, only_on=True):
    """Whether rule k's result was rewritten by a later rule of the same file
    (a chain such as App Store first, then Google Play) that is applied."""
    _, f, _, new = RELEASE_DOC_EDITS[k]
    for switch, f2, old2, new2 in RELEASE_DOC_EDITS[k + 1:]:
        if f2 == f and old2 in new and (not only_on or gates()[switch]):
            return new2 in text or superseded(RELEASE_DOC_EDITS.index((switch, f2, old2, new2)), text, only_on)
    return False


def release_edit(text, path, only_on=True):
    """Applies the RELEASE_DOC_EDITS of one file whose switch is on."""
    for k, (switch, f, old, new) in enumerate(RELEASE_DOC_EDITS):
        if f != path or (only_on and not gates()[switch]):
            continue
        if new in text:  # already applied (the new text may contain the old one)
            continue
        if old not in text and superseded(k, text, only_on):
            continue  # a later step (e.g. "play" after "ios") already rewrote this one's text
        n = text.count(old)
        if n == 1:
            text = text.replace(old, new)
        else:
            raise SystemExit(f"RELEASE_DOC_EDITS: {f} contains the text {n} times: {old[:70]!r}")
    return text


def release_docs():
    """--release-docs: writes the switched-on RELEASE_DOC_EDITS into the files."""
    changed = []
    for f in sorted({e[1] for e in RELEASE_DOC_EDITS if gates()[e[0]]}):
        p = ROOT / f
        old = p.read_text(encoding="utf-8")
        new = release_edit(old, f)
        if new != old:
            p.write_text(new, encoding="utf-8")
            changed.append(f)
    print("--release-docs: " + (", ".join(changed) if changed else "nothing to change")
          + f" (RELEASED = {RELEASED}, STORES_LIVE = {STORES_LIVE})")


# The first line of each guide page links to the other language; the site
# header already has that switch.
LANG_LINE = re.compile(r"^\[(?:中文|English)\]\([^)]*\)(?: · \[[^\]]*\]\([^)]*\))*[ \t]*\n", re.M)


def url(lang, slug):
    p = T[lang]["prefix"]
    return f"{p}/{slug}/" if slug else f"{p}/"


def gates():
    """Marker name -> whether its blocks are shown (the "no" name gets the opposite)."""
    return {"src": SOURCE_PUBLIC, "rel": RELEASED, "store": STORES_LIVE, "ios": IOS_LIVE, "play": PLAY_LIVE}


def gate(text):
    """Keeps or drops <!--X-->…<!--/X--> and <!--noX-->…<!--/noX--> blocks for
    each switch in gates(): src (SOURCE_PUBLIC), rel (RELEASED), store
    (STORES_LIVE). A block that fills whole lines takes its line break with it.
    Blocks of one kind do not nest; blocks of different kinds may."""
    for base, on in gates().items():
        for name in (base, "no" + base):
            if text.count(f"<!--{name}-->") != text.count(f"<!--/{name}-->"):
                raise SystemExit(f"unbalanced <!--{name}--> markers")
        keep, drop = (base, "no" + base) if on else ("no" + base, base)
        inner = rf"(?:(?!<!--/?{drop}-->).)*"
        text = re.sub(rf"^[ \t]*<!--{drop}-->{inner}<!--/{drop}-->[ \t]*\n", "", text, flags=re.S | re.M)
        text = re.sub(rf"<!--{drop}-->{inner}<!--/{drop}-->", "", text, flags=re.S)
        text = re.sub(rf"^[ \t]*<!--/?{keep}-->[ \t]*\n", "", text, flags=re.M)
        text = re.sub(rf"<!--/?{keep}-->", "", text)
    return text


def fill(text, lang):
    text = gate(text)
    text = text.replace("{{REPO}}", REPO).replace("{{SUPPORT_EMAIL}}", SUPPORT_EMAIL).replace("{{PRIVACY_EMAIL}}", PRIVACY_EMAIL).replace("{{EMAIL}}", EMAIL).replace("{{TELEGRAM}}", TELEGRAM).replace("{{UPDATED}}", UPDATED)
    def price(m):
        if m.group(1) not in PRICES:
            raise SystemExit(f"unknown price {{{{P:{m.group(1)}}}}}")
        return PRICES[m.group(1)]
    text = re.sub(r"\{\{P:([a-z_]+)\}\}", price, text)
    text = text.replace("{{SITE}}", SITE_URL).replace("{{APP_STORE}}", APP_STORE).replace("{{GOOGLE_PLAY}}", GOOGLE_PLAY)
    # {{L:slug}} is the page in this language, {{E:slug}} the English one
    # (for the reference documents, which exist in English only).
    text = re.sub(r"\{\{L:([a-z0-9/-]*)\}\}", lambda m: url(lang, m.group(1)), text)
    text = re.sub(r"\{\{E:([a-z0-9/-]*)\}\}", lambda m: url("en", m.group(1)), text)
    # Notes for the maintainers stay in the sources, not in the published pages.
    text = re.sub(r"\s*<!--\s*TODO\(owner\).*?-->", "", text, flags=re.S)
    # Scrollable code blocks must be reachable from the keyboard.
    return text.replace("<pre>", '<pre tabindex="0">')


# ---- typesetting ----
#
# A few spaces must not become line breaks: between a number and its unit
# ("120 fps", "8 Mbit/s", "60 秒"), between a product and its version
# ("macOS 12", "TLS 1.3"), and, in Chinese, around 和/或/与/及 between two
# Latin names ("iPhone 和 iPad"), which otherwise often splits after the
# conjunction. typeset() turns those spaces into no-break spaces in the text
# of a page, never inside tags, <pre>, <code>, <svg>, <script> or <style>.
UNITS = (r"%|ms|s|fps|Hz|kHz|Mbit/s|kbit/s|Gbit/s|MB|KB|GB|MiB|KiB|GiB|px|bits?|bytes?|"
         r"seconds?|minutes?|hours?|days?|weeks?|months?|pixels|packets|毫秒|秒|分钟|小时|天|个|位|字节|像素|比特|核")
NUM_UNIT = re.compile(r"(\d) (?=(?:" + UNITS + r")(?![A-Za-z0-9/]))")
PRODUCT_VERSION = re.compile(r"\b(Windows|macOS|iOS|iPadOS|Android|TLS|Go|Chromium|JDK|API|Ubuntu|Debian|RemoHop|ffmpeg) (?=\d)")
NAME_PAIR = re.compile(r"([A-Za-z][A-Za-z0-9.+-]*) (和|或|与|及) (?=[A-Za-z])")
RAW_TAGS = ("pre", "code", "svg", "script", "style")


def typeset(text, lang):
    out, depth = [], 0
    for part in re.split(r"(<[^>]*>)", text):
        if part.startswith("<"):
            m = re.match(r"<(/?)([a-zA-Z0-9]+)", part)
            if m and m.group(2).lower() in RAW_TAGS and not part.endswith("/>"):
                depth += -1 if m.group(1) else 1
            out.append(part)
            continue
        if depth == 0 and part:
            part = NUM_UNIT.sub("\\1&nbsp;", part)
            part = PRODUCT_VERSION.sub("\\1&nbsp;", part)
            if lang == "zh":
                part = NAME_PAIR.sub("\\1&nbsp;\\2&nbsp;", part)
        out.append(part)
    if depth:
        raise SystemExit("typeset: unbalanced <pre>/<code>/<svg> tags")
    return "".join(out)


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
            f'<meta property="og:image" content="{SITE_URL}/assets/img/web-desktop-v2-1280.png">',
            '<meta property="og:image:width" content="1280">',
            '<meta property="og:image:height" content="1006">',
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
    content = typeset((body if raw else fill(body, lang)).strip(), lang)
    if not is_home:
        content = f'<div class="wrap {cls}">\n{content}\n</div>'

    def foot_link(s, zh, en):
        name = zh if lang == "zh" else en
        if s == "#src":
            return f'<li><a href="{REPO}">{name}</a></li>' if SOURCE_PUBLIC else ""
        return f'<li><a href="{url(lang, s)}">{name}</a></li>'
    foot_cols = []
    for key, links in FOOT:
        items = "\n".join(x for x in (foot_link(*l) for l in links) if x)
        foot_cols.append(f'<div>\n<h2>{t[key]}</h2>\n<ul>\n{items}\n</ul>\n</div>')
    support = next(zh if lang == "zh" else en for s, zh, en in NAV if s == "support")

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
{chr(10).join(foot_cols)}
<div>
<h2>{t["f_contact"]}</h2>
<ul>
<li><a href="{url(lang, "support")}">{support}</a></li>
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
        text = release_edit((DOCS / f"{rel}.md").read_text(encoding="utf-8"), f"docs/{rel}.md")
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
        if target in ("README.md", "README.zh-CN.md") and frag in ("许可证", "license"):
            return url("zh" if target == "README.zh-CN.md" else "en", "licensing")
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
            if slug == "download":
                for key, part in download_parts(lang).items():
                    if f"{{{{DL:{key}}}}}" not in body:
                        raise SystemExit(f"src/{lang}/download.html has no {{{{DL:{key}}}}} placeholder")
                    body = body.replace(f"{{{{DL:{key}}}}}", part)
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
    if not re.fullmatch(r"v\d+\.\d+\.\d+", LATEST_RELEASE):
        raise SystemExit(f"LATEST_RELEASE {LATEST_RELEASE!r} is not vX.Y.Z")
    tag = os.environ.get("REMOHOP_LATEST_TAG", "")
    if tag and tag != LATEST_RELEASE:
        raise SystemExit(f"LATEST_RELEASE is {LATEST_RELEASE} but the newest release tag is {tag}: set it and rebuild")
    (OUT / "version.json").write_text(json.dumps({"version": LATEST_RELEASE}) + "\n", encoding="utf-8")
    (OUT / "robots.txt").write_text(f"User-agent: *\nAllow: /\n\nSitemap: {SITE_URL}/sitemap.xml\n", encoding="utf-8")

    # Before launch, nothing published may point at the private repository.
    if not SOURCE_PUBLIC:
        needle = REPO.split("://", 1)[1].lower()
        leaks = sorted(str(p.relative_to(OUT)) for p in OUT.rglob("*")
                       if p.suffix in (".html", ".xml", ".txt") and needle in p.read_text(encoding="utf-8").lower())
        if leaks:
            raise SystemExit(f"SOURCE_PUBLIC is False but these files mention {needle}: {leaks}")
    # Nothing may announce what is not out yet: no link into the releases before
    # RELEASED, no store address before STORES_LIVE, and no gate marker left over.
    generated = {p: p.read_text(encoding="utf-8") for p in OUT.rglob("*") if p.suffix in (".html", ".xml", ".txt")}
    bad = []
    for p, text in generated.items():
        name = str(p.relative_to(OUT))
        if not RELEASED and re.search(r'href="' + re.escape(REPO) + r'/releases', text):
            bad.append(f"{name}: links to the releases but RELEASED is False")
        if not IOS_LIVE and APP_STORE in text:
            bad.append(f"{name}: names the App Store address but IOS_LIVE is False")
        if not PLAY_LIVE and GOOGLE_PLAY in text:
            bad.append(f"{name}: names the Google Play address but PLAY_LIVE is False")
        if re.search(r"<!--/?(no)?(src|rel|store|ios|play)-->", text):
            bad.append(f"{name}: a gate marker is left over")
    if bad:
        raise SystemExit("\n".join(bad))
    pages = sum(len(e) for e in sitemap) + 2
    print(f"wrote {pages} pages to {OUT} (SOURCE_PUBLIC = {SOURCE_PUBLIC}, RELEASED = {RELEASED}, IOS_LIVE = {IOS_LIVE}, PLAY_LIVE = {PLAY_LIVE})")


if __name__ == "__main__":
    if sys.argv[1:] == ["--release-docs"]:
        release_docs()
    elif sys.argv[1:]:
        raise SystemExit("usage: build.py [--release-docs]")
    else:
        main()
