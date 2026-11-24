# 平台支持与限制

[English](../en/platforms.md)

## 平台支持

Linux 是参考平台。macOS 和 Windows 由托管运行器上的自动化测试覆盖，没有人在实体桌面机上逐项验证过。仓库自带的 CI（`.github/workflows/ci.yml`）只在 Linux 上运行测试（包括 NAT 实验环境），并为 macOS 和 Windows 交叉编译；下表中 macOS 和 Windows 的结果来自在 GitHub 托管的 macOS 和 Windows 运行器上单独进行的测试。

| | Linux | macOS | Windows |
|---|---|---|---|
| 自动化测试（托管运行器） | 全部通过，包括 NAT 实验环境、systemd 服务、真实内核上的 USB/IP、SMTP 账号和 Cloudflare 隧道 | 全部通过，包括服务安装、WebDAV 挂载、utun VPN、远程桌面和输入 | 全部通过，包括服务安装、WebDAV 挂载（Windows 11）、SendInput、截图、远程桌面、Wintun VPN 和无人值守访问 |
| shell | PTY | PTY | ConPTY；命令在 PowerShell 中运行 |
| 文件、转发、剪贴板、截图 | 支持 | 支持 | 支持；Windows 客户端上下载不支持续传 |
| 挂载 | FUSE（或配合 davfs2 用 `--webdav`） | WebDAV（系统自带）；macFUSE 可选，未测试 | 以盘符方式 WebDAV 挂载；已在托管的 Windows 11 运行器上验证 |
| 远程桌面（被控端） | X11 | AVFoundation 采集，VideoToolbox 或软件 H.264/AV1（已在托管的 macOS 虚拟机上验证）；有“辅助功能”权限时可输入（需要“屏幕录制”权限才能采集） | ddagrab 采集，无 GPU 时回退到 gdigrab（回退路径已在托管运行器上验证） |
| 图形界面输入 | X11 | Quartz 事件（CoreGraphics，经 purego 加载，无 cgo）；被控机需要“辅助功能”权限；已在托管的 macOS 虚拟机上验证，未在实体 Mac 上测试 | SendInput；已在托管的 Windows 运行器上验证 |
| VPN | 支持（TUN） | 客户端（utun）已在托管的 macOS 虚拟机上验证，`--exit` 未在真实网络上运行；作为被控端不能转发 | 控制端（Wintun，需自备 DLL），网卡已在托管运行器上验证；不能作为转发主机 |
| 串口（本机虚拟串口） | 支持 | 支持 | 只能作为远程一侧 |
| USB/IP | 支持（已在托管运行器上用真实内核验证） | 不支持 | 不支持 |
| 系统服务 | systemd | launchd | Windows 服务；只有开启 `--unattended` 时才能看到控制台、登录界面和锁屏（已在托管运行器上验证） |

## 状态与限制

- **NAT 穿透可能失败。** 双方都是对称型 NAT，或一方对称型、另一方端口受限型时无法打洞；没有中继可用时，连接会在大约一秒内失败并给出诊断信息。对于会记录未请求入站包的 NAT，Linux 被控机在 IPv4 上用低 TTL 预热包来应对。
- **IPv6 路径和真实的家用路由器未经测试。** NAT 测试使用的是 Linux 网络命名空间。
- **远程桌面。** 在一台只有 CPU 的 4 核虚拟机上，于浏览器中测得的端到端延迟：H.264 约 20 ms（720p）和 31 ms（1080p），软件 AV1 分别约 50 ms 和 61 到 227 ms。硬件编码器优先使用。其中 VideoToolbox（macOS）已验证：在 GitHub 托管的 Apple Silicon macOS 虚拟机（单显示器，1024x768，缩放 1）上，被控端会选用它，解码出的画面、指针和点击输入、只读模式以及两个观看者同时观看都在那里检查过，macOS 上的 libx264 和 SVT-AV1 也是如此。Retina 和多显示器的 Mac、macOS 上的锁屏控制以及实体 Mac 没有测试。NVENC、QSV、VAAPI、AMF 和 Media Foundation 未经测试；4K 60 fps 没有测试过。强制或选用软件 AV1 时，SVT-AV1 2.2 到 3.0 的实时预设有一个上游缺陷（屏幕上的小改动要到下一个关键帧才显示），在这些主机上 RemoHop 改用 libaom 并给出说明。在 X11 被控机上，输入屏蔽和隐私黑屏会报告为不支持。不支持原生 Wayland 采集。
- **串口**用伪终端和 picocom 验证过，没有真实的 UART 可用。**USB/IP** 已在托管运行器上用真实内核（dummy_hcd 虚拟 U 盘）验证，没有在实体 USB 设备上测试过。
- **浏览器客户端**在无头 Chromium 中对接真实的被控端验证过。
- **shell 会话**在客户端断开后会保留一段时间。被控端重启后会话不会保留。
- **打包与发布。** `packaging/` 中有 nfpm 配置（.deb、.rpm、.apk）、安装脚本、systemd 单元、launchd 模板、汇合服务器的 Dockerfile，以及 Homebrew、Scoop、winget、AUR 和 Nix 的模板。推送 `v*` 标签触发的发布工作流还没有运行过，尚无已发布的版本，Windows 二进制也未签名。让被控端开机自启的受支持方式是 `remohop service install`。
- **尚未实现：** 把移动设备（Android 或 iOS）作为被控机。
- **未在真实硬件上测试：** 实体 Mac 上的一切（macOS 的 VPN 客户端、桌面被控端和输入只在托管虚拟机上验证过）、Windows 的 WebDAV 挂载、真实内核上的 USB/IP、除该虚拟机中的 VideoToolbox 以外的 GPU 编码器、设备上的 iOS 应用（它在 CI 的模拟器中运行）、Android 应用（没有真机测试）。
- **账号**由汇合服务器托管，需要运营者开启；服务器可以对设备隐瞒最新的日志条目，见 [security.md](../security.md#accounts)。
- **许可证：** AGPL-3.0，另提供商业授权，见[许可证](../../README.zh-CN.md#许可证)。
