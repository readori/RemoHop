# 串口与 USB 设备

[English](../en/serial-and-usb.md)

远程命令的格式都是 `remohop <ID> <command>`。本页的例子用 `dev` 作为别名，它来自 `remohop <ID> pair --name dev`（见[快速开始](quick-start.md)）。每个命令的完整语法用 `remohop help <command>` 查看。

把远程机器的串口变成本机的虚拟串口，任何终端程序都能打开：

```bash
remohop dev serial list
remohop dev serial -b ttyUSB0 115200 --local /tmp/board
picocom /tmp/board
remohop unserial /tmp/board
```

虚拟串口需要本机是 Linux 或 macOS；远程一侧可以是 Linux、macOS 或 Windows。本地程序修改波特率或帧格式时，改动会同步到远程串口。

像本地设备一样使用插在远程机器上的 USB 设备（USB/IP；两端都必须是 Linux 且有 root 权限，被控机需要内核模块 `usbip-host`，客户端需要 `vhci-hcd`）：

```bash
remohop dev usb list
remohop usb allow 1-2
sudo remohop dev usb attach -b 1-2
remohop usb
sudo remohop usb detach 1-2
```

`remohop usb allow` 在被控机上执行：被控机的主人必须逐个允许设备。键盘、鼠标、存储设备和集线器需要 `--force`，集线器永远不会被导出。
