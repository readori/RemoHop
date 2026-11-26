# How it works

[中文](../zh/how-it-works.md)

The [How it works](https://remohop.com/en/how-it-works/) page on the website shows all of this as one diagram: the rendezvous server, the connection paths in the order they are tried, the default ports, the six connection steps and the session security. This guide has the details.

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
- A symmetric NAT gives every destination a new public port, so the other side cannot know where the probes will come from, and a NAT that only lets in packets from ports it has sent to (port-restricted) drops them. Many symmetric NATs (common on home routers and carrier networks, in China especially) hand out those ports in sequence, though. While connecting to the rendezvous server, each side sees two consecutive ports of its NAT. When they are a small step apart (1 to 16), it sends a prediction of its next ports with its candidates, and the other side, if its NAT is a cone, also probes a window of 48 predicted ports on the address the server saw the symmetric side at. Such pairs connect directly instead of through the relay. A host that has been up for a while measures again before it answers. Two symmetric NATs, or a symmetric NAT that picks random ports facing a port-restricted one, still cannot be punched. The rendezvous server needs no change for this, and older peers ignore the prediction.
- The host also asks its router to forward its UDP port (PCP, NAT-PMP or UPnP, whichever the router speaks). Where the router agrees, the forwarded public address is one more candidate, which works whatever NAT the client is behind, so even two symmetric NATs connect directly instead of through the relay. `remohop config portmap off` or `REMOHOP_PORTMAP=0` on the host turns it off; see [security.md](../security.md#router-port-mapping).
- The two machines run a password-authenticated key exchange over that connection. A man in the middle, including the rendezvous server, cannot complete it.

## Relay

Direct peer to peer is the default, and the only path unless the rendezvous operator opts in:

- A relay exists only when the operator runs `remohop signal --relay`. It is off by default because it is the only thing that costs the server real bandwidth.
- It is used only after hole punching has failed, for example between two symmetric NATs, or right away when one side reaches the server only through its websocket bridge (see below).
- A relayed session is the same end to end encrypted, authenticated session. The relay passes ciphertext and cannot read or join it.
- It is announced on stderr and shown as `relayed` in `remohop <ID> ping` and `remohop status`. It is slower and capped (`--relay-kbps`, default 8000 per session; `--relay-total-kbps`, default 40000; `--relay-max`). Traffic over the cap is paced (queued for up to 100 ms, then dropped) rather than cut off, which QUIC's congestion control follows smoothly. Relaying costs about 1 % of a CPU core per 10 Mbit/s.
- A client or host refuses relays with `remohop config relay off` or `REMOHOP_RELAY=0`. After a punch to a host has failed, further commands to it from the same networks set up the relay at once for 10 minutes (the punch still runs alongside, and a direct path that answers wins).
- A relayed session does not stay relayed if it does not have to. While it lasts, the client keeps trying to punch in the background: 5 s after the session started, then 20 s and 60 s after that, then every 3 minutes, and at once when its network changes. Each attempt gets a fresh introduction from the rendezvous server (the same request a new connection makes) and punches from the session's own socket. When a path answers, the session's QUIC connection moves onto it (QUIC connection migration: the client validates the new path, then switches; the host follows by itself). Streams, the desktop picture and transfers carry on without a break. Nothing is logged in again and the host's owner is not asked again. The relay is released a few seconds later. Both sides log `relayed session moved to a direct path after …`, and the path shown by the web client, `remohop status` and the desktop apps changes from `relayed` to `punched` (or `direct`, `lan`). The new path is validated inside the session's encryption, so only the host the session authenticated can answer on it; see [security.md](../security.md#relay). Neither side nor the rendezvous server needs anything new for this, so it works with servers and hosts of older versions too. `REMOHOP_RELAY_UPGRADE=0` on the client keeps relayed sessions on the relay. Sessions over the websocket bridge stay relayed.

## IPv6-only networks (NAT64)

Some networks give a device IPv6 only and reach the IPv4 Internet through NAT64 and DNS64: some mobile carriers, and the network Apple uses to review iOS apps. Host names work there as anywhere (DNS64 answers with an IPv6 address the NAT64 translates), but IPv4 addresses do not, and RemoHop sees many: an address the user types (`203.0.113.7:21118`), a rendezvous server configured by address, and the host and relay addresses the rendezvous server hands out.

- On iOS (or anywhere with `REMOHOP_NAT64=on`), when a datagram to an IPv4 address cannot be sent because there is no IPv4 route, the client finds the network's NAT64 prefix the standard way (RFC 7050: it asks the system resolver for `ipv4only.arpa`'s IPv6 addresses) and sends to the IPv6 address that embeds the IPv4 one (RFC 6052, every prefix length). Answers from that address are treated as coming from the IPv4 address, so punching, relaying and the QUIC session work unchanged. The prefix is kept for 10 minutes; a send that then finds no IPv6 route goes back to IPv4 and the prefix is looked up again.
- Where IPv4 works, nothing changes: no lookup, no translation. Android translates in the system (464XLAT), so it does not need this; desktops and hosts can turn it on with `REMOHOP_NAT64=on` (it costs the batched UDP I/O that QUIC uses on Linux).
- Limits: a NAT64 that DNS64 does not announce (no synthesized `ipv4only.arpa`) is not found; only RemoHop's UDP traffic is translated (the websocket bridge is reached by name). The translated path is a NAT like any other: punching to and through it works as with a carrier-grade NAT.

[ARCHITECTURE.md](../ARCHITECTURE.md) describes the packages and the session model. [security.md](../security.md) describes the security model.
