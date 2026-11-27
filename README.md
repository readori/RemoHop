# RemoHop

English | [简体中文](README.zh-CN.md)

RemoHop is a peer-to-peer remote control tool. One static Go binary, `remohop`, is the command-line client, the host daemon on the machine you want to control, the rendezvous server that introduces the two machines, a browser gateway, and an MCP server that lets AI agents use remote machines. Sessions are end to end encrypted and go directly between the two machines by default.

## What it does

| Capability | Notes | Docs |
|---|---|---|
| Shell and background jobs | persistent interactive shells, single commands, jobs that keep running after you disconnect; PowerShell on Windows hosts | [Shell and jobs](docs/en/shell-and-jobs.md) |
| Files | list, copy, sync, deliver to the remote Downloads folder; large files resume; mount a remote directory locally | [Files](docs/en/files.md) |
| Port forwarding and Wake-on-LAN | TCP and UDP forwards, TCP reverse forwards, Wake-on-LAN | [Forwarding](docs/en/forwarding.md) |
| VPN | a virtual IPv4 link inside the session, to reach the host's networks or use the host as an exit | [VPN](docs/en/vpn.md) |
| Mesh network | the devices of your account (Linux, macOS, Windows) in one private network with names like `nas.remohop`; works next to Tailscale | [Mesh network and Tailscale](docs/en/mesh-and-tailscale.md) |
| Serial and USB | a remote serial port as a local virtual one; USB/IP (Linux) | [Serial and USB](docs/en/serial-and-usb.md) |
| Clipboard | two-way sync of text and images, with history | [Clipboard](docs/en/clipboard.md) |
| Remote desktop and GUI automation | view and control the screen in a browser (H.264 or AV1), with audio, multiple monitors and recording; screenshots, clicks, typing | [Screen and remote desktop](docs/en/remote-desktop.md) |
| Browser client | `remohop web` serves desktop, terminal, files, clipboard and system info in a browser | [Browser client](docs/en/web-gateway.md) |
| Quick support | the other person double-clicks one program, reads you an ID and a password, and presses Allow for every connection | [Quick support](docs/en/quick-support.md) |
| AI agents | MCP server (29 tools), six Agent Skills, scoped and expiring grants | [AI agents](docs/en/ai-agents.md) |
| Host access control | feature switches, approval, one-time passwords, key logins, audit log | [Host access control](docs/en/host-access-control.md) |
| Accounts (optional) | group your own devices and see which are online; grants no access | [Accounts](docs/en/accounts.md) |
| Your own server | run the rendezvous server yourself, optionally with a relay and a websocket bridge | [Self-hosting](docs/en/self-hosting.md) |
| Mobile | Android and iOS apps: controllers, and on Android the phone can also be viewed and controlled ([App Store](https://apps.apple.com/app/id6838609056); Google Play coming soon) | [Mobile apps](docs/en/mobile-apps.md) |

Security: QUIC with TLS 1.3 inside, a password-authenticated key exchange (CPace) bound to the connection, host keys pinned on first use. A relay exists only if the rendezvous operator turns it on, is used only when no direct path can be made, and carries the same encrypted session. Accounts are optional and grant no access. There is no built-in default rendezvous server. RemoHop has not had an independent security audit. See [docs/security.md](docs/security.md).

## Platforms and status

- Linux is the reference platform. The repository's CI runs the Go tests (with the race detector) and the NAT lab (end-to-end runs across kernel NATs in network namespaces) on Linux.
- The main macOS and Windows features (remote desktop, input, mounts, VPN, Windows unattended access) are covered by end-to-end runs on hosted runners, but not checked by a person on physical desktops; the quick-support window and audio have not been run on those systems yet.
- Releases are on [GitHub Releases](https://github.com/readori/RemoHop/releases), with every download listed at [remohop.com/en/download](https://remohop.com/en/download/); see [Install](docs/en/install.md). Windows binaries are not code-signed yet.
- Details: [Platform support and limitations](docs/en/platforms.md).

## Quick start

```bash
make build                                 # writes bin/remohop; needs the Go version in go.mod
remohop signal                                 # on a public machine with open UDP ports: the rendezvous server
remohop config set signal sig.example.net      # once on the host and once on your machine
remohop host                                   # on the host: prints its ID and a temporary password
remohop 276941009 shell                        # on your machine: asks for the password, opens a remote shell
```

The full walkthrough (pairing, running the host as a service, direct connections without a rendezvous server) is in [Quick start](docs/en/quick-start.md) and [Install](docs/en/install.md).

## Documentation

The index of all documents is [docs/README.md](docs/README.md).

- Getting started: [Install](docs/en/install.md) · [Quick start](docs/en/quick-start.md) · [Quick support](docs/en/quick-support.md)
- Using RemoHop: [Shell and jobs](docs/en/shell-and-jobs.md) · [Files](docs/en/files.md) · [Forwarding](docs/en/forwarding.md) · [VPN](docs/en/vpn.md) · [Mesh network and Tailscale](docs/en/mesh-and-tailscale.md) · [Serial and USB](docs/en/serial-and-usb.md) · [Clipboard](docs/en/clipboard.md) · [Screen and remote desktop](docs/en/remote-desktop.md) · [Browser client](docs/en/web-gateway.md) · [Accounts](docs/en/accounts.md) · [AI agents](docs/en/ai-agents.md) · [Mobile apps](docs/en/mobile-apps.md)
- Hosts and servers: [Host access control](docs/en/host-access-control.md) · [Self-hosting](docs/en/self-hosting.md) · [How it works](docs/en/how-it-works.md)
- Reference: [Platform support and limitations](docs/en/platforms.md) · [Building and testing](docs/en/building.md)
- In-depth references (English): [Security model](docs/security.md) · [Troubleshooting](docs/troubleshooting.md) · [Grants and MCP client setup](docs/agents.md) · [Mesh network in detail](docs/net.md) · [Public server](docs/public-server.md) · [Architecture](docs/ARCHITECTURE.md) · [Roadmap](docs/ROADMAP.md) · [Agent Skills and MCP](skills/README.md)

## License

RemoHop is dual-licensed: [GNU AGPL-3.0](LICENSE) for everyone, and a commercial license from the copyright holder for uses the AGPL does not fit. The AGPL's network clause matters to operators: if you run a **modified** `remohop signal` or `remohop web` for other people, you must offer them your modified source. Running unmodified RemoHop adds no obligation. **Any work based on this code must name RemoHop and link to https://github.com/readori/RemoHop, and keep the [NOTICE](NOTICE) file**; a modified version may not present itself as the original or use the RemoHop name for its own product (additional terms under AGPL-3.0 section 7). Details, including third-party licenses and why ffmpeg is not bundled, are in [LICENSING.md](LICENSING.md); contributions need a CLA ([CONTRIBUTING.md](CONTRIBUTING.md)).

Contact: <info@remohop.com> (commercial licensing, the CLA, and security problems that should not be reported in public). For questions and chat there is also Telegram: [t.me/remohop](https://t.me/remohop).
