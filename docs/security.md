# RemoHop security model

This document describes what RemoHop protects, against which threats, and what it does not protect. It is written for people who run a host, people who connect to machines with `remohop`, and people who let AI agents or a browser gateway use RemoHop. The design is summarised in [ARCHITECTURE.md](ARCHITECTURE.md); this page covers the user-facing consequences.

RemoHop has not had an independent security audit. The mechanisms below are described from the code and its tests, and the limits are stated as they are.

## Summary

- Session traffic is encrypted end to end between the two machines. The rendezvous server cannot read it or join a session, whether the session is direct or relayed.
- A session is authenticated with a password-authenticated key exchange (CPace), bound to the TLS connection and to both machines' identity keys. A man in the middle cannot complete it, and nobody can test password guesses offline.
- Password guessing against a host is rate limited per source address and globally.
- A client pins each host's identity key on first login and refuses a different key afterwards.
- Pairing replaces the password with a random 256-bit token, and a client can also be authorised by its identity key alone. The host can revoke both.
- The host's owner can switch features off, require approval for each login, use one-time passwords and read an audit log.
- An authenticated client can do everything the host's user can do, within the features the host allows. Pair only machines you trust.
- Accounts are optional. An account grants no access by itself. A host may opt in to accepting its account's other devices; the membership list is signed by those devices, so the rendezvous server cannot add one. See [Accounts](#accounts).
- The mesh network (`remohop net`) lets an account's devices reach each other over IP. Its logins are limited to the mesh service, and a signed policy is enforced by each receiving device. A shared tailnet is used only as a path. See [Mesh network and Tailscale](#mesh-network-and-tailscale).
- Unattended access to a Windows machine's console (sign-in screen, lock screen, UAC prompts) is off by default; it exists only on a service installed with `remohop service install --unattended`. See [Windows unattended access](#windows-unattended-access).
- Two components deliberately sit outside the end-to-end channel: the browser gateway (`remohop web`) and the loopback WebDAV server behind macOS and Windows mounts. Both are explained below.

## Actors and what they can do

| Actor | Can | Cannot |
|---|---|---|
| Network observer between the machines | see that UDP traffic flows, its size and timing | read commands, files or screen contents; learn the password |
| Rendezvous server (`remohop signal`) operator, or a compromise of it | see which IDs are online, their public addresses and when they connect to each other; refuse to introduce peers, or go down; when it relays a session, see that session's ciphertext, size and timing, and throttle or drop it | decrypt or join a session; impersonate a host to a client that has pinned its key; test passwords offline |
| Active attacker on the path, including a malicious rendezvous server | block or disrupt traffic; cause connections to fail | complete the login without a credential; read session data |
| Someone with a host's ID but not its password | attempt password guesses, subject to the limits below | log in; obtain a token |
| Rendezvous server with accounts on | see which devices belong to an account and when they are online; refuse service; withhold the newest membership entries from a device | add a device to an account; log in to a host; forge the signed member list; read a session |
| Client that has paired or been authorised | everything the host's user can do, within the allowed features (see below) | use its credential from a different identity; keep access after `remohop host revoke` |
| Host operator (the machine owner) | everything on that machine; see every command, file, clipboard item and keystroke a client sends; with VPN `--exit`, see the client's traffic as it leaves the tunnel | nothing is hidden from them; they control the machine |
| Operator of a browser gateway (`remohop web`) | see and change everything that passes through the page, and use the gateway's saved pairings | nothing is hidden from them, see [Browser gateway](#browser-gateway) |
| Someone with read access to your disk | read whatever is stored in the config directory (see "Secrets on disk") | nothing beyond that, unless they also get the process |

## Rendezvous server

The rendezvous server is untrusted by design:

- It assigns each host's 9-digit ID to the host's Ed25519 identity key and keeps that mapping in its data directory (`signal` under the config directory, or `--data`).
- It tells a client where the host is: the candidate addresses on both sides, and the NAT type.
- It answers `remohop devices` presence queries for the IDs a client asks about: online, offline or unknown, no more than a connection attempt would reveal.
- It forwards no session traffic unless its operator enabled the relay.

A malicious server can lie about addresses, which only prevents a connection, because the login below fails. It can refuse service and it sees the metadata: which IDs connect and when. There is no built-in default server; you choose and run your own.

To stop a client from accepting a different rendezvous server than you intend, pin its key:

```bash
remohop config set signal-pin KEY
```

`remohop signal` prints its public key on start. A client refuses a rendezvous server whose key does not match the pin.

## Accounts

Accounts are optional and off by default. The rendezvous operator enables them with `remohop signal --accounts`. A person logs in with an email address (`remohop login`) and the server mails an 8-digit code (valid 10 minutes, single use; five wrong codes burn it and lock the address for 15 minutes).

**What an account is for.** It groups your devices so each can list the others and see which are online. It grants no access. Connecting to a host still needs a password, a pairing or a key that the host accepts.

**The one exception: account-trust.** A host whose owner ran `remohop config account-trust on` (default off) accepts the account's other devices like keys authorised with `remohop authorize`. Their keys appear in `remohop host paired` as `account:NAME`. The host's `ask-paired` setting applies to them. `remohop host revoke` of one keeps that device out until account-trust is turned off and on again.

**How membership is protected.** The membership of an account is an append-only, hash-chained log. Each entry is signed with the identity key of a device that is a member at that point: the first device creates the account, an existing member signs each addition, and any member can sign a removal. The hash of the first entry is the account ID. The server stores and relays the log but holds no member key.

**Joining a device.** Two comparisons make this safe against the server, and both are required:

1. The new device shows the account fingerprint. Confirm it equals the one `remohop account` shows on a device already in the account (scripts pass `--account-fingerprint`).
2. The new device prints its own identity fingerprint. On an existing device, run `remohop account approve FINGERPRINT` with the complete value. A prefix is refused.

Both fingerprints are 80 bits of SHA-256, so the server cannot grind a key that matches. Compare them over a channel you trust, not over the mail the code came in. A device keeps the account ID it confirmed, and it rejects a log that does not extend the longest one it already verified.

**What a malicious or compromised rendezvous server can do:**

- refuse service, or delete the account's stored data;
- see which devices belong together, which are online, when, and from which addresses, and the (hashed-index) account records it stores; it also sees the email address when it mails a code;
- withhold the newest log entries from a device. For account-trust that delays a removal: a host that has not yet seen it keeps accepting the removed device. A host never accepts a shorter log than one it has verified, so it cannot be rolled back once seen. A host that cannot reach the server keeps the last verified list for up to 7 days, then trusts no account device until it can check again. `remohop config account-trust off` takes effect at once;
- show itself as a "pending" device in the account. Never approve a device you did not start yourself.

**What it cannot do:** add a device to an account, log in to a host (even one with account-trust on), read a session, or forge the member list.

**What remains your job.** Compare the fingerprints for real. Anyone who controls a device that is already in the account can add other devices and, for hosts with account-trust on, can log in to those hosts. Remove a lost device with `remohop account remove` as soon as you can, and consider `account-trust` only for hosts where that risk is acceptable. The mail server and the email address are trusted only to deliver the login code.

The server's account file is `accounts.journal` in its data directory, indexed by a hash of the address, with session tokens hashed and codes never stored. RemoHop has not had an independent audit of this design.

## Mesh network and Tailscale

`remohop net` (see [net.md](net.md)) joins the devices of one account into an IP network. What it rests on:

- **Membership is the account's signed log**, as for account-trust: the rendezvous server cannot add a device to the mesh. A device whose log was last verified more than 7 days ago does not run the mesh until it can refresh it.
- **Links are ordinary sessions.** Each link is a RemoHop session from one device's host to the other's, with the host key pinned from the signed log and the client's identity key proven in the TLS handshake. The host admits such a key through a limited login scope: the session may open the mesh service (and ping) and nothing else, owner approval (`ask`) does not apply, and it is not a `remohop authorize` entry. A mesh device therefore gets no shell, files or screen on another device unless that host grants them in one of the usual ways. A login that does have full access (a pairing, `remohop authorize`, account-trust) may also open the mesh service, which still checks that the key is a member.
- **Addresses are claims both ends check.** Each device computes every member's address from its key and the log; a link whose peer claims another address is refused.
- **Policy at the receiver.** Every packet that arrives over a link is checked where it arrives: its source must be the sending device's own mesh address (or, as a reply, a network that device routes for this one), its destination this device (or a network this device routes for the mesh), and the policy must allow it; replies to connections this device opened pass. The policy is signed by a member and adopted only with a higher version, so a peer cannot roll it back to an older one it holds; any member can set a new one, as any member can approve or remove devices.
- **Subnet routes and exit nodes** are opt-in on the forwarding device (`--route`, `--exit`) and use the VPN's forwarding rules (masquerade, only replies back, guard on the other interfaces). A device using an exit node keeps its own LAN, its links and the rendezvous server off that route.
- **System DNS** is changed only per interface (systemd-resolved, vanishing with the interface) or, on request, in a marked block of the hosts file. The responder answers only `.remohop` names and refuses everything else.

**Tailscale.** RemoHop reads Tailscale's status (local API, CLI or interface) and, when both machines are on one tailnet, uses the peer's tailnet address as one more path for the same session. Nothing Tailscale says is taken as identity: the session over a tailnet is authenticated by RemoHop's login and host key pinning exactly as over any other path. A stub status (`REMOHOP_TAILSCALE_STATUS`) or a lying Tailscale can only make RemoHop prefer or try a path; it cannot make a session succeed without the credential and the pinned key. `REMOHOP_TAILSCALE=off` turns the detection off.

## Relay

A relay is off unless the rendezvous operator runs `remohop signal --relay`. It is used only after hole punching has failed, and it carries the same QUIC session with the same login:

- The relay passes ciphertext. It cannot read the session, and it cannot join one, because it does not know the credential and the key exchange is bound to the end-to-end TLS connection.
- The relay sees more than a rendezvous-only server: the size and timing of the whole session, and both peers' addresses for its duration. It can slow the session down or cut it.
- A relayed session is announced on stderr when it is set up and is labelled `relayed` in `remohop <ID> ping` and `remohop status`.
- A client or host refuses relayed connections with `remohop config relay off` or `REMOHOP_RELAY=0`. With that set, a connection that cannot be made directly fails.
- Traffic over the cap is paced: held back for up to 100 ms, then dropped. The queue is bounded per session (1024 packets, at most 100 ms of the cap), so pacing adds no unbounded memory.

## Websocket bridge

`remohop signal --ws-listen` serves a websocket bridge for peers whose network lets only HTTP(S) out. It is off by default. Each WebSocket connection gets its own UDP socket on the server, and each binary message carries one datagram and a port number.

- **Not an open proxy.** A datagram from a WebSocket goes only to one of the server's own UDP ports: the rendezvous port, the NAT-detection port, or the port of a relay session open at that moment. Every other port is dropped, and so is anything arriving at the bridge socket from an address other than those server ports. The destination host is never taken from the client. The bridge cannot be used to send UDP to a third party, and it reaches nothing a UDP client could not reach already.
- **The server still sees only ciphertext.** What crosses the bridge is the same QUIC a UDP peer would send: the rendezvous protocol (TLS 1.3, server key pinnable) and relayed sessions (end-to-end encrypted, PAKE-authenticated). A TLS-terminating proxy in front, such as a Cloudflare Tunnel, sees the same ciphertext and the metadata the relay sees (sizes, timing, the client's address). It cannot read or join a session or learn a password. The server pin defeats an impostor bridge exactly as it defeats an impostor UDP server.
- **Bridged peers are relay-only.** The server marks a bridged peer in introductions. The other side then neither punches nor probes any address of it, and the bridged peer's self-reported candidates are discarded, so it cannot make a host send probes to that host's own loopback or LAN. Such connections need the relay, and a client or host with relaying turned off refuses them.
- **Abuse limits.** Open WebSocket connections are capped (`--ws-max-conns`, default 512) and so are those per source address (32). Each connection has a byte budget of the relay's per-session rate plus 32 KB/s, there is a total budget across connections, a message carries at most 2051 bytes, and a connection idle for 2 minutes is closed. Bridged peers count against the rendezvous server's own per-address limits by the WebSocket client's address. Behind a proxy that address comes from `--ws-real-ip-header` (for example `CF-Connecting-IP`), and a request without that header is refused. The header is trusted, so only set it when the bridge is reachable through that proxy alone (for example `--ws-listen 127.0.0.1:PORT` behind `cloudflared`). Otherwise a client could pick its own address for the limits.
- **Browsers are refused.** The server accepts only the remohop subprotocol and the remohop client's Origin value, and browsers always send the page's origin. A web page therefore cannot use its visitors to talk to the bridge.
- **Access tokens.** A Cloudflare Access service-token secret is read from a file (`signal-ws-access-secret-file`) or `REMOHOP_SIGNAL_WS_ACCESS_SECRET`, never from the command line. It is sent only to the bridge, inside TLS for `wss://` (also through an HTTP proxy, which sees only the CONNECT target), and it is never logged. Proxy credentials in `HTTPS_PROXY` are sent to the proxy only and kept out of error messages.
- The server logs one line when a bridge opens and one when it closes (client address, duration, byte counts). Headers and payloads are not logged.

## Public relay servers

An operator who offers the relay to the public (see [public-server.md](public-server.md)) limits it as follows. Rendezvous stays open and keeps its own rate limits:

- **Per-address cap** (`--relay-per-source`, default 2): concurrent relayed sessions per source (IPv4 address, IPv6 /64), counting both peers. A bridged peer counts by its WebSocket client's address. A client releases its session with an authenticated message when it closes, so the cap does not hold slots for the relay's idle timeout.
- **Tiers** (`--relay-account-kbps`, `--relay-for accounts`): the better tier applies only when the server itself can verify that a peer is a member of an account in its own ledger. A host's key is proven by its registration signature. A client proves its key with an Ed25519 signature over a label, the introduction's nonce and the TLS exporter of its rendezvous connection. The signature is fresh for each introduction and useless on any other connection. A key without a valid proof is refused, not ignored. A client sends its key only when it is a member of an account on that same server, identified by the pinned server key. Otherwise relay requests stay anonymous and the server never learns the client's identity key.
- **Deny list** (`--relay-deny`): addresses, networks and identity keys that get no relay. Listed keys also cannot register as hosts. Clients are listed by address unless they identify themselves, which only account members do.
- **Monthly budget** (`--relay-monthly-gb`): relayed bytes are counted per UTC month in the data directory, written atomically about once a minute. A crash loses at most the last minute. Anonymous sessions get half their rate from 80 %, anonymous sessions are refused from 100 %, and all new sessions are refused from 110 %.
- **Summaries without addresses**: the hourly `relay summary` log line has counts only. Per-session log lines do contain both peers' addresses (see public-server.md for what to tell users and how long to keep logs).

None of this changes what the relay can see: ciphertext, both addresses, timing and volume.

## Session establishment and authentication

1. QUIC is set up over UDP. TLS 1.3 inside QUIC encrypts the traffic, but the peers have no certificate authority, so TLS alone does not say who the peer is.
2. On the first stream, both sides run CPace over the credential (password or token). The TLS exporter of this exact connection is mixed into the key derivation, each side binds its identity key into the exchange, and each side proves knowledge of the key in a confirmation step.
3. A man in the middle ends up with a different TLS exporter on each leg. The two legs derive different keys, the confirmation fails, and the attacker learns nothing useful for guessing the password.
4. The password is never sent. An attacker who records a failed login cannot check candidate passwords offline.
5. Before it tells the client it is in, the host sets the session up completely (who the client is, the grant that limits it, if any) and makes its last checks in one step: a grant's session limit, a grant or pairing that is still current, a one-time password that is still unspent. Only then does it serve the streams the client opens, so no request is handled for a session that is not fully set up or was refused. A login refused at this point is recorded as refused, not accepted. On the host side, a session with no record of its grant is treated as allowed nothing, never as full access.

## Passwords and rate limiting

A host has one password at a time:

- **Temporary password.** Generated with a cryptographic random generator on every start: 8 characters, which the code estimates at about 40 bits. It is shown as `xxxx-xxxx`; the dash is not part of it. `remohop status --show-password` and `remohop otp` print it.
- **One-time password.** With `remohop config otp on`, the temporary password is replaced after every login that used it. `remohop otp new` replaces it at once. The host spends it in one step when it accepts the login, after any approval and before it tells the client it is in: of several logins that present it at the same time exactly one gets in. A login that is denied, is not answered in time, or gives up while it waits for approval does not spend it. A login that proved a temporary password that was replaced in the meantime (spent by another login, or by `remohop otp new`) is refused.
- **Fixed password.** Set with `remohop passwd`, which asks without echo and keeps the password off the command line and out of shell history. It must be at least 8 characters, not counting spaces and dashes. `remohop status` and `remohop host` never print a fixed password. It is stored in the host's `config.json` (owner-only, see "Secrets on disk"), so anyone who can read that file can read it.

Failed logins are limited by the host (`internal/auth/limiter.go`). With its defaults:

- Per source address, the first two failures are free. From the third consecutive failure the source is locked out for 10 seconds; each further failure doubles the lockout, up to 10 minutes.
- A source's failure count is cleared after 30 minutes without attempts.
- Across all sources there is a burst of 20 failed attempts, refilled at one attempt every 5 seconds.

Online guessing at this rate is slow, but not impossible against a weak password. Prefer pairing or key authorisation to typing a password, and consider `remohop config ask on` for hosts whose password is shared.

## Pairing, authorised keys and identities

**Pairing.** `remohop <ID> pair` asks the host for a random 256-bit token bound to the client's identity key. The client stores it in `peers.json`. Later connections present the token instead of the password. `remohop host --no-pairing` refuses pairing.

**Authorised keys.** A client can log in with its identity key alone:

1. On the client, `remohop key export` prints one line with the public key.
2. On the host, `remohop authorize "LINE"` authorises that key and prints the host's own key line.
3. On the client, `remohop peers trust TARGET "HOST-LINE"` records the host's key.

No password is involved, so there is nothing to guess, and the host's key is pinned from a line you received from its owner instead of on first use. The exchange is only as trustworthy as the channel you used to pass the two lines.

**Revocation.** `remohop host paired` lists pairings and authorised keys. `remohop host revoke NAME|ID...` removes them, and open sessions that used them are closed within seconds. On the client, `remohop peers forget` discards the saved token and key.

**Identity management.** `remohop key export --private FILE` writes the private key to a file with mode 0600 so that `remohop key import FILE --yes` can move the identity to another machine. Delete that file once imported: it is a full copy of the machine's identity. `remohop key gen --yes` and `remohop key clear --yes` discard the identity for good: every pairing and authorised key that hosts hold for it stops working, clients that pinned it as a host refuse it, and a host on this machine gets a new ID.

**Device names.** `remohop config set name NAME` is shown to peers in `ping`, approval prompts and `remohop status`. A name is whatever the other side chose to call itself. When you approve a login, judge it by the identity fingerprint and source address, not by the name.

## Host access control

The host's owner decides what clients may do. Changes take effect at once, also for sessions that are already connected:

- **Feature switches.** `remohop config feature NAME on|off` for `shell`, `file`, `forward`, `clip`, `screen`, `input`, `info`, `vpn`, `serial` and `usb`. A refused request names the feature and the command that would allow it. Switch off what a client does not need: `input` for a view-only machine, `shell` for a file drop, `usb` and `serial` unless you use them. Switching a feature off also ends what connected clients have open with it (the host re-checks every open stream within a quarter of a second and cuts streams that do not stop by themselves half a second later): commands are killed, attached terminals ended, transfers, forwards and tunnels cut. `input` off leaves an open remote desktop showing the screen but releases the keys and buttons the client holds and ignores its input from then on. Detached terminals and background jobs keep running, unreachable while the feature is off. See [Host access control](en/host-access-control.md#what-a-switch-does-to-sessions-already-connected).
- **VPN routing.** `remohop config vpn-exit` is off by default. Without it a VPN client reaches only the host itself and the networks the host is directly attached to.
- **Approval.** With `remohop config ask on`, each password login waits until someone at the host says yes, in the `remohop host` terminal or with `remohop host approve N`. `remohop config ask-paired on` extends this to paired clients and authorised keys. An unanswered request is refused after a few minutes. A host running as a service has no terminal, so answer with `remohop host approve`.
- **USB allow list.** A client can only attach a USB device the host's owner allowed with `remohop usb allow BUSID`, which records its bus ID and VID:PID.
- **Account devices.** `remohop config account-trust on` is off by default. Leave it off on hosts that should only accept credentials you gave out one by one.
- **Grants.** `remohop host grant NAME --for 2h --allow ...` issues a credential with less than full access: allowed services, file roots, a command allow-list, expiry, a session limit, approval of each command and file change, and recording. The host checks it on every stream and every command or file operation; `remohop host revoke NAME` and `remohop host panic` end its sessions at once. Each check also compares the session's credential with the grant's current one, so reissuing a grant under the same name (`remohop host grant NAME ...` again) cuts off sessions on the old code at their next request, even when the running host is not told: they never get the new grant's rules. File roots are pinned when the host loads the grant and file operations go through a handle on each root (`os.Root`), so links, junctions and directories swapped in cannot lead them out; a root whose path comes to name a different directory is refused, a granted session cannot remove or replace a root or create links that lead out of it, and resumable uploads use partial files the host names and binds to the grant's credential (see [agents.md](agents.md#grants)). See [agents.md](agents.md) for the vocabulary and [what grants do not do](agents.md#limits-of-the-protection).
- **Audit log.** `remohop log` shows logins accepted and refused with the reason, approval decisions, the first use of each feature in a session, and session ends. It is `audit.jsonl` in the config directory, readable only by the owner and rotated at 1 MB. It records which features were used, not every command, and anyone who controls the host account can edit it.

## Host identity and key pinning

Each machine has an Ed25519 identity key in `identity.key` in its config directory. A host's fingerprint is shown by `remohop status` on the host and `remohop peers` on the client.

On the first successful password login, the client records the host's key ("trust on first use"). That login succeeds only if the host proved the password on that connection, so an attacker who does not know the password cannot be pinned in. For a sensitive host, compare the fingerprint through a channel you trust, or use `remohop peers trust` with the host's key line.

On later connections, a different host key is refused before the client sends any login message:

```
host identity has changed: <target> now presents key <new> but <old> was pinned on first login. Someone may be impersonating the host. If the host was reinstalled, run `remohop peers forget <target>` and connect again
```

A host started by hand and the same machine's installed service are two instances with two identities and two IDs. Only run `remohop peers forget` after you know why the key changed.

## What an authenticated client can do

A client that passes the login gets the host's services, and those services run as the user that runs the host. With every feature on, a client can:

- run commands, shells and background jobs, and forward TCP and UDP ports, with that user's privileges;
- read, write and delete files the user can access, and mount directories;
- read and set the clipboard, read the host's clipboard history (`clip history --remote`), and take screenshots;
- move the pointer, click and type, unless `input` is off or `REMOHOP_DESKTOP_VIEW_ONLY=1` is set on the host. On a macOS host this works only after the owner grants `remohop` the Accessibility permission (System Settings > Privacy & Security > Accessibility; capture needs Screen Recording). Without it macOS drops the events and `remohop` reports the missing permission;
- listen to what the host plays (`desktop --audio`): the `screen` feature covers it, a client must ask for it per session, `REMOHOP_DESKTOP_AUDIO=off` on the host refuses it alone, and only the machine's output is captured, never a microphone, unless the owner sets `REMOHOP_DESKTOP_AUDIO_SOURCE`;
- open the host's serial ports, which may be consoles with a login prompt;
- export a USB device the owner allowed, which detaches it from the host's own drivers while attached;
- open a VPN tunnel and reach the host at the IP level, plus its directly attached networks; with `vpn-exit on`, any network the host can reach, including the internet, using the host's address;
- ask the host to send Wake-on-LAN packets on its own networks. The host picks the destinations and limits the rate.

Below the feature switches, the only finer permissions are grants (see [Host access control](#host-access-control) and [agents.md](agents.md)): a client that logs in with a grant's credential gets what the grant allows and nothing else. Otherwise, pair only machines you trust, switch off features you do not need, and revoke pairings that you no longer use. A host installed as a system-wide service runs as root (or LocalSystem on Windows) unless you chose otherwise with `--run-as`; a client of that host has those privileges.

## What a host can do to a client

A host does not get services on your machine. Hosts export services, and a client exposes local services only to sessions it opts into. A host cannot open a shell, read files or use the clipboard on your machine by itself. But several commands you run give the host a way in, and you should know which:

- **Reverse forwards** (`forward -R`) let the remote machine connect to a local address you named.
- **Clipboard sync** (`clip`, unless `--pull-only`) sends your clipboard to the host while it runs.
- **VPN.** While a tunnel is up, the host can send IP packets to this machine's tunnel address, so local services listening on all interfaces are reachable from it. With `--exit`, all your traffic leaves through the host, which sees whatever is not encrypted end to end above the tunnel; DNS goes through the tunnel too where systemd-resolved is in use.
- **USB attach.** An attached remote device is handled by this machine's kernel USB stack, so a malicious host can present whatever device it likes to your kernel. Attach devices only from hosts you trust.
- **Serial.** Whatever the remote port sends arrives in your local terminal program.
- **Files.** Files you copy or open from a mount come from the host. Downloads with `cp` and `sync` never get setuid or setgid bits.
- A host sees everything sent to it: commands, files, clipboard content, typed text.

Treat any output from a remote machine as data, not instructions. This matters most for AI agents, which can be steered by text in command output, files or screenshots.

## Windows unattended access

`remohop service install --unattended` (Windows only, from an elevated prompt) changes what a client of the Windows service can do. Without it the service runs as LocalSystem in session 0 and sees no desktop. With it, a client that passes the service's login sees and controls the session on the physical console, whoever is logged in there or nobody, as LocalSystem:

- the user's desktop, and the secure desktop: the sign-in screen, the lock screen, the Ctrl+Alt+Del screen and UAC prompts. Input reaches elevated windows and the secure desktop, which input from a user's session cannot (UIPI). A client can therefore type a password into the sign-in screen, answer a UAC prompt, or use an unlocked session it found;
- Ctrl+Alt+Del, sent by the service with `SendSAS`, but only where the machine's policy allows services to (`SoftwareSASGeneration` = 1 or 3; the service does not change the policy);
- the console session after a log off, fast user switching, or a remote desktop connection that took the user's session away from the console. `REMOHOP_UNATTENDED_SESSION=active` in the service's environment shows a session with a user logged in, such as a remote desktop one, instead of an empty console.

What does not change: who may log in. The service's password, pairings, authorised keys, approval (`remohop config ask on`), one-time passwords, feature switches (`screen`, `input`) and the audit log apply exactly as before; `--unattended` grants nothing to anyone by itself. It is the reach of an authenticated client that grows, from session 0 to the console, so treat the service's credentials like the machine's administrator password. Nobody at the console is asked or told; there is no on-screen indicator yet. Reinstall without the flag to turn it off.

How it is built (internal/unattended), and what each part prevents:

- **Only the service starts helpers.** For the screen and input the service starts a helper process, `remohop service session-helper`, with a copy of its own LocalSystem token moved into the console session (SeTcbPrivilege), on `winsta0\Winlogon`. The helper refuses to run unless it is LocalSystem, so starting it by hand does nothing; only something that already has LocalSystem-level rights can start one.
- **A private pipe per launch.** Service and helper talk over a named pipe with a random 128-bit name, created by the helper with `FILE_FLAG_FIRST_PIPE_INSTANCE` (it does not serve on a name someone else made) and an access list that admits LocalSystem alone, not Administrators and not the logged-in user; remote clients are rejected. One instance always waits, so the name never lapses while the helper runs.
- **Both ends check each other.** The helper accepts connections only from the process ID of the service that started it; the service talks only to a pipe served by the process ID it started. Both then prove knowledge of a 256-bit secret made for that launch (HMAC-SHA256 challenge and response in both directions, so neither side's answer can be replayed). The secret reaches the helper through an inherited anonymous pipe (its standard input), never the command line; only that pipe and the helper's log pipe are inherited.
- **Lifetime.** Helpers are started on demand, ended after a minute without use, ended when the console moves to another session, and placed in a job object that kills them with the service. A helper also exits when the service process ends.
- **No windows.** The helper creates no windows, so window messages from the user's session cannot reach it. Its threads attach to the input desktop (`OpenInputDesktop`, `SetThreadDesktop`) for each capture and input operation; capture uses GDI, which works on the secure desktop.
- **Binaries.** The service runs `remohop.exe` as LocalSystem in the console session, and the desktop service runs ffmpeg as LocalSystem. Keep both where only administrators can write, such as under Program Files; `install --unattended` warns when `remohop.exe` is elsewhere. A user who can replace either file gets LocalSystem.

## Windows VPN driver

The Windows VPN client needs the Wintun driver. `remohop` does not ship or download it. It looks for `wintun.dll` in `REMOHOP_WINTUN_DIR`, next to `remohop.exe`, in the config directory and on `PATH`, and accepts only the build whose SHA-256 it has pinned (Wintun 0.14.1). It copies the matching file into a directory only the current user can write and loads it from there, because the DLL runs with Administrator rights. A different file with the same name is refused.

## Mobile apps

The Android and iOS apps are controllers only. Each is a native shell with a WebView on an in-process `remohop web` gateway, so the [browser gateway](#browser-gateway) caveats apply, with the gateway running on the phone. The access token is in the URL the app loads. The apps keep their identity and pairings in the app's private directory. The iOS app has only run in the iOS Simulator in CI, and the Android app has had no real-device test.

## Local listeners on the client

Several commands open a listener on this machine. All of them bind to `127.0.0.1` by default:

- **Desktop viewer** (`remohop <ID> desktop`): the URL contains a random 128-bit token, the server rejects requests whose `Host` header is not a loopback name, and it compares the token in constant time. Anyone who can read the printed URL while the viewer runs can use it. Files dropped on the page are uploaded with a POST that must carry the viewer's own `Origin` and a custom header, so a web page of another origin cannot write to the remote machine through your browser; uploads need the host's `file` feature and never overwrite.
- **WebDAV mounts** (the default on macOS and Windows, or `--webdav`): a WebDAV server inside remohop on a random port, below a random 128-bit path. It refuses requests from web pages and requests not addressed to 127.0.0.1. There is no password, because Windows refuses Basic authentication over plain HTTP: the URL is the credential, and it shows in the mount table (`mount`, `net use`), so other users of the same computer who can read it can use the mount while it exists. On a shared computer, prefer `remohop cp` or FUSE.
- **Port forwards** listen on 127.0.0.1 unless you give a bind address; `*` exposes them to your network.
- **Recordings** (`desktop --record`) are written with mode 0600 and contain everything shown on the remote screen.

## Browser gateway

`remohop web` serves a browser client. A browser cannot hole-punch or speak the QUIC session, so the gateway process is the client: it speaks the end-to-end encrypted session to the host and serves the page. That has consequences:

- **The gateway sees everything it relays:** the screen, every keystroke, files, the clipboard, and any host password typed into the page.
- **The browser-to-gateway leg is not end to end with the host.** It is plain HTTP on loopback, or TLS that terminates at the gateway when self-hosted.
- **Anyone who logs in to a gateway can use its saved pairings**, the peers in its config directory. A shared gateway should run with an empty config directory or `--no-saved`, so users bring the host's ID and password each time and cannot pair.
- A password typed into the page is used for one login and not stored, unless "pair this gateway" is ticked, which saves a token in the gateway's peers.

Protections:

- By default the gateway listens on 127.0.0.1 and the login is an unguessable token in the URL. `Host` and `Origin` headers are checked against DNS rebinding and cross-site WebSocket hijacking, and the page uses no external assets.
- Any non-loopback `--listen` address refuses to start without `--tls-cert`, `--tls-key` and `--password-file`. The password (at least 8 characters) is read from a file with mode 600. Login is rate limited with lockout, the session cookie is HttpOnly, Secure and SameSite=Strict, and every state-changing request carries a per-session CSRF token.
- Behind a TLS-terminating proxy or tunnel, `--public-url https://HOST` adds that one host (as https) to the accepted `Host` and `Origin` values. It requires a loopback `--listen` and `--password-file`, so the plain-HTTP port is reachable only from the machine itself. Anyone with a local account on that machine can reach it too, and could set `CF-Connecting-IP` to spread login guesses over many limiter keys; the limiter's global failure budget still caps the total. The proxy terminates TLS, so its operator (for example Cloudflare) sees everything the gateway sees.
- Program access is off unless the gateway is started with `--api-token-file FILE` (which needs `--password-file`). A program can then authenticate every request with `Authorization: Bearer <token>` instead of the login call and cookie, and needs no CSRF token or Origin: a browser never adds an Authorization header by itself. A reverse proxy can add one to browser traffic, though, and the gateway cannot tell who added it. So a bearer request that carries browser signals (`Origin`, `Sec-Fetch-Site`/`Sec-Fetch-Mode`, or cookies) is held to the browser rules: it must not be cross-site, and a state-changing one must carry the CSRF token; a bearer WebSocket or download that sends an `Origin` must be same-origin. Old browsers that send none of these headers are not covered, so a proxy that injects the token must apply it to program traffic only (see [Browser client](en/web-gateway.md)). Use a token other than the gateway password, so that an injected program credential is not also the browser login. Wrong tokens count against the same rate limiter as the login form. All bearer clients share one gateway session, so anyone holding the token shares its host connections.
- The host's own feature switches still apply to what the page can do.

Use only a gateway you control.

## Quick support window

`remohop quick` (and `RemoHop.exe` / `RemoHop.app`, which run it) is a host plus a local page that shows the ID and the temporary password, asks about each connection and lists sessions. Its rules are fixed in code and cannot be loosened from the config file: every login needs approval (pairings and authorised keys too, refused after 60 seconds without an answer), only the temporary password of the run is accepted, no pairing is minted, and only the features ticked in the window are offered (features switched off in the config file stay off).

- **The page is local.** It is served on `127.0.0.1` only, under a path that starts with a 128-bit random token; the process refuses a `Host` header that is not the loopback address with its port (DNS rebinding), refuses API requests that a browser marks as coming from elsewhere (`Origin`, `Sec-Fetch-Site`), and accepts state changes only as JSON `POST`s carrying a second random value that the page obtains over that same checked channel. It sets a strict Content-Security-Policy, cannot be framed, and puts text from the other side (device names) into the page as text only. The browser is started on a private file that redirects to the page, so the token is not in any process's argument list.
- **Nobody approves themselves.** Approval happens during login, before a session exists; a client that is waiting has no service to call and no way to send input to the host. There is no remote API for approving. The native question on Windows (a message box) and macOS (`osascript`) is shown by the host process on its own desktop and receives the client's name as data, never as script or markup.
- **An allowed helper with mouse control can click the window.** That includes "Allow" for a later connection and the three switches. This is inherent in remote control; the user limits it by unticking mouse and keyboard, and ends it with Disconnect or Stop sharing. Unticking applies to the open desktop stream before the click returns: keys and buttons the helper holds are released and nothing the helper sends afterwards is injected, while the screen keeps showing. Unticking files or commands likewise ends the helper's open transfers, terminals, commands, forwards and VPN.
- **Other users of the same computer** cannot use the page without the token, which is in memory, in the page's address in the user's browser (and its history), and in `quick.url` in the config directory (mode 0600, removed on exit) so that a second start can bring the window back. The token is new on every start.
- **Closing the window stops sharing.** The page tells the process when it is closed and the process ends every session. If the browser dies without saying so, the process stops once nobody is connected.
- **On Windows** the program runs with the user's rights, not elevated: the helper cannot operate UAC prompts or elevated windows.

## Secrets on disk

The config directory is `$REMOHOP_CONFIG_DIR`, or the platform default. An installed system-wide service keeps its own: `/var/lib/remohop` on Linux, `/Library/Application Support/remohop` on macOS and `%ProgramData%\RemoHop` on Windows.

On Linux and macOS the directory is created with mode 0700 and the files below with mode 0600. Windows has no mode bits: RemoHop sets an owner-only access list on its configuration directory, and the Windows service keeps its state under ProgramData restricted to SYSTEM and Administrators.

| File | Holds |
|---|---|
| `identity.key` | the machine's Ed25519 identity key |
| `peers.json` (client) | pinned host keys, pairing tokens, aliases |
| `paired.json` (host) | pairing tokens and authorised keys |
| `config.json` | settings, including a fixed `host-password` if set |
| `usb-allow.json` (host) | the USB devices the owner allowed |
| `audit.jsonl` (host) | the audit log |
| `grants.json` (host) | grants: names, limits, expiry; their credentials are in `paired.json` |
| `recordings/` (host) | asciicast recordings of granted sessions made with `--record`; they contain whatever the commands printed |
| `signal/` (rendezvous server) | the server key and the ID table |
| `jobs/` (host) | background job records and logs; logs contain whatever the job prints |
| `clip/` | clipboard history; on a host, `clip/host` records what passed through its clipboard during sessions. It contains clipboard text verbatim |
| `wake.json` (client) | names saved with `remohop wake --save` |

Nothing in the config directory is encrypted at rest. Clipboard history can hold copied passwords; `remohop clip history off` stops recording and `remohop clip history clear` removes entries. The service definition written by `remohop service install` never contains the password. A `--password-file` for `remohop web` and a `key export --private` file are secrets of their own.

## MCP server and agents

`remohop mcp` runs as a child of the agent host and speaks MCP over stdio:

- Connections use saved pairings, or `REMOHOP_PASSWORD` in the server's environment if you set it there. The server cannot prompt, so a machine that is not paired produces a tool error that asks a person to run `remohop <ID> pair` in a terminal.
- The tools accept no password argument. The model never sees a password.
- File tools that write on this machine take absolute paths, refuse to replace an existing file unless told to, never follow a symlink at the destination, and refuse anything inside RemoHop's own config directory.
- The GUI tools let an agent click and type on the remote screen. Do not let an agent type secrets: the text passes through its transcript. Switch `input` off on hosts where an agent should only look.
- An agent acts with the authority of the credential it uses. With a pairing that is everything the host's user can do; with a grant (`remohop host grant`, redeemed with `remohop peers redeem`) it is what the grant allows, enforced by the host. `remohop_limits` reports the grant's limits to the agent, refusals name the limit and the grant, and `remohop mcp --read-only` hides the tools that change anything (a convenience on the agent's side, not a boundary). See [agents.md](agents.md).

## Limits and known gaps

- RemoHop has not been independently audited.
- Accounts rely on you comparing both fingerprints. A device already in an account can add others, and hosts with account-trust on then accept them. See [Accounts](#accounts).
- There are no published releases yet. Windows binaries are not code-signed; the release workflow signs and notarizes the macOS binary. The install script checks a download against the release's checksum file, which on its own detects a damaged download, not a compromised release; with cosign installed (or `--verify require`) it also checks that file's keyless signature, which ties it to this repository's release workflow.
- The rendezvous server learns which IDs talk to each other and when; a relay also sees the volume and timing of the session.
- Trust on first use relies on the first login being genuine.
- A weak fixed password can be guessed online within the rate limits.
- Feature switches are coarse: `shell` on means any command. Grants are finer, but a command allow-list decides which program starts, not what it does, and file roots do not confine a shell; see [agents.md](agents.md#limits-of-the-protection).
- The browser gateway and the WebDAV mount server are outside the end-to-end channel by design.
- Windows unattended access shows nothing on the console while a client watches or controls it, and has been run only on hosted CI runners (one session, one monitor, no physical console); multi-monitor consoles, remote desktop sessions with `REMOHOP_UNATTENDED_SESSION=active` and fast user switching are not covered by automated runs.
- USB/IP has been run against the real kernel only in CI (a virtual dummy_hcd device), not with physical USB hardware; treat it as experimental, and remember that the kernel's USB/IP code is part of what you expose.
- macOS and Windows are tested by automated runs on hosted runners, not on physical desktops; GUI input and WebDAV mounts on Windows have not been run. The macOS VPN client, the macOS desktop host (capture, VideoToolbox, input) and the iOS app are verified on hosted machines only: a macOS VM with one 1024x768 display, and the iOS Simulator. Physical Macs and iOS devices, Retina and multi-monitor Macs, `vpn up --exit` from macOS on a real network, GPU encoders other than VideoToolbox, and the Android app on a real device are untested. See [troubleshooting.md](troubleshooting.md#platform-notes).
