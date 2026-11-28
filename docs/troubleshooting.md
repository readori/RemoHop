# Troubleshooting

This page covers the errors `remohop` prints when connecting to a host, how to read `remohop <ID> ping` and `remohop status`, NAT traversal and the relay, and the features with platform requirements: services, mounts, VPN, serial and USB, the screen and GUI input, the browser gateway and ffmpeg. Messages are quoted as the program prints them; `<ID>` stands for the 9-digit ID and `dev` for an alias.

Exit status: `remohop` exits with 255 when it cannot connect or the connection fails. For `shell -c`, it exits with the remote command's status, 124 when `--timeout` expires, and 130 when interrupted locally.

## Debugging first

Run the failing command with `-v`, or set `REMOHOP_DEBUG=1`, to get debug logs on stderr:

```bash
remohop -v 276941009 info
```

On the host, three commands show what it is doing:

```bash
remohop status
remohop log 20
remohop service status
```

- `remohop status` shows whether the host daemon runs, its name and ID (or the reason it has none), the password mode, the listen address, features that are switched off, the approval mode and live sessions with their path. Add `--show-password` to print a temporary password; a fixed password is never shown.
- `remohop log` shows the audit log: logins accepted and refused with the reason, approval decisions and feature use.
- `remohop service status` and `remohop service logs -f` cover a host installed with `remohop service install`.

`remohop doctor` puts the network situation on one page, on either side: the rendezvous server (does it answer over UDP, which NAT type), the addresses this machine offers to peers, a running Tailscale and what it changes, the mesh network (`remohop net status`, [net.md](net.md)) and VPN tunnels, followed by advice. `--offline` skips contacting the server.

On the client, `remohop devices` shows each saved host, whether the rendezvous server sees it online (hosts saved by address show `not checked`), and the background commands running to it. `remohop ps` lists every background command on this machine.

## Reading `remohop <ID> ping`

```
PING 127.0.0.1:47301 over direct path 0.0.0.0:41009 -> 127.0.0.1:47301 (peer-to-peer, no relay)
reply from 127.0.0.1:47301: seq=0 time=0.14 ms
1 probes, rtt min/avg/max = 0.14/0.14/0.14 ms
the host calls itself "labhost"
the host sees this machine as 127.0.0.1:41009
```

The first line names the path:

| Path | Meaning |
|---|---|
| `lan` | direct to the host's private address on the same network |
| `direct` | direct to the host's own public address, or to a HOST:PORT you gave |
| `punched` | through NAT mappings opened by UDP hole punching |
| `relayed` | no direct path exists and the rendezvous server passes the packets |

The first three are peer to peer. A relayed session is as encrypted and authenticated as any other, but slower and subject to the relay's bandwidth cap. It is announced on stderr when it is set up (`REMOHOP_QUIET=1` silences the notice). `the host calls itself` shows the name set with `remohop config set name` on the host, if any.

## Connection errors

### Target and rendezvous

**`no rendezvous server configured. Choose one:`** (or **`... is a RemoHop ID, and IDs are looked up on a rendezvous server ... None is set up here. Choose one:`**)

You used an ID, but this machine has no rendezvous server, and there is no built-in default. The message lists the choices, one per line:

```
  official server: remohop config set signal signal.remohop.com  (as offered by the desktop app and Quick Support)
  your own server: remohop config set signal HOST[:PORT]  (start one with `remohop signal`)
  same network:    remohop HOST:PORT <command>  (no server needed)
```

The official server is the one the desktop app and Quick Support offer on their first start; nothing uses it unless you choose it. Without a server, connect by address, for example `remohop 192.168.1.5:21118 info`, with the host started as `remohop host --no-signal --listen :21118`.

**`no host with ID 276 941 009 is known to the rendezvous server .... Check the ID with the person at that computer ...`**

Check the digits (IDs are printed with spaces; both forms work) and check that both machines use the same server. A host installed as a service has a different ID from a `remohop host` started by hand on the same machine, and `remohop key gen` on a host gives it a new ID.

**`host 276 941 009 is offline: it is not connected to the rendezvous server .... Check that RemoHop is running there ...`**

The ID exists, but the host is not registered right now. On the host, run `remohop status`; while it cannot register it shows the reason in the ID line, and the daemon keeps retrying. `remohop doctor 276941009` asks the server about that ID from here, without logging in.

**`host ... is registered with the rendezvous server but did not answer. Try again in a moment ...`**

Usually transient. Try again, and check `remohop status` on the host if it persists.

**`rendezvous server ... unreachable: it did not answer. Check the address and port ...`** or **`rendezvous server key ... does not match the pinned key`**

Check the address and port (`remohop config get signal`), that `remohop signal` runs there, and that the server's two UDP ports (21116-21117 by default) are open to the internet, in its firewall and its cloud provider's. `remohop doctor` goes through these. If the key differs, confirm that the server was replaced, then update the pin with `remohop config set signal-pin KEY` or remove it with `remohop config unset signal-pin`.

**`unknown target "x": expected a 9-digit ID, a saved alias (see `remohop peers`) or HOST:PORT`**

List saved names with `remohop peers` or `remohop devices`.

**`no remohop host answered at <address>. Check the address and port ...`**

Nothing answered at a HOST:PORT target. Check the address, the port given to `--listen`, and firewalls for UDP.

### Credentials and approval

**`no credential for this host (...): no password given and no saved pairing; ...`**

Run `remohop <ID> pair` once in a terminal and type the password. Scripts and agents cannot answer a prompt, so they need a pairing or an authorised key first.

**`login to <ID> failed: wrong password (or the connection is being intercepted)`**

Check the current password on the host with `remohop status --show-password` or `remohop otp`. A temporary password changes every time the host restarts, and after every login when `remohop config otp on` is set. A fixed password is never displayed; set a new one with `remohop passwd` if it is forgotten.

**`login to <ID> refused: locked out after too many failed attempts; retry in 10s`**

The lockout starts at 10 seconds after the third consecutive failure from your address and doubles on each further failure, up to 10 minutes. The count clears after 30 minutes without attempts. `remohop log` on the host shows who else has been trying.

**`remohop: waiting for approval on the host…`** then **`login to <ID> refused: the connection was not approved at the host (denied, or nobody answered in time)`**

The host has `remohop config ask on`. Someone has to answer at the host, in the terminal where `remohop host` runs or from any terminal of the same user:

```bash
remohop host approve
remohop host approve 1
```

The first lists waiting connections (with the client's identity and name), the second approves one; `remohop host deny N` refuses. A host running as a service has no terminal, so use `remohop host approve` there.

**`login to <ID> failed: the saved pairing was not accepted ...`** or **`... the host no longer accepts the saved pairing; ...`**

The pairing was revoked or replaced, the host's state was reset, or this machine's identity changed (`remohop key gen`, `key import` or `key clear`). Pair again with `remohop --password PW 276941009 pair --name dev`.

**`login to <ID> failed: the host has not authorised this machine's key; ...`**

This machine logs in with its key (`remohop peers trust` was used), but the host has no authorisation for it, or it was revoked. Run `remohop key export` here and `remohop authorize "LINE"` on the host again.

**`the host is busy with other logins; try again in a moment`**

Too many logins are in progress on the host. Try again.

**`host identity has changed: ... If the host was reinstalled, run `remohop peers forget <target>` and connect again`**

The host presented a different identity key than the one pinned earlier: it was reinstalled, its identity was replaced with `remohop key gen` or `key import`, you now reach a different instance (for example an installed service where a hand-started host used to answer), or someone is impersonating it. If you know the change is legitimate, run `remohop peers forget <target>` and connect again. See [security.md](security.md#host-identity-and-key-pinning).

### Features switched off

**`remote refused "exec": the host does not allow exec (feature "shell" is switched off; its owner can allow it with `remohop config feature shell on`)`**

The host's owner switched that feature off. `remohop status` on the host lists what is not allowed. Only the owner can change it, on the host, for example `remohop config feature serial on`. The features are `shell`, `file`, `forward`, `clip`, `screen`, `input`, `info`, `vpn`, `serial` and `usb`. Agents should report this message and not retry.

### No direct path

**`could not connect to <ID>: <explanation>: no direct path to the peer`**

Both machines found each other through the rendezvous server, but no UDP packet got through in either direction. When both NATs look punchable the client has already retried once on a fresh socket (or, with a relay on offer, tried the relay alongside the punch). The explanation names the NAT types detected on each side:

- `both sides are behind symmetric NATs, which cannot be hole-punched ...`: each NAT picks a new port per destination. This case fails fast, in about a second.
- `... behind a symmetric NAT whose ports cannot be predicted and the other side's NAT only accepts packets from ports it has sent to (port-restricted) ...`: the same problem in one direction. That NAT picks its ports at random, or with large gaps between them.
- `... behind a symmetric NAT that seemed to hand out its ports in sequence, so ... also probed the ports it was likely to use next, but none answered ...`: RemoHop predicted the symmetric NAT's next ports and the other side probed them, without luck. Other traffic behind that NAT moves its ports on, so retrying when that network is quieter can help. An older RemoHop on the other side does not probe predicted ports; update both sides.
- `no probe got through although both NATs look punchable. Likely causes: a firewall on either side that blocks UDP, or a NAT that keeps state for unsolicited inbound packets ...`: check host firewalls and the router. Linux hosts send low-TTL priming probes over IPv4 to get through NATs of the second kind.
- `no probe got through in either direction; a firewall may be blocking UDP, or a NAT on the path is not punchable`: UDP is blocked somewhere, often on a corporate or hotel network.

What to do, in order of reliability:

1. Forward a UDP port on the host's router and connect by address, which skips the rendezvous server and punching. On the host run `remohop host --listen :21118`; from the client, `remohop 203.0.113.7:21118 info`.
2. Use IPv6, or put one side on a different network such as a phone hotspot.
3. Use a relay. This needs the operator of the rendezvous server to run `remohop signal --relay`; nothing changes on the clients.

These messages concern the relay:

- `... (the rendezvous server offers a relay, but relaying is turned off on this machine)`: you set `remohop config relay off` or `REMOHOP_RELAY=0`. Turn it on with `remohop config relay on` if you accept a relayed session.
- `...; the relay could not be used either: ...`: each relayed session uses its own UDP port on the server, which must be reachable too; ask the operator to check the server's firewall.
- `... rendezvous server: this network already has N relayed sessions on this server ...`: a public server caps concurrent relayed sessions per address (`--relay-per-source`), counting both ends. Close another relayed session (a forward, a shell) first. A session closed normally frees its slot at once; one whose client crashed is freed after about 30 seconds.
- `... this server relays only for devices of an account on it ...`: the server runs `--relay-for accounts`. Log in with `remohop login EMAIL`, or run your own server.
- `... this server's relay budget for the month is used up ...`: the operator's monthly budget (`--relay-monthly-gb`) is spent. It resets on the 1st, UTC.
- `... the relay of this server is not available for this connection`: the operator's deny list names your address or a key.

A relayed connection prints `no direct path to <ID>: connected through the rendezvous server's relay (still end-to-end encrypted, but slower)`. It does not have to stay relayed: while the session lasts, the client keeps trying to punch in the background (5 s after it started, then 20 s and 60 s later, then every 3 minutes, and at once when its network changes), and when a path answers the session moves onto it without a break. Both sides then log `relayed session moved to a direct path after …`, and `remohop status` shows the new path. `REMOHOP_RELAY_UPGRADE=0` on the client keeps sessions on the relay; sessions over the websocket bridge stay relayed. See [how-it-works.md](en/how-it-works.md#relay). The relay's per-session cap is 8000 kbit/s by default (`remohop signal --relay-kbps`). Every new command dials again. The first one to a host that cannot be punched punches alone for 1.5 seconds, then sets up the relay alongside and uses it unless a direct path answers first (between two symmetric NATs the relay starts at once). After that, `remohop` remembers for 10 minutes that this host needed the relay from the same pair of public addresses (`relay-hints.json` in the config directory), so the next commands go to the relay at once. A change of network on either side brings punching back.

### Only HTTPS gets out (websocket bridge)

Where UDP is blocked, `rendezvous server ... unreachable` is the usual symptom. If the server operator runs the websocket bridge (`remohop signal --ws-listen`), set `remohop config set signal-ws wss://HOST/remohop/v1/udp`. `remohop` then falls back to it after 4 seconds without a UDP answer, and `REMOHOP_TRANSPORT=ws` skips the wait. `remohop -v <ID> ping` logs the fallback, and the ping line reads `relayed path (websocket)`.

- **`websocket endpoint answered HTTP/1.1 403 Forbidden (refused: behind Cloudflare Access ...)`**: Cloudflare Access rejected the request. Set `remohop config set signal-ws-access-id ID` and put the secret in the file named by `signal-ws-access-secret-file` (or `REMOHOP_SIGNAL_WS_ACCESS_SECRET`).
- **`... answered HTTP/1.1 404 ...`**: wrong path. The default is `/remohop/v1/udp`, or whatever `--ws-path` says on the server.
- **`... answered HTTP/1.1 502/503/530 ...`**: the tunnel is up but `remohop signal` is not listening on the `--ws-listen` address that `cloudflared`'s ingress points at, or the bridge is full (`--ws-max-conns`, 32 per address).
- **`proxy HOST:PORT refused to connect to ...: 407 Proxy Authentication Required`**: put the credentials in the proxy URL, `HTTPS_PROXY=http://USER:PASSWORD@proxy:3128`. Only `http://` and `https://` proxies with CONNECT are supported. `NO_PROXY` is honoured, so check it does not list the bridge's host.
- **`could not connect to <ID>: this machine reaches the rendezvous server only through its websocket bridge, and the server does not relay`**: bridged connections always need the relay. The operator must add `--relay`.
- **`... which needs the relay, but relaying is turned off on this machine`**: `remohop config relay on` (or unset `REMOHOP_RELAY=0`).
- **A host behind the same firewall** registers through the bridge too (log line `registered with the rendezvous server through its websocket bridge`; `remohop host status` says `over its websocket bridge`). Clients then always reach it through the relay.
- **`REMOHOP_TRANSPORT must be auto, udp or ws`** / **`transport ws needs the server's websocket bridge`**: fix the variable or set `signal-ws`.

### Host side

**`host: already running (pid N); see `remohop host status``**: a host daemon is already running for this config directory.

**`host: <error>` with `(or use --no-signal to serve direct connections only)`**: the host could not use its rendezvous server. Fix the address, or start with `--no-signal`.

**`password too short: need at least 8 characters (not counting spaces and dashes)`**: a fixed password must have at least 8 characters.

**`host: the host has no temporary password (password mode: fixed)`**: `remohop otp` only applies to a temporary password.

## Running your own rendezvous server

For the setup, see [Self-hosting](en/self-hosting.md) ([中文](zh/self-hosting.md)). Common problems:

- **Clients say `rendezvous server ... unreachable` but the process runs:** UDP `21116` and `21117` must be open in two places, the machine's firewall and the cloud provider's security list (Oracle Cloud security list, AWS security group). `remohop signal --print-firewall [same flags]` prints the machine-side commands. Test from outside, not from the VM itself.
- **Relayed sessions fail with "full" or `the relay could not be used either`:** the relay ports are used up or not reachable. With `--relay-ports LO-HI` open exactly that range (UDP) in both firewalls and keep `--relay-max` at most the size of the range. Without `--relay-ports`, a relay needs all of 1024-65535/udp open.
- **The server refuses connections on a small VM:** `--max-conns` is sized to the available memory (about 6400 on 1 GB) and the server refuses rather than run out of memory. A systemd unit installed with `remohop service install --role signal` has `MemoryMax=512M`, and the limit follows it. Raise `--max-conns` only if you have the memory (about 70 KB per connection).
- **`remohop service install --role signal` fails:** it needs Linux with systemd and root. Elsewhere run `remohop signal` yourself, or adapt the unit that `remohop service print` shows. It also refuses a binary under `/home`, `/root`, `/tmp` or `/var/tmp`: the unit's sandbox (ProtectHome, PrivateTmp) hides those, and the service would fail with `203/EXEC`. Install the binary first (`sudo install -m 755 remohop /usr/local/bin/remohop`) and run that one.
- **Where is the server's public key?** `sudo remohop signal --show-key --data /var/lib/remohop-signal` for the installed service, or `remohop signal --show-key` for the default data directory. Clients pin it with `remohop config set signal-pin KEY`.
- **Cloudflare Tunnel in front of the server does not work:** it carries HTTP(S) only. The rendezvous server needs UDP reachable directly. Only the websocket bridge (`--ws-listen 127.0.0.1:PORT`) goes through a tunnel, with an ingress rule `service: http://127.0.0.1:PORT`.
- **The websocket bridge log shows every client as 127.0.0.1, or bridged clients hit "too many connections from this address":** add `--ws-real-ip-header CF-Connecting-IP` when the bridge sits behind `cloudflared`.
- **Bridged clients register but cannot connect to anyone:** the bridge needs `--relay`, and the relay ports must be open as usual.
- **Clients built with a baked-in server** (`-X github.com/readori/RemoHop/internal/dial.DefaultSignal=...`) can still be overridden with `--signal`, `REMOHOP_SIGNAL` or `remohop config set signal`.

## Accounts

Accounts are optional and need a rendezvous server started with `--accounts` (see `remohop help signal`). Background: [security.md](security.md#accounts).

- **`remohop login` says the server does not offer accounts, or fails at once:** the operator has not enabled `--accounts`, or the address you gave is outside the server's allowed domains or invite list. Ask the operator.
- **No mail arrives:** check the spam folder, and ask the operator to check the server's SMTP settings and log. Codes have 8 digits, last 10 minutes and work once. Five wrong codes burn the code and lock the address for 15 minutes. Requests are also rate limited.
- **`joining an existing account needs its fingerprint confirmed ...`:** without a terminal, pass `--account-fingerprint FP` with the value `remohop account` shows on a device already in the account.
- **`the account on the rendezvous server has fingerprint X, not Y: not joining it`:** the account you are logging in to is not the one you meant. Do not continue until you know why. A malicious server could present a different account.
- **`this device is waiting to be approved: on another device of the account run `remohop account approve ...``:** run that command on a device already in the account, with the complete 20-digit fingerprint. A prefix is refused. `remohop account` on the existing device lists waiting devices. `remohop login` waits 10 minutes by default (`--wait`, `--no-wait`).
- **`no device with identity ... is waiting to join the account`:** the fingerprint is wrong, or the request was rejected or has expired. Run `remohop login` again on the new device and copy the fingerprint it prints.
- **`the rendezvous server sent an account log that does not extend the one this device ...` and similar `refusing it` errors:** this device has verified a longer or different log than the server now sends. Treat it as a server problem or an attack. Do not log in again blindly; check with the server's operator.
- **`this device is logged in as X; run `remohop logout` first`:** one account per device.
- **A host with `account-trust on` still refuses another device of the account:** `remohop account sync` on the host refreshes the list now (a running `remohop host` does it every minute). Check that the device is a member (`remohop account`) and that `remohop host paired` shows it as `account:NAME`. A device you revoked with `remohop host revoke` stays out until you turn `account-trust` off and on again. A host that could not reach the server for 7 days trusts no account device.
- **A removed device can still log in for a while:** hosts apply a removal at their next refresh, about a minute when they can reach the server. A server can delay it. `remohop config account-trust off` on the host takes effect at once.

## Running as a service

`remohop service install` writes a systemd unit, a launchd job or a Windows service. Common surprises:

- **The service is a separate instance.** It has its own identity, ID, password and pairings. Read them with `remohop status` (add `--show-password`). For a system-wide service, point `remohop` at its state directory, for example `sudo env REMOHOP_CONFIG_DIR=/var/lib/remohop remohop status` on Linux.
- **No screen.** A system-wide service on Linux and a Windows service (session 0) cannot see the desktop: shell, files and forwards work; screenshots, GUI input and the remote desktop do not. Use a per-user service (`remohop service install --user`) or start `remohop host` inside the desktop session. On Windows, `remohop service install --unattended` gives the service the console session instead, sign-in, lock and UAC screens included (read [security.md](security.md#windows-unattended-access) first); Ctrl+Alt+Del then also needs the `SoftwareSASGeneration` policy (see `remohop service --help`).
- **Linux without systemd** (containers, OpenRC): install fails with an explanation. `remohop service print` shows the unit so you can adapt it.
- **Per-user service does not start at boot on Linux:** install with `--linger`.
- **Windows:** install from an elevated prompt.
- **VPN on a service host:** a Linux system service runs as root, which the VPN needs on the host side.

`remohop passwd` changes the fixed password of a running host, including a service instance, without a restart.

## Background commands

Port forwards, mounts, serial ports, clipboard syncs and VPN tunnels started with `-b` keep running after the command returns:

```bash
remohop ps
remohop forward
remohop unforward 18080
remohop stop --all
```

`remohop forward` lists running forwards and `remohop unforward PORT` stops one; a process holding other forwards keeps them. `remohop serial`, `remohop vpn` and `remohop usb` list their own kind. Remote background jobs (`remohop <ID> spawn`) run on the host: use `remohop <ID> jobs`, and `remohop <ID> jobs rm --exited` to remove finished ones with their logs.

Interactive shell sessions stay on the host for `term-keep` (30 minutes by default; `remohop config set term-keep 2h` on the host, read when the host starts, and `REMOHOP_SHELL_KEEPALIVE` overrides it). List and end them with `remohop <ID> shell ls` and `remohop <ID> shell kill NAME`. They do not survive a restart of the host.

## Files

- **Interrupted copies.** `cp` and `sync` keep the data of an interrupted file of 8 MiB or more in a hidden `.<name>.remohop-partial-<id>` file next to the destination. Running the same command again continues from it if the source is unchanged; the next successful copy removes it. `--no-resume` starts over. Resumable downloads are off on Windows clients.
- **Permissions differ after a copy.** `cp` and `sync` apply the receiving side's umask. Use `cp -p` or `sync --perms` to copy permission bits exactly.
- **`send` did not overwrite.** By design: a taken name is saved as `name (1).ext`. Set `REMOHOP_DOWNLOADS_DIR` in the host's environment to change the folder.

## Mounts

The mount method depends on this machine's OS: FUSE on Linux, the system's WebDAV client on macOS and Windows. `--webdav` and `--fuse` override the default. A mount runs in the background; `remohop ps` shows it and `remohop umount <dir | drive>` stops it. If the session drops, operations fail while remohop reconnects (2 minutes by default; `-o reconnect=DUR` changes it), then it unmounts.

### FUSE mount

- Linux, regular user: `/dev/fuse` must be accessible and the `fusermount3` helper (package `fuse3`) installed. Otherwise: `mounting as a regular user needs the fusermount3 (or fusermount) helper: install fuse3, or run as root`.
- Linux, root: the mount is made directly.
- `-o allow_other` needs `user_allow_other` in `/etc/fuse.conf`.
- macOS: `--fuse` uses macFUSE if installed; untested.

If `remohop umount <dir>` reports `process N did not exit`, try `remohop stop <PID>`, then `kill <PID>`, then check `/proc/mounts` and remove a leftover mount with `fusermount3 -u <dir>` (or `umount <dir>` as root).

### WebDAV mount

- **Linux with `--webdav`:** `--webdav needs davfs2 (mount.davfs) on Linux: install it, or use `-o nomount` and point any WebDAV client at the printed URL, or drop --webdav to use FUSE`.
- **macOS:** uses `mount_webdav` on an existing directory. Errors from it are passed through as `mount_webdav: ...`.
- **Windows:** the target is a drive letter (`Z:`) or `*` for a free one. The WebClient service must be available:
  - `the WebClient service (Windows' WebDAV redirector) is not installed ...`: on Windows Server, run `Install-WindowsFeature WebDAV-Redirector` from an elevated PowerShell and restart.
  - `the WebClient service ... is disabled ...`: run `sc config WebClient start= demand`, then `net start WebClient`, from an elevated prompt.
  - Files above the redirector's `FileSizeLimitInBytes` (50 MB by default) will not open; remohop prints the limit when it mounts. Use `remohop cp` for bigger files.
  - Windows mounts have not been run by the automated tests.
- **Any OS:** `remohop <ID> mount -f -o nomount :/srv/www` prints a loopback URL for any WebDAV client and mounts nothing.

## VPN

`remohop <ID> vpn up` is tested with Linux on both ends. A Windows client (Wintun) and a macOS client (utun) are implemented. The macOS client is verified by automated tests on a hosted macOS VM (utun interface, routes including `--route`, packets both ways, clean-up, clean-up after a killed process, and `remohop host` plus `vpn up` on one Mac); `--exit` on macOS has not been run on a real network and nothing has been run on a physical Mac. The Windows end-to-end test is opt-in and not verified. Windows and macOS hosts cannot forward for `--route` or `--exit`:

- **`... needs the right to create network interfaces and change routes (root, or the CAP_NET_ADMIN capability) ...`**: run with `sudo`, or grant the binary the capability once with `sudo setcap cap_net_admin,cap_net_raw+ep $(command -v remohop)`. A host needs it too; a Linux system service (`sudo remohop service install --system`) runs as root.
- **`the VPN on Windows needs the Wintun driver (wintun.dll), which remohop does not include and never downloads by itself ...`**: download Wintun 0.14.1 from https://www.wintun.net, check the zip's SHA-256 (`07c256185d6ee3652e09fa55c0b673e2624b565e02c4b9091c79ca7d2f24ef51`), and copy `wintun\bin\<arch>\wintun.dll` next to `remohop.exe`, into the remohop config directory, or into the directory named by `REMOHOP_WINTUN_DIR`. If the message ends with `Found, but not the Wintun ... build remohop loads`, the DLL on disk has a different hash (another version, wrong architecture or modified); `remohop` refuses it on purpose. Run `remohop` as Administrator.
- **`this kernel has no TUN support (/dev/net/tun is missing; load the `tun` module)`**: run `sudo modprobe tun`.
- **`the host does not allow routes beyond the host's own networks (its owner can allow it with `remohop config vpn-exit on`)`**, and the same for `--exit`: the host's owner has to run `remohop config vpn-exit on`. Routes to the host's directly attached networks work without it.
- **`remote refused "vpn": ...`**: the host switched the `vpn` feature off.
- **`a tunnel to dev is already up (pid N)`**: one tunnel per host. `remohop dev vpn down` stops it; `remohop vpn` lists tunnels.
- **`route ... overlaps ..., which is reserved for the tunnels themselves`**: the tunnels use `10.77.0.0/16`; pick a route outside it.
- **`a macOS host cannot forward for VPN clients yet`**: a macOS host can only be reached itself through the tunnel.
- **`N tunnel(s) of a killed remohop left settings behind; run any remohop command as root to remove them`**: a `remohop` process was killed outright. Any `remohop` command run with the same privilege cleans up.
- **The tunnel stopped:** `the session to the host was lost; the tunnel is down`. Start it again; everything it set up has been removed.
- **With `--exit`, DNS still uses local resolvers:** DNS goes through the tunnel only where systemd-resolved is in use.

## Serial ports and USB

**Serial.**

- The virtual port needs Linux or macOS on this side. It appears in the `serial/` directory of the remohop config directory unless you give `--local PATH`.
- PORT can be a path, a short name or a `/dev/serial/by-id` name as shown by `remohop <ID> serial list`. The port is opened exclusively on the host, so another program holding it there makes the open fail.
- `remote refused "serial"`: the host switched the `serial` feature off.
- `remohop serial` lists active virtual ports and `remohop unserial PATH` closes one.

**USB.** USB/IP needs Linux and root on both sides; it has been run against the real kernel only in CI, with a virtual device:

- `attaching a USB device needs root on this machine: run with sudo`.
- `kernel module vhci-hcd is not loaded on this machine ...`: run `sudo modprobe vhci-hcd`. The host needs `usbip-host` (`sudo modprobe usbip-host`); missing modules are reported by name and side.
- A device must be allowed on the host first with `remohop usb allow BUSID`; keyboards, mice, storage and hubs need `--force`, and hubs are never exported. `remohop usb allowed` lists what is allowed.
- `remohop usb release BUSID` on the host recovers a device after a crash.
- `remohop usb` lists attached devices on this machine and `sudo remohop usb detach N|BUSID` detaches one.

## Screen, GUI input and desktop

`screenshot`, `desktop`, `click`, `move`, `type`, `press` and `scroll` need a display on the host. The host reports the reason from its side, for example:

```
remohop: click: no display available on the host: DISPLAY is not set; X display :0: dial unix /tmp/.X11-unix/X0: connect: connection refused; ...; input needs a running X11 session
```

- **Linux:** the host must run inside the logged-in X11 session: start `remohop host` from a terminal in that session, or install a per-user service. Native Wayland capture is not supported.
- **Windows:** the host must run in the interactive session, not as the Windows service.
- **macOS:** the host needs the Screen Recording permission for capture and the Accessibility permission for input (System Settings > Privacy & Security); without Accessibility the desktop is view-only and the input commands fail with the reason. See [Platform notes](#platform-notes).
- **Clicks land in the wrong place.** Coordinates are pixels of the full-size screenshot of the selected display. If you measured them in a downscaled image, pass `--scale-from WxH`. With `screenshot -R X,Y,W,H`, add X and Y back. Use the same `-d` display for the screenshot and the click.
- **Input refused.** The `input` feature is off on the host, or `REMOHOP_DESKTOP_VIEW_ONLY=1` is set there.
- **"Block input" and "blackout" are unavailable in the viewer** on X11 hosts. That is reported by the host, not a fault.
- **Clipboard.** A host without a system clipboard uses an in-memory one. `remohop <ID> clip status` names the backend. X11 needs `xclip` or `xsel`; Wayland needs `wl-clipboard`.

### Recording

- `remohop: desktop: recording: FILE already exists (use --force to replace it)`: add `--force` or choose another name.
- Recording starts when the viewer page connects. If the stream changes size or codec, recording continues in `FILE-2.mp4`, `FILE-3.mp4` and so on. Files are fragmented MP4, so a recording survives `remohop` being killed.

### Audio

`remohop <ID> desktop --audio` (or the page's Audio button) forwards the host's audio output. It is off unless asked for. When it cannot work, the error comes from the host and says what to do:

- **`cannot capture the host's audio output (...)`** on Linux: the host's `remohop` cannot reach a PulseAudio or PipeWire server. Run `remohop host` inside the logged-in desktop session, or make `XDG_RUNTIME_DIR` or `PULSE_SERVER` point at the user's sound server (a host started from systemd or ssh without them tries `/run/user/UID/pulse/native`). PipeWire needs its `pipewire-pulse` service. `REMOHOP_DESKTOP_AUDIO_SOURCE=NAME` on the host picks another source; `pactl list short sources` lists them (what is played is the `.monitor` of an output). The host's ffmpeg needs libpulse and libopus (`ffmpeg -devices | grep pulse`, `ffmpeg -encoders | grep opus`).
- **`this Mac has no loopback audio device`**: macOS cannot record its own output. Install a loopback driver such as BlackHole (`brew install blackhole-2ch`), create a Multi-Output Device containing your speakers and BlackHole in Audio MIDI Setup, make it the sound output, and allow `remohop` (or the terminal it runs in) under Privacy & Security > Microphone, because macOS treats loopback devices as microphones. remohop only picks devices whose names look like loopbacks (BlackHole, Loopback, Soundflower, iShowU); `REMOHOP_DESKTOP_AUDIO_SOURCE` names another by name or index. Not verified on a real Mac.
- **`this machine has no audio loopback device ffmpeg can record`** on Windows: install the free `virtual-audio-capturer` driver (screen-capture-recorder), or enable "Stereo Mix" (Sound settings > Recording > show disabled devices) if the sound card offers it, or use an ffmpeg build with WASAPI loopback. `REMOHOP_DESKTOP_AUDIO_SOURCE` names a DirectShow audio device. Not verified on a real Windows machine.
- **`audio forwarding is switched off on this host (REMOHOP_DESKTOP_AUDIO=off)`**, or `the host does not allow desktop.audio (feature "screen" is switched off ...)`: the owner turned it off, either audio alone or all screen access.
- **The Audio button says "click the page to start".** The browser starts sound only after a click or key press on the page; do either.
- **Crackling, drift or lagging sound.** The page keeps sound a fixed delay behind capture (a jitter buffer of at least 40 ms, more on a jittery link, and at least the video's own latency so lips match). The Stats overlay shows buffer, delay and skew; frequent underruns mean the link or the browser tab is starved. Background tabs throttle audio scheduling.
- Nothing is heard when nothing plays on the host. If the host's default output changes while audio runs, switch Audio off and on again to capture the new one.

### Monitors

- The monitor menu appears when the host reports two or more monitors. **All monitors** is one picture of the virtual desktop; the pointer maps onto the whole picture. On Linux that is the whole X screen, so monitors that are not touching leave the desktop background between them; on Windows it is a GDI capture of the bounding box (black gaps), which uses more CPU than the per-monitor Desktop Duplication capture. **macOS has no all-monitors view** (the host says `not available on macOS hosts: AVFoundation captures one display at a time`); pick one monitor. `remohop <ID> desktop` prints the host's monitors with their size and position.
- Linux monitor lists come from RandR 1.5. An X server without it (some VNC servers) offers only the whole screen.
- Clicks land on the wrong monitor after the host's display layout changed: reconnect the page, which reads the layout again.

### Files

Dropping a file on the viewer page (or the Upload button) copies it to the host user's Downloads folder, or to `--upload-dir`.

- `the host does not allow fs.put (feature "file" is switched off ...)`: the host's `file` feature is off; the owner can enable it with `remohop config feature file on`.
- `this is a view-only session: files cannot be uploaded`: restart without `--view-only` (and with `input` allowed on the host).
- Folders are not accepted by the page: zip them first. Existing names are never overwritten: the new file becomes `name (1).ext`. Characters Windows forbids in file names are replaced by `_`.
- **Copy and paste of files is not supported.** The page passes Ctrl+V to the remote machine; the remote clipboard carries text and images only. Drop or upload, then open the Downloads folder on the remote machine.

### Measuring

`remohop <ID> desktop --bench` prints frame rate, bitrate and capture-to-received latency without a browser; add `--audio` to include audio and `--json` for scripts. Latency is host capture clock to arrival, so it excludes the browser's decode and draw (the Stats overlay's `latency` includes them) and, with x11grab, up to one frame period before the grab. `n/a` means the host's capture device has no wall-clock timestamps (macOS and Windows capture devices may not). A run with nonzero `encoder restarts` or many drops is the host or the link being overloaded.

## ffmpeg and encoders

`desktop` needs ffmpeg on the host, with an H.264 or AV1 encoder. `remohop <ID> desktop --no-open --json` lists the encoders the host found (for example `"av1": "libsvtav1", "h264": "libx264"`); the codec is chosen when the page connects and tells the host what the browser can decode.

- **Which codec is used.** AV1 when the host has a GPU AV1 encoder; otherwise H.264, hardware-encoded where available (NVENC, QSV, VAAPI, AMF, VideoToolbox, Media Foundation), else libx264. Software AV1 is used only when the browser decodes only AV1, or with `--codec av1`. `--encoder NAME` forces one ffmpeg encoder.
- **`desktop: the host has no ffmpeg, which screen sharing needs. ...`**, followed by the host's `ffmpeg not found on the host: ...` and an install hint: install ffmpeg on the host (6.0 or newer is recommended), or set `REMOHOP_FFMPEG=/path/to/ffmpeg` there. Meanwhile `remohop <ID> screenshot` takes still pictures without it. On the host, `remohop doctor` shows whether ffmpeg and a screen are found; Quick Support says so in its window.
- **`ffmpeg at <path> has no <name> encoder`**: the encoder given with `--encoder` is not in this build.
- **No usable encoder:** install an ffmpeg build with libx264, or with libsvtav1 or libaom for AV1. Of the hardware encoders only VideoToolbox (macOS) is verified, on a hosted Apple Silicon VM; the others are untested. For VAAPI on a non-default device set `REMOHOP_VAAPI_DEVICE` on the host.
- **A notice about SVT-AV1** (only when AV1 runs in software): SVT-AV1 2.2 to 3.0 loses small screen updates in its real-time presets. On those hosts (Ubuntu 26.04 ships 2.3) RemoHop uses libaom instead and says so; without libaom it runs SVT-AV1 at a slow preset and warns to expect a low frame rate. H.264, SVT-AV1 1.7 or 3.1 and newer, or a GPU encoder avoid this.
- **Low frame rate or high latency.** Software encoding is CPU-bound. Leave the codec on its default (H.264 on a CPU-only host), or lower the load with `--max-height 1080`, `--fps 30` or `--scale 0.5`. A relayed session is also capped by the relay's bandwidth limit.

The viewer decodes with WebCodecs and needs a browser that supports it.

## Browser gateway

- **`remohop: web: refusing to listen on a non-loopback address without --tls-cert/--tls-key and --password-file ...`**: by design. Provide all three, or keep the default loopback listener.
- **404 on the gateway address:** the token in the URL is the login; open the full URL that `remohop web` printed.
- **The page cannot use a saved host:** the gateway runs with `--no-saved`, or under a different config directory from the one that holds the pairing.
- **A tab shows the host's refusal:** the host switched that feature off.
- **The browser opens but shows "file not found" or "access denied" for an `open-…html` file** (Linux): the page is opened through a short-lived private file so the access token never appears on a command line. RemoHop puts that file where Snap and Flatpak browsers can read it (inside `~/snap/<browser>/common/` or `~/.var/app/<id>/cache/`), detected from the default browser (`xdg-settings get default-web-browser`) or `--app-browser`. If your sandboxed browser isn't detected, set `REMOHOP_HANDOFF_DIR` to a directory it can read (mode 0700 is fine), or open the URL that was printed. This applies to `remohop web`, quick support and the desktop viewer.

### Through Cloudflare Tunnel

- **`remohop web` answers 403 or the page does not load through the tunnel:** start it with `--public-url https://YOUR-HOSTNAME` (exactly the hostname in the browser, https, no path) and keep `cloudflared` pointed at the loopback port. Do not rewrite the `Host` header in `cloudflared`; the gateway checks it against `--public-url`. `--public-url` requires a loopback `--listen` and `--password-file`.
- **Cloudflare Access blocks you:** that is the Access policy, not `remohop web`. The gateway has its own login as well (`--password-file`).
- **The rendezvous server cannot be put behind the tunnel:** see [Running your own rendezvous server](#running-your-own-rendezvous-server).

## Mobile apps

- The iOS app is a controller only: an iPhone or iPad cannot be viewed or controlled yet. An Android phone can be, with Settings → This device in the app ([mobile apps](en/mobile-apps.md#this-phone-as-a-host-android)).
- Both apps are coming to the App Store and Google Play (submitted for review). Until then, see [Mobile apps](en/mobile-apps.md): build Android with `mobile/android/build.sh` (Go, JDK 17+, `ANDROID_HOME`, an NDK) and iOS with `mobile/ios/build-xcframework.sh` on macOS, then Xcode.
- "No rendezvous server is set" when you enter a 9-digit ID: IDs are looked up on a rendezvous server, and the apps have no default one. Tap **Set up** (or open Settings → Rendezvous server) and enter your server's address, or connect by the host's address (`host:port`) instead, which needs no server.
- Please report what goes wrong on your device, with a diagnostic log (Settings → Diagnostic logs) if you can.

## Platform notes

Linux is the reference platform. macOS and Windows are tested by automated runs on hosted runners, not on physical desktops.

**Windows hosts**

- `shell -c`, `spawn` and the interactive shell use PowerShell (`pwsh.exe`, else `powershell.exe`; `REMOHOP_SHELL` on the host pins one). Write commands in PowerShell syntax: `remohop dev shell -c 'Get-ChildItem'`. `remohop dev info` tells you the OS first.
- Piped stdin is read with `$input`; `cat` is `Get-Content` there and does not read stdin.
- A job killed on a Windows host reports exit code 1.
- Audio forwarding (WASAPI loopback, `virtual-audio-capturer` or Stereo Mix), the monitor list, input on monitors other than the primary, and the all-monitors view (GDI capture of the virtual desktop) are implemented and unit-tested only for their geometry; none has run on a real Windows machine.
- GUI input uses SendInput and has not been run by a test. The VPN client needs the Wintun driver (see [VPN](#vpn)); a Windows host cannot forward for `--route` or `--exit`. Mounts on a Windows client use WebDAV on a drive letter and have not been run by a test. Resumable downloads are off on Windows clients. Serial ports on a Windows host work as the remote side.

**macOS**

- Mounts use the built-in WebDAV client; macFUSE is optional and untested.
- GUI input is implemented with Quartz events and needs the Accessibility permission on the host; pointer moves and clicks through a desktop session are verified on a hosted macOS VM, not on a physical Mac. If `remohop` reports that macOS has not granted the Accessibility permission, open System Settings > Privacy & Security > Accessibility, enable `remohop` (or, when it runs inside Terminal, iTerm or a launchd agent, that app or the agent's program), then restart `remohop host`. Screen capture needs Screen Recording in the same place. Without the permission macOS drops the events silently, so the desktop looks view-only.
- The VPN client (utun) is verified on a hosted macOS VM, see [VPN](#vpn); `--exit` has not been run on a real network. A macOS host cannot forward for VPN clients.
- Audio forwarding needs a loopback device (BlackHole or similar), see [Audio](#audio); there is no all-monitors view; neither is verified on a real Mac.
- Remote desktop host: capture uses AVFoundation and needs the Screen Recording permission; without it the picture shows only the desktop background, and the viewer says so. Verified on a GitHub-hosted Apple Silicon macOS VM (one 1024x768 display at scale 1): VideoToolbox H.264 (the default there), libx264 and SVT-AV1, the decoded picture, input, view-only, and two viewers at once. Not tested: Retina and multi-monitor Macs, the Lock control (it puts the display to sleep), physical Macs.
- macOS gives the screen to one capture at a time: a second capture started while one runs gets no frames. All sessions of a macOS host therefore share one capture. If that shared capture misbehaves on some Mac, `REMOHOP_MAC_DIRECT_CAPTURE=1` in the host's environment gives each session its own capture again (on the Mac this was measured on, only one viewer at a time then gets a picture).
- **`ffmpeg delivered no video within 15s of starting`** or **`screen capture delivered no picture`**: the capture started but produced nothing. Check the Screen Recording permission and that a user is logged in to the desktop.
- A launchd service writes its logs to files; `remohop service logs` shows them.

## Still stuck

Collect the output of `remohop -v <command>` on the client and `remohop status`, `remohop log` and `remohop config` on the host (remove secrets), and note the operating systems and network setup on both sides. For security questions, see [security.md](security.md).
