# VPN

[English](../en/vpn.md)

远程命令的格式都是 `remohop <ID> <command>`。本页的例子用 `dev` 作为别名，它来自 `remohop <ID> pair --name dev`（见[快速开始](quick-start.md)）。每个命令的完整语法用 `remohop help <command>` 查看。

`remohop <ID> vpn up` 在加密会话里为本机和被控机建立一条虚拟 IPv4 链路：本机是 `10.77.0.2`，被控机是 `10.77.0.1`。在 Linux 上两端都需要 root 权限或 `CAP_NET_ADMIN`，在 Windows 上需要管理员权限。

```bash
sudo remohop dev vpn up -b
sudo remohop dev vpn up --route 192.168.1.0/24 -b
sudo remohop dev vpn up --exit --bypass 10.0.0.0/8
remohop vpn
sudo remohop dev vpn down
```

- `--route CIDR` 访问被控机那一侧的某个网络，由被控机负责转发。
- `--exit` 让本机的全部流量经由被控机出去。
- 访问被控机自身网络以外的地址，以及使用 `--exit`，都需要在被控机上执行 `remohop config vpn-exit on`，它默认关闭。被控机也可以用 `remohop config feature vpn off` 关掉整个功能。
- 隧道创建的接口、路由和防火墙规则在隧道结束时全部删除。如果 `remohop` 被强行杀掉，下一条以相同权限运行的 `remohop` 命令会清理残留。

两端都是 Linux 是测试过的组合。其他平台：

- **Windows**：可以作为控制端（支持 `--route` 和 `--exit`），自身可以被访问；不能作为 `--route` 或 `--exit` 的转发主机。需要 Wintun 驱动，`remohop` 不包含也不下载它。从 <https://www.wintun.net> 下载 Wintun 0.14.1，核对 zip 的 SHA-256（`07c256185d6ee3652e09fa55c0b673e2624b565e02c4b9091c79ca7d2f24ef51`），把 `wintun\bin\<架构>\wintun.dll` 放在 `remohop.exe` 旁边或 `remohop` 配置目录里，也可以用环境变量 `REMOHOP_WINTUN_DIR` 指定目录。`remohop` 还会核对这个 DLL 本身的 SHA-256，不匹配的文件会被拒绝。需要在真实 Windows 机器上手动做端到端测试（仓库里的测试默认跳过），尚未验证。
- **macOS**：使用 utun。客户端（包括 `--route`）以及自身被访问的 macOS 被控端，已在托管的 macOS 虚拟机上由自动化测试验证：真实的 utun 接口、路由、双向收发数据包，以及 `vpn down` 后和进程被杀后的清理。`--exit` 没有在真实网络上运行过（它的例外路由测试过，接管默认路由没有测试），也没有在实体 Mac 上运行过。作为被控端不能为 VPN 客户端转发。
