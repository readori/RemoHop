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
| 一次性密码 | `remohop config otp on` | 临时密码每次登录后更换；`remohop otp` 打印当前密码，`remohop otp new` 立即更换 |
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
