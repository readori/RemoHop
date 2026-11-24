# How it works

[中文](../zh/how-it-works.md)

```
 controlling machine                                 controlled machine
+--------------------------+   UDP hole punch + QUIC   +--------------------------+
| remohop <ID> shell ...   |<=========================>| remohop host             |
| remohop web, remohop mcp |   end to end encrypted    |  shell, files, vpn,      |
+----------+---------------+                           |  desktop, ...            |
           |                                           +------------+-------------+
           |  signalling: who is where                              |
           +------------------>  remohop signal  <------------------+
                          (optional relay, off by default)
```

- The host registers with the rendezvous server and receives its ID.
- The client asks the rendezvous server for the host. The server passes each side the other's candidate addresses.
- Both sides send probes to punch holes through their NATs, and the client connects over QUIC on the first path that answers. If both NATs look punchable but the first attempt fails, the client retries once on a fresh socket.
- The two machines run a password-authenticated key exchange over that connection. A man in the middle, including the rendezvous server, cannot complete it.

## Relay

Direct peer to peer is the default, and the only path unless the rendezvous operator opts in:

- A relay exists only when the operator runs `remohop signal --relay`. It is off by default because it is the only thing that costs the server real bandwidth.
- It is used only after hole punching has failed, for example between two symmetric NATs, or right away when one side reaches the server only through its websocket bridge (see below).
- A relayed session is the same end to end encrypted, authenticated session. The relay passes ciphertext and cannot read or join it.
- It is announced on stderr and shown as `relayed` in `remohop <ID> ping` and `remohop status`. It is slower and capped (`--relay-kbps`, default 8000 per session; `--relay-total-kbps`, default 40000; `--relay-max`). Traffic over the cap is paced (queued for up to 100 ms, then dropped) rather than cut off, which QUIC's congestion control follows smoothly. Relaying costs about 1 % of a CPU core per 10 Mbit/s.
- A client or host refuses relays with `remohop config relay off` or `REMOHOP_RELAY=0`. A relayed session does not later upgrade to a direct one. After a punch to a host has failed, further commands to it from the same networks skip punching for 10 minutes and go to the relay at once.

## IPv6-only networks (NAT64)

Some networks give a device IPv6 only and reach the IPv4 Internet through NAT64 and DNS64: some mobile carriers, and the network Apple uses to review iOS apps. Host names work there as anywhere (DNS64 answers with an IPv6 address the NAT64 translates), but IPv4 addresses do not, and RemoHop sees many: an address the user types (`203.0.113.7:21118`), a rendezvous server configured by address, and the host and relay addresses the rendezvous server hands out.

- On iOS (or anywhere with `REMOHOP_NAT64=on`), when a datagram to an IPv4 address cannot be sent because there is no IPv4 route, the client finds the network's NAT64 prefix the standard way (RFC 7050: it asks the system resolver for `ipv4only.arpa`'s IPv6 addresses) and sends to the IPv6 address that embeds the IPv4 one (RFC 6052, every prefix length). Answers from that address are treated as coming from the IPv4 address, so punching, relaying and the QUIC session work unchanged. The prefix is kept for 10 minutes; a send that then finds no IPv6 route goes back to IPv4 and the prefix is looked up again.
- Where IPv4 works, nothing changes: no lookup, no translation. Android translates in the system (464XLAT), so it does not need this; desktops and hosts can turn it on with `REMOHOP_NAT64=on` (it costs the batched UDP I/O that QUIC uses on Linux).
- Limits: a NAT64 that DNS64 does not announce (no synthesized `ipv4only.arpa`) is not found; only RemoHop's UDP traffic is translated (the websocket bridge is reached by name). The translated path is a NAT like any other: punching to and through it works as with a carrier-grade NAT.

[ARCHITECTURE.md](../ARCHITECTURE.md) describes the packages and the session model. [security.md](../security.md) describes the security model.
