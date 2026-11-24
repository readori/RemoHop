# Networking: the mesh (`remohop net`) and Tailscale

RemoHop has two ways to carry IP traffic, and it gets along with Tailscale:

- **`remohop <ID> vpn up`** joins this machine and one host point to point (see [VPN](en/vpn.md)).
- **`remohop net`** joins all devices of your account into one private network: every device gets a stable address and a name like `laptop.remohop`, and any device can reach any other. It covers the everyday uses of Tailscale for one person's devices; what it does not cover is listed under [What remohop net is not](#what-remohop-net-is-not).
- **Tailscale** can run on the same machines. RemoHop detects it and uses a shared tailnet as a direct path for its own sessions; see [Using RemoHop with Tailscale](#using-remohop-with-tailscale).

`remohop doctor` explains how all of this looks from the machine it runs on.

## remohop net

### Requirements

- An account (`remohop login`, see [Accounts](en/accounts.md)) with every device in it. The mesh is exactly the account's devices, as listed in the account's signed device log.
- On every device: the host daemon (`remohop host`, or the service installed with `remohop service install --system`), running as root or with `CAP_NET_ADMIN`. The mesh runs inside the host process; that is also how devices reach each other.
- Linux. macOS and Windows do not run the mesh yet (see [Limits](#limits-and-what-was-tested)).

The host's config directory is the device: log in, and run `remohop net`, with the same directory the host uses. For a system service that is `sudo env REMOHOP_CONFIG_DIR=/var/lib/remohop remohop ...` (as `remohop service status` prints).

### Quick start

On every device:

```bash
sudo remohop net up
remohop net status
remohop net peers
ping nas.remohop
```

`remohop net up` switches the mesh on in the configuration (`remohop config get net`); the running host brings it up within two seconds and again at every start. `remohop net down` takes it down and keeps it down.

### Addresses and names

- Every device gets an IPv4 address in `10.78.0.0/16` and an IPv6 unique-local address in a /64 of its own account (`fdXX:XXXX:XXXX::/64`, derived from the account ID).
- The address is derived from the device's identity key (SHA-256 of the key). Devices are taken in the order the account's log added them, every device ever added included; a device whose first choice is taken by an earlier one takes the next value of its key's sequence. Every device computes the same table from the same signed log, nobody's address changes when a device joins or leaves, and each side of a link checks that the other claims the address it computed for it.
- The network can be moved for the whole account with `"prefix4"` in the policy (a private network between /8 and /24); a mesh network that overlaps a network the machine already routes is refused with an explanation. The VPN keeps `10.77.0.0/16` and Tailscale `100.64.0.0/10`, so all three can coexist.
- Names: the name a device had when it joined the account (`remohop config name` at `remohop login` time), turned into a DNS label; duplicates get `-2`, `-3`. `remohop net peers` shows them.

### DNS, per operating system

Each device runs a small DNS responder on its own mesh address, port 53, that answers A and AAAA for `NAME.remohop` and refuses everything else, so it can never stand in for your normal resolver. How the system is pointed at it is the `net-dns` setting (`remohop net up --dns MODE`), conservative by default:

| Mode | Linux | Effect |
|---|---|---|
| `auto` (default) | with systemd-resolved: `resolvectl dns`/`domain ~remohop`/`default-route false` on the mesh interface | only `.remohop` goes to the responder; the settings belong to the interface and vanish with it, so nothing can be left behind |
| `auto` | without systemd-resolved | nothing is changed; `remohop net status` says so. Point a resolver at `MESH-ADDRESS:53` for `.remohop` yourself, or use `hosts` |
| `hosts` | | a block between `# BEGIN remohop-net` and `# END remohop-net` in `/etc/hosts`, rewritten as devices come and go, removed by `remohop net down` (and by the next start after a crash) |
| `off` | | only the responder |

macOS (`/etc/resolver/remohop`) and Windows (NRPT rules) would be the equivalents of `auto`; they are not implemented because the mesh does not run there yet.

### How links work

- A link between two devices is an ordinary RemoHop session from one device's host daemon to the other's, opened when the first packet needs it: rendezvous, hole punching, then the relay or the websocket bridge if that is all that works, and a tailnet path when both are on one. It is end-to-end encrypted and authenticated like every session.
- The login uses the device's identity key, and the other host checks it against the account's signed device log. Such a login is **limited to the mesh**: the session can carry the mesh service (and ping), nothing else. A mesh device gets no shell or files on another unless that host's owner grants that separately (a password, a pairing, `remohop authorize`, or `remohop config account-trust on`).
- Packets travel in QUIC datagrams when they fit, otherwise on the link's stream; TCP is clamped to datagram size (as the VPN does). Interface MTU 1400.
- Keep-alives every 4 seconds; a link that hears nothing for 15 seconds is closed and the next packet opens a new one (also after the other device was killed or changed networks). Idle links close after 3 minutes; the link to an exit node in use, and with `--accept-routes` links to all online devices, stay up.
- When both devices open a link at the same moment, both keep the one opened by the device with the smaller identity key.
- A device that is removed from the account loses its links within 30 seconds (the mesh re-reads the account's devices every 30 seconds) and can no longer log in for the mesh.

### Access control

Without a policy every device of the mesh may reach every port of every other, like a LAN. A policy restricts that:

```json
{
  "version": 2,
  "rules": [
    {"from": ["laptop", "phone"], "to": ["nas"], "ports": ["22/tcp", "445/tcp", "icmp"]},
    {"from": ["*"], "to": ["printer"], "ports": ["631/tcp"]},
    {"from": ["laptop"], "to": ["192.168.1.0/24"]}
  ]
}
```

```bash
remohop net policy set policy.json
remohop net policy
remohop net policy reset
```

- **Selectors**: `"*"`, a device's mesh name (without `.remohop`), or its identity fingerprint. In `"to"`, a CIDR selects destinations behind a subnet router or exit node; those match only CIDRs and `"*"`.
- **Ports**: `"*"` or absent for all, `"22"`, `"22/tcp"`, `"8000-8100/udp"`, `"icmp"` (ping).
- **Enforced on the receiving device**, for every packet that arrives over the mesh. Replies to connections the device opened itself always pass, as do ICMP errors and echo replies. A later IP fragment (no ports) passes if the sender may reach the destination at all. An empty rule list lets nothing in.
- **Signed and shared**: `remohop net policy set` signs the policy with this device's key, as the next version. Devices hand it to each other when a link comes up, and a device that installs a new policy connects to every online device once to deliver it. A device adopts a policy only if it is signed by a current member of its account and has a higher version than its own (for equal versions, all settle on the same one). Any member can set the policy, as any member can approve or remove devices.
- Sources are checked too: a packet must come from the sending device's own mesh address (or, as a reply, from a network it routes for this device), so one device cannot pose as another.

### Subnet routes and exit node (Linux, IPv4)

```bash
sudo remohop net up --route 192.168.1.0/24 --exit     # the office machine
sudo remohop net up --accept-routes                   # a device that wants the office LAN
sudo remohop net up --use-exit office                 # a device that sends all traffic via the office
sudo remohop net up --no-exit-node --no-accept-routes # back to plain
```

- A subnet router or exit node forwards for the mesh and hides it behind its own address, with the same firewall rules and clean-up as the VPN's `--route`/`--exit` on a host (iptables or nft; recorded and undone after a crash too).
- `--accept-routes` installs the routes other devices offer; when two offer the same network, the first wins.
- `--use-exit NAME` sends all traffic through that device once its link is up. The device's local networks, the paths of its current links and the rendezvous server stay off that route. DNS keeps using the current resolvers.
- Routed and exit traffic also passes the receiving device's policy (`"to": ["192.168.1.0/24"]` or `"*"`).

### Status and service

- `remohop net status` (`--json`): address, name, interface, network, policy, what this device offers and uses, notes (DNS, IPv6).
- `remohop net peers` (`--json`): every other device with its address, whether it is online, its link (`up`, `connecting`, `-` for none yet, `failed` with the reason) and the link's path (`lan`, `direct`, `punched`, `relayed`, `tailnet`).
- The mesh starts with the host service by itself when `net` is on: no separate service.

## Using RemoHop with Tailscale

RemoHop and Tailscale can run side by side; neither depends on the other.

- **Detection.** RemoHop looks for Tailscale through its local API socket (`/var/run/tailscale/tailscaled.sock` on Linux), its CLI (`tailscale status --json`), and as a fallback its network interface (`tailscale0`, `Tailscale` on Windows, a macOS `utun` with an address in `100.64.0.0/10` or `fd7a:115c:a1e0::/48`). Nothing is linked from Tailscale. `REMOHOP_TAILSCALE=off` makes RemoHop ignore it.
- **Tailnet addresses as candidates.** A host lists its tailnet address among its candidate addresses, first, so it survives the limit of six. When the client is on the same tailnet (the address belongs to a peer in its own `tailscale status`), it prefers that path: a punched path that answers first waits up to a second for the tailnet path, the punch gets at least 3 seconds even between NATs that cannot be punched, and the "go straight to the relay" shortcut after an earlier failed punch is skipped. `remohop <ID> ping` shows `tailnet path`, `remohop status` on the host `tailnet`.
- **Security is unchanged.** The tailnet is only a path. The RemoHop session over it is the same QUIC session with the same login (password, pairing or key) and host key pinning as over any other path; Tailscale's own encryption is an additional layer, and what Tailscale says about a peer is never taken as proof of who it is.
- **Exit nodes.** If Tailscale sends this machine's traffic through an exit node, RemoHop sessions to machines outside the tailnet go that way too, and hole punching sees the exit node's NAT. `remohop doctor` points this out.
- **MagicDNS and `.remohop`.** The mesh answers only `.remohop`; MagicDNS keeps its own suffix. With systemd-resolved both are per-interface routing domains and do not collide.
- **Address ranges.** Tailscale uses `100.64.0.0/10` and `fd7a:115c:a1e0::/48`; the mesh uses `10.78.0.0/16` and an `fd` prefix of its own; the point-to-point VPN `10.77.0.0/16`.
- **Userspace networking.** A Tailscale in userspace-networking mode (no `tailscale0` interface, as in some containers) gives the machine no tailnet address that RemoHop's sockets can use; RemoHop then ignores it for paths.
- **Not implemented:** a client that reaches the rendezvous server only through its websocket bridge (UDP blocked) does not try the tailnet path, although Tailscale itself might get through; such sessions use the relay.

Tested in the NAT lab (`test/natlab/natlab.sh tailnet`) with a fake Tailscale: an interface named `tailscale0` with 100.x addresses joining host and client over a deliberately slow link, and stub status files. The session takes the tailnet although the punched path is faster, takes the punched path when the preference is switched off, and connects over the tailnet between two symmetric NATs. Not tested against a real tailscaled.

## What remohop net is not

`remohop net` is deliberately the one-person, own-devices part of what Tailscale offers. Not replicated:

- **Single sign-on, users and groups.** The mesh is one account's devices; there are no identity providers, no user roles, no tags or groups in the policy.
- **Sharing with other people**, devices of other accounts, or explicitly invited guests. (The design leaves room for it: addresses are derived from keys and checked by both ends; what is missing is how a guest learns and verifies the mesh it joins.)
- **An admin console** or central device management. Every device is managed on itself; the policy travels between devices.
- **Funnel, Serve, HTTPS certificates, Taildrop, Tailscale SSH**, app connectors, network flow logs, posture checks.
- **Its own relays.** Links use the rendezvous server's relay and websocket bridge, which its operator must have enabled.
- **macOS, Windows, iOS and Android** as mesh devices, IPv6 exit traffic, and DNS through the exit node.
- **Key expiry and device approval flows** beyond the account's own (a new device is approved with `remohop account approve`; a lost one removed with `remohop account remove`).

### Roadmap (phase 2)

Team features are postponed on purpose, until the single-account mesh has seen real use:

1. Shared devices and guests across accounts (invitation by fingerprint, verified address claims, policy selectors per account).
2. Groups and tags in the policy; a policy history and `remohop net policy diff`.
3. macOS (utun, `/etc/resolver/remohop`) and Windows (Wintun, NRPT) data planes.
4. IPv6 subnet routes and exit traffic; DNS through the exit node.
5. A team view of devices and links (still without a central server that holds keys).

## Security notes

- The rendezvous server cannot add a device to the mesh: membership is the account's signed log (see [security.md](security.md#accounts)), and every link's login proves the device key in the TLS handshake of the session. A server can withhold log entries, which delays a removal on some devices, and a device whose log was last verified more than 7 days ago refuses to run the mesh until it can refresh it.
- A mesh login is limited to the mesh service. It is not a `remohop authorize` and does not need `account-trust`.
- The policy is enforced where packets arrive; a compromised member device can do what any member can (set a policy, remove devices), as for accounts.
- Exit and subnet routing reuse the VPN's forwarding rules: forward from the mesh interface, masquerade, only replies back, and while forwarding is on, a guard keeps the machine's other interfaces from forwarding anything not headed into a tunnel.

## Limits and what was tested

- **Tested** in the NAT lab (`sudo test/natlab/natlab.sh mesh`), Linux network namespaces with real kernel NATs: three devices of one account behind three different NATs form a mesh, links come up only on traffic, the devices ping each other by name (through each one's own responder), a mesh login cannot open a shell, a policy set on one device reaches another, which then blocks a port and lets another through, a reset reopens it, a device whose host is killed and restarted is reachable again, one device serves its LAN and is the exit node of another (internet traffic seen from its NAT), and after `remohop net down` routes, firewall rules and forwarding settings on all three are exactly as before.
- **Not tested:** IPv6 inside the mesh (the lab's kernel has no IPv6), systemd-resolved split DNS (not present in the lab), `net-dns hosts` on a real system, relayed and websocket-bridged links in a mesh (the session machinery is the same as for `remohop <ID> ...`), large accounts, mobile networks, physical machines.
- Linux only. The VPN's macOS and Windows tunnel devices exist, but the mesh's interface, route and DNS setup is written for Linux.
- One link per pair of devices; traffic between two devices goes directly between them, never through a third mesh device (except via an exit node or subnet router).
