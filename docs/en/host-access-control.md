# Host access control

[中文](../zh/host-access-control.md)

The owner of a host decides what connected clients may do. These commands run on the host and take effect at once, also for sessions already connected:

```bash
remohop config feature input off
remohop config ask on
remohop config otp on
```

| Control | Command | Effect |
|---|---|---|
| Feature switches | `remohop config feature NAME on\|off` | NAME is `shell`, `file`, `forward`, `clip`, `screen`, `input`, `info`, `vpn`, `serial` or `usb`; all are on by default |
| VPN routing | `remohop config vpn-exit on` | let VPN clients reach networks beyond the host's own LAN, and use the host as an exit; off by default |
| Approval | `remohop config ask on` | every password login waits for a yes at the host: answer in the `remohop host` terminal, or with `remohop host approve N` / `remohop host deny N` |
| Approval for known clients | `remohop config ask-paired on` | with `ask on`, paired clients and authorised keys need approval too |
| One-time password | `remohop config otp on` | the temporary password is replaced after each login, and only one of several logins presenting it at once gets in; a denied, unanswered or abandoned login does not use it up; `remohop otp` prints the current one, `remohop otp new` replaces it |
| Fixed password | `remohop passwd` | set or (`--clear`) remove it; `remohop status` never prints a fixed password |
| Pairings | `remohop host paired`, `remohop host revoke NAME` | list and remove paired clients and authorised keys |
| USB devices | `remohop usb allow BUSID`, `remohop usb allowed` | allow each device before a client may attach it |
| Account devices | `remohop config account-trust on` | accept the account's other devices like authorised keys; off by default (see [Accounts](accounts.md)) |
| Audit log | `remohop log` | logins accepted and refused, approval decisions, first use of each feature, session ends |

Two more settings: `remohop config set name office-pc` gives the device a name shown to peers (in `ping`, approval prompts and `remohop status`), and `remohop config set term-keep 2h` changes how long a detached shell stays on the host.

A client can also log in with its identity key alone, with no password to guess. On the client, print the public key; on the host, authorise it; back on the client, record the host's key line that `authorize` printed:

```bash
remohop key export
remohop authorize "remohop-key ... laptop"
remohop peers trust 276941009 "remohop-key ... host"
```

The first and third commands run on the client, the second on the host. `remohop key export --private FILE` and `remohop key import FILE --yes` move an identity to another machine; `remohop key gen --yes` replaces it. [security.md](../security.md) explains what each control protects against.

## What a switch does to sessions already connected

Switching a feature off refuses new requests for it at once, and also ends what connected clients already have open with it. The host notices a change made with `remohop config` within a quarter of a second; the switches of the [quick-support window](quick-support.md) apply before the click returns. A stream that does not end by itself is cut half a second later.

| Feature off | What is already open |
|---|---|
| `input` | An open remote desktop keeps showing the screen. The host lets go of every key and mouse button the helper holds, ignores the helper's keyboard and mouse from then on (also "type text", lock screen and the other host controls) and tells the viewer, which shows the session as view-only. Switched back on, the same desktop session can control again. Input commands still running (`remohop <ID> type`, `click`, ...) end. |
| `screen` | Remote desktop, desktop audio and screenshots end; the viewer says the host's owner switched the screen off. |
| `shell` | A running command (`remohop <ID> shell -c ...`) is killed with its process group. An attached terminal is ended (its shell is hung up, not kept for re-attaching) after a notice in the terminal. Terminals that were already detached and background jobs (`spawn`) keep running on the host, as they do when a client disconnects, but no client can reach them until `shell` is on again. |
| `file` | File sessions (browsing, `mount`) and transfers in progress end. An interrupted upload does not install the file. |
| `clip` | Clipboard streams end. |
| `forward` | Forwarded connections are cut, reverse-forward listeners on the host are closed, UDP forwards and wake requests end. |
| `vpn` | The tunnel ends. |
| `serial`, `usb` | Serial ports are closed and attached USB devices are given back to the host. |
| `info` | System information requests end. |

The same happens to a session that logged in with a grant when the grant stops allowing a service. Turning a feature on again does not reopen anything: the client opens it again (the desktop's keyboard and mouse are the exception above).
