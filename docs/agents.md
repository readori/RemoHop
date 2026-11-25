# Handing a machine to an AI agent

RemoHop lets an AI agent (Claude Code, Claude Desktop, Cursor, VS Code, or any other MCP client) operate a remote machine through `remohop mcp`. This page covers how to do that safely: how the machine's owner gives the agent limited, expiring access, how to watch and stop it, and how to register the MCP server with each client.

- [The short version](#the-short-version)
- [Grants](#grants)
- [Approving actions](#approving-actions)
- [Audit log and recordings](#audit-log-and-recordings)
- [Stopping everything](#stopping-everything)
- [On the agent's machine](#on-the-agents-machine)
- [Client setup](#client-setup): [Claude Code](#claude-code), [Claude Desktop](#claude-desktop), [Cursor](#cursor), [VS Code](#vs-code), [other clients](#other-mcp-clients)
- [What the agent sees](#what-the-agent-sees)
- [Limits of the protection](#limits-of-the-protection)

## The short version

On the machine the agent should work on (the host), as the user that runs `remohop host`:

```bash
remohop host grant claude-session --for 2h \
    --allow shell:run-only,files:read:~/project,screenshot
```

It prints a code that starts with `remohopg1-`. On the machine where the agent runs:

```bash
remohop peers redeem 276941009 remohopg1-...
claude mcp add remohop -- remohop mcp --target 276941009
```

The agent can now run commands (no interactive terminal), read files below `~/project` and take screenshots on that machine for two hours. Everything else is refused by the host. `remohop host revoke claude-session` ends it early; `remohop host panic` ends every agent session at once.

## Grants

A grant is a credential with limits. The host stores it and enforces it on every stream and every command or file operation; nothing on the agent's side can widen it.

```
remohop host grant NAME --allow LIST [--for DURATION] [--cmd COMMAND]...
                    [--approve each|first|none] [--sessions N] [--record]
                    [--key "KEY-LINE"]
```

| Flag | Meaning |
|---|---|
| `--allow LIST` | What the grant allows, comma-separated, repeatable. See the table below. |
| `--for DURATION` | How long the grant is valid: `30m`, `2h`, `24h`. Default `1h`. Sessions end when it expires. |
| `--cmd COMMAND` | Allow only commands that start with this program (and optional leading arguments). Repeatable. Implies `shell:run-only`. |
| `--approve MODE` | `each`: the owner approves every command and every file change. `first`: the first one of each session. `none` (default). |
| `--sessions N` | Concurrent sessions allowed. Default 2. |
| `--record` | Record the output of commands and terminals as asciicast v2 files. |
| `--key "KEY-LINE"` | Bind the code to one client identity (the line `remohop key export` prints there). Without it, the first machine to log in with the code is bound to it, and the code is useless to anyone else from then on. |

Allow-list items:

| Item | Allows |
|---|---|
| `shell` | Commands, background jobs and interactive terminals. |
| `shell:run-only` | Commands and background jobs; no interactive terminal and no `-t` terminal for commands. |
| `shell:cmd=PROGRAM` | Only commands whose first words are `PROGRAM`, e.g. `shell:cmd=git`, `"shell:cmd=go test"`. Same as `--cmd`. |
| `files` | Read and write any file the host's user can. |
| `files:read` | Read any file. |
| `files:read:PATH` | Read files below `PATH` only. |
| `files:write:PATH` | Read and write files below `PATH` only. |
| `screenshot` | Screenshots only. |
| `screen` | Screenshots and desktop viewing, without keyboard and mouse. |
| `desktop` | `screen` plus keyboard and mouse. |
| `input` | Keyboard and mouse. |
| `clip` | The clipboard. |
| `info` | System information. |
| `forward`, `vpn`, `serial`, `usb` | Port forwarding (and Wake-on-LAN), the VPN, serial ports, USB devices. Never included unless named. |

Anything not listed is refused, including pairing: a granted session cannot mint itself an ordinary pairing that would outlive the grant. The host's own feature switches (`remohop config feature NAME off`) still apply on top of every grant.

File roots are checked after resolving symbolic links (and, on Windows, junctions), both of the path and of its parent directory, so a link inside a root cannot lead out of it; a link whose target does not exist yet counts as the place a file created through it would land. Paths containing `..` are refused. Directories above a root may be looked at (stat) so a client can find its way to the root, but not listed.

Each root is pinned when the host loads the grant: the file service does its work through a handle on that directory (Go's `os.Root`), which cannot be led out of it by a link or junction, even one swapped in after the check, and it remembers the directory's identity. If the root's path later names a different directory (it was moved, deleted or replaced by a link), everything below it is refused until you reissue the grant. Within a root, a granted session:

- may not remove, rename or replace the root itself (its contents are fine);
- may create symbolic links only if they point inside the same root and their target has no `..` component: a relative target names the link's own directory or something below it, an absolute one a place under the root's path. Where a link with `..` leads depends on where it sits, so moving it (or replacing a directory it climbs out of) could make it lead out; without `..`, a link made under the grant leads inside the root wherever it is moved to within the root. Links that already exist and point outside are not followed;
- may create hard links only between files inside the same root; renames and hard links between two different roots fail with `EXDEV` (tools then copy);
- resumes uploads (`cp`, `sync`) through partial files the host names itself, bound to the destination and to the grant's credential: a reissued grant starts such uploads over, and a partial file is used only if it is a regular file with a single link. Older clients cannot resume under a grant and upload whole files instead.

With a command allow-list, a command must be a single simple command: `;`, `&`, `|`, `<`, `>`, parentheses and braces outside quotes, and `$`, backticks or line breaks anywhere, are refused, and custom environment variables are not accepted. Quotes group words as usual (`git commit -m "fix: x"` is fine).

Examples:

```bash
# Read-only code review of one repository, with screenshots
remohop host grant reviewer --for 4h --allow files:read:~/src/app,screenshot

# A CI-style agent that may only test and inspect git, writing to one directory
remohop host grant ci-bot --for 8h --cmd "go test" --cmd "git status" --cmd "git diff" \
    --allow files:write:~/src/app/out,files:read:~/src/app

# Full shell, but the owner approves every command and file change, and it is recorded
remohop host grant pairing-session --for 1h --allow shell,files --approve each --record
```

Managing grants:

```bash
remohop host grants             # state (active, expired, suspended), expiry, sessions, binding
remohop host grants --json
remohop host revoke NAME        # delete it; its live sessions end at once
remohop host grant NAME ...     # reissue: replaces the grant and its code; old sessions end
```

Sessions on a reissued grant's old code never get the new rules: every check compares the session's credential with the grant's current one, so they are refused (and ended) at their next request, whether or not `remohop host grant` reached the running host.

From the agent's machine, `remohop <ID> limits` shows what its credential allows.

## Approving actions

With `--approve each` (or `first`), a command or file change waits until the owner answers. The request reaches the owner in three ways:

- the terminal running `remohop host` asks `Allow? Type y or n`;
- `remohop host approve` lists waiting requests; `remohop host approve N` or `remohop host deny N` answers one;
- a desktop notification, where one can be shown (`notify-send` on Linux desktops, the notification centre on macOS; not on Windows).

Requests that nobody answers within 30 seconds are refused, and the agent is told so. One approval of a file change covers further changes to the same path for a minute, so a single upload (create, write, set times, rename) is asked about once. With `first`, one approval covers the rest of the session. Reading files needs no approval. In an interactive terminal (grants with full `shell`), opening the terminal is approved, not each keystroke.

## Audit log and recordings

Every action under a grant is written to the host's audit log (`audit.jsonl` in the config directory, readable by its user only) with the grant's name: logins, each command, each file opened or changed, each other service used, refusals with their reason, approval decisions, recordings started and the session's end.

```bash
remohop host audit --grant claude-session        # the last 100 entries of that grant, and its recordings
remohop host audit --grant claude-session 1000 --json
```

With `--record`, the output of every command and interactive terminal under the grant is saved as an [asciicast v2](https://docs.asciinema.org/manual/asciicast/v2/) file in `recordings/NAME/` inside the config directory (files mode 0600). Play one back with `asciinema play FILE`, or read it as text: it is JSON lines. A recording stops at 64 MiB. Background jobs (`remohop_spawn`) are not recorded this way; their output is kept in the host's job logs (`jobs/` in the config directory) as for any job. The audit log is rotated at 1 MB with four old files kept, so a very busy grant can still push older entries out; copy the log away if you need to keep it. Refusals go to a separate file (`audit-refused.jsonl`) and are summarised past 10 a minute per session, so a session cannot evict its own trail by provoking refusals.

## Stopping everything

```bash
remohop host panic          # suspend every grant and end every granted session now
remohop host panic --all    # ... and every other session too
remohop host resume         # lift the suspension of all grants (or: remohop host resume NAME)
```

`panic` suspends first and then ends the sessions, so nothing slips in between. It needs no running GUI and no hotkey; it works from any terminal of the host's user, including over SSH. If the host is not reachable on its control socket, the suspension still applies within five seconds.

## On the agent's machine

Redeem the code once:

```bash
remohop peers redeem 276941009 remohopg1-...   # or HOST:PORT, or --alias office
remohop 276941009 limits                       # check what it allows
```

The code is stored like a pairing (in `peers.json`, mode 0600) and replaces any pairing saved for that host. The first login binds it to this machine's identity key.

If the same computer also has full access to that host (a pairing of yours), give the agent its own configuration directory, so it holds only the grant:

```bash
export REMOHOP_CONFIG_DIR=~/.config/remohop-agent   # %APPDATA%\remohop-agent on Windows
remohop config set signal rendezvous.example.net    # if you use a rendezvous server
remohop peers redeem 276941009 remohopg1-...
```

and pass the same `REMOHOP_CONFIG_DIR` to the MCP server in the client configuration below (`env`).

`remohop mcp --read-only` additionally hides every tool that changes something (commands, writes, input, clipboard changes, forwards). That is a convenience on the agent's side; the grant is what the host enforces.

## Client setup

`remohop` must be on the client's `PATH`, or give its absolute path (`which remohop`, or `where remohop` on Windows). Replace `276941009` with the host's ID, alias or `HOST:PORT`; omit `--target` to let the agent pass a target on every call.

### Claude Code

```bash
claude mcp add remohop -- remohop mcp --target 276941009
# available in every project:
claude mcp add --scope user remohop -- remohop mcp --target 276941009
# with a separate configuration directory and read-only tools:
claude mcp add remohop -e REMOHOP_CONFIG_DIR=$HOME/.config/remohop-agent -- remohop mcp --target 276941009 --read-only
```

Check it with `claude mcp list`, or `/mcp` inside a session.

### Claude Desktop

Edit `claude_desktop_config.json` (Settings, Developer, Edit Config). It is in `~/Library/Application Support/Claude/` on macOS and `%APPDATA%\Claude\` on Windows. Claude Desktop does not read your shell's `PATH`, so use the absolute path:

```json
{
  "mcpServers": {
    "remohop": {
      "command": "/usr/local/bin/remohop",
      "args": ["mcp", "--target", "276941009"],
      "env": { "REMOHOP_CONFIG_DIR": "/Users/me/.config/remohop-agent" }
    }
  }
}
```

On Windows use `"command": "C:\\Program Files\\RemoHop\\remohop.exe"` (or wherever `remohop.exe` is) and a Windows path in `env`. Leave out `env` when the agent uses your normal configuration. Restart Claude Desktop after editing.

### Cursor

`~/.cursor/mcp.json` for all projects, or `.cursor/mcp.json` in one project:

```json
{
  "mcpServers": {
    "remohop": {
      "command": "remohop",
      "args": ["mcp", "--target", "276941009"],
      "env": { "REMOHOP_CONFIG_DIR": "/home/me/.config/remohop-agent" }
    }
  }
}
```

### VS Code

`.vscode/mcp.json` in the workspace (or run "MCP: Add Server" from the command palette for a user-wide server):

```json
{
  "servers": {
    "remohop": {
      "type": "stdio",
      "command": "remohop",
      "args": ["mcp", "--target", "276941009"],
      "env": { "REMOHOP_CONFIG_DIR": "/home/me/.config/remohop-agent" }
    }
  }
}
```

Or from a terminal:

```bash
code --add-mcp '{"name":"remohop","command":"remohop","args":["mcp","--target","276941009"]}'
```

Use the tools in Copilot Chat's agent mode.

### Other MCP clients

Any client that starts stdio servers works: the command is `remohop`, the arguments `mcp` plus optional `--target ID`, `--read-only` and `--idle-timeout DURATION`. Nothing but protocol messages is written to stdout; diagnostics go to stderr (`-v` for debug logs).

## What the agent sees

- `remohop_limits` asks the host what the agent may do: the grant's name, expiry, allowed and denied capabilities, command allow-list, file roots, approval mode and whether output is recorded. The server's instructions tell the agent to call it and to treat refusals as final.
- A refused action fails with a sentence that names the limit and the grant, for example `files:write is not allowed by grant claude-session (/home/me/project/README is outside the writable roots: /home/me/project/out)` or `command "rm -rf build" is not allowed by grant ci-bot (allowed commands: go test; git status)`, followed by advice not to work around it.
- A denied or unanswered approval says so (`the host's owner denied: exec: git push`), and the agent is told not to repeat the action unless the person asks.
- When a grant expires, is revoked or suspended, the session ends and further calls explain that the access has ended.

## Limits of the protection

Grants narrow what an authenticated client may do; they are enforced by the host process, which runs with its user's privileges. Know what they do not do:

- **A command allow-list is a guard rail, not a sandbox.** It decides which program is started. A permitted program that can run others (`git` with aliases, hooks or `-c`, `make`, `find -exec`, `npm run`, any interpreter) carries that power along. Allow such programs only when that is acceptable. Write access combines with this: an agent that may write in a repository can add git hooks or aliases (`.git/hooks`, `.git/config`) that an allowed `git` command then runs, and one that may write to a directory on `PATH` can plant a program under an allowed name. Do not give write roots that contain such files when you rely on the allow-list. The check follows the quoting rules of the shell the host runs commands with: POSIX-like shells (sh, bash, zsh, dash, ksh, fish, ...) and PowerShell, where single and double quotes group; and cmd.exe (used on Windows when PowerShell is missing or `REMOHOP_SHELL` names it), where only double quotes do and `%`, `^` and `!` are refused anywhere. With any other shell (csh, nushell, ...) commands under an allow-list are refused; set `REMOHOP_SHELL` to a shell above on the host.
- **`shell` and `shell:run-only` without an allow-list can do anything the host's user can**, including reading and writing files outside the file roots. File roots only constrain the file tools. Combine roots with `--cmd`, or with no shell, when files matter.
- **Background jobs and terminals are separated by grant.** A granted session sees, waits for, reads the logs of, stops and removes only the background jobs started under its grant (stopping and removing are logged), and sees and attaches only to terminals it started. Jobs and terminals of full-access sessions and of other grants are invisible to it. A job keeps running as the host user after the grant ends, and a later grant with the same name sees it.
- **File roots confine names, not files.** The file service cannot leave a root, but a hard link that the host's user (or other software) already put in a root shares its content with a file elsewhere, and a granted session can change that content. A root that is replaced is refused, but one that was already replaced before the host loaded the grant (or restarted) is taken as it is then. Keep roots to directories whose contents you control.
- **Approvals rely on the person reading them.** Approving `exec: ./deploy.sh` approves whatever that script does.
- **Recordings and the audit log live on the host**, readable by its user, and are as trustworthy as that account. A full-shell grant can, in principle, edit them; prefer `shell:run-only` with `--cmd` when the record matters.
- **The agent's machine holds a credential.** Anyone who can read its `peers.json` and identity key can use the grant until it expires or is revoked. Keep grants short.

See [security.md](security.md) for the rest of RemoHop's security model.
