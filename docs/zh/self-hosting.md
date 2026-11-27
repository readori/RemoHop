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

## 运营者网页面板

`--panel ADDR` 为服务器的运营者增加一个只读网页：在线主机和已存 ID 数、打开的连接和 websocket 桥、中继（当前和今天的会话、本月流量与预算对比、每日用量、按原因统计的拒绝次数）、最近 24 小时和 7 天的趋势、当前生效的限额以及服务器离各项限额有多近、内存和 goroutine 数，以及日志中最近的警告和错误。默认关闭。

面板只显示计数和汇总，从不列出 IP 地址、主机 ID、账号邮箱或密钥；日志记录在送到面板之前已去除这些内容。它的历史数据（每分钟一个样本保留 24 小时，每 15 分钟一个点保留 7 天）写在 `--data` 目录的 `panel-history.json` 里，重启后仍在。

```bash
remohop signal --relay --relay-ports 40000-40099 --panel 127.0.0.1:8810
sudo remohop service install --role signal --relay --relay-ports 40000-40099 --panel 127.0.0.1:8810
```

谁可以打开：

- **在服务器本机，无需登录。** 面板监听回环地址。从你自己的电脑通过 SSH 访问，然后打开 `http://127.0.0.1:8810/`：

  ```bash
  ssh -L 8810:127.0.0.1:8810 server
  ```

  回环请求的 Host 必须是 `127.0.0.1` 或 `localhost`，这样网页无法通过 DNS 重绑定访问面板。
- **通过 Cloudflare Tunnel 和 Cloudflare Access**，在任何地方用浏览器打开。凡是经过 Cloudflare 的请求（带有 `Cf-Ray`、`Cf-Connecting-Ip` 或 `Cf-Access-Jwt-Assertion` 头）都必须带有效的 Access 令牌，放在该请求头或 `CF_Authorization` cookie 里。面板用团队的公钥（`https://TEAM/cdn-cgi/access/certs`，缓存一小时，遇到新的 key ID 时重新获取）校验 RS256 签名，并检查受众（应用的 AUD 标签）、签发者（`https://TEAM`）和有效期（允许少量时钟偏差）。设置了邮箱名单时，令牌中的邮箱必须在名单上。面板顶部显示当前登录的邮箱。没有设置 `REMOHOP_PANEL_CF_TEAM` 和 `REMOHOP_PANEL_CF_AUD` 时，经过 Cloudflare 的请求一律拒绝，所以没有 Access 保护的隧道永远打不开面板。
- **用密码。** 设置 `REMOHOP_PANEL_PASSWORD` 后，除 `/healthz` 外的每个页面都要求输入它（HTTP Basic，用户名任意）。没有密码时，拒绝在回环以外的地址上监听（例如 `--panel 0.0.0.0:8810`）。更推荐回环加隧道：普通 HTTP 上的 Basic 认证是明文传输的。

| 环境变量 | 含义 |
|---|---|
| `REMOHOP_PANEL_CF_TEAM` | Cloudflare Zero Trust 团队域名，例如 `myteam.cloudflareaccess.com` |
| `REMOHOP_PANEL_CF_AUD` | Access 应用的 Audience（AUD）标签 |
| `REMOHOP_PANEL_CF_EMAILS` | 可选，逗号分隔：只允许这些邮箱（Access 策略仍先生效） |
| `REMOHOP_PANEL_PASSWORD` | 可选：所有页面的密码；在回环以外监听时必需 |

这些设置只用环境变量，不用命令行参数，因此不会出现在命令行里。安装为服务时，把它们写进 `/etc/remohop/remohop-signal-panel.env`（服务启动时读取；没有这个文件也能启动），只让 root 可读，然后重启：

```bash
sudo install -m 600 /dev/null /etc/remohop/remohop-signal-panel.env
sudoedit /etc/remohop/remohop-signal-panel.env       # 每行一个，例如 REMOHOP_PANEL_CF_TEAM=...
sudo systemctl restart remohop-signal
```

页面是内置在程序里的纯 HTML、CSS 和 JavaScript：不用 CDN、不用网络字体，有严格的内容安全策略，禁止被嵌入框架，数据不缓存，只接受 `GET` 请求。界面语言跟随浏览器（中文或英文），主题跟随系统的浅色或深色，两者都可在侧栏切换。

### Cloudflare Tunnel 与 Access

1. 在 Cloudflare Zero Trust 控制台添加一个 *Self-hosted* 类型的 Access 应用，主机名例如 `panel.example.com`，策略允许你的团队。复制它的 *Application Audience (AUD) tag*。
2. 用隧道把这个主机名转发到面板。服务器上 `cloudflared` 的 `config.yml`：

   ```yaml
   tunnel: <tunnel-id>
   credentials-file: /etc/cloudflared/<tunnel-id>.json
   ingress:
     - hostname: panel.example.com
       service: http://127.0.0.1:8810
     - hostname: relay.example.com          # websocket 桥（如果使用）
       service: http://127.0.0.1:8080
     - service: http_status:404
   ```

3. 用 `--panel 127.0.0.1:8810` 启动服务器，并设置 `REMOHOP_PANEL_CF_TEAM=myteam.cloudflareaccess.com` 和 `REMOHOP_PANEL_CF_AUD=<AUD 标签>`（需要的话再设置 `REMOHOP_PANEL_CF_EMAILS`）。

Cloudflare Access 是第一道锁，面板自己校验令牌是第二道：绕过 Access 到达隧道的请求也会被面板拒绝。

### Docker

容器里的回环地址只属于容器自己。有两种方式访问面板：

- **主机网络**（Linux）：使用 `network_mode: host` 时，`--panel=127.0.0.1:8810` 监听在宿主机的回环地址上，SSH 隧道和宿主机上的 `cloudflared` 都按上文的方式使用，不需要密码。
- **默认的 bridge 网络**：面板必须在容器内监听 `0.0.0.0:8810`，因此需要 `REMOHOP_PANEL_PASSWORD`。只把它发布到宿主机的回环地址（`127.0.0.1:8810:8810`），或让同一网络中的 `cloudflared` 容器访问它（`service: http://signal:8810`），并设置 Cloudflare 相关变量。设置了密码后，每个页面都会要求输入，在 Access 后面也一样。

在 `packaging/docker/compose.yaml` 中：

```yaml
services:
  signal:
    command:
      - --relay
      - --relay-ports=40000-40099
      - --panel=0.0.0.0:8810
    environment:
      REMOHOP_PANEL_PASSWORD: ${REMOHOP_PANEL_PASSWORD:?在 .env 中设置}
      REMOHOP_PANEL_CF_TEAM: ${REMOHOP_PANEL_CF_TEAM:-}
      REMOHOP_PANEL_CF_AUD: ${REMOHOP_PANEL_CF_AUD:-}
    ports:
      - "127.0.0.1:8810:8810"   # 面板：只在宿主机回环地址上
```

`GET /healthz` 无需登录，返回 `ok`，可用于健康检查。
