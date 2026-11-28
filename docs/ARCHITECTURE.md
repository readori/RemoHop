# RemoHop architecture

RemoHop is a P2P remote-control tool. One static Go binary, `remohop`, is the
controlling CLI, the controlled host daemon, the rendezvous server, the
browser gateway and the MCP server. The user guide is in [en/](en/README.md)
and [zh/](zh/README.md); this page is for people reading the code.

```
 controlling machine                                       controlled machine
┌──────────────────────────┐   UDP hole punch + QUIC     ┌──────────────────────────┐
│ remohop <ID> shell ...   │◄═══════════════════════════►│ remohop host             │
│ remohop mcp  (AI agents) │   end-to-end encrypted      │  shell / fs / forward    │
└─────────┬────────────────┘                             │  clip / desktop ...      │
          │                                              └────────────┬─────────────┘
          │  signalling (who is where)                                │
          └──────────────►  remohop signal  ◄─────────────────────────┘
                    (optional relay and websocket bridge, off by default)
```

## Layers

| Layer | Package | Job |
|---|---|---|
| Framing | `internal/wire` | length-prefixed JSON messages and typed binary frames |
| Session | `internal/peer` | `Conn`: an authenticated session multiplexing service streams |
| Rendezvous | `internal/signal` | ID allocation, presence, candidate exchange, the optional relay and websocket bridge, optional accounts, the operator's web panel (`remohop signal`) |
| Cloudflare Access | `internal/cfaccess` | checks Cloudflare Access tokens for the rendezvous server's web panel |
| NAT traversal | `internal/nat` | candidate gathering, UDP hole punching |
| Router port mapping | `internal/portmap` | PCP, NAT-PMP and UPnP IGD: the host's UDP port forwarded by its router, offered as a candidate |
| Websocket bridge | `internal/wsdgram` | UDP datagrams over a WebSocket (HTTPS proxy aware), for networks without UDP |
| Authentication | `internal/auth` | identity keys, password PAKE bound to the TLS channel, pairing |
| Host daemon | `internal/hostd` | `remohop host`: registers, accepts sessions, serves `peer.HostServices` |
| Dialling | `internal/dial` | `remohop <ID> ...`: resolve, punch, connect, authenticate → `*peer.Conn` |
| Services | `internal/svc/*` | one package per feature, owning host handler + client API + CLI |
| X11 discovery | `internal/x11disp` | which display a daemon should use, and its Xauthority cookie |
| CLI | `internal/cli`, `cmd/remohop` | command registry and dispatcher |
| Background | `internal/bg` | `-b` detaching, `remohop ps`, `remohop stop` |
| MCP | `internal/mcp` | `remohop mcp`: the services as MCP tools for AI agents |
| Accounts | `internal/account` | client side of accounts (`remohop login`, `remohop account`), the signed membership log, host-side account-trust |
| Mesh | `internal/mesh` | `remohop net`: the mesh network of an account's devices, running inside the host daemon (see [net.md](net.md)) |
| Tailscale | `internal/tailnet` | detects a running Tailscale and offers its addresses as candidate paths |
| Web gateway | `internal/web` | `remohop web`: a browser UI that is the client of a normal session |
| Desktop app | `internal/desktop`, `internal/localui`, `cmd/remohop-quick` | `remohop app`, what a double-click on `RemoHop.exe` / `RemoHop.app` opens (see [desktop-app.md](desktop-app.md)) |
| Native windows | `internal/nativeui`, `internal/webview` | WebView2, WKWebView and WebKitGTK windows and the tray, without cgo; linked only into `remohop-quick` |
| Quick support | `internal/quick`, `cmd/remohop-quick` | `remohop quick`, and the quick-support downloads (`RemoHop-QuickSupport.exe`, `RemoHop Quick Support.app`, `remohop-quick`) |
| OS services | `internal/service`, `internal/unattended` | `remohop service install` (systemd, launchd, Windows service); Windows console access for `--unattended` |
| Input | `internal/inject` | pointer and keyboard injection (XTEST on X11, Quartz events on macOS, SendInput on Windows) |
| Permissions | `internal/perm` | which feature switch each host service belongs to |
| Mobile core | `internal/mobile`, `mobile/` | the in-process gateway of the Android and iOS apps |
| Grants | `internal/grant` | scoped, expiring access for agents: allow-list parsing, path and command checks, the `Guard` services ask, asciicast recording (stored and enforced by `internal/hostd`, see [agents.md](agents.md)) |

## Services

| Package | Commands | Notes |
|---|---|---|
| `svc/shell` | `shell`, `spawn`, `jobs`, `logs`, `kill` | PTY sessions that outlive the connection; exec with separate stdout/stderr; detached jobs with on-disk logs |
| `svc/fs` | `ls`, `cp`, `send`, `sync`, `mount`, `umount` | `rfs` is the pipelined file protocol, `xfer` copies and syncs, `mount` is the FUSE filesystem or a loopback WebDAV server |
| `svc/forward` | `forward`, `unforward` | one stream per TCP connection, UDP forwards; reverse forwards call back into the client |
| `svc/clip` | `clip` | text and image sync, one-shot get/set, local history |
| `svc/screenshot` | `screenshot` | pure-Go X11 capture on Linux; platform capture on macOS and Windows |
| `svc/input` | `click`, `move`, `type`, `press`, `scroll` | single pointer and keyboard actions through `internal/inject` |
| `svc/desktop` | `desktop` | H.264 or AV1 video (and optional Opus audio) from an `ffmpeg` subprocess on the host, WebCodecs viewer served on the client's loopback, input through `internal/inject` |
| `svc/vpn` | `vpn` | IP packets through the session (TUN on Linux, utun on macOS, Wintun on Windows) |
| `svc/serial` | `serial` | a host serial port as a local virtual port |
| `svc/usb` | `usb` | USB/IP export of allowed host devices (Linux) |
| `svc/wake` | `wake`, `wol` | Wake-on-LAN, sent locally or by the host |
| `svc/sysinfo` | `info` | what kind of machine the host is |

The desktop service is the one place the binary leans on an external
program: capturing and encoding in `ffmpeg` is what makes hardware video
encoders reachable without cgo. The host needs `ffmpeg` only for `desktop`.

## Session model

A `peer.Conn` is one QUIC connection. Every feature opens its own
bidirectional streams with `conn.Open(ctx, "service", req)`:

1. opener writes a JSON header `{"service": "...", "req": {...}}`
2. acceptor looks the service up and answers `{"ok": true}` or
   `{"ok": false, "error": "..."}`
3. from there the stream carries whatever the service defines, built from
   `wire.WriteJSON/ReadJSON` and `wire.WriteFrame/ReadFrame`

Hosts expose `peer.HostServices` (services register with `peer.Handle` from
`init()`). Clients expose nothing unless they opt in per session with
`conn.HandleLocal`, so a host can never open a shell on the machine that is
controlling it.

## Connection establishment

1. **Host** generates an Ed25519 identity, opens one UDP socket and connects
   to the rendezvous server over QUIC *from that socket*. The server sees the
   socket's public (reflexive) address, verifies key possession and assigns a
   stable 9-digit ID to the key.
2. **Client** does the same from its own socket and asks for the ID.
3. The server sends each side the other's candidates (reflexive address plus
   local interface addresses). It never sees session traffic.
4. Both sides send punch probes to every candidate from the same socket, then
   the client opens a QUIC connection to the host on the first path that
   answers. Both ends of that connection reuse the punched socket.
5. **Authentication**: QUIC's TLS 1.3 gives encryption but the peers have no
   PKI, so on the first stream they run CPace (a balanced PAKE) over the
   password, with the TLS exporter of this exact connection mixed in. A
   man-in-the-middle — including a malicious rendezvous server — ends up with
   different exporters on its two legs and fails the key confirmation, and
   learns nothing that helps guess the password offline. The host rate-limits
   failed attempts.
6. The credential is either the host's temporary password or a high-entropy
   token minted by `remohop <ID> pair`, so paired clients (and AI agents)
   reconnect without prompts.

If no direct path can be punched (for example two symmetric NATs), the
client uses the rendezvous server's relay when its operator enabled one
(`remohop signal --relay`); the relay carries the same QUIC session and sees only
ciphertext. Without a relay the connection fails with a diagnostic. A peer
whose network blocks UDP can reach the server through its websocket bridge
(`internal/wsdgram`); such sessions always use the relay. A shared Tailscale
tailnet is used as one more candidate path when both machines are on it.

## Trust boundaries

- **Rendezvous server: untrusted.** It learns who is online and their
  addresses, and a relay also sees the size and timing of relayed sessions.
  It cannot read or join a session, and a malicious one gets no password
  guesses, because the key exchange is bound to the end-to-end TLS connection.
  See [security.md](security.md).
- **Strangers on the host's UDP port: untrusted.** Until login completes a
  connection can open a bounded number of streams, send bounded messages and
  hold one of a bounded number of pending slots; no service is reachable.
- **Authenticated client: trusted by the host within the enabled features.**
  It acts with the host user's privileges, unless it logged in with a grant
  (`internal/grant`), which narrows what it may do.
- **Host: not trusted by the client.** It can open only the callbacks the
  client registered, its file names are validated before anything is written
  locally, and text it supplies is sanitised before reaching the terminal.

## Testing

- Unit and integration tests run against real QUIC on loopback
  (`peertest.Pair`), real PTYs, real FUSE mounts and a real X server (Xvfb).
- NAT traversal is tested twice: against simulated NATs of every type inside
  one process (`internal/nat/vnet`), and with the real binary across two
  Linux-kernel NATs in network namespaces (`test/natlab/natlab.sh`).

## Conventions for feature packages

- A service package owns **everything** for its feature: the host handler
  (`peer.Handle`), an exported Go client API taking a `*peer.Conn` (the MCP
  server calls it), and its CLI commands (`cli.Register`).
- Pure Go, `CGO_ENABLED=0`. Linux is the reference platform; the package must
  still compile for `GOOS=darwin` and `GOOS=windows` (stubs that return a
  clear "not supported on this platform" error are fine).
- Tests use `peertest.Pair(t)` for a real loopback session and
  `cli.Env{DialFunc: ...}` to drive commands.
- CLI: human-readable output on stdout, diagnostics on stderr via
  `env.Errorf`, `--json` wherever an agent would want to parse the result,
  exit codes from `internal/cli` (`ExitOK`, `ExitError`, `ExitUsage`,
  `ExitConnect`).
- Remote paths are written with a leading colon (`:/var/log`), local paths
  without.
- Long-lived client commands take `-b/--background` through `internal/bg`.
