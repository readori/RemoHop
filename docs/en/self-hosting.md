# Self-hosting the rendezvous server

[中文](../zh/self-hosting.md)

The rendezvous server is light. A small VM is enough, for example an Oracle Cloud Always Free instance with 1 OCPU and 1 GB. Measured figures, not guarantees: about 17 MB RSS idle, about 70 KB per online host, about 85 MB with 1000 hosts online.

```bash
remohop signal --relay --relay-ports 40000-40099
remohop signal --print-firewall --relay --relay-ports 40000-40099
remohop signal --show-key
```

- `--relay` turns the relay on (off by default; it costs bandwidth). `--relay-ports LO-HI` takes relay ports from one range, so the firewall opens only that range. Without it each session gets a random high port.
- `--max-conns N` caps open connections (hosts online plus clients asking), about 70 KB each. The default is sized to the available memory (cgroup limit, else RAM): about 6400 on a 1 GB machine, at most 50000.
- `--print-firewall` prints the commands that open the ports (iptables on Ubuntu/Debian, firewall-cmd on Oracle Linux/RHEL). It runs nothing.
- `--show-key` prints the server's public key and how to pin it.

To install a hardened systemd service (Linux with systemd only, needs root):

```bash
sudo install -m 755 remohop /usr/local/bin/remohop   # the unit's sandbox hides /home, /root and /tmp
sudo /usr/local/bin/remohop service install --role signal --relay --relay-ports 40000-40099
remohop service status --role signal
sudo remohop signal --show-key --data /var/lib/remohop-signal
```

Open UDP `21116`, `21117` and the relay range in **both** places: the machine's firewall and the cloud provider's security list (Oracle Cloud security list, AWS security group, and so on). Clients reach the server directly over UDP; Cloudflare Tunnel does not carry UDP. Machines where only HTTPS gets out use the websocket bridge below.

Point clients at it and pin its key:

```bash
remohop config set signal HOST:PORT
remohop config set signal-pin KEY
```

Or bake both into a build:

```bash
go build -ldflags "-X github.com/readori/RemoHop/internal/dial.DefaultSignal=HOST:PORT -X github.com/readori/RemoHop/internal/dial.DefaultSignalPin=KEY" -o remohop ./cmd/remohop
```

## Networks that allow only HTTPS (websocket bridge)

CI runners, cloud containers and corporate networks often block UDP and let only HTTPS out, sometimes only through an HTTP proxy. For them the rendezvous server can also serve a websocket bridge, and both clients and hosts fall back to it by themselves:

```bash
remohop signal --relay --relay-ports 40000-40099 \
    --ws-listen 127.0.0.1:8080 --ws-real-ip-header CF-Connecting-IP
```

Serve it through a Cloudflare Tunnel (the UDP ports stay open as before; only the bridge goes through the tunnel). In `cloudflared`'s `config.yml`:

```yaml
tunnel: <tunnel-id>
credentials-file: /etc/cloudflared/<tunnel-id>.json
ingress:
  - hostname: relay.example.com
    service: http://127.0.0.1:8080
  - service: http_status:404
```

On the machines:

```bash
remohop config set signal relay.example.com:21116        # as before, for UDP
remohop config set signal-ws wss://relay.example.com/remohop/v1/udp
```

- When the UDP rendezvous does not answer within 4 seconds and `signal-ws` is set, `remohop` and `remohop host` go through the bridge. `REMOHOP_TRANSPORT=ws` (or `remohop config set signal-transport ws`) skips the UDP attempt; `REMOHOP_TRANSPORT=udp` never uses the bridge. `REMOHOP_SIGNAL_WS` overrides the URL. A machine with only `signal-ws` configured uses the bridge alone.
- The bridge honours `HTTPS_PROXY`, `HTTP_PROXY` and `NO_PROXY` (CONNECT; `http://USER:PASSWORD@proxy:port` for proxy authentication).
- Behind the bridge no hole punching is possible, so such sessions always go through the server's relay, which must be on (`--relay`). `remohop <ID> ping` shows `relayed path (websocket)`, `remohop host status` shows `over its websocket bridge`. Encryption, the password exchange and the server pin are exactly as over UDP: the bridge and Cloudflare see only QUIC ciphertext.
- `--ws-real-ip-header CF-Connecting-IP` makes the per-address limits use the client's address that Cloudflare passes on; without it every client counts as the tunnel's own address. `--ws-tls-cert`/`--ws-tls-key` serve `wss://` directly without a tunnel. `--ws-max-conns` (default 512, 32 per address) bounds open bridges.
- With a Cloudflare Access application in front of the hostname, give machines a service token. The secret never goes on a command line:

```bash
remohop config set signal-ws-access-id <client-id>.access
remohop config set signal-ws-access-secret-file /etc/remohop/ws-secret   # or REMOHOP_SIGNAL_WS_ACCESS_SECRET
```

- A build can carry the bridge too: add `-X github.com/readori/RemoHop/internal/dial.DefaultSignalWS=wss://relay.example.com/remohop/v1/udp` to the `-ldflags` above.

The bridge was tested end to end in network namespaces (UDP dropped except DNS, then only an HTTP CONNECT proxy allowed) and with in-process servers; it has not yet been tried through a real Cloudflare tunnel.

## Running a public server

To offer the relay to people you do not know, for example as the built-in default of your own `remohop` builds, give it access controls and a budget. Rendezvous (IDs, presence, hole punching) stays open to everyone; only the relay is limited. For an Oracle Cloud Always Free micro VM with 10 TB of outbound traffic a month:

```bash
sudo remohop service install --role signal --relay --relay-ports 40000-40099 -- \
    --relay-kbps 2000 --relay-per-source 2 --relay-monthly-gb 8000 \
    --relay-deny /etc/remohop/relay-deny \
    --ws-listen 127.0.0.1:8080 --ws-real-ip-header CF-Connecting-IP
sudo remohop signal --status --data /var/lib/remohop-signal --relay-monthly-gb 8000
```

- `--relay-per-source N` (default 2): concurrent relayed sessions per address (IPv6 by its /64), counting both peers and websocket clients' real addresses. A closed session frees its slot at once.
- `--relay-kbps` caps anonymous sessions. With `--accounts`, `--relay-account-kbps N` gives a higher cap to sessions where either peer is a verified member of an account on this server, and `--relay-for accounts` relays only those. Membership is proven by signatures, never taken on the client's word.
- `--relay-deny FILE`: addresses, networks and identity keys that get no relay (listed keys also cannot register as hosts). The file is re-read when it changes and on SIGHUP.
- `--relay-monthly-gb N`: monthly budget, persisted across restarts. From 80 % anonymous sessions get half their rate; at 100 % anonymous sessions are refused; at 110 % all are. `remohop signal --status` shows the month's usage, and an hourly log line summarises sessions, bytes and refusals without addresses.

[public-server.md](../public-server.md) is the full reference for these options; it also covers what such an operator can and cannot see, and has a privacy notice template.
