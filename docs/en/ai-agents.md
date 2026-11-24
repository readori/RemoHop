# AI agents

[中文](../zh/ai-agents.md)

## MCP server

`remohop mcp` runs RemoHop as an MCP server on stdio. The agent host starts it. Register it with Claude Code:

```bash
claude mcp add remohop -- remohop mcp
claude mcp add remohop -- remohop mcp --target 276941009
```

For Cursor and other clients that read an `mcpServers` configuration:

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

Add `"--target", "276941009"` to `args` to set a default machine. The server uses the pairings you created with `remohop <ID> pair`, so pair first. It cannot prompt for a password and the tools never accept one. If a machine is not paired, or the host has switched a feature off, the tool error says what a person has to run.

The 29 tools fall into these groups. Every tool takes a `target` (ID, alias or `host:port`) unless `--target` is set.

| Group | Tools |
|---|---|
| Machines | `remohop_info`, `remohop_peers`, `remohop_limits` |
| Shell and jobs | `remohop_exec`, `remohop_spawn`, `remohop_jobs`, `remohop_job_logs`, `remohop_job_kill`, `remohop_job_wait` |
| Files | `remohop_read_file`, `remohop_write_file`, `remohop_list_dir`, `remohop_upload`, `remohop_download`, `remohop_send` |
| Network | `remohop_forward_start`, `remohop_forward_list`, `remohop_forward_stop`, `remohop_wake` |
| Clipboard | `remohop_clipboard_get`, `remohop_clipboard_set`, `remohop_clipboard_history` |
| Screen and GUI | `remohop_screenshot`, `remohop_displays`, `remohop_click`, `remohop_move`, `remohop_scroll`, `remohop_type`, `remohop_key` |

The pointer tools take coordinates in the last screenshot image by default. The tool table is in [skills/README.md](../../skills/README.md).

## Agent Skills

`skills/` contains six Agent Skills that teach an agent to use the CLI: `remohop-shell`, `remohop-port-forward`, `remohop-clipboard`, `remohop-screenshot`, `remohop-gui` and `remohop-files`. For Claude Code (run from the repository root):

```bash
mkdir -p ~/.claude/skills
for s in skills/remohop-*; do ln -sfn "$PWD/$s" ~/.claude/skills/$(basename "$s"); done
```

See [skills/README.md](../../skills/README.md) for other agents and the setup steps.

## Agent security

An agent with a pairing acts on the remote machine with the privileges of the user running the host. To hand a machine to an agent with less than that, issue a grant on the host: it limits what the agent may use (commands only or an allow-list of commands, files below given directories, screenshots, ...), expires, can ask you to approve each command and file change, records the session, and is written to the audit log under its name.

```bash
remohop host grant claude-session --for 2h --allow shell:run-only,files:read:~/project,screenshot   # on the host
remohop peers redeem 276941009 remohopg1-...                                                        # on the agent's machine
remohop host panic                                                                                  # on the host: end every agent session now
```

The host enforces the grant; `remohop_limits` tells the agent what it allows, and refusals name the limit (`files:write is not allowed by grant claude-session`). `remohop mcp --read-only` also hides every tool that changes something. [agents.md](../agents.md) covers grants, approvals, the audit log and recordings, the kill switch, and exact setup for Claude Code, Claude Desktop, Cursor and VS Code. Without grants: pair only the machines you want the agent to reach, switch off the features it does not need (`remohop config feature input off`), and revoke pairings you no longer need with `remohop host revoke`. See [security.md](../security.md#what-an-authenticated-client-can-do).
