# 构建与测试

[English](../en/building.md)

需要 `go.mod` 中指定的 Go 工具链版本。发布版本使用 `CGO_ENABLED=0` 构建。

| 命令 | 作用 |
|---|---|
| `make build` | 生成 `bin/remohop` |
| `make cross` | 为 linux/amd64、linux/arm64、darwin/amd64、darwin/arm64 和 windows/amd64 生成程序，输出到 `dist/` |
| `make test` | 运行 `go test ./...` |
| `make race` | 使用竞态检测器运行测试（需要 cgo） |
| `make vet`、`make fmt-check` | 运行 `go vet`，并在存在未格式化的文件时失败 |
| `make all` | 依次执行 `fmt-check`、`vet`、`build` 和 `test` |

本机回环冒烟测试在同一台机器上启动一个被控端和一个客户端，测试已编译好的程序；macOS 和 Windows 上的自动化测试用的就是它：

```bash
test/smoke/smoke.sh bin/remohop
```

NAT 实验环境在网络命名空间里让真实的程序穿过 Linux 内核实现的 NAT，汇合服务器位于两者之间，所以只有打洞成功（或中继可用）会话才能建立。它需要 root 权限、iproute2 和 iptables：

```bash
sudo test/natlab/natlab.sh cone
```

模式：`cone`、`symmetric`、`permissive`、`relay`、`wsrelay`、`tailnet`（在被控端和客户端之间模拟一个 Tailscale：必须走 tailnet 路径）、`mesh`（同一账号的三台设备分别在三个 NAT 后面：名字、策略、子网路由、出口节点、干净移除）、`vpn` 和 `all`。`vpn` 模式检查隧道结束后（包括任一端被强行杀掉后）路由、防火墙规则和 sysctl 是否恢复原样，覆盖 iptables 和 nft 主机；它不包含在 `all` 中。
