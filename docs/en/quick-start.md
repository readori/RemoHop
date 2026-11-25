# Quick start

[中文](../zh/quick-start.md)

This walkthrough uses a rendezvous server on a machine with a public address (`sig.example.net` below) and two machines: the one you control and your own workstation.

## 1. Install

Install `remohop` on every machine that uses it (see [Install](install.md)); no other software is needed.

## 2. Run a rendezvous server

On a machine with a public IP, open two UDP ports (`21116` and `21117` by default) and run:

```bash
remohop signal
```

The server prints its public key, which clients may pin (see [security.md](../security.md#rendezvous-server)). To also relay sessions that cannot connect directly, start it with `remohop signal --relay` (see [Relay](how-it-works.md#relay)). For a long-running server on a small VM, see [Self-hosting the rendezvous server](self-hosting.md).

## 3. Start the host

On the machine to control:

```bash
remohop config set signal sig.example.net
remohop host
```

`remohop host` runs in the foreground and prints something like this (the ID and password are generated):

```
RemoHop host ready
  ID:        276 941 009
  Password:  xrb8-upuu  (temporary, changes on restart)
  Direct:    203.0.113.7:51820
  Identity:  7197:70c3:1130:65a3:9c97
Connect with `remohop 276941009 <command>`.
```

The password is shown only when the output is a terminal; when it goes to a file or a log, the banner says `(not shown: ...)` and `remohop status --show-password` prints it. The ID belongs to the host's identity key and stays the same across restarts. The temporary password changes on every start. For a fixed password, run `remohop passwd`, which asks for it without echo and never prints it afterwards.

## 4. Or run the host as a service

To have the host running from boot or login without a terminal:

```bash
remohop service install
remohop status --show-password
```

`remohop service install` writes a systemd unit (Linux), a launchd job (macOS) or a Windows service, enables it and starts it. The service has its own identity, ID and password, separate from a `remohop host` you start by hand; `remohop status` shows them. A system-wide service (`--system`) or a Windows service cannot see the desktop session, so use a per-user service (the default unless you are root) when screenshots or the remote desktop matter. On Windows, `remohop service install --unattended` instead gives the service's clients the console session, including the sign-in screen, the lock screen and UAC prompts (off by default; read [security.md](../security.md#windows-unattended-access) first). See `remohop help service` for per-platform details.

## 5. Connect from your machine

```bash
remohop config set signal sig.example.net
remohop 276941009 ping
remohop 276941009 pair --name dev
remohop dev info
```

- `ping` shows the path to the host. The first connection needs the password, so it prompts for it.
- `pair --name dev` asks for the password once, stores a pairing token bound to your machine, and saves the alias `dev`.
- After pairing, `remohop dev ...` connects without a password. `remohop devices` lists your saved hosts and whether they are online.

## Without a rendezvous server (LAN or public IP)

RemoHop does not need a rendezvous server when the host is reachable by address. On the host:

```bash
remohop host --no-signal --listen :21118
```

From the client, use the address in place of the ID. The password is still required:

```bash
remohop 192.168.1.5:21118 pair --name lab
remohop lab info
```

## Status and devices

```bash
remohop dev info
remohop dev ping -c 10
remohop status
remohop devices
```

`info` describes the remote machine. `ping` measures the round-trip time and reports the path: `lan`, `direct` and `punched` are peer to peer; `relayed` goes through the rendezvous server's relay. `remohop status` shows this machine's host daemon, its ID and its sessions. `remohop devices` lists your saved hosts, asks the rendezvous server which of them are online, and shows background commands running to each.
