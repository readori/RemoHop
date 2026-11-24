# 账号（可选）

[English](../en/accounts.md)

账号只把你自己的设备归在一起，让每台设备能列出其他设备并看到谁在线（`remohop devices`）。账号不会授予任何访问权：连接被控机仍然需要被控机接受的密码、配对令牌或密钥。

账号托管在汇合服务器上，由服务器运营者用 `remohop signal --accounts` 开启（还需要 SMTP 和允许的邮箱范围，见 `remohop help signal`）。

```bash
remohop login me@example.com
remohop account
remohop account approve 3f9a:01bc:77d2:e410:9c55
remohop account remove old-laptop
remohop logout
```

- 登录码由服务器通过邮件发送：8 位数字，10 分钟有效，只能用一次。
- 第一台登录的设备创建账号。之后每台新设备都必须由账号里已有的设备批准。
- 加入设备要在两台设备上各比对一次完整指纹：新设备显示**账号指纹**，请确认它与旧设备上 `remohop account` 显示的一致；新设备还会打印**自己的身份指纹**，在旧设备上用 `remohop account approve 指纹` 输入完整的值，不接受前缀。
- 成员列表是一条哈希链接、由账号内设备签名的日志。服务器只负责保存和转发，没有任何成员密钥，所以服务器不能自行添加设备。
- 默认不信任账号设备。被控机的主人可以用 `remohop config account-trust on` 选择让本账号的其他设备像 `remohop authorize` 授权的密钥一样登录；被控机每分钟左右检查一次成员列表。用 `remohop account remove` 移除设备后，开启了该选项的被控机会在下一次检查时停止接受它。
- 服务器仍然可以拒绝服务，也可以对某台设备隐瞒最新的日志条目（例如一次移除）。被控机在连不上服务器时最多沿用上次验证过的列表 7 天，之后不再信任任何账号设备。完整说明见 [security.md](../security.md#accounts)。
