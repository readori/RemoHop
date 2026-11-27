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

## 状态与设备

```bash
remohop dev info
remohop dev ping -c 10
remohop status
remohop devices
```

`info` 描述远程机器。`ping` 测量往返时间并报告路径类型：`lan`、`direct`、`punched` 是点对点路径，`relayed` 表示经过汇合服务器的中继。`remohop status` 显示本机守护进程的状态、ID 和当前会话。`remohop devices` 列出已保存的主机，向汇合服务器查询其中哪些在线，并显示正在连向每台主机的后台命令。
