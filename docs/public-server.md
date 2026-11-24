# Running a public rendezvous server

This page is for operators who offer `remohop signal` to people they do not know, for example as the default server of a distribution of `remohop`. The rendezvous service itself is cheap and stays open to everyone. The relay is the part that costs bandwidth, so it has access controls and a budget.

## Recommended flags

For an Oracle Cloud Always Free micro VM (1/8 OCPU, 1 GB RAM, 10 TB of outbound traffic a month), with the websocket bridge behind a Cloudflare Tunnel:

```bash
sudo remohop service install --role signal --relay --relay-ports 40000-40099 -- \
    --relay-kbps 2000 --relay-per-source 2 --relay-monthly-gb 8000 \
    --relay-deny /etc/remohop/relay-deny \
    --ws-listen 127.0.0.1:8080 --ws-real-ip-header CF-Connecting-IP
```

| Flag | What it does |
|---|---|
| `--relay-kbps 2000` | Per-session cap for anonymous sessions (2 Mbit/s). Traffic over the cap is paced, not cut off. |
| `--relay-per-source 2` | At most two relayed sessions at a time per address (an IPv6 address counts by its /64). Both peers count, and so does the real address of a websocket client. A session is freed as soon as the client closes it. |
| `--relay-monthly-gb 8000` | Monthly relay budget (UTC calendar month). At 80 % anonymous sessions get half their rate. At 100 % new anonymous sessions are refused. At 110 % all new sessions are refused. It is counted in `--data` and survives restarts. |
| `--relay-deny FILE` | No relay for the addresses, networks and identity keys listed. Listed keys also cannot register as hosts. |
| `--ws-listen` / `--ws-real-ip-header` | The websocket bridge for networks without UDP. See [Self-hosting](en/self-hosting.md#networks-that-allow-only-https-websocket-bridge). |

If you also run accounts (`--accounts`, with SMTP), sessions where either peer is a verified member of an account on your server can get a better tier:

```bash
    --accounts ... --relay-account-kbps 8000           # account devices get 8 Mbit/s
    --accounts ... --relay-for accounts                # relay only for account devices
```

A host proves its identity key when it registers. A client proves its key by signing its relay request, and the signature is bound to its TLS connection and to that introduction. The server checks membership in its own account ledger. An account that is only claimed counts for nothing. A client sends its key only if it is a member of an account on that very server (checked by the server's pinned key). Otherwise its relay requests are anonymous.

The rendezvous service (IDs, presence, introductions, hole punching) has its own per-address rate limits and is never restricted by these flags. Only the relay is.

## The deny list

One entry per line, `#` starts a comment:

```
# abuse report 2026-11-02
203.0.113.7
198.51.100.0/24
2001:db8:1234::/48
Xk3...base64-identity-key...=
```

The server re-reads the file within 10 seconds of a change, and at once on SIGHUP (`sudo systemctl kill -s HUP remohop-signal`). If the file has a syntax error, the previous list stays in force and the error is logged. Under the installed service, keep the file outside `/home` and `/tmp`; the sandbox hides those.

## Watching it

- `sudo remohop signal --status --data /var/lib/remohop-signal --relay-monthly-gb 8000` prints this month's relayed bytes and the budget state. The running server writes the count about once a minute.
- The startup line shows the limits in force and this month's usage so far.
- Once an hour the log has a `relay summary` line: sessions started, bytes relayed, active sessions, and refusals by reason (`per_source`, `mode`, `deny`, `budget`, `full`). It contains no addresses.
- Lines at 50 %, 80 %, 100 % and 110 % of the budget are logged once per month.
- `journalctl -u remohop-signal` shows everything else.

## What a public operator can and cannot see

The server never sees session content. Sessions are end-to-end encrypted and authenticated between the two machines, and a relay passes ciphertext. The server cannot learn passwords or join a session. It does see metadata:

- **Rendezvous:** each host's identity key, its 9-digit ID, its public address while it is online, and when it was last seen. For each introduction it sees which address asked for which ID.
- **Relay:** both peers' addresses, start time, duration and byte count of each relayed session. A client's identity key appears only if it is an account member asking for its tier.
- **Websocket bridge:** the client's address (from Cloudflare's header), when it connected and how many bytes it carried.
- **Accounts:** the hash of each email address, the devices' identity keys and names, and the signed membership log. Login codes are mailed and never stored.

What the server writes to its log by default: hosts going online and offline (ID and address), relay sessions starting and ending (both addresses, tier, rate, duration, bytes), websocket connections opening and closing (address, duration, bytes), account logins, and the hourly summary. With `-v` it also logs each introduction. The logs go to the systemd journal. Set a retention period you can defend, for example `MaxRetentionSec=14day` in `/etc/systemd/journald.conf`, and say what it is in your notice.

## Privacy notice template

Adapt this to your setup and your jurisdiction. Keep it factual.

> **Privacy notice for the RemoHop rendezvous server at `<HOST>`**
>
> This server helps RemoHop devices find each other. When a direct connection is impossible, it can also relay the encrypted connection between them. It is operated by `<OPERATOR, CONTACT>`.
>
> **What we cannot see.** Connections between your devices are end-to-end encrypted. This server cannot read or change what you send, see your screen or files, or learn your passwords, and that includes relayed connections.
>
> **What the server processes.**
> - For devices that register as hosts: the device's public identity key, its 9-digit ID, its IP address while it is online, and when it was last seen.
> - For connection requests: which IP address asked for which ID.
> - For relayed connections: the IP addresses of both devices, start time, duration and amount of data.
> - For connections through the websocket endpoint: your IP address as reported by Cloudflare, connection times and amount of data.
> - If you create an account: a hash of your email address, your devices' public keys and names, and the account's signed device list. Your email address is used to send login codes through `<MAIL PROVIDER>`.
>
> **Logs.** The server logs devices coming online and going offline, relayed connections and websocket connections, with the IP addresses and data volumes listed above. Logs are kept for `<N>` days and then deleted. Hourly statistics contain no IP addresses.
>
> **Why.** To provide the service, to keep it within its limits (per-address and monthly caps), and to stop abuse.
>
> **Sharing.** We do not sell or share this data, except `<e.g. with Cloudflare, which carries the websocket endpoint, and Oracle Cloud, which hosts the server>` and where the law requires it.
>
> **Your choices.** You can run your own server (`remohop signal`) and point your devices at it. You can turn relaying off on your device (`remohop config relay off`). To delete an account, run `remohop account delete --yes`. An ID that is not used for 90 days is released.
>
> **Contact.** `<EMAIL>` for questions, deletion requests and abuse reports.
