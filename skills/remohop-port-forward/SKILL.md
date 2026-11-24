---
name: remohop-port-forward
description: Reach a service that runs on or behind a remote machine (web app, API, database, dashboard) from localhost by forwarding a port through RemoHop with `remohop <ID> forward`. Use when a remote service is unreachable or only listens on the remote machine's localhost, when you need to curl/test/browse something on another machine, or when a remote machine must reach a service running locally (reverse forward). No SSH, VPN or open ports.
---

# RemoHop port forwarding

Maps a TCP port on the remote machine's network to a port on this machine (like `ssh -L`), or the other way round (`ssh -R`). Requires a paired host (`remohop <ID> pair`; see `skills/README.md`).

(With the MCP server: `remohop_forward_start`, `remohop_forward_list`, `remohop_forward_stop`. Those forwards live inside the MCP server process and end when it exits.)

## Workflow: "remote service unreachable"

1. **Diagnose on the remote side first** - is it up, and where does it listen?
   ```bash
   remohop <ID> shell -c 'ss -ltnp | grep -E ":8080\b"'          # listening? on 127.0.0.1 or 0.0.0.0?
   remohop <ID> shell -c 'curl -sS -m 5 -o /dev/null -w "%{http_code}\n" http://127.0.0.1:8080/'
   ```
   If it is not listening or answers locally with an error, fix that - forwarding will not help.
2. **Forward it to a free local port, in the background, with machine-readable output:**
   ```bash
   remohop <ID> forward -b --json 0:8080
   # {"direction":"local","listen":"127.0.0.1:41873","target":"127.0.0.1:8080"}
   ```
   `0` as the local port picks a free one; **always read the actual address** from the output (the `listen` field).
3. **Test against localhost:**
   ```bash
   curl -sS -m 10 http://127.0.0.1:41873/health
   psql -h 127.0.0.1 -p 41874 -U app appdb          # for a database forward
   ```
4. **Clean up when done:**
   ```bash
   remohop ps                      # lists running forwards with PID, target, listen -> target
   remohop stop <PID>              # stop one
   remohop stop --all              # stop every background remohop command (including mounts, clip sync)
   ```

## Spec forms (default bind is 127.0.0.1)

| Spec | Meaning |
|---|---|
| `LPORT:RPORT` | local `LPORT` -> `127.0.0.1:RPORT` as seen from the remote machine |
| `LPORT:RHOST:RPORT` | local `LPORT` -> `RHOST:RPORT`, any host the remote machine can reach (e.g. `5433:db.internal:5432`) |
| `BIND:LPORT:RHOST:RPORT` | listen on `BIND` instead of loopback (`*` = all interfaces; avoid unless the user asks) |

IPv6 literals go in brackets. `LPORT` may be `0` in every form. Several specs can be given at once: `remohop <ID> forward -b 0:3000 0:5432`.

## Reverse forward (remote machine reaches something local)

```bash
remohop <ID> forward -b -R 9000:3000      # remote :9000 -> my localhost:3000
remohop <ID> shell -c 'curl -sS -m 5 http://127.0.0.1:9000/'   # test it from the remote side
```
The remote machine listens on `LPORT`; connections are carried back here to `RHOST:RPORT` (default `127.0.0.1`). Use it to let a remote build or test hit a dev server you run locally.

## The machine you need is asleep (`wol`)

If the service lives on a computer that is powered down or sleeping, a small always-on RemoHop host on the same LAN can wake it:

```bash
remohop <ID> wol aa:bb:cc:dd:ee:ff      # <ID> = the always-on box; it broadcasts the magic packet on ITS network
remohop wake --save nas aa:bb:cc:dd:ee:ff && remohop <ID> wol nas   # names are saved on this machine
```
The packet is fire-and-forget: wait up to a minute, then check (`remohop <OTHER-ID> ping`, or forward and curl). The sleeping computer needs Wake-on-LAN enabled and usually a wired connection. `remohop wake MAC` (no ID) sends from this machine instead. (MCP: `remohop_wake`.)

## Notes

- Without `-b` the command runs in the foreground until interrupted - an agent shell would hang. Use `-b` (and `--json`), then `remohop ps` / `remohop stop`.
- `remohop <ID> ping` shows the session's path. `relayed` (through the rendezvous server, when no direct path exists) is slower: expect lower throughput through the forward.
- A forward ends if the session drops; re-run the command. `remohop ps` shows what is still alive.
- Forwards carry raw TCP, so HTTPS and protocol clients work unchanged; set `Host` headers or `--resolve` yourself if the app routes by hostname.
- Default loopback bind means only this machine can use the forward. Do not bind `*` to publish a remote service to the network unless explicitly asked.
- Stop forwards you started when the task is finished.
