# RemoHop documentation

The user guide exists in two languages with the same pages: [中文](zh/README.md) and [English](en/README.md). The in-depth references below it are in English only.

## User guide

| Topic | 中文 | English |
|---|---|---|
| Install | [安装](zh/install.md) | [Install](en/install.md) |
| Quick start | [快速开始](zh/quick-start.md) | [Quick start](en/quick-start.md) |
| Quick support | [快速协助](zh/quick-support.md) | [Quick support](en/quick-support.md) |
| Shell and jobs | [shell 与后台任务](zh/shell-and-jobs.md) | [Shell and background jobs](en/shell-and-jobs.md) |
| Files | [文件](zh/files.md) | [Files](en/files.md) |
| Port forwarding and Wake-on-LAN | [端口转发与网络唤醒](zh/forwarding.md) | [Port forwarding and Wake-on-LAN](en/forwarding.md) |
| VPN | [VPN](zh/vpn.md) | [VPN](en/vpn.md) |
| Mesh network and Tailscale | [网状网络与 Tailscale](zh/mesh-and-tailscale.md) | [Mesh network and Tailscale](en/mesh-and-tailscale.md) |
| Serial ports and USB | [串口与 USB 设备](zh/serial-and-usb.md) | [Serial ports and USB devices](en/serial-and-usb.md) |
| Clipboard | [剪贴板](zh/clipboard.md) | [Clipboard](en/clipboard.md) |
| Screen, remote desktop, GUI automation | [屏幕与远程桌面](zh/remote-desktop.md) | [Screen and remote desktop](en/remote-desktop.md) |
| Browser client | [浏览器客户端](zh/web-gateway.md) | [Browser client](en/web-gateway.md) |
| Accounts | [账号](zh/accounts.md) | [Accounts](en/accounts.md) |
| AI agents | [AI 代理](zh/ai-agents.md) | [AI agents](en/ai-agents.md) |
| Mobile apps | [移动端应用](zh/mobile-apps.md) | [Mobile apps](en/mobile-apps.md) |
| Host access control | [被控端访问控制](zh/host-access-control.md) | [Host access control](en/host-access-control.md) |
| Self-hosting the rendezvous server, relay, websocket bridge | [自建汇合服务器](zh/self-hosting.md) | [Self-hosting](en/self-hosting.md) |
| How it works | [工作原理](zh/how-it-works.md) | [How it works](en/how-it-works.md) |
| Platform support and limitations | [平台支持与限制](zh/platforms.md) | [Platform support and limitations](en/platforms.md) |
| Building and testing | [构建与测试](zh/building.md) | [Building and testing](en/building.md) |

## In-depth references (English)

- [security.md](security.md): threat model and security mechanisms.
- [troubleshooting.md](troubleshooting.md): connection errors, NAT problems and platform notes.
- [agents.md](agents.md): handing a machine to an AI agent safely (grants, approvals, audit, kill switch) and MCP client setup.
- [net.md](net.md): the mesh network (`remohop net`) and using RemoHop with Tailscale, in detail.
- [public-server.md](public-server.md): running a public rendezvous server and relay, with a privacy notice template.
- [ARCHITECTURE.md](ARCHITECTURE.md): packages, session model and connection establishment.
- [ROADMAP.md](ROADMAP.md): what is planned.
- [../skills/README.md](../skills/README.md): Agent Skills and the MCP tool list.

## For maintainers

- [../packaging/README.md](../packaging/README.md): building, signing and publishing releases; package-manager manifests; the Docker image.
- [../packaging/quick/README.md](../packaging/quick/README.md): the quick-support packages (`RemoHop.exe`, `RemoHop.app`).
- [app-store.md](app-store.md): App Store Connect listing and review material for the iOS app (Chinese).
- [../CONTRIBUTING.md](../CONTRIBUTING.md): how to contribute.
