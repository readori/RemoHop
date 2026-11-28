# RemoHop rendezvous server image

`ghcr.io/readori/remohop-signal` runs `remohop signal`, the rendezvous server (and, if you turn it on, the
relay and the websocket bridge). It is a static binary on `scratch`: no shell, no package manager, runs as
UID 65532, works with a read-only root filesystem, and is built for `linux/amd64` and `linux/arm64`.

The image is for the **server**. Hosts and clients use the `remohop` binary (see
[docs/en/install.md](../../docs/en/install.md)).

## Run it

```bash
docker run -d --name remohop-signal --restart unless-stopped \
  --read-only --cap-drop ALL --security-opt no-new-privileges \
  -p 21116:21116/udp -p 21117:21117/udp \
  -p 40000-40099:40000-40099/udp \
  -v remohop-signal:/data \
  ghcr.io/readori/remohop-signal:v1.2.3 \
  --relay --relay-ports 40000-40099

docker logs remohop-signal | grep 'server key'     # the public key clients may pin
```

Everything after the image name is passed to `remohop signal` (see `remohop signal --help`). The entrypoint
already sets `--data /data`.

- **Ports.** `21116/udp` is the rendezvous, `21117/udp` is NAT-type detection (the port after `--listen`),
  and `--relay-ports LO-HI` is the range relayed sessions use (only if you pass `--relay`; the range must be
  published and match). Open them in the host firewall **and** the cloud provider's security list. Docker
  publishes UDP like TCP: `-p HOST:CONTAINER/udp`.
- **Volume.** `/data` holds the server's key and the ID table. Without a volume a new container is a new
  server with a new key and new IDs. The image's `/data` is owned by UID 65532, which a named volume
  inherits; for a bind mount run `chown 65532:65532 DIR` first.
- **Source addresses.** The server learns each client's address from its packets, so NAT between the
  client and the container must not rewrite it. Published ports on the default bridge keep the real source
  for outside traffic; rootless Docker, Podman with slirp4netns, and testing from the same machine show the
  bridge address instead. On Linux, `--network host` (no `-p`) avoids all of that.
- **Health.** `HEALTHCHECK` runs `/healthcheck`, which does a real QUIC/TLS handshake with the server on
  loopback (a scratch image has no `curl` or `nc`). `docker ps` shows `(healthy)`. It probes
  `127.0.0.1:21116`; if you pass `--listen` with another port, or an address that is not reachable as
  `127.0.0.1` inside the container (a specific IP), set the same address for the probe with
  `-e REMOHOP_HEALTH_ADDR=IP:PORT` (compose: `environment: [REMOHOP_HEALTH_ADDR=IP:PORT]`), or the container
  stays "unhealthy" and anything waiting for `service_healthy` (the tunnel in `compose.yaml`) never starts.
- **Licenses.** The image carries `/LICENSE` (AGPL-3.0), `/NOTICE` and `/THIRD_PARTY_LICENSES.txt` (the Go
  runtime's and every linked module's license).
- **Read-only.** Nothing is written outside `/data`, so `--read-only` needs no tmpfs.
- **Accounts and mail.** `--accounts ...` options work as documented in `remohop help signal` and
  [docs/en/accounts.md](../../docs/en/accounts.md); the image
  carries CA roots for SMTP over TLS. Put secrets in files mounted read-only and pass the file flags, not on
  the command line.

## Behind Cloudflare (websocket bridge)

Cloudflare Tunnel carries HTTPS, not UDP. Keep the UDP ports published as above, and add the bridge for
networks that only allow HTTPS:

```bash
docker run -d --name remohop-signal ... \
  ghcr.io/readori/remohop-signal:v1.2.3 \
  --relay --relay-ports 40000-40099 \
  --ws-listen 0.0.0.0:8080 --ws-real-ip-header CF-Connecting-IP
```

Do not publish 8080 to the internet; give `cloudflared` access to it over a private Docker network and point
the tunnel's public hostname at `http://signal:8080` (the compose file does this). Then, on clients and hosts:

```bash
remohop config set signal relay.example.com:21116
remohop config set signal-ws wss://relay.example.com/remohop/v1/udp
remohop config set signal-pin <server key>
```

`--ws-real-ip-header CF-Connecting-IP` makes the per-address relay limits use the client's address that
Cloudflare forwards; only use it when the bridge is reachable **only** through Cloudflare, or the header
can be forged. Sessions that come through the bridge always use the relay, so `--relay` must be on.
Details: [Networks that allow only HTTPS](../../docs/en/self-hosting.md#networks-that-allow-only-https-websocket-bridge).

## The web panel

`--panel` serves a read-only web panel for the operator (counts only, never addresses). Inside the
container it has to listen on `0.0.0.0`, which needs a password, and it should be published on the host's
loopback only:

```bash
docker run -d --name remohop-signal ... \
  -p 127.0.0.1:8810:8810 -e REMOHOP_PANEL_PASSWORD \
  ghcr.io/readori/remohop-signal:v1.2.3 \
  --relay --relay-ports 40000-40099 --panel 0.0.0.0:8810
```

Then `ssh -L 8810:127.0.0.1:8810 server` and open `http://127.0.0.1:8810/`. With `--network host` the panel
can stay on `127.0.0.1:8810` without a password. Cloudflare Access and the other settings:
[The operator's web panel](../../docs/en/self-hosting.md#the-operators-web-panel).

## docker compose

[`compose.yaml`](compose.yaml) has the same setup with hardening options and an optional `tunnel` profile
for `cloudflared`. Fill in the `REPLACE_ME_*` values first.

## Build it yourself

From the repository root:

```bash
docker build -f packaging/docker/Dockerfile --build-arg VERSION=v1.2.3 -t remohop-signal .
docker buildx build -f packaging/docker/Dockerfile --platform linux/amd64,linux/arm64 \
  --build-arg VERSION=v1.2.3 -t example/remohop-signal:v1.2.3 --push .
```

The Dockerfile cross-compiles with the Go toolchain (no QEMU). To pin the Go base image, pass
`--build-arg GO_IMAGE=golang:1.26-bookworm@sha256:<digest>`.

## Verify a release image

Release images are signed with cosign (keyless) by the release workflow, and carry an SBOM and build
provenance as OCI attestations:

```bash
cosign verify ghcr.io/readori/remohop-signal:v1.2.3 \
  --certificate-identity-regexp '^https://github\.com/readori/RemoHop/\.github/workflows/release\.yml@refs/tags/v.+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```
