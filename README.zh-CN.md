# RemoHop

[English](README.md) | 简体中文

RemoHop 是一个点对点远程控制工具。一个静态链接的 Go 程序 `remohop` 同时是命令行客户端、被控机上的守护进程、让两台机器找到彼此的汇合服务器（rendezvous server）、浏览器网关，以及供 AI 代理使用的 MCP 服务器。会话端到端加密，默认在两台机器之间直连。

## 能做什么

| 功能 | 说明 | 文档 |
|---|---|---|
| shell 与后台任务 | 持久的交互式 shell、单条命令、断开后继续运行的后台任务；Windows 被控机上是 PowerShell | [shell 与后台任务](docs/zh/shell-and-jobs.md) |
| 文件 | 列目录、复制、同步、投递到对方的“下载”目录；大文件可续传；把远程目录挂载到本机 | [文件](docs/zh/files.md) |
| 端口转发与网络唤醒 | TCP 和 UDP 转发、TCP 反向转发、Wake-on-LAN | [端口转发](docs/zh/forwarding.md) |
| VPN | 在会话里建立一条 IPv4 虚拟链路，访问被控机所在的网络或把它当作出口 | [VPN](docs/zh/vpn.md) |
| 网状网络 | 把账号里的 Linux 设备连成带名字（如 `nas.remohop`）的私有网络；可与 Tailscale 同时使用 | [网状网络与 Tailscale](docs/zh/mesh-and-tailscale.md) |
| 串口与 USB | 把远程串口变成本机的虚拟串口；USB/IP（Linux） | [串口与 USB](docs/zh/serial-and-usb.md) |
| 剪贴板 | 双向同步文本和图片，带历史记录 | [剪贴板](docs/zh/clipboard.md) |
| 远程桌面与图形界面自动化 | 在浏览器中查看和控制屏幕（H.264 或 AV1），支持音频、多显示器和录制；截图、点击、输入 | [屏幕与远程桌面](docs/zh/remote-desktop.md) |
| 浏览器客户端 | `remohop web` 在浏览器里提供桌面、终端、文件、剪贴板和系统信息 | [浏览器客户端](docs/zh/web-gateway.md) |
| 快速协助 | 对方双击一个程序，把 ID 和密码念给你，每次连接都要对方点“允许” | [快速协助](docs/zh/quick-support.md) |
| AI 代理 | MCP 服务器（29 个工具）、六个 Agent Skills、限定范围并自动过期的授权 | [AI 代理](docs/zh/ai-agents.md) |
| 被控端访问控制 | 功能开关、连接审批、一次性密码、密钥登录、审计日志 | [被控端访问控制](docs/zh/host-access-control.md) |
| 账号（可选） | 把自己的设备归为一组并查看在线状态；不授予访问权 | [账号](docs/zh/accounts.md) |
| 自建服务器 | 自己运行汇合服务器，可选中继和 websocket 桥 | [自建汇合服务器](docs/zh/self-hosting.md) |
| 移动端 | Android 和 iOS 控制端应用（[App Store](https://apps.apple.com/app/id6838609056)；Google Play 即将上架） | [移动端应用](docs/zh/mobile-apps.md) |

安全机制：QUIC 内部是 TLS 1.3，登录时用密码做带认证的密钥交换（CPace）并与连接绑定，主机密钥在首次登录时固定。中继只有在汇合服务器运营者主动开启时才存在，只在无法直连时使用，承载的仍是同一条加密会话。账号是可选的，不授予访问权。没有内置的默认汇合服务器。RemoHop 尚未经过独立的安全审计。详见 [docs/security.md](docs/security.md)（英文）。

## 平台与状态

- Linux 是参考平台。仓库的 CI 在 Linux 上运行 Go 测试（带竞态检测器）和 NAT 实验环境（在网络命名空间里穿过内核 NAT 的端到端测试）。
- macOS 和 Windows 的主要功能（远程桌面、输入、挂载、VPN、Windows 无人值守等）都由托管运行器上的端到端测试覆盖，但还没有在实体桌面机上逐项验证；快速协助窗口和音频在这两个系统上还没有实际运行过。
- 发布版本在 [GitHub Releases](https://github.com/readori/RemoHop/releases)，全部下载见 [remohop.com/download](https://remohop.com/download/)；安装方法见[安装](docs/zh/install.md)。Windows 二进制尚未做代码签名。
- 详见[平台支持与限制](docs/zh/platforms.md)。

## 快速开始

```bash
make build                                 # 生成 bin/remohop，需要 go.mod 中指定的 Go 版本
remohop signal                                 # 在一台开放了 UDP 端口的公网机器上运行汇合服务器
remohop config set signal sig.example.net      # 在被控机和你自己的机器上各执行一次
remohop host                                   # 在被控机上：打印 ID 和临时密码
remohop 276941009 shell                        # 在你的机器上：输入密码后打开远程 shell
```

完整步骤（配对、系统服务、不用汇合服务器的直连）见[快速开始](docs/zh/quick-start.md)和[安装](docs/zh/install.md)。

## 文档

全部文档的索引见 [docs/README.md](docs/README.md)。

- 入门：[安装](docs/zh/install.md) · [快速开始](docs/zh/quick-start.md) · [快速协助](docs/zh/quick-support.md)
- 使用：[shell 与后台任务](docs/zh/shell-and-jobs.md) · [文件](docs/zh/files.md) · [端口转发](docs/zh/forwarding.md) · [VPN](docs/zh/vpn.md) · [网状网络与 Tailscale](docs/zh/mesh-and-tailscale.md) · [串口与 USB](docs/zh/serial-and-usb.md) · [剪贴板](docs/zh/clipboard.md) · [屏幕与远程桌面](docs/zh/remote-desktop.md) · [浏览器客户端](docs/zh/web-gateway.md) · [账号](docs/zh/accounts.md) · [AI 代理](docs/zh/ai-agents.md) · [移动端应用](docs/zh/mobile-apps.md)
- 被控端与服务器：[被控端访问控制](docs/zh/host-access-control.md) · [自建汇合服务器](docs/zh/self-hosting.md) · [工作原理](docs/zh/how-it-works.md)
- 参考：[平台支持与限制](docs/zh/platforms.md) · [构建与测试](docs/zh/building.md)
- 英文参考文档：[安全模型](docs/security.md) · [故障排查](docs/troubleshooting.md) · [AI 代理的授权与配置](docs/agents.md) · [网状网络详解](docs/net.md) · [公共服务器](docs/public-server.md) · [架构](docs/ARCHITECTURE.md) · [路线图](docs/ROADMAP.md) · [Agent Skills 与 MCP](skills/README.md)

## 许可证

RemoHop 采用双许可：所有人都可以按 [GNU AGPL-3.0](LICENSE) 使用；AGPL 不适合的场景，可以向版权方购买商业授权。运营者要特别注意 AGPL 的网络条款：如果你把**修改过的** `remohop signal` 或 `remohop web` 提供给他人使用，就必须向这些用户提供修改后的源代码；运行未修改的 RemoHop 没有额外义务。**基于本仓库源码的二次开发，必须标明项目名称 RemoHop 和开源地址 https://github.com/readori/RemoHop，并保留 [NOTICE](NOTICE) 文件**；修改版不得自称原版，也不得用 RemoHop 作为自己产品的名称（AGPL-3.0 第 7 条的附加条款）。第三方组件的许可证、以及为什么不捆绑 ffmpeg，见 [LICENSING.md](LICENSING.md)；贡献代码需要签署 CLA，见 [CONTRIBUTING.md](CONTRIBUTING.md)。

联系方式：<info@remohop.com>（商业授权、CLA，以及不宜公开的安全问题）。 交流和提问也可以到 Telegram：[t.me/remohop](https://t.me/remohop)。
