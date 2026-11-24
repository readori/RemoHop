# VPN

[中文](../zh/vpn.md)

Every remote command has the form `remohop <ID> <command>`. `dev` is the example alias from `remohop <ID> pair --name dev` (see [Quick start](quick-start.md)). Run `remohop help <command>` for the full syntax.

`remohop <ID> vpn up` joins this machine and the host with a virtual IPv4 link inside the encrypted session: this side is `10.77.0.2`, the host `10.77.0.1`. On Linux both sides need root or `CAP_NET_ADMIN`; on Windows, Administrator.

```bash
sudo remohop dev vpn up -b
sudo remohop dev vpn up --route 192.168.1.0/24 -b
sudo remohop dev vpn up --exit --bypass 10.0.0.0/8
remohop vpn
sudo remohop dev vpn down
```

- `--route CIDR` reaches a network on the host's side; the host forwards for it.
- `--exit` sends all of this machine's traffic through the host.
- Routes beyond the host's own networks and `--exit` need `remohop config vpn-exit on` on the host, which is off by default. The host can switch the whole feature off with `remohop config feature vpn off`.
- Every interface, route and firewall rule the tunnel creates is removed when it ends. If `remohop` is killed outright, the next `remohop` command run with the same privilege removes what was left.

Linux on both ends is the tested combination. Other platforms:

- **Windows** can be the controlling machine (with `--route` and `--exit`) and can be reached itself. It cannot forward for `--route` or `--exit` as a host. It needs the Wintun driver, which `remohop` neither includes nor downloads. Get Wintun 0.14.1 from <https://www.wintun.net>, check the zip's SHA-256 (`07c256185d6ee3652e09fa55c0b673e2624b565e02c4b9091c79ca7d2f24ef51`), and put `wintun\bin\<arch>\wintun.dll` next to `remohop.exe` or in the remohop config directory. `REMOHOP_WINTUN_DIR` names another directory to search. `remohop` also checks the DLL's own SHA-256 and refuses a file that does not match. The end-to-end test needs a real Windows machine and is skipped by default; this is not verified.
- **macOS** uses utun. The client (including `--route`), and a macOS host that is reached itself, are verified by automated tests on a hosted macOS VM: a real utun interface, routes, packets both ways, and clean-up after `vpn down` and after a killed process. `--exit` has not been run on a real network (its exception routes are tested, taking over the default route is not), and nothing has been run on a physical Mac. As a host it cannot forward for VPN clients.
