# RemoHop agent skills and MCP server

RemoHop lets AI agents (Claude Code, Cursor, Codex and others) work on another computer without a public SSH port or a VPN: with the machine's 9-digit ID and a pairing done once by a person, an agent can run commands, read logs, forward ports, use the clipboard, take screenshots and move files there, over a peer-to-peer, end-to-end encrypted session.

There are two ways to give an agent that ability. Use either or both:

| | What it is | Works in |
|---|---|---|
| **Agent Skills** (`skills/*/SKILL.md`) | Instructions that teach an agent the `remohop` CLI | any agent that can run shell commands and loads skills |
| **MCP server** (`remohop mcp`) | The same capabilities as typed tools | any MCP-capable host |

## 1. One-time human setup

There is no built-in rendezvous server: IDs are handed out by a server you run (or that your team runs). Any machine with a public UDP port will do; it only introduces peers and never sees session traffic.

```bash
# a) once, on a machine with a public UDP port (default :21116, plus the next port)
remohop signal

# b) on the REMOTE machine (the one to be controlled): point it at that server and run the host
remohop config set signal rendezvous.example.net      # or REMOHOP_SIGNAL=..., or `remohop host --signal ...`
remohop host                                          # prints the machine's 9-digit ID and a password; keep it running
#   or, to have it running from boot/login without a terminal:
#   remohop service install && remohop status --show-password

# c) on THIS machine: use the same server, then pair once
remohop config set signal rendezvous.example.net
remohop <ID> pair --name office                       # prompts for the password; alias is optional
#   non-interactive alternative: REMOHOP_PASSWORD=... remohop <ID> pair --name office

# d) from now on: no password, by ID or alias
remohop office info
remohop office ping                                   # shows the path: lan / direct / punched are peer to peer; relayed = via the rendezvous server (slower)
```

On a LAN with no rendezvous server, run the host with `remohop host --no-signal --listen :21118` and use `remohop HOST:21118 pair`.

The password is needed only for pairing; afterwards the agent connects with the saved token and never sees a password. Agents cannot answer prompts: when a command or tool reports a missing pairing, the agent should tell the human to run `remohop <ID> pair`. Revoke on the host with `remohop host paired` / `remohop host revoke NAME`; `remohop peers` lists what this machine has paired, `remohop peers forget` discards a pairing.

### Host-side switches and troubleshooting

- `remohop status` (on the host) shows whether the daemon runs, its ID, switched-off features and live sessions with their path; `remohop service status` / `remohop service logs` cover the installed service. A service has its own identity and password, separate from a hand-started `remohop host`.
- The host's owner decides what clients may do: `remohop config feature NAME on|off` with NAME one of `shell`, `file`, `forward`, `clip`, `screen`, `input`, `info`, `vpn`, `serial`, `usb` (all on by default; takes effect at once). When a feature is off, commands and MCP tools fail with a message naming the exact `remohop config feature NAME on` command - the agent should report that, not retry.
- `remohop config ask on` makes password logins need approval at the host; `remohop config relay on|off` accepts or refuses relayed connections when no direct path exists.
- Screenshots and GUI input need the host to run inside a graphical session (a per-user service or `remohop host` started from the desktop; a system service or a Windows service in session 0 cannot see the screen, unless the Windows service was installed with `--unattended`).

Install `remohop` on `PATH` on the machine where the agent runs. Other `remohop` features (for humans, not covered by a skill): `remohop <ID> desktop` shows the remote screen in a browser.

## 2. Install the skills

Each skill is a directory with a `SKILL.md`. Copy or symlink the ones you want where your agent looks for skills:

```bash
# Claude Code: personal skills (all projects)
mkdir -p ~/.claude/skills
for s in skills/remohop-*; do ln -sfn "$PWD/$s" ~/.claude/skills/$(basename "$s"); done

# or per project
mkdir -p .claude/skills && cp -r skills/remohop-* .claude/skills/
```

Other agents that support the Agent Skills format use their own directory (for example `.agents/skills/` or the tool's documented skills folder); copy the same directories there. Agents without skill support can be pointed at the `SKILL.md` files as ordinary instructions.

| Skill | Use it for |
|---|---|
| `remohop-shell` | probing a machine, running commands, log analysis, background jobs (`spawn`, `jobs`, `logs`, `kill`) |
| `remohop-port-forward` | reaching a remote service from localhost (`forward -b`), reverse forwards, `remohop ps` / `remohop stop`, waking a sleeping machine (`wol`) |
| `remohop-clipboard` | reading and setting a remote clipboard, push/pull, history as Markdown |
| `remohop-screenshot` | capturing the remote screen to an image the agent can look at |
| `remohop-gui` | driving a remote desktop: `click`, `move`, `scroll`, `type`, `press` in a screenshot-act-verify loop |
| `remohop-files` | `ls`, `cp`, `send` (to the remote Downloads folder), `sync` and `mount` |

The skills only need the `remohop` CLI. Where an MCP tool does the same job, the skill names it.

## 3. Register the MCP server

`remohop mcp` speaks MCP over stdio (newline-delimited JSON-RPC); the agent host starts it. Nothing but protocol messages is written to stdout.

```bash
claude mcp add remohop -- remohop mcp                       # Claude Code
claude mcp add remohop -- remohop mcp --target 123456789    # with a default machine
```

Cursor and other clients with an `mcpServers` config (for example `~/.cursor/mcp.json` or the project's `.cursor/mcp.json`):

```json
{
  "mcpServers": {
    "remohop": {
      "command": "remohop",
      "args": ["mcp"]
    }
  }
}
```

Add `"--target", "123456789"` to `args` to default the machine; add `"env": {"REMOHOP_PASSWORD": "..."}` only if you cannot pair (prefer pairing). Flags: `--target ID`, `--idle-timeout DURATION` (default 10m; sessions with an active port forward are kept), `--read-only` (hide every tool that changes something). Use `remohop -v mcp` for debug logs on stderr.

### Tools

Every tool takes `target` (9-digit ID, alias or `host:port`; optional with `--target`). A session per machine is opened on first use, reused, re-dialled if it dies and closed when idle or at exit.

| Tool | What it does |
|---|---|
| `remohop_exec` | run a command, wait, return stdout/stderr/exit code (default 60 s timeout, max 900 s; output capped) |
| `remohop_spawn` | start a background job, return its id |
| `remohop_jobs` | list background jobs |
| `remohop_job_logs` | tail a job's log, optionally following briefly with `wait_seconds` |
| `remohop_job_kill` | signal a job's process group |
| `remohop_job_wait` | wait (bounded) for a job to finish |
| `remohop_forward_start` | forward a remote port to a local address (or reverse); lives in the MCP server process |
| `remohop_forward_list` | active forwards with connection and byte counters |
| `remohop_forward_stop` | close a forward |
| `remohop_clipboard_get` | read the remote clipboard (text, or an image block) |
| `remohop_clipboard_set` | set the remote clipboard text |
| `remohop_clipboard_history` | clipboard history as Markdown |
| `remohop_screenshot` | capture the remote screen (or a `region`) as an image block, with image and real display size; optional `save_path` (refuses to replace a file unless `overwrite`) |
| `remohop_displays` | list remote monitors |
| `remohop_info` | describe the remote machine (OS decides the shell language) and the connection path |
| `remohop_read_file` | read a text file (offset/limit, binary detection) |
| `remohop_write_file` | write or append a text file atomically |
| `remohop_list_dir` | list a directory |
| `remohop_upload` / `remohop_download` | copy one file local <-> remote (up to 512 MiB) |
| `remohop_send` | deliver files to the remote user's Downloads folder (never overwrites) |
| `remohop_click` / `remohop_move` / `remohop_scroll` | pointer actions; coordinates are pixels of the last screenshot image by default |
| `remohop_type` / `remohop_key` | type text, press key combos |
| `remohop_wake` | have the remote machine send a Wake-on-LAN packet on its LAN |
| `remohop_peers` | saved machines and aliases, open sessions and their path (never credentials) |
| `remohop_limits` | what this computer may do on the machine: the grant's allowed and denied capabilities, commands, file roots, approval mode, expiry |

## Security model

Local files: `save_path`, `remohop_upload` and `remohop_download` take absolute paths, refuse to replace existing files unless `overwrite` is true (and never follow a symlink at the destination), and refuse anything inside RemoHop's own config directory (identity key, pairing tokens).

An agent with these skills or tools acts on the remote machine with the full privileges of the user running `remohop host`, and on this machine with the privileges of the agent. Pair only machines you intend to hand to the agent, review what the agent proposes to run on important systems, and revoke the pairing (`remohop host revoke`) when done. To give it less than that, issue a scoped, expiring grant on the host (`remohop host grant`, see [docs/agents.md](../docs/agents.md)); refusals under a grant read "... is not allowed by grant NAME" and are final. Passwords never flow through the model: pairing is a human step and the MCP tools accept no password argument.
