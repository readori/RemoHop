# Port forwarding and Wake-on-LAN

[中文](../zh/forwarding.md)

Every remote command has the form `remohop <ID> <command>`. `dev` is the example alias from `remohop <ID> pair --name dev` (see [Quick start](quick-start.md)). Run `remohop help <command>` for the full syntax.

Map a remote service to localhost (like `ssh -L`), carry UDP, or expose a local service to the remote machine (like `ssh -R`, TCP only):

```bash
remohop dev forward -b 18080:8080
remohop dev forward -b --udp 5353:53
remohop dev forward -R 9000:3000
remohop forward
remohop unforward 18080
```

`remohop forward` without a SPEC lists the running forwards; `remohop unforward PORT` stops one. `remohop ps` lists every background command (forwards, mounts, serial ports, clipboard syncs, VPN tunnels) and `remohop stop PID` ends one.

Wake a sleeping computer on your own network, or have an always-on host wake one on its network:

```bash
remohop wake --save nas aa:bb:cc:dd:ee:ff
remohop wake nas
remohop dev wol nas
```
