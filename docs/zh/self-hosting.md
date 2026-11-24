# 自建汇合服务器

[English](../en/self-hosting.md)

汇合服务器很轻，一台小虚拟机就够，例如 Oracle Cloud Always Free 的 1 OCPU / 1 GB 实例。实测（数字来自 `remohop help signal` 和测量，不是保证）：空闲时约 17 MB 内存，每个在线主机约 70 KB，1000 个主机约 85 MB。

```bash
remohop signal --relay --relay-ports 40000-40099
remohop signal --print-firewall --relay --relay-ports 40000-40099
remohop signal --show-key
```

- `--relay` 开启中继（默认关闭，会消耗带宽）。`--relay-ports LO-HI` 把中继端口限制在一个范围内，防火墙只需开放这个范围；不加则每个会话用随机高端口。
- `--max-conns N` 限制同时打开的连接数（在线主机加正在查询的客户端），每个约 70 KB。默认值按可用内存（cgroup 限制，否则按物理内存）估算：1 GB 的机器约 6400，最多 50000。
- `--print-firewall` 只打印开放端口所需的命令（Ubuntu/Debian 用 iptables，Oracle Linux/RHEL 用 firewall-cmd），不会执行。
- `--show-key` 打印服务器公钥以及如何固定。

安装为加固的 systemd 服务（只支持 Linux + systemd，需要 root）：

```bash
sudo install -m 755 remohop /usr/local/bin/remohop   # 服务的沙箱看不到 /home、/root 和 /tmp
sudo /usr/local/bin/remohop service install --role signal --relay --relay-ports 40000-40099
remohop service status --role signal
sudo remohop signal --show-key --data /var/lib/remohop-signal
```

必须开放 UDP `21116`、`21117` 和中继端口范围，而且要在**两处**都开放：机器自己的防火墙，以及云厂商的安全列表（Oracle Cloud 的 security list、AWS 的 security group 等）。客户端通过 UDP 直接访问汇合服务器；Cloudflare Tunnel 不转发 UDP。只能用 HTTPS 出网的机器使用下面的 websocket 桥。

客户端指向这台服务器并固定它的公钥：

```bash
remohop config set signal HOST:PORT
remohop config set signal-pin KEY
```

也可以在构建时写进程序：

```bash
go build -ldflags "-X github.com/readori/RemoHop/internal/dial.DefaultSignal=HOST:PORT -X github.com/readori/RemoHop/internal/dial.DefaultSignalPin=KEY" -o remohop ./cmd/remohop
```

## 只允许 HTTPS 出网的网络（websocket 桥）

CI 运行器、云容器和企业网络经常封锁 UDP，只放行 HTTPS，有时还必须经过 HTTP 代理。为此汇合服务器还可以提供一个 websocket 桥，客户端和被控机都会自动回退到它：

```bash
remohop signal --relay --relay-ports 40000-40099 \
    --ws-listen 127.0.0.1:8080 --ws-real-ip-header CF-Connecting-IP
```

通过 Cloudflare Tunnel 对外提供（UDP 端口照旧开放，只有这个桥走隧道）。`cloudflared` 的 `config.yml`：

```yaml
tunnel: <tunnel-id>
credentials-file: /etc/cloudflared/<tunnel-id>.json
ingress:
  - hostname: relay.example.com
    service: http://127.0.0.1:8080
  - service: http_status:404
```

在各台机器上：

```bash
remohop config set signal relay.example.com:21116        # 和以前一样，用于 UDP
remohop config set signal-ws wss://relay.example.com/remohop/v1/udp
```

- 设置了 `signal-ws` 时，如果 4 秒内 UDP 汇合没有应答，`remohop` 和 `remohop host` 会改走这个桥。`REMOHOP_TRANSPORT=ws`（或 `remohop config set signal-transport ws`）直接跳过 UDP 尝试；`REMOHOP_TRANSPORT=udp` 从不使用桥。`REMOHOP_SIGNAL_WS` 覆盖 URL。只配置了 `signal-ws` 的机器只用桥。
- 桥遵循 `HTTPS_PROXY`、`HTTP_PROXY` 和 `NO_PROXY`（CONNECT 方式；代理认证写成 `http://USER:PASSWORD@proxy:port`）。
- 经过桥无法打洞，所以这类会话总是走服务器的中继，中继必须开启（`--relay`）。`remohop <ID> ping` 显示 `relayed path (websocket)`，`remohop host status` 显示 `over its websocket bridge`。加密、密码交换和服务器公钥固定都和 UDP 完全一样：桥和 Cloudflare 只能看到 QUIC 密文。
- `--ws-real-ip-header CF-Connecting-IP` 让按地址的限额使用 Cloudflare 传来的客户端地址；不加的话所有客户端都算作隧道自己的地址。`--ws-tls-cert`/`--ws-tls-key` 可以不经隧道直接提供 `wss://`。`--ws-max-conns`（默认 512，每个地址 32）限制同时打开的桥连接数。
- 如果主机名前面有 Cloudflare Access 应用，就给各台机器一个服务令牌。密钥永远不出现在命令行上：

```bash
remohop config set signal-ws-access-id <client-id>.access
remohop config set signal-ws-access-secret-file /etc/remohop/ws-secret   # 或 REMOHOP_SIGNAL_WS_ACCESS_SECRET
```

- 也可以把桥写进构建：在上面的 `-ldflags` 里加上 `-X github.com/readori/RemoHop/internal/dial.DefaultSignalWS=wss://relay.example.com/remohop/v1/udp`。

这个桥已在网络命名空间里做过端到端测试（除 DNS 外丢弃全部 UDP，然后只放行一个 HTTP CONNECT 代理），也有进程内服务器的单元测试；还没有经过真实的 Cloudflare 隧道。

## 运行公共服务器

如果要把中继提供给不认识的人使用（例如作为你自己构建的 `remohop` 的内置默认服务器），应当给它加上访问控制和流量预算。汇合服务（ID、在线状态、打洞）仍然对所有人开放，只有中继受限。以每月 10 TB 出站流量的 Oracle Cloud Always Free 微型实例为例：

```bash
sudo remohop service install --role signal --relay --relay-ports 40000-40099 -- \
    --relay-kbps 2000 --relay-per-source 2 --relay-monthly-gb 8000 \
    --relay-deny /etc/remohop/relay-deny \
    --ws-listen 127.0.0.1:8080 --ws-real-ip-header CF-Connecting-IP
sudo remohop signal --status --data /var/lib/remohop-signal --relay-monthly-gb 8000
```

- `--relay-per-source N`（默认 2）：每个地址（IPv6 按 /64 计）同时进行的中继会话数，双方都计入，websocket 客户端按其真实地址计入。会话关闭后立即释放名额。
- `--relay-kbps` 限制匿名会话的带宽。开启 `--accounts` 时，`--relay-account-kbps N` 为任一方是本服务器上某个账号的已验证成员的会话提供更高上限，`--relay-for accounts` 则只为这类会话中继。成员身份由签名证明，绝不采信客户端的自称。
- `--relay-deny FILE`：列出的地址、网段和身份密钥不能使用中继（列出的密钥也不能注册为被控机）。文件变化时以及收到 SIGHUP 时会重新读取。
- `--relay-monthly-gb N`：每月流量预算，重启后继续累计。达到 80% 时匿名会话的带宽减半；达到 100% 时拒绝匿名会话；达到 110% 时拒绝所有会话。`remohop signal --status` 显示当月用量，每小时一行日志汇总会话数、字节数和拒绝次数，不含地址。

这些选项的完整说明以 [public-server.md](../public-server.md)（英文）为准，那里还说明了这样的运营者能看到和看不到什么，并附有隐私声明模板。
