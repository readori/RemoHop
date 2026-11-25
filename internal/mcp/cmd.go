package mcp

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/dial"
	"github.com/readori/RemoHop/internal/peer"
)

const usage = `Usage: remohop mcp [--target ID] [--idle-timeout DURATION] [--read-only]
                   [--download-dir DIR]...

Run RemoHop as a Model Context Protocol server on stdin/stdout, so an AI agent
(Claude Code, Cursor, Codex, ...) can operate remote machines by ID: run
commands and background jobs, read and write files, forward ports, use the
clipboard, take screenshots and inspect the system.

The server is started by the agent host, not by hand. Nothing but protocol
messages is written to stdout; diagnostics go to stderr (-v for debug logs).

Flags:
  --target ID           default machine for tool calls that omit "target"
  --idle-timeout DUR    close a machine's session after this much inactivity
                        (default 10m; 0 keeps sessions until exit). Sessions
                        with an active port forward are never closed.
  --read-only           offer only tools that change nothing (screenshots,
                        reading files, system information, job logs, limits)
  --download-dir DIR    a directory on this computer the tools may write to
                        (remohop_download, saved screenshots and clipboard
                        contents); repeat for more. A relative path the
                        agent gives lands in the first. Default:
                        ~/Downloads/remohop. Anything a tool writes came from
                        a remote machine, so keep this away from ~/.ssh,
                        shell startup files and the like

What the agent may do on a machine is decided there: a host owner can hand
the machine over with a grant (` + "`remohop host grant`" + `) that limits tools, files and
commands, expires, and can ask for approval of each action. Redeem its code
here with ` + "`remohop peers redeem ID CODE`" + `; the remohop_limits tool tells the agent
what the grant allows. Setup for Claude Code, Claude Desktop, Cursor and VS
Code: docs/agents.md.

Connections use the pairings saved by ` + "`remohop <ID> pair`" + ` (or REMOHOP_PASSWORD in
the server's environment). The server cannot prompt: when a machine needs
pairing, the tool error tells the model to ask the person to run
` + "`remohop <ID> pair`" + ` in a terminal. Passwords are never accepted as tool arguments.

Register it with an agent:
  claude mcp add remohop -- remohop mcp
  claude mcp add remohop -- remohop mcp --target 123456789

  Cursor and other clients (mcp.json):
  {"mcpServers": {"remohop": {"command": "remohop", "args": ["mcp"]}}}

Tools: remohop_info, remohop_peers; shell: remohop_exec, remohop_spawn,
remohop_jobs, remohop_job_logs, remohop_job_kill, remohop_job_wait; files:
remohop_read_file, remohop_write_file, remohop_list_dir, remohop_upload,
remohop_download, remohop_send; network: remohop_forward_start,
remohop_forward_list, remohop_forward_stop, remohop_wake; clipboard:
remohop_clipboard_get, remohop_clipboard_set, remohop_clipboard_history; screen
and GUI: remohop_screenshot, remohop_displays, remohop_click, remohop_move,
remohop_scroll, remohop_type, remohop_key; access: remohop_limits.
`

// newDial builds the production dialer. It never prompts: stdin belongs to
// the protocol, so a host that needs a password the server was not given
// fails with an explanation (dial.ErrNoCredential) instead of blocking.
// It is a variable so tests can run the command without a network.
var newDial = func(g cli.Globals) DialFunc {
	return func(ctx context.Context, target string) (*peer.Conn, error) {
		return dial.Dial(ctx, target, dial.Options{Signal: g.Signal, Password: g.Password})
	}
}

func init() {
	cli.Register(&cli.Command{
		Name:    "mcp",
		Summary: "run the MCP server (stdio) that gives AI agents access to remote machines",
		Usage:   usage,
		Run:     run,
	})
}

func run(ctx context.Context, env *cli.Env, args []string) int {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	fs.Usage = func() { fmt.Fprint(env.Stderr, usage) }
	target := fs.String("target", "", "")
	idle := fs.Duration("idle-timeout", DefaultIdleTimeout, "")
	readOnly := fs.Bool("read-only", false, "")
	var writeDirs []string
	fs.Func("download-dir", "", func(v string) error {
		p, err := localPath(v)
		if err != nil {
			return err
		}
		writeDirs = append(writeDirs, p)
		return nil
	})
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return cli.ExitOK
		}
		return cli.ExitUsage
	}
	if fs.NArg() != 0 {
		env.Errorf("mcp: unexpected argument %q", fs.Arg(0))
		return cli.ExitUsage
	}
	if *idle < 0 {
		env.Errorf("mcp: --idle-timeout must not be negative")
		return cli.ExitUsage
	}
	idleTimeout := *idle
	if idleTimeout == 0 {
		idleTimeout = -1 // "0 keeps sessions until exit"
	}

	level := slog.LevelWarn
	if env.Globals.Verbose {
		level = slog.LevelDebug
	}
	srv := New(Options{
		Dial:          newDial(env.Globals),
		DefaultTarget: strings.TrimSpace(*target),
		IdleTimeout:   idleTimeout,
		ReadOnly:      *readOnly,
		WriteDirs:     writeDirs,
		Version:       cli.Version,
		Logger:        slog.New(slog.NewTextHandler(env.Stderr, &slog.HandlerOptions{Level: level})),
	})
	in := env.Stdin
	if in == nil {
		in = strings.NewReader("")
	}
	t := &sdk.IOTransport{Reader: io.NopCloser(in), Writer: nopWriteCloser{env.Stdout}}
	if err := srv.Run(ctx, t); err != nil && ctx.Err() == nil {
		env.Errorf("mcp: %v", err)
		return cli.ExitError
	}
	return cli.ExitOK
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }
