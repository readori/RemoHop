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

## The operator's web panel

`--panel ADDR` adds a read-only web page for whoever runs the server: hosts online and IDs stored, open connections and websocket bridges, the relay (sessions now and today, this month's bytes against the budget, usage per day, refusals by reason), trends over 24 hours and 7 days, the limits in force and how close the server is to each, memory and goroutines, and recent warnings and errors from the log. It is off by default.

The panel shows counts and aggregates only. It never lists an IP address, a host ID, an account's email or a key, and log records reach it with those removed. Its history (one sample a minute, kept for 24 hours, and 15-minute points for 7 days) is written to `panel-history.json` in `--data`, so a restart keeps it.

```bash
remohop signal --relay --relay-ports 40000-40099 --panel 127.0.0.1:8810
sudo remohop service install --role signal --relay --relay-ports 40000-40099 --panel 127.0.0.1:8810
```

Who may open it:

- **From the server itself, no login.** The panel listens on loopback. Reach it from your own machine over SSH, then open `http://127.0.0.1:8810/`:

  ```bash
  ssh -L 8810:127.0.0.1:8810 server
  ```

  A loopback request must name `127.0.0.1` or `localhost` as its host, so that a web page cannot reach the panel through DNS rebinding.
- **Through Cloudflare Tunnel with Cloudflare Access**, for a browser anywhere. Every request that comes through Cloudflare (it carries a `Cf-Ray`, `Cf-Connecting-Ip` or `Cf-Access-Jwt-Assertion` header) must carry a valid Access token, in that header or the `CF_Authorization` cookie. The panel checks its RS256 signature against the team's keys (`https://TEAM/cdn-cgi/access/certs`, cached for an hour and fetched again for a new key ID), the audience (your application's AUD tag), the issuer (`https://TEAM`), and its expiry, with a little clock skew. If an email list is set, the token's email must be on it. The panel's header shows who is signed in. Without `REMOHOP_PANEL_CF_TEAM` and `REMOHOP_PANEL_CF_AUD`, requests through Cloudflare are refused, so a tunnel without Access in front never opens the panel.
- **With a password.** With `REMOHOP_PANEL_PASSWORD` set, every page except `/healthz` asks for it (HTTP Basic, any user name). Listening beyond loopback (`--panel 0.0.0.0:8810`, say) is refused without it. Prefer loopback and a tunnel: Basic authentication over plain HTTP crosses the network in the clear.

| Environment variable | Meaning |
|---|---|
| `REMOHOP_PANEL_CF_TEAM` | your Cloudflare Zero Trust team domain, e.g. `myteam.cloudflareaccess.com` |
| `REMOHOP_PANEL_CF_AUD` | the Access application's Audience (AUD) tag |
| `REMOHOP_PANEL_CF_EMAILS` | optional, comma-separated: the only emails let in (Access policies still apply first) |
| `REMOHOP_PANEL_PASSWORD` | optional: a password for every page; required to listen beyond loopback |

The settings are environment variables, never flags, so they stay off the command line. Under the installed service, put them in `/etc/remohop/remohop-signal-panel.env` (the unit reads it when the service starts; without the file it starts anyway), make it readable by root only, and restart:

```bash
sudo install -m 600 /dev/null /etc/remohop/remohop-signal-panel.env
sudoedit /etc/remohop/remohop-signal-panel.env       # REMOHOP_PANEL_CF_TEAM=..., one per line
sudo systemctl restart remohop-signal
```

The page is plain HTML, CSS and JavaScript built into the binary: no CDN, no web fonts, a strict Content-Security-Policy, no framing, no caching of data, and only `GET` requests. It follows the browser's language (English or Chinese) and the system's light or dark theme; both can be switched in the sidebar.

### Cloudflare Tunnel and Access

1. In the Cloudflare Zero Trust dashboard, add an Access application of type *Self-hosted* for a hostname such as `panel.example.com`, with a policy that allows your team. Copy its *Application Audience (AUD) tag*.
2. Route the hostname through a tunnel to the panel. In `cloudflared`'s `config.yml` on the server:

   ```yaml
   tunnel: <tunnel-id>
   credentials-file: /etc/cloudflared/<tunnel-id>.json
   ingress:
     - hostname: panel.example.com
       service: http://127.0.0.1:8810
     - hostname: relay.example.com          # the websocket bridge, if you use it
       service: http://127.0.0.1:8080
     - service: http_status:404
   ```

3. Start the server with `--panel 127.0.0.1:8810` and set `REMOHOP_PANEL_CF_TEAM=myteam.cloudflareaccess.com` and `REMOHOP_PANEL_CF_AUD=<AUD tag>` (and, if you like, `REMOHOP_PANEL_CF_EMAILS`).

Cloudflare Access is the first lock and the panel's own check of the token is the second: a request that reaches the tunnel without passing Access is refused by the panel too.

### Docker

Inside a container, loopback is the container's own. Two ways to reach the panel:

- **Host networking** (Linux): with `network_mode: host`, `--panel=127.0.0.1:8810` listens on the host's loopback, and the SSH tunnel and a `cloudflared` on the host work as above, without a password.
- **The default bridge network**: the panel must listen on `0.0.0.0:8810` inside the container, so it needs `REMOHOP_PANEL_PASSWORD`. Publish it on the host's loopback only (`127.0.0.1:8810:8810`), or reach it from a `cloudflared` container on the same network (`service: http://signal:8810`) with the Cloudflare variables set. With a password set, every page asks for it, also behind Access.

In `packaging/docker/compose.yaml`:

```yaml
services:
  signal:
    command:
      - --relay
      - --relay-ports=40000-40099
      - --panel=0.0.0.0:8810
    environment:
      REMOHOP_PANEL_PASSWORD: ${REMOHOP_PANEL_PASSWORD:?set it in .env}
      REMOHOP_PANEL_CF_TEAM: ${REMOHOP_PANEL_CF_TEAM:-}
      REMOHOP_PANEL_CF_AUD: ${REMOHOP_PANEL_CF_AUD:-}
    ports:
      - "127.0.0.1:8810:8810"   # the panel: on the host's loopback only
```

`GET /healthz` answers `ok` without a login, for a health check.
