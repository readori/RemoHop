# RemoHop 使用文档

[English](../en/README.md) · [全部文档](../README.md)

远程命令的格式都是 `remohop <ID> <command>`。各页的例子用 `dev` 作为别名，它来自 `remohop <ID> pair --name dev`。每个命令的完整语法用 `remohop help <command>` 查看。

## 入门

- [安装](install.md)：安装脚本、软件包、手动验证下载、从源码编译。
- [快速开始](quick-start.md)：汇合服务器、被控机、配对、系统服务、不用汇合服务器的直连、状态与设备。
- [桌面应用](desktop-app.md)：一个窗口，连接你的电脑，也让别人连接这台电脑。
- [快速协助](quick-support.md)：帮助一个不会打开终端的人。

## 使用

- [shell 与后台任务](shell-and-jobs.md)
- [文件](files.md)：复制、同步、投递、挂载。
- [端口转发与网络唤醒](forwarding.md)
- [VPN](vpn.md)
- [网状网络与 Tailscale](mesh-and-tailscale.md)
- [串口与 USB 设备](serial-and-usb.md)
- [剪贴板](clipboard.md)
- [屏幕与远程桌面](remote-desktop.md)：截图、远程桌面、音频、多显示器、图形界面自动化。
- [浏览器客户端](web-gateway.md)：`remohop web`，以及通过 Cloudflare Tunnel 对外提供。
- [账号](accounts.md)
- [AI 代理](ai-agents.md)：MCP 服务器、Agent Skills、授权。
- [移动端应用](mobile-apps.md)

## 被控端与服务器

- [被控端访问控制](host-access-control.md)：功能开关、审批、一次性密码、密钥登录、审计日志。
- [自建汇合服务器](self-hosting.md)：中继、websocket 桥、公共服务器。
- [工作原理](how-it-works.md)：连接如何建立，中继何时使用。

## 参考

- [平台支持与限制](platforms.md)
- [构建与测试](building.md)
- 英文参考文档：[安全模型](../security.md)、[故障排查](../troubleshooting.md)、[AI 代理的授权与配置](../agents.md)、[网状网络详解](../net.md)、[公共服务器](../public-server.md)、[架构](../ARCHITECTURE.md)、[路线图](../ROADMAP.md)
