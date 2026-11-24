# AI 代理

[English](../en/ai-agents.md)

## MCP 服务器

`remohop mcp` 以 stdio 方式运行 MCP 服务器，由代理宿主程序启动。以 Claude Code 为例：

```bash
claude mcp add remohop -- remohop mcp
claude mcp add remohop -- remohop mcp --target 276941009
```

Cursor 等读取 `mcpServers` 配置的客户端：

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

在 `args` 中加入 `"--target", "276941009"` 可以设置默认机器。服务器使用你用 `remohop <ID> pair` 保存的配对，所以请先配对。它无法提示输入密码，工具也不接受密码参数。如果某台机器尚未配对，或者被控机关闭了某项功能，工具返回的错误会说明需要由人执行哪条命令。

29 个工具分为以下几组。除非设置了 `--target`，每个工具都需要 `target` 参数（ID、别名或 `host:port`）。

| 类别 | 工具 |
|---|---|
| 机器 | `remohop_info`、`remohop_peers`、`remohop_limits` |
| shell 与后台任务 | `remohop_exec`、`remohop_spawn`、`remohop_jobs`、`remohop_job_logs`、`remohop_job_kill`、`remohop_job_wait` |
| 文件 | `remohop_read_file`、`remohop_write_file`、`remohop_list_dir`、`remohop_upload`、`remohop_download`、`remohop_send` |
| 网络 | `remohop_forward_start`、`remohop_forward_list`、`remohop_forward_stop`、`remohop_wake` |
| 剪贴板 | `remohop_clipboard_get`、`remohop_clipboard_set`、`remohop_clipboard_history` |
| 屏幕与图形界面 | `remohop_screenshot`、`remohop_displays`、`remohop_click`、`remohop_move`、`remohop_scroll`、`remohop_type`、`remohop_key` |

指针类工具默认使用上一张截图中的坐标。完整的工具表见 [skills/README.md](../../skills/README.md)。

## Agent Skills

`skills/` 目录包含六个 Agent Skills，教代理使用命令行：`remohop-shell`、`remohop-port-forward`、`remohop-clipboard`、`remohop-screenshot`、`remohop-gui` 和 `remohop-files`。Claude Code 的安装方式（在仓库根目录执行）：

```bash
mkdir -p ~/.claude/skills
for s in skills/remohop-*; do ln -sfn "$PWD/$s" ~/.claude/skills/$(basename "$s"); done
```

其他代理的安装方式见 [skills/README.md](../../skills/README.md)。

## 代理的安全边界

持有配对的代理，会以运行被控端的用户的权限在远程机器上操作。如果只想把机器的一部分权限交给代理，在被控机上签发一个授权（grant）：它限定代理能用什么（只能执行命令或只能执行列入白名单的命令、只能访问指定目录下的文件、截图等），到期自动失效，可以要求你逐条批准命令和文件改动，可以录制会话，并以授权名记入审计日志。

```bash
remohop host grant claude-session --for 2h --allow shell:run-only,files:read:~/project,screenshot   # 在被控机上
remohop peers redeem 276941009 remohopg1-...                                                        # 在代理所在的机器上
remohop host panic                                                                                  # 在被控机上：立即结束所有代理会话
```

授权由被控机强制执行；`remohop_limits` 工具告诉代理它能做什么，被拒绝时错误信息会指明是哪条限制（`files:write is not allowed by grant claude-session`）。`remohop mcp --read-only` 还会隐藏所有会改动东西的工具。授权、批准、审计日志与录像、紧急停止，以及 Claude Code、Claude Desktop、Cursor 和 VS Code 的具体配置，见 [agents.md](../agents.md)（英文）。不使用授权时：只为希望代理访问的机器配对，关掉它用不到的功能（例如 `remohop config feature input off`），不再需要时用 `remohop host revoke` 撤销配对。详见 [security.md](../security.md#what-an-authenticated-client-can-do)。
