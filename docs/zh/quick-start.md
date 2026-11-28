# 快速开始

[English](../en/quick-start.md)

下面的步骤假设有一台带公网地址的机器运行汇合服务器（下文用 `sig.example.net` 表示），另有两台机器：被控机和你自己的工作机。

命令行没有内置的汇合服务器。除了自己运行一台（第 2 步），也可以使用官方服务器 `signal.remohop.com`，桌面应用和快速协助首次启动时提供的就是它：把下文的 `sig.example.net` 换成它即可（`remohop config set signal signal.remohop.com`）。完全不用服务器的用法见下文“不使用汇合服务器”一节。

## 1. 安装

在每台要用它的机器上安装 `remohop`（见[安装](install.md)），不需要其他软件。

## 2. 运行汇合服务器

在一台有公网 IP 的机器上开放两个 UDP 端口（默认 `21116` 和 `21117`），然后运行：

```bash
remohop signal
```

服务器启动时会打印自己的公钥，客户端可以固定这把公钥（见 [security.md](../security.md#rendezvous-server)）。如果还想为无法直连的会话提供中继，用 `remohop signal --relay` 启动（见[中继](how-it-works.md#中继)）。在小虚拟机上长期运行见[自建汇合服务器](self-hosting.md)。

## 3. 启动被控机

在要被控制的机器上：

```bash
remohop config set signal sig.example.net
remohop host
```

`remohop host` 在前台运行，输出类似下面的内容（ID 和密码由程序生成）：

```
RemoHop host ready
  ID:        276 941 009
  Password:  xrb8-upuu  (temporary, changes on restart)
  Direct:    203.0.113.7:51820
  Identity:  7197:70c3:1130:65a3:9c97
Connect with `remohop 276941009 <command>`.
```

只有输出是终端时才显示密码；输出到文件或日志时，横幅显示 `(not shown: ...)`，可用 `remohop status --show-password` 查看密码。ID 属于主机的身份密钥，重启后保持不变。临时密码每次启动都会重新生成。需要固定密码时执行 `remohop passwd`：它以不回显的方式询问密码，之后也不会再把它打印出来。

## 4. 或者把被控端安装为系统服务

如果希望开机或登录后自动运行，不必一直开着终端：

```bash
remohop service install
remohop status --show-password
```

`remohop service install` 会写入 systemd 单元（Linux）、launchd 任务（macOS）或 Windows 服务，设为自启并立即启动。服务有自己独立的身份、ID 和密码，与手动启动的 `remohop host` 不是同一个实例，用 `remohop status` 查看。系统级服务（`--system`）和 Windows 服务看不到桌面会话，所以需要截图或远程桌面时请使用用户级服务（非 root 时的默认方式）。在 Windows 上，`remohop service install --unattended` 让服务的客户端看到并控制控制台会话，包括登录界面、锁屏和 UAC 提示（默认关闭；请先阅读 [security.md](../security.md#windows-unattended-access)）。各平台的细节见 `remohop help service`。

## 5. 从你的机器连接

```bash
remohop config set signal sig.example.net
remohop 276941009 ping
remohop 276941009 pair --name dev
remohop dev info
```

- `ping` 显示到被控机的路径。第一次连接需要密码，所以它会提示输入。
- `pair --name dev` 只需输入一次密码，随后在你的机器上保存一个绑定到本机身份的配对令牌，并保存别名 `dev`。
- 配对之后，`remohop dev ...` 无需密码即可连接。`remohop devices` 列出已保存的主机以及它们是否在线。

## 不使用汇合服务器（局域网或公网 IP 直连）

如果能直接通过地址访问被控机，就不需要汇合服务器。在被控机上：

```bash
remohop host --no-signal --listen :21118
```

在客户端上用地址代替 ID，仍然需要密码：

```bash
remohop 192.168.1.5:21118 pair --name lab
remohop lab info
```

### 查找本网络中的被控机

不必知道地址。`remohop lan` 在大约两秒内列出本地网络中的被控机，不需要汇合服务器，也不需要任何设置：

```
$ remohop lan
NAME     ADDRESS             ID           HOST IDENTITY             SAVED HERE
studio   192.168.1.5:21118   -            9b2e:771c:0aa4:d3f1:1234  lab = 192.168.1.5:21118 (paired)
nas      192.168.1.9:40213   276 941 009  0c41:9d2a:77e0:12f8:aa03  -
```

- 用列出的地址连接：`remohop 192.168.1.9:40213 info`。被控机在汇合服务器上注册过时会显示 ID；*SAVED HERE* 表示本机已经以什么方式保存了它（按身份密钥对应）。`--json` 供脚本使用；`--wait 5s` 会多等一会儿。
- 运行 `remohop host`（无论是否使用服务器）、打开了“允许别人连接”的[桌面应用](desktop-app.md)的电脑会被列出；快速协助只在不使用汇合服务器时被列出（用服务器时，协助者用它的 ID 和临时密码连接）。移动端应用暂时不会公布自己。
- 被控机关于自己的说法只是提示。连接时照常核对它的密钥，以 ID 保存的被控机仍使用已固定的密钥。如果某台机器自称是已保存的某个 ID，公布的却是另一个密钥，`remohop lan` 会警告它可能是冒充的。
- 它使用组播 DNS（mDNS，即 Bonjour 和 Avahi 所用的协议），UDP 端口 5353，所以能用它们的地方就能用：防火墙挡住这个端口，或 Wi-Fi 网络隔离了各个客户端时，就找不到被控机。在 Windows 上，请在专用网络中允许 `remohop` 通过防火墙。
- 被控机只公布它的名称、监听的 UDP 端口、ID（如果有）和密钥指纹，不含任何机密，并且只回答与它在同一子网中的机器。关闭方法：在被控机上执行 `remohop config lan-discovery off`（立即生效），或设置 `REMOHOP_LAN_DISCOVERY=0`。绑定在回环地址上的被控机从不回答。另见 [security.md](../security.md#lan-discovery)。

## 状态与设备

```bash
remohop dev info
remohop dev ping -c 10
remohop status
remohop devices
```

`info` 描述远程机器。`ping` 测量往返时间并报告路径类型：`lan`、`direct`、`punched` 是点对点路径，`relayed` 表示经过汇合服务器的中继。`remohop status` 显示本机守护进程的状态、ID 和当前会话。`remohop devices` 列出已保存的主机，向汇合服务器查询其中哪些在线，并显示正在连向每台主机的后台命令。
