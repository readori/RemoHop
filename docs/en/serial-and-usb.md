# Serial ports and USB devices

[中文](../zh/serial-and-usb.md)

Every remote command has the form `remohop <ID> <command>`. `dev` is the example alias from `remohop <ID> pair --name dev` (see [Quick start](quick-start.md)). Run `remohop help <command>` for the full syntax.

Use a serial port of the remote machine as a local virtual port that any terminal program can open:

```bash
remohop dev serial list
remohop dev serial -b ttyUSB0 115200 --local /tmp/board
picocom /tmp/board
remohop unserial /tmp/board
```

The virtual port needs Linux or macOS on this side; the remote side may be Linux, macOS or Windows. Baud rate and framing changes made by the local program follow to the remote port.

Use a USB device plugged into the remote machine as if it were local (USB/IP, Linux and root on both sides, kernel modules `usbip-host` on the host and `vhci-hcd` on the client):

```bash
remohop dev usb list
remohop usb allow 1-2
sudo remohop dev usb attach -b 1-2
remohop usb
sudo remohop usb detach 1-2
```

`remohop usb allow` runs on the host: its owner must allow each device first. Keyboards, mice, storage and hubs need `--force`, and hubs are never exported.
