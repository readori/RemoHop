# App Store Connect 填写资料（iOS）

本文档列出 RemoHop iOS 应用（`com.remohop.app`，`mobile/ios`）在 App Store Connect 里需要填写的全部内容，尽量做到可以直接粘贴。内容按当前代码整理；代码中的权限、数据流或加密发生变化时，要同步更新本文档。

标有 **【提交前确认】** 的项目需要发布者在提交前决定或补充，汇总在[最后一节](#提交前需要确认的事项)。法律和出口管制方面的内容是常见做法的整理，不是法律意见。

网站地址统一用 `https://remohop.com`（域名注册并上线网站之前，这些地址还打不开）。

商店文案和元数据由 fastlane（deliver）从 `mobile/ios/fastlane/metadata/` 上传，那里的文件目前是 `TODO-REPLACE` 占位内容，提交前用本文档的文案替换。App 隐私问卷无法通过 API 提交，在浏览器中填写的步骤见 [`mobile/ios/fastlane/app-privacy-checklist.md`](../mobile/ios/fastlane/app-privacy-checklist.md)。

## 1. 基本信息

| 项目 | 值 | 说明 |
|---|---|---|
| Bundle ID | `com.remohop.app` | 与 Xcode 工程和 CI 一致；Android 的 applicationId 相同。旧的 `com.readori.sodesk` 记录不再使用 |
| SKU | `remohop-ios` | 自定，仅内部可见；App Store 记录创建后不能修改 |
| 主要语言 | English (U.S.) | 应用的开发语言是英文，主语言与之一致 |
| 其他本地化 | 简体中文 | 商店页面和应用界面都有简体中文：应用在“设置 > 语言”里可选“跟随系统 / 简体中文 / English”，权限说明由 `zh-Hans.lproj/InfoPlist.strings` 翻译，`CFBundleLocalizations` 声明了 `en` 和 `zh-Hans`，App Store 会列出这两种语言 |
| 版本号 | `0.1.0` | 工程里的 `MARKETING_VERSION`。也可以在上架时改为 `1.0.0` |
| 版权 | `© 2026 Readori` | **【提交前确认】** 如需写法律实体全称请替换 |
| 主要类别 | Utilities（工具） | 与工程里的 `LSApplicationCategoryType = public.app-category.utilities` 一致 |
| 次要类别 | Developer Tools（开发者工具） | 可选；也可以选 Productivity（效率） |
| 价格 | 免费 | 无内购、无订阅、无广告 |
| 设备 | iPhone、iPad | `TARGETED_DEVICE_FAMILY = 1,2`，最低 iOS 16.0 |
| 许可协议 | Apple 标准 EULA | 不必上传自定义 EULA |
| 面向儿童 | 否 | |

## 2. 名称与文案

长度限制：名称 30、副标题 30、宣传文本 170、关键词 100、描述 4000（均按字符计）。下面每项都在限制之内，字符数写在括号里。

### English (U.S.)

**Name**（7）

```
RemoHop
```

如果 `RemoHop` 已被占用，备选：`RemoHop Remote`（14）。**【提交前确认】** 名称是否可用要在 App Store Connect 里创建应用时才知道。

**Subtitle**（29）

```
P2P remote desktop & terminal
```

**Promotional Text**（145）

```
Control your own computers from your phone: screen, terminal, files and clipboard over an end-to-end encrypted, peer-to-peer session. No account.
```

**Description**

```
RemoHop lets you operate your own computers from an iPhone or iPad. It is the controller app for RemoHop, an open-source remote control project.

On the computer you run the RemoHop host (the "remohop" command-line program, for Linux, macOS and Windows). It shows a 9-digit ID and a temporary password. In the app you enter the ID, or the computer's address, and the password. After pairing once, you connect without a password.

WHAT YOU CAN DO
• Remote desktop: see and control the computer's screen, with full screen, quality presets and a monitor switch.
• Terminal: persistent shell sessions that keep running on the computer when you disconnect, with helper keys for Esc, Tab, Ctrl and arrows.
• Files: browse, upload, download, rename and delete; upload photos from your library or camera.
• Clipboard: send text between the phone and the computer.
• System information and screenshots.

HOW IT CONNECTS
• Sessions are end-to-end encrypted (QUIC with TLS 1.3) and go directly between your phone and your computer whenever the network allows it.
• Login is a password-authenticated key exchange. The password is never sent over the network.
• The computer's identity key is pinned on first login, and the app refuses a different key later.
• No RemoHop account is needed, and the app has no sign-up.

YOUR DATA
• The app contains no analytics, ads or tracking.
• Your identity key, pairings and settings stay in the app's private storage on your device.
• RemoHop has no built-in server. To find a computer by its ID, you enter the address of a rendezvous server that you or your organisation run. On the same network you can connect by address with no server at all.

WHAT YOU NEED
• A computer running the RemoHop host. See the website for how to get and run it.
• For remote desktop, the computer needs ffmpeg with an H.264 or AV1 encoder.

GOOD TO KNOW
• This app is a controller. It cannot make your iPhone or iPad controllable.
• The app must stay in the foreground during a session.
• The app is available in English and Simplified Chinese; choose the language in Settings.

Support: admin@remohop.com
```

**Keywords**（97）

```
remote desktop,remote control,terminal,shell,ssh,p2p,file transfer,screen,self-hosted,quic,server
```

没有使用竞品名称或他人商标（App Review 指南 2.3.7）。

**What's New（首个版本）**

```
First release.
```

### 简体中文

**名称**（7）

```
RemoHop
```

**副标题**（10）

```
点对点远程桌面与终端
```

**宣传文本**（46）

```
用手机操作你自己的电脑：屏幕、终端、文件和剪贴板，走端到端加密的点对点会话，不需要注册账号。
```

**描述**

```
RemoHop 让你在 iPhone 或 iPad 上操作自己的电脑。它是开源远程控制项目 RemoHop 的控制端应用。

在电脑上运行 RemoHop 被控端（命令行程序 remohop，支持 Linux、macOS 和 Windows），它会显示一个 9 位 ID 和一个临时密码。在应用里输入 ID（或电脑的地址）和密码即可连接。配对一次之后，再连接不需要密码。

能做什么
• 远程桌面：查看并控制电脑屏幕，支持全屏、画质预设和切换显示器。
• 终端：持久的 shell 会话，断开后仍在电脑上运行；带 Esc、Tab、Ctrl 和方向键等辅助按键。
• 文件：浏览、上传、下载、重命名和删除；可以上传相册里的照片或现拍的照片。
• 剪贴板：在手机和电脑之间传文本。
• 系统信息和截图。

怎么连接
• 会话端到端加密（QUIC，内层 TLS 1.3），网络允许时在手机和电脑之间直连。
• 登录使用密码认证密钥交换，密码本身不会在网络上传输。
• 首次登录时固定电脑的身份密钥，之后密钥不符就拒绝连接。
• 不需要 RemoHop 账号，应用里没有注册流程。

你的数据
• 应用不含统计分析、广告或跟踪。
• 身份密钥、配对信息和设置只保存在本机的应用私有存储里。
• RemoHop 没有内置服务器。要用 ID 找到电脑，需要填写一台由你或你的组织运行的汇合服务器；在同一个网络里可以直接用地址连接，不需要任何服务器。

使用前提
• 一台运行 RemoHop 被控端的电脑。获取和运行方法见官网。
• 远程桌面需要电脑上装有带 H.264 或 AV1 编码器的 ffmpeg。

需要知道的
• 本应用只是控制端，不能让 iPhone 或 iPad 被别人控制。
• 会话期间应用需要保持在前台。
• 应用支持简体中文和英文，可在设置里切换。

支持：admin@remohop.com
```

**关键词**（50）

```
远程桌面,远程控制,终端,ssh,shell,点对点,p2p,文件传输,内网穿透,自建,服务器,运维
```

**新功能（首个版本）**

```
首个版本。
```

## 3. 网址

| 项目 | 值 |
|---|---|
| Support URL（必填） | `https://remohop.com/en/support/`（中文本地化填 `https://remohop.com/support/`） |
| Marketing URL（可选） | `https://remohop.com/en/apps/`（中文：`https://remohop.com/apps/`） |
| Privacy Policy URL（必填） | `https://remohop.com/en/privacy/`（中文：`https://remohop.com/privacy/`） |

**【提交前确认】** 域名未定。上面的页面已经在 `site/public/` 里，部署后这些路径即可访问。提交审核前这三个地址必须能在公网打开，否则会被拒（指南 1.5、5.1.1）。

## 4. 年龄分级问卷

所有内容类问题都选“无 / None”：卡通或幻想暴力、写实暴力、色情或裸露、粗俗语言、酒精烟草毒品、成人或性暗示题材、恐怖惊悚、医疗信息、赌博、竞赛。

| 问题 | 答案 | 理由 |
|---|---|---|
| 不受限制的网页访问（Unrestricted Web Access） | 否 | WebView 只允许加载应用进程内网关的回环地址（`GatewayWebView.swift` 里其他来源的导航一律 `.cancel`），不能浏览任意网页 |
| 用户生成内容、消息与聊天、广告 | 否 | 应用里没有这些功能 |
| 赌博、抽奖 | 否 | |
| 家长控制、年龄验证 | 否 | |

预期结果：4+。如果 Apple 的新版问卷把“可以访问用户自己的电脑上的任意内容”归入某个新问题，按实际情况回答即可，这不改变应用本身不提供内容的事实。

## 5. App 隐私（“营养标签”）

### 结论

**以当前代码构建的应用：选择 “Data Not Collected（不收集数据）”。**

**跟踪（Tracking）：否。** 应用不含任何广告或分析 SDK，不读取 IDFA，不与数据经纪商共享数据，也不需要 App Tracking Transparency 弹窗。

### 推理过程

Apple 对“收集”的定义是：把数据传出设备，并且开发者或其第三方合作伙伴能够在处理当次请求所需的时间之外继续访问这些数据。逐项对照应用的实际行为：

| 应用里的数据 | 去了哪里 | 开发者（Readori）是否收集 |
|---|---|---|
| 身份密钥、配对令牌、已保存主机、设置 | 只在应用私有目录（Application Support/remohop，已排除备份） | 否，不离开设备 |
| 屏幕、按键、文件、剪贴板、终端内容 | 端到端加密后发往用户自己连接的电脑 | 否，Readori 不在路径上，也无法解密 |
| 主机密码 | 只用于当次登录的密钥交换，不发送、不保存 | 否 |
| 相机、照片 | 用户通过系统选择器选中后，上传到自己连接的电脑 | 否 |
| IP 地址、要连接的 ID、连接时间 | 发往**用户在设置里自己填写的**汇合服务器 | 否：应用没有默认服务器（`SettingsView.swift`：“RemoHop has no default server”），服务器由用户或其组织运营，不是开发者的服务 |
| 邮箱地址（账号） | 移动端没有账号登录功能 | 否，应用里没有这个流程 |
| 崩溃和使用统计 | 应用自身不采集。用户同意“与 App 开发者共享”时由 Apple 提供的崩溃报告不计入开发者的收集声明 | 否 |

所以在“应用不带默认服务器”这个前提下，开发者不收集任何数据。

### 什么情况下必须改答案

**【提交前确认】** 下面两种情况一旦出现，标签要在对应版本上线前更新：

1. **发布的构建内置了 Readori 运营的默认汇合服务器**（通过 `-ldflags -X …dial.DefaultSignal=…`）。这时应用会默认把用户的 IP 地址和查询的 ID 发给 Readori 的服务器，服务器日志按隐私政策保留 14 天。保留时间超出了“处理当次请求”，稳妥的做法是声明：
   - **Other Data → Other Data Types**（连接元数据：IP 地址、连接时间、数据量），用途 **App Functionality**；
   - 是否关联到用户身份：**否**（没有账号，日志不与身份绑定）；
   - 是否用于跟踪：**否**。

   Apple 的类型列表里没有单独的“IP 地址”一项。也有同类应用认为短期的安全日志不构成“收集”而按“不收集”声明，但这样做有申报不实的风险；更保守的声明不会带来审核问题。
2. **移动端加入了账号登录**（目前代码里没有相关调用）。那时要增加：
   - **Contact Info → Email Address**，用途 **App Functionality**，**关联到用户**，不用于跟踪；
   - **Identifiers → Device ID**（设备的身份公钥和设备名会进入账号的设备列表），用途 **App Functionality**，**关联到用户**，不用于跟踪。

   同时按指南 5.1.1(v) 在应用内提供删除账号的入口（命令行里已有 `remohop account delete --yes`，应用里需要对应的按钮）。

隐私政策（`/privacy/`）与上述回答一致：它写明应用不收集分析数据、数据留在设备上，并单独说明了汇合服务器处理的元数据。

## 6. 出口合规（加密）

### 应用里的加密

- QUIC（内层 TLS 1.3）用于会话加密；
- CPace（基于密码的认证密钥交换）；
- Ed25519 身份密钥和签名；
- 以上都由 Go 标准库和开源 Go 模块实现，**不是**调用 Apple 操作系统自带的加密。

全部是公开的标准算法，没有自研或专有算法。

### App Store Connect 里怎么答

1. “你的 App 是否使用加密？”：**是**。
2. 加密类型：选 **“使用标准加密算法，代替或补充 Apple 操作系统中的加密”**（Standard encryption algorithms instead of, or in addition to, using or accessing the encryption within Apple's operating system）。不要选“只使用操作系统内的加密”，因为 TLS 和 QUIC 是 Go 自己实现的。也不要选“专有或非标准算法”。
3. 按 Apple 目前的规则，这一类应用**只有在法国上架时**才需要上传法国的加密申报文件；不在法国销售则不需要上传文件。两种处理方式：
   - 首发时在“销售范围”里取消法国，之后办好法国申报再加回来；
   - 或者先向法国 ANSSI 完成申报，取得文件后上传。

   **【提交前确认】** 选择其中一种。

### 通常的豁免思路（供参考）

- 美国《出口管理条例》（EAR）下，使用标准算法、面向大众公开销售的软件一般归入大众市场类（5D992.c，许可例外 ENC §740.17(b)(1)），自 2021 年起多数此类产品不再需要提交年度自我分类报告。
- RemoHop 的源代码以 AGPL-3.0 公开。公开可得的加密源代码可以适用 EAR §742.15(b)，条件之一是在公开时向美国商务部工业与安全局（BIS）和国家安全局（NSA）发送一封通知邮件，说明源代码的网址。**【提交前确认】** 仓库公开时决定是否发送这封通知。
- 发布方所在国家或地区自己的密码产品进出口规定同样适用。

### `ITSAppUsesNonExemptEncryption`

`Info.plist` 里没有写死这个键。构建时按 [`mobile/ios/fastlane/export-compliance.env`](../mobile/ios/fastlane/export-compliance.env) 中的 `USES_NON_EXEMPT_ENCRYPTION` 处理：

- 留空（当前状态）：不写入这个键，每次上传构建后要在 App Store Connect 里手动回答一次；
- `NO`：认定属于豁免范围、不需要文件，构建时写入 `ITSAppUsesNonExemptEncryption = NO`；
- `YES`：需要文件（例如在法国销售并已上传申报），构建时写入 `YES`，App Store Connect 还会要求提供申报文件或 `ITSEncryptionExportComplianceCode`。

在做出决定之前保持留空。

## 7. App Review 审核备注

### 需要先准备的东西

**【提交前确认：演示主机】** 这个应用本身不带服务器，也没有演示模式，审核员必须有一台能连上的被控机才能看到任何功能。提交前要准备一台，并在审核期间保持在线：

- 一台有公网 IP 的 Linux 虚拟机，开放一个 UDP 端口（例如 21118）；
- 用一个没有特权的专用账户运行，装好带 libx264 的 ffmpeg 和一个 X 显示（Xvfb 加一个简单的桌面或终端窗口即可），这样远程桌面有画面；
- 关掉审核用不到的功能：`remohop config feature vpn off`、`usb off`、`serial off`、`forward off`；
- 设置固定密码：`remohop passwd`；
- 启动：`remohop host --no-signal --listen :21118`（按地址直连，不依赖汇合服务器；Apple 的审核网络通常可以发出 UDP）；
- 审核通过后更换密码或销毁这台机器。

如果届时已有 Readori 的公共汇合服务器，也可以改为提供 9 位 ID，并在备注里写明需要在 Settings 里填入的服务器地址。

**纯 IPv6 网络。** Apple 的审核网络（以及部分运营商网络）只有 IPv6，通过 NAT64/DNS64 访问 IPv4。审核员按 IP 地址连接演示主机，所以这一点直接决定能否过审：iOS 应用在 IPv4 发不出去时会自动发现 NAT64 前缀，把 IPv4 地址转换成 IPv6 地址再发送（见 [how-it-works](en/how-it-works.md#ipv6-only-networks-nat64)）。提交前建议在 Mac 上用“互联网共享”里的“创建 NAT64 网络”（按住 Option 点“共享”设置里的“互联网共享”可见）搭一个纯 IPv6 Wi-Fi，用真机连上后按备注里的步骤连接演示主机验证一次。如果演示主机有域名，备注里也可以写域名，域名在 NAT64 网络里由 DNS64 处理。

### Sign-in 信息

“需要登录（Sign-in required）”：**不勾选**。应用没有账号。演示主机的地址和密码写在备注（Notes）里。

### 备注正文（英文，替换尖括号里的内容后粘贴）

```
RemoHop is a remote control client. It connects to a computer that runs the RemoHop host program ("remohop host"). The app has no accounts and no sign-in.

HOW TO TEST
We keep a demo host online for review:
  Address:  <DEMO_HOST_ADDRESS>:21118
  Password: <DEMO_HOST_PASSWORD>

1. Open the app. The connect screen appears.
2. In "Host ID, name or host:port" enter the address above. Enter the password. Tap Connect.
3. The Desktop tab shows the demo computer's screen. Tap or drag to control it.
4. Terminal tab: type a command, for example "uname -a".
5. Files tab: browse folders, download a file, or upload a photo.
6. Clipboard tab: send text to the computer or fetch its clipboard.
7. The gear button opens the native Settings screen (rendezvous server, device identity, pairing, saved hosts).

The demo host is a disposable Linux virtual machine that we own, running under an unprivileged account.

TO TEST WITH YOUR OWN COMPUTER (optional)
On a Mac or Linux machine on the same network as the device, build the host from <REPO_OR_DOWNLOAD_URL> and run:
  remohop host --no-signal --listen :21118
It prints a temporary password. In the app, connect to <computer-ip>:21118 with that password.

NOTES FOR REVIEW
• Architecture: the app embeds the RemoHop core (Go, built with gomobile) and runs it in-process. The user interface is rendered in a WKWebView that loads only http://127.0.0.1 from that in-process core; navigation to any other origin is cancelled. It is not a wrapper around a website and loads no remote web content. NSAllowsLocalNetworking is set for this loopback page only.
• The session between the app and the computer is peer-to-peer and end-to-end encrypted (QUIC/TLS 1.3 with a password-authenticated key exchange). No server of ours carries or can read session content.
• Remote desktop: the app shows and controls the screen of a computer the user owns or is authorised to use. Everything shown is rendered on that computer. The app offers no store, catalogue or third-party content.
• Local Network permission: used to connect directly to computers on the user's LAN.
• Camera and Photo Library: used only by the system picker when the user uploads a photo or video to their computer from the Files tab.
• Clipboard: read or written only when the user taps the clipboard buttons.
• The app collects no data and contains no analytics, advertising or third-party SDKs.
• Encryption: standard algorithms only (TLS 1.3/QUIC, CPace, Ed25519), implemented in Go.

Contact: admin@remohop.com
```

**【提交前确认】** App Store Connect 还要求填写审核联系人的姓名、电话和邮箱。邮箱可用 `admin@remohop.com`。联系人姓名、电话以及演示主机的地址和密码只填在 App Store Connect 或 CI 的密钥里，不要提交到仓库：`mobile/ios/fastlane/metadata/review_information/` 中只保留占位内容。

### 审核中可能被问到的点

- **指南 4.2.7（远程桌面客户端）。** 该条要求客户端连接的是用户自己拥有的主机，客户端上显示的内容完全在主机上执行和渲染，账号的创建和管理不在客户端进行，界面不模仿 iOS 或 App Store。RemoHop 符合这些要求；条文里还有一句主机和客户端处于本地局域网的表述，市面上的远程桌面应用普遍经互联网连接并已上架，但如果审核员引用这一句，回复时强调：连接的是用户自己的电脑，点对点、端到端加密，应用不提供任何云端应用或内容。提交前请对照指南的最新原文。
- **指南 4.2（最低功能）与 WebView。** 备注里已说明界面来自应用内嵌的本地核心，不是网站套壳。
- **指南 2.1（应用完整性）。** 审核员无法连接时会直接拒绝，所以演示主机必须在线，地址和密码必须准确。
- **被控端软件的可获得性。** 描述里写了“需要一台运行 RemoHop 被控端的电脑”。在桌面端有可下载的发布版本之前，用户只能从源码编译。**【提交前确认】** 建议先发布桌面端的首个版本再提交 App Store 审核，否则用户（和审核员）不容易得到被控端，可能被以“应用无法独立使用”为由拒绝。TestFlight 内部测试不受此影响。
- **AGPL 与 App Store。** Readori 是版权方，可以按自己的条款在 App Store 分发；链接的第三方 Go 模块都是宽松许可证。接受外部贡献前要有 CLA（见 `CONTRIBUTING.md`），否则外部贡献者的 AGPL 代码会与 App Store 的条款冲突。
- **欧盟《数字服务法》交易者身份。** 在欧盟上架需要在 App Store Connect 里声明是否为交易者（trader），是的话要提供可公开的联系信息。**【提交前确认】**

## 8. 截图

### 必需的尺寸

应用同时支持 iPhone 和 iPad，所以两组都要有。每组 1 到 10 张，PNG 或 JPEG，不带透明通道。

| 设备组 | 竖屏像素 | 横屏像素 | 是否必需 |
|---|---|---|---|
| iPhone 6.9 英寸 | 1320 × 2868（也接受 1290 × 2796、1260 × 2736） | 2868 × 1320 | 必需 |
| iPhone 6.5 英寸 | 1284 × 2778 或 1242 × 2688 | 2778 × 1284 | 可选；不提供时用 6.9 英寸的缩放 |
| iPad 13 英寸 | 2064 × 2752（也接受 2048 × 2732） | 2752 × 2064 | 必需 |

提交前请在 App Store Connect 的上传页面核对一次，Apple 每年都会调整接受的尺寸。

### 建议的五张

1. 远程桌面（横屏最能说明问题）：屏幕上是一台电脑的真实桌面。
2. 终端：一个正在运行命令的会话，能看到辅助按键栏。
3. 文件：一个目录列表。
4. 连接页：ID 和密码输入框，以及已保存的主机。
5. 设置：身份指纹、配对和中继开关。

截图必须是应用的真实界面（指南 2.3.3）。远程桌面里显示的内容用自己的演示主机，不要出现他人的商标、真实的个人信息、真实的 ID 或密码。可以在图上加一行说明文字，但不要用与实际界面不符的效果图。

### 怎么生成

在 macOS 上用模拟器（CI 的 `verify` 模式已经能在模拟器里连接一台本机的 `remohop host`）：

```bash
# 1. 本机启动一台被控端（固定端口，一次性密码）
remohop host --no-signal --listen 127.0.0.1:21118

# 2. 启动模拟器并安装应用
xcrun simctl boot "iPhone 17 Pro Max"        # 6.9 英寸；iPad 用 "iPad Pro 13-inch (M4)"
xcrun simctl status_bar booted override --time "9:41" --batteryState charged --batteryLevel 100 --cellularBars 4 --wifiBars 3
xcodebuild -project mobile/ios/RemoHop.xcodeproj -scheme RemoHop -destination 'platform=iOS Simulator,name=iPhone 17 Pro Max' build
xcrun simctl install booted <构建出的 RemoHop.app 路径>
xcrun simctl launch booted com.remohop.app

# 3. 在应用里连接 127.0.0.1:21118，切到要截的标签页，然后：
xcrun simctl io booted screenshot desktop-6.9.png
```

模拟器名称以 `xcrun simctl list devices` 的输出为准。更省事的做法是在 `RemoHopTests/GatewayE2ETests.swift` 的现有流程里，每到一个标签页就加一个 `XCTAttachment(screenshot: XCUIScreen.main.screenshot())`（需要改成 UI 测试目标），让 CI 在两种模拟器上各跑一遍并导出附件。远程桌面那一张需要被控端有显示器：在 macOS 运行器上直接采集运行器的桌面即可。

App 预览视频不是必需的，首发可以不做。

## 9. `Info.plist` 与隐私清单的缺口

按重要程度排列，都在 `mobile/ios` 下。

1. **缺少 `PrivacyInfo.xcprivacy`（隐私清单）。**Swift 代码只用到一个需要声明理由的 API：`UserDefaults`，保存“设置 > 语言”的选择（只在本应用内读写，理由 `CA92.1`）；没有读取文件时间戳。Go 运行时和标准库编译进来的符号通常会触发 Apple 的 ITMS-91053 邮件，常见的三类是文件时间戳、系统启动时间和磁盘空间。建议在应用目标里加入一份清单，内容为：
   - `NSPrivacyTracking` = `false`，`NSPrivacyTrackingDomains` 为空；
   - `NSPrivacyCollectedDataTypes` 为空数组（与第 5 节的“不收集数据”一致）；
   - `NSPrivacyAccessedAPITypes`：`NSPrivacyAccessedAPICategoryUserDefaults`（理由 `CA92.1`）、`NSPrivacyAccessedAPICategoryFileTimestamp`（理由 `C617.1`）、`NSPrivacyAccessedAPICategorySystemBootTime`（理由 `35F9.1`）、`NSPrivacyAccessedAPICategoryDiskSpace`（理由 `E174.1`）。

   具体需要哪几类，以首次上传后 Apple 邮件里列出的为准，多报无害、漏报会收到警告。`Remohopcore.xcframework` 是自己构建的，不在 Apple 要求单独签名和附带清单的第三方 SDK 名单上。
2. **`ITSAppUsesNonExemptEncryption` 尚未确定。** 见第 6 节；做出决定后在 `export-compliance.env` 中设置。
3. **可能缺少 `NSMicrophoneUsageDescription`。** “文件”标签页的上传使用 WebKit 的系统选择器，其中的“拍照或录像”在录像时会用到麦克风。iOS 在访问麦克风而没有用途说明时会直接终止应用。需要在真机上验证选择器是否提供录像；如果提供，加上例如 `Record a video to upload it to a remote computer.` 的说明，或者把上传控件限制为只接受图片。
4. **`NSPhotoLibraryUsageDescription` 可能并不需要。** 系统照片选择器在应用进程之外运行，不需要相册权限。保留这个键没有害处，但审核员有时会问为什么声明了却从不弹出权限框；可以保留并在备注里说明（第 7 节的备注已写），也可以在真机验证后删掉。`NSCameraUsageDescription` 是需要的。
5. **界面语言。** 应用界面（原生设置页和内嵌的网页控制台）有英文和简体中文，默认跟随系统（任何 zh-* 语言显示中文，其余显示英文），可在“设置 > 语言”里切换，立即生效。权限说明（`NSLocalNetworkUsageDescription`、`NSCameraUsageDescription`、`NSPhotoLibraryUsageDescription`）在 `en.lproj` 和 `zh-Hans.lproj` 的 `InfoPlist.strings` 里各有一份；以后新增权限键时两份都要加。
6. **本地网络。** `NSLocalNetworkUsageDescription` 已有。应用不使用 Bonjour，所以不需要 `NSBonjourServices`。首次按局域网地址连接时系统会弹出权限框，用户拒绝后直连会失败且没有明确提示，建议在连接失败的错误信息里提示去“设置 > 隐私与安全性 > 本地网络”检查。
7. **`NSAppTransportSecurity`。** 只设置了 `NSAllowsLocalNetworking`，没有 `NSAllowsArbitraryLoads`，这是正确的，审核时不需要额外说明理由。
8. **后台行为。** 没有声明任何后台模式，应用进入后台后会话会被系统挂起。这符合预期，描述里已写明“会话期间应用需要保持在前台”。
9. **应用图标。** 只有一张 1024 × 1024 的 PNG，RGB 无透明通道，符合要求。
10. **iPad 多任务。** iPad 的四个方向都已声明，支持分屏，不需要 `UIRequiresFullScreen`。
11. **尚未在真机上运行过。** 应用目前只在 CI 的模拟器里跑过。提交审核前至少在一台 iPhone 和一台 iPad 上完整走一遍：本地网络权限、相机上传、键盘、横竖屏、从后台返回后重新连接。

## 10. TestFlight

- 内部测试（团队成员，最多 100 人）不需要审核，上传后填完出口合规问题即可分发。
- 外部测试需要通过 Beta App Review，要填写“测试信息”：
  - Beta App Description：用第 2 节英文描述的前两段；
  - Feedback Email：`admin@remohop.com`；
  - What to Test：`Connect to a computer running "remohop host" by address or ID, then try Desktop, Terminal, Files and Clipboard. Please report the device model and iOS version with any problem.`；
  - 审核备注同第 7 节，同样需要演示主机。
- 隐私政策 URL 在外部测试时也是必填的。

## 提交前需要确认的事项

| 事项 | 在哪一节 | 说明 |
|---|---|---|
| 域名 | 3 | `https://remohop.com` 需要注册并部署网站；三个 URL 提交前必须可访问 |
| 演示主机 | 7 | 需要准备并在审核期间保持在线；地址和密码只填进 App Store Connect 的备注 |
| 审核联系人姓名和电话 | 7 | App Store Connect 必填 |
| 被控端的获取途径 | 7 | 桌面端的首个发布版本，最好先于 App Store 提交完成 |
| 出口合规的最终答案、是否在法国销售 | 6 | 之后在 `export-compliance.env` 中设置 `USES_NON_EXEMPT_ENCRYPTION` |
| 开源加密源代码的 BIS/NSA 通知 | 6 | 仓库公开时决定 |
| 是否内置默认汇合服务器 | 5 | 内置则隐私标签和隐私政策都要更新 |
| 服务器日志保留期限 | 5 | 隐私政策里暂写 14 天 |
| 隐私清单、麦克风说明 | 9 | 需要改 `mobile/ios`，并在真机上验证 |
| 应用名称是否可用 | 2 | 创建应用记录时确认 |
| 版权行、法律实体名称 | 1 | |
| 欧盟交易者身份 | 7 | 在欧盟上架时必填 |
| 截图 | 8 | 需要在 macOS 上生成 |

## 附：Google Play 的前台服务声明（Android）

Android 应用在有被控端会话时以前台服务（`SessionService`）保持连接，并显示常驻通知（“已连接到 …”，带“断开连接”）。`targetSdk 36` 要求声明前台服务类型，这里用的是 **`specialUse`**：

- `dataSync` 从 Android 15 起每天最多 6 小时，而且用途是上传、下载、备份等数据传输，不适合“保持一个远程控制会话”；
- `connectedDevice` 面向蓝牙、USB、NFC 等外接设备；
- 所以选 `specialUse`，清单里的 `PROPERTY_SPECIAL_USE_FGS_SUBTYPE` 写明了用途。

**【提交前确认】** 在 Play Console 的“应用内容 → 前台服务权限”中声明 `FOREGROUND_SERVICE_SPECIAL_USE`，用途说明可写：

```
RemoHop is a remote-control client for the user's own computers. While the user has an active remote session (terminal, file transfer or remote desktop) and switches to another app, a foreground service keeps that session's network connection alive so it is still there when they return. The service runs only while a session exists, shows an ongoing notification ("Connected to <host>") with a Disconnect action, and stops as soon as the user disconnects. It is started only by the user connecting to a host.
```

Play Console 可能要求一段演示视频：连接演示主机 → 切到桌面 → 下拉通知栏看到“已连接到 …” → 点“断开连接”。应用在第一次建立会话时请求通知权限（Android 13+），拒绝也不影响会话，只是通知不显示。
