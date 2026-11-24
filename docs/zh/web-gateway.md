# 浏览器客户端

[English](../en/web-gateway.md)

`remohop web` 在本机提供一个浏览器客户端（远程桌面、终端、文件、剪贴板和系统信息）：

```bash
remohop web
remohop web --target dev
```

页面有简体中文和英文两种语言：默认跟随浏览器语言（任何 `zh-*` 都显示中文），页面顶部的“中文 / English”开关可随时切换，选择会记在这个浏览器里。在地址后加 `?lang=zh` 或 `?lang=en` 也可以指定语言。

默认只监听 `127.0.0.1`，并打开一个带有不可猜测令牌的地址，这个令牌就是登录凭据。浏览器本身无法建立 P2P 会话，所以这个进程才是真正的客户端，页面里的一切它都能看到。监听非回环地址时必须同时提供 `--tls-cert`、`--tls-key` 和 `--password-file`；供多人共用的网关应加上 `--no-saved`。见 [security.md](../security.md#browser-gateway)。

## 通过 Cloudflare Tunnel 对外提供

如果要在外网使用 `remohop web`，让它只监听本机回环地址的固定端口，用 `cloudflared` 把隧道指向它，并在前面加一条 Cloudflare Access 策略。`--public-url` 告诉网关浏览器实际使用的 https 地址，网关据此接受隧道转发过来的 `Host` 和 `Origin` 头。网关端已有单元测试模拟隧道请求，但没有接真实的 Cloudflare 测试过。

```bash
remohop web --listen 127.0.0.1:8470 --password-file /etc/remohop/web-pass \
        --public-url https://remohop.example.com --no-open --no-saved
cloudflared tunnel run --url http://127.0.0.1:8470 <隧道名>
```

`--public-url` 要求回环监听地址和 `--password-file`。经公网地址来的请求，登录限速按 Cloudflare 填写的 `CF-Connecting-IP` 区分访客。

- 在 Cloudflare Zero Trust 里为这个主机名创建 Access 应用，只允许你自己的身份或域。
- 保留 `remohop web` 自己的登录（`--password-file`）。Access 挡在前面，但网关仍然能看到页面里的一切。
- 浏览器与网关之间的这一段经过 Cloudflare，它终止 TLS，因此能看到屏幕、按键、文件和你在页面里输入的主机密码。只有接受这一点才这样做。
- 供多人使用时加上 `--no-saved`，否则任何登录网关的人都能使用网关里保存的配对。
- Cloudflare Tunnel 只转发 HTTP(S)。汇合服务器本身使用 UDP；它为只能用 HTTPS 出网的机器提供的 websocket 桥可以走隧道（见[只允许 HTTPS 出网的网络](self-hosting.md#只允许-https-出网的网络websocket-桥)）。

## 程序客户端（Bearer 令牌）

程序接入默认关闭。用 `--api-token-file FILE` 启动网关（文件权限 600，至少 8 个字符，需要同时有 `--password-file`）后，程序客户端（不是浏览器）可以不调用登录接口，在每个请求里带 `Authorization: Bearer <令牌>`。这适合由代理注入请求头、程序本身看不到令牌的场景，例如在 Cloudflare Access 服务令牌之外再注入这个令牌。这类请求共用一个网关会话，不需要 Cookie、CSRF 令牌和 Origin，失败次数计入登录限速。没有 `--api-token-file` 时，Bearer 请求会得到 `bearer_disabled` 错误。

```bash
remohop web --listen 127.0.0.1:8470 --password-file /etc/remohop/web-pass \
        --api-token-file /etc/remohop/web-api-token \
        --public-url https://remohop.example.com --no-open --no-saved
```

网关无法知道 `Authorization` 头是谁加上的。因此，带有浏览器特征（`Origin`、`Sec-Fetch-Site` 或 `Sec-Fetch-Mode`、Cookie）的 Bearer 请求同样要通过浏览器检查：来自其他站点的请求被拒绝，改变状态的调用需要带上 `POST api/v1/session` 返回的 CSRF 令牌，带 `Origin` 的 WebSocket 和下载必须来自网关自己的地址。会保存 Cookie 的程序（例如带着 Cloudflare 的 `CF_Authorization`）要么不把 Cookie 发给网关，要么同时发送 CSRF 令牌。

**如果由代理注入令牌，只对程序流量注入。** 不要给到达这个主机名的所有请求都加上这个头：那样浏览器会被代理自动登录，上面的检查就成了恶意网页与网关之间唯一的屏障（不发送 `Origin` 和 `Sec-Fetch-*` 的旧浏览器不在保护范围内）。给程序单独的主机名或路径，只在那里、只对程序的身份注入（在 Cloudflare 中，用匹配服务令牌的规则，而不是放行你浏览器的 Access 策略）。浏览器继续使用登录页面。

**迁移。** 在 `--api-token-file` 出现之前，只要有 `--password-file`，网关就把网关密码当作 Bearer 令牌接受。这样的部署在加上 `--api-token-file` 之前无法使用（程序会收到 `bearer_disabled`）。把它指向密码文件可以让旧令牌继续有效；更好的做法是生成新令牌，更新注入它的代理规则，网关密码只留给浏览器使用。
