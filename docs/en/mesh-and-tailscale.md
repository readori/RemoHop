# Mesh network and Tailscale

[中文](../zh/mesh-and-tailscale.md)

## Mesh network (remohop net)

`remohop net up` joins all devices of your account (see [Accounts](accounts.md)) into one private network, like a small Tailscale for your own machines: each device gets a stable address derived from its identity key (`10.78.x.y` and an IPv6 unique-local address) and a name like `nas.remohop`.

```bash
sudo remohop net up
remohop net peers
ping nas.remohop
sudo remohop net up --route 192.168.1.0/24 --exit     # on the office machine
sudo remohop net up --accept-routes --use-exit office  # on the laptop
remohop net policy set policy.json
sudo remohop net down
```

- Links between devices are ordinary RemoHop sessions, opened when traffic needs them (punched, else relayed or through the websocket bridge), kept alive and re-opened after a device restarts or moves. Their login is limited to the mesh: being in the account gives no shell or files on another device.
- A signed policy (which device may reach which ports) is handed from device to device and enforced by the receiving one. Without one, every device reaches every port of every other.
- The mesh runs inside the host daemon (as root or with `CAP_NET_ADMIN`) and starts with the host service. Linux only so far; subnet routes and exit nodes are IPv4.
- DNS is touched conservatively: per-interface split DNS for `.remohop` with systemd-resolved, otherwise only on request (`--dns hosts`).

[net.md](../net.md) has the details, what is tested, and what Tailscale features are not replicated (single sign-on, sharing with other people, an admin console, Funnel and others).

## Using RemoHop with Tailscale

RemoHop detects a running Tailscale (local API, `tailscale status --json`, or its interface) without depending on it. When both machines are on the same tailnet, the tailnet address is one more candidate path, preferred over punching and tried before the relay; `remohop <ID> ping` then shows `tailnet path`. The session over it is RemoHop's own, end-to-end encrypted and authenticated as on any path. `remohop doctor` explains the network situation, Tailscale included; `REMOHOP_TAILSCALE=off` makes RemoHop ignore Tailscale. See [net.md](../net.md#using-remohop-with-tailscale).
