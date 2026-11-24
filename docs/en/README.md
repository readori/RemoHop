# RemoHop user guide

[中文](../zh/README.md) · [All documents](../README.md)

Every remote command has the form `remohop <ID> <command>`. The pages use `dev` as the example alias, from `remohop <ID> pair --name dev`. Run `remohop help <command>` for the full syntax.

## Getting started

- [Install](install.md): install scripts, packages, verifying a download by hand, building from source.
- [Quick start](quick-start.md): rendezvous server, host, pairing, running as a service, direct connections without a rendezvous server, status and devices.
- [Quick support](quick-support.md): helping someone who will never open a terminal.

## Using RemoHop

- [Shell and background jobs](shell-and-jobs.md)
- [Files](files.md): copy, sync, send, mount.
- [Port forwarding and Wake-on-LAN](forwarding.md)
- [VPN](vpn.md)
- [Mesh network and Tailscale](mesh-and-tailscale.md)
- [Serial ports and USB devices](serial-and-usb.md)
- [Clipboard](clipboard.md)
- [Screen and remote desktop](remote-desktop.md): screenshots, remote desktop, audio, monitors, GUI automation.
- [Browser client](web-gateway.md): `remohop web`, and exposing it through Cloudflare Tunnel.
- [Accounts](accounts.md)
- [AI agents](ai-agents.md): MCP server, Agent Skills, grants.
- [Mobile apps](mobile-apps.md)

## Hosts and servers

- [Host access control](host-access-control.md): feature switches, approval, one-time passwords, key logins, audit log.
- [Self-hosting](self-hosting.md): the rendezvous server, relay, websocket bridge and public servers.
- [How it works](how-it-works.md): how a connection is made and when the relay is used.

## Reference

- [Platform support and limitations](platforms.md)
- [Building and testing](building.md)
- In depth: [security model](../security.md), [troubleshooting](../troubleshooting.md), [grants and MCP client setup](../agents.md), [mesh network in detail](../net.md), [public server](../public-server.md), [architecture](../ARCHITECTURE.md), [roadmap](../ROADMAP.md)
