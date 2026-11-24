# Building and testing

[中文](../zh/building.md)

Requirements: the Go toolchain version in `go.mod`. Release binaries are built with `CGO_ENABLED=0`.

| Command | What it does |
|---|---|
| `make build` | builds `bin/remohop` |
| `make cross` | builds linux/amd64, linux/arm64, darwin/amd64, darwin/arm64 and windows/amd64 into `dist/` |
| `make test` | runs `go test ./...` |
| `make race` | runs the tests with the race detector (needs cgo) |
| `make vet`, `make fmt-check` | run `go vet` and fail on unformatted files |
| `make all` | `fmt-check`, `vet`, `build` and `test` |

The loopback smoke test runs one host and one client of a built binary on the same machine; the macOS and Windows runs use it:

```bash
test/smoke/smoke.sh bin/remohop
```

The NAT lab runs the real binary across Linux-kernel NATs in network namespaces, with the rendezvous server between them, so a session works only if hole punching (or the relay) does. It needs root, iproute2 and iptables:

```bash
sudo test/natlab/natlab.sh cone
```

Modes: `cone`, `symmetric`, `permissive`, `relay`, `wsrelay`, `tailnet` (a fake Tailscale between host and client: the tailnet path must win), `mesh` (three devices of one account behind three NATs: names, policy, subnet router, exit node, clean removal), `vpn` and `all`. `vpn` checks that routes, firewall rules and sysctls are identical after a tunnel ends, also after either side is killed, on iptables and nft hosts; it is not part of `all`.
