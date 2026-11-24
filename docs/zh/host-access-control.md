# 被控端访问控制

[English](../en/host-access-control.md)

被控机的主人决定已连接的客户端能做什么。下面的命令在被控机上执行，立即生效，对已经连上的会话同样有效：

```bash
remohop config feature input off
remohop config ask on
remohop config otp on
```

| 控制项 | 命令 | 效果 |
|---|---|---|
| 功能开关 | `remohop config feature NAME on\|off` | NAME 可以是 `shell`、`file`、`forward`、`clip`、`screen`、`input`、`info`、`vpn`、`serial`、`usb`，默认全部开启 |
| VPN 路由 | `remohop config vpn-exit on` | 允许 VPN 客户端访问被控机自身局域网以外的网络，并把被控机当作出口；默认关闭 |
| 连接审批 | `remohop config ask on` | 每次密码登录都要在被控机上确认：在 `remohop host` 所在的终端回答，或用 `remohop host approve N` / `remohop host deny N` |
| 已知客户端也要审批 | `remohop config ask-paired on` | 在 `ask on` 的基础上，已配对的客户端和已授权的密钥也需要审批 |
| 一次性密码 | `remohop config otp on` | 临时密码每次登录后更换，多个同时使用它的登录只有一个能成功；被拒绝、无人应答或中途放弃的登录不会消耗它；`remohop otp` 打印当前密码，`remohop otp new` 立即更换 |
| 固定密码 | `remohop passwd` | 设置固定密码，`--clear` 清除；`remohop status` 从不打印固定密码 |
| 配对管理 | `remohop host paired`、`remohop host revoke NAME` | 列出、移除已配对的客户端和已授权的密钥 |
| USB 设备 | `remohop usb allow BUSID`、`remohop usb allowed` | 客户端挂接某个设备之前，必须先允许它 |
| 账号设备 | `remohop config account-trust on` | 让本账号的其他设备像已授权的密钥一样登录，默认关闭（见[账号](accounts.md)） |
| 审计日志 | `remohop log` | 登录成功与被拒、审批结果、每个会话首次使用某项功能、会话结束 |

另外两项设置：`remohop config set name office-pc` 给设备起一个名字，对方会在 `ping`、审批提示和 `remohop status` 中看到它；`remohop config set term-keep 2h` 修改分离后的 shell 在被控机上保留多久。

客户端也可以只凭身份密钥登录，完全不存在可被猜测的密码。在客户端打印公钥，在被控机上授权它，再回到客户端记录 `authorize` 打印出的主机密钥行：

```bash
remohop key export
remohop authorize "remohop-key ... laptop"
remohop peers trust 276941009 "remohop-key ... host"
```

第一条和第三条在客户端执行，第二条在被控机上执行。`remohop key export --private FILE` 和 `remohop key import FILE --yes` 用于把身份迁移到另一台机器，`remohop key gen --yes` 生成新的身份。每项控制分别防范什么，见 [security.md](../security.md)。

## 开关对已连接会话的作用

关闭某项功能后，新的请求立即被拒绝，已连接的客户端用这项功能打开的东西也会结束。用 `remohop config` 做的修改，被控端在四分之一秒内察觉；[快速协助窗口](quick-support.md)的开关在点击返回之前就已生效。不会自行结束的流在半秒后被切断。

| 关闭的功能 | 已经打开的部分 |
|---|---|
| `input` | 已打开的远程桌面继续显示画面。被控端松开协助者按住的所有按键和鼠标按钮，此后不再接受协助者的键盘鼠标（包括“输入文字”、锁屏等主机控制），并通知查看器把会话显示为仅查看。重新打开后，同一个桌面会话恢复控制。仍在执行的输入命令（`remohop <ID> type`、`click` 等）会结束。 |
| `screen` | 远程桌面、桌面声音和截图结束；查看器会说明是被控端主人关闭了屏幕。 |
| `shell` | 正在运行的命令（`remohop <ID> shell -c ...`）连同其进程组被终止。已连接的终端在显示一条提示后结束（shell 被挂断，不保留以供重新连接）。此前已经分离的终端和后台任务（`spawn`）像客户端断开时一样继续在被控端运行，但在 `shell` 重新打开之前，任何客户端都无法访问它们。 |
| `file` | 文件会话（浏览、`mount`）和进行中的传输结束。被中断的上传不会写入目标文件。 |
| `clip` | 剪贴板的流结束。 |
| `forward` | 转发的连接被切断，被控端上的反向转发监听被关闭，UDP 转发和唤醒请求结束。 |
| `vpn` | 隧道结束。 |
| `serial`、`usb` | 串口被关闭，已挂接的 USB 设备交还给被控端。 |
| `info` | 系统信息请求结束。 |

以授权（grant）登录的会话，在授权不再允许某项服务时也是如此。重新打开功能不会自动恢复任何东西，需要客户端重新打开（上面桌面的键盘鼠标是例外）。
