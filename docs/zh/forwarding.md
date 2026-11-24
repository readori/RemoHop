# 端口转发与网络唤醒

[English](../en/forwarding.md)

远程命令的格式都是 `remohop <ID> <command>`。本页的例子用 `dev` 作为别名，它来自 `remohop <ID> pair --name dev`（见[快速开始](quick-start.md)）。每个命令的完整语法用 `remohop help <command>` 查看。

把远程服务映射到本机（相当于 `ssh -L`）、转发 UDP，或者把本机服务暴露给远程机器（相当于 `ssh -R`，仅支持 TCP）：

```bash
remohop dev forward -b 18080:8080
remohop dev forward -b --udp 5353:53
remohop dev forward -R 9000:3000
remohop forward
remohop unforward 18080
```

不带 SPEC 的 `remohop forward` 列出正在运行的转发，`remohop unforward PORT` 停止其中一个。`remohop ps` 列出所有后台命令（转发、挂载、串口、剪贴板同步、VPN 隧道），`remohop stop PID` 结束其中一个。

唤醒本地网络里休眠的电脑，或者让一台常开的被控机在它所在的网络里发送唤醒包：

```bash
remohop wake --save nas aa:bb:cc:dd:ee:ff
remohop wake nas
remohop dev wol nas
```
