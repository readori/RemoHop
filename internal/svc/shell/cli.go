package shell

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/readori/RemoHop/internal/cli"
)

// Exit codes of `remohop <ID> shell -c` beyond the remote command's own.
const (
	exitTimeout     = 124 // --timeout expired (as timeout(1))
	exitInterrupted = 130 // the local command was interrupted
)

func init() {
	cli.Register(&cli.Command{
		Name: "shell", Remote: true, Run: runShell,
		Summary: "interactive terminal (persistent sessions), or run a command with -c",
		Usage:   shellUsage,
	})
	cli.Register(&cli.Command{
		Name: "spawn", Remote: true, Run: runSpawn,
		Summary: "start a background job that keeps running on the host",
		Usage:   spawnUsage,
	})
	cli.Register(&cli.Command{
		Name: "jobs", Remote: true, Run: runJobs,
		Summary: "list, wait for and remove background jobs",
		Usage:   jobsUsage,
	})
	cli.Register(&cli.Command{
		Name: "logs", Remote: true, Run: runLogs,
		Summary: "show or follow a background job's output",
		Usage:   logsUsage,
	})
	cli.Register(&cli.Command{
		Name: "kill", Remote: true, Run: runKill,
		Summary: "signal a background job's process group",
		Usage:   killUsage,
	})
}

const shellUsage = `Usage:
  remohop <ID> shell [-s NAME] [--new] [-e CHAR|none]     interactive terminal
  remohop <ID> shell -c COMMAND [-t] [-n] [--cwd DIR] [--env K=V]... [--timeout DUR]
  remohop <ID> shell ls [--json]                          list sessions
  remohop <ID> shell kill NAME...                         end sessions

Interactive sessions are persistent, like tmux: they live on the host and
survive the client. ` + "`remohop <ID> shell`" + ` attaches to the session "main",
creating it if needed (your login shell on a PTY, in your home directory).
When you leave for any reason other than the shell exiting (detach escape,
closed terminal, lost network) the session keeps running, detached, for the
host's keep-alive (30 minutes by default; REMOHOP_SHELL_KEEPALIVE on the host,
e.g. 45m, or 0 to end sessions on disconnect). Attaching again replays the
recent output and resumes where you were. When the shell exits the session
is gone and remohop exits with the shell's exit code. Background jobs still
attached to the terminal at that point keep running (as after a logout) but
lose their terminal.

A session has one client at a time: attaching to a session that is already
attached takes it over and the other client is detached.

Interactive options:
  -s NAME        session name (default "main")
  --new          always start a new session: NAME, or NAME-2, NAME-3, ...
  -e CHAR        escape character, recognised after a newline (default "~"):
                 ~. detaches, ~? lists escapes, ~~ sends a literal ~.
                 Use ^X for a control character, or "none" to disable.
  -t             attach even if stdin is not a terminal (stdin EOF detaches)

Command mode (-c) runs COMMAND through the remote login shell without a PTY:
stdin is streamed to it (use -n for an empty stdin), its stdout and stderr
arrive on remohop's stdout and stderr, and remohop exits with its exit code (128+n
if it died from signal n). Ctrl-C or a lost connection kills the remote
process group.
  -c COMMAND     the command line to run
  -t             run it on a PTY instead (output merged on stdout)
  -n             do not read stdin
  --cwd DIR      working directory (default: home; relative to home)
  --env K=V      set an environment variable (repeatable)
  --timeout DUR  kill the remote process group after DUR (e.g. 30s, 5m)

Exit status in command mode: the remote command's; 124 if --timeout
expired; 130 if interrupted locally; 255 if remohop itself failed (connection,
remote start error).

Windows hosts: the shell is PowerShell (pwsh.exe, else powershell.exe;
cmd.exe only if neither exists; the host can pin one with REMOHOP_SHELL), so
COMMAND is PowerShell syntax there. It runs with -NoProfile -NonInteractive,
output is UTF-8 whatever the console code page, and the exit status is N
for "exit N", else 0 if the last statement succeeded, else the exit code of
the failing native command that ran last, else 1 (also for an uncaught
error). Piped stdin is read with $input (one string per line) or
[Console]::In; to feed it to a program, pipe it: '$input | sort.exe' ('cat'
is Get-Content there and does not read stdin). Errors arrive on stderr as
plain text (NO_COLOR=1 is set for commands without -t). Interactive sessions and -t use a ConPTY pseudo console; killing a
command or session ends its whole process tree (a job object), including
background processes it started.

Examples:
  remohop 123456789 shell
  remohop 123456789 shell -s build --new
  remohop 123456789 shell ls
  remohop 123456789 shell kill build
  remohop 123456789 shell -c 'uname -a; df -h'
  remohop 123456789 shell -c 'tar czf - /etc' > etc.tgz
  remohop 123456789 shell -c 'cat > notes.txt' < notes.txt
  remohop 123456789 shell -c 'make test' --cwd src/app --env CI=1 --timeout 10m
  remohop 123456789 shell -t -c htop
  remohop 123456789 shell -c 'Get-Process | Sort-Object CPU -Desc | Select -First 5'   # Windows host`

const spawnUsage = `Usage: remohop <ID> spawn [--name NAME] [--cwd DIR] [--env K=V]... [--json] COMMAND...

Start COMMAND (the words are joined with spaces and run by the remote login
shell; PowerShell on Windows hosts, see ` + "`remohop help shell`" + `) as a background job on the host and print its job ID. The job runs in
its own session with stdin from /dev/null; stdout and stderr are appended to
a log file on the host. It keeps running after this client disconnects and
after the host daemon restarts. Logs are not rotated; remove finished jobs
with ` + "`remohop <ID> jobs rm`" + `.

Flags must come before COMMAND.
  --name NAME   label to refer to the job by (instead of its ID)
  --cwd DIR     working directory (default: home; relative to home)
  --env K=V     set an environment variable (repeatable)
  --json        print the job as JSON

Examples:
  remohop 123456789 spawn ./long-benchmark.sh --iterations 100
  remohop 123456789 spawn --name build --cwd src/app make -j8
  remohop 123456789 logs -f build`

const jobsUsage = `Usage:
  remohop <ID> jobs [--json]                   list background jobs
  remohop <ID> jobs wait <JOB>                 wait for a job to end
  remohop <ID> jobs rm [--exited] [<JOB>...]   remove finished jobs and their logs

JOB is a job ID or a unique job name. States: running, exited(CODE), and
unknown (the job is gone but the host was not running to record its exit
status). ` + "`jobs wait`" + ` exits with the job's exit code (128+n if it died
from signal n, 1 if its status is unknown). A job killed on a Windows host
reports exit code 1.

Examples:
  remohop 123456789 jobs
  remohop 123456789 jobs --json
  remohop 123456789 jobs wait build && echo built
  remohop 123456789 jobs rm --exited`

const logsUsage = `Usage: remohop <ID> logs [-f] [--tail N] <JOB>

Print a background job's output (stdout and stderr, interleaved as written).
  -f, --follow  keep printing new output until the job ends, then exit with
                the job's exit code (128+n if it died from signal n, 1 if its
                status is unknown)
  --tail N      start with the last N lines

Without -f the exit status is 0.

Examples:
  remohop 123456789 logs build
  remohop 123456789 logs -f --tail 50 build`

const killUsage = `Usage: remohop <ID> kill [-s SIGNAL] <JOB>...

Send SIGNAL (default TERM) to the whole process group of running background
jobs. SIGNAL is a name (TERM, KILL, INT, HUP, ...) or a number. To end an
interactive session use ` + "`remohop <ID> shell kill NAME`" + `.

Examples:
  remohop 123456789 kill build
  remohop 123456789 kill -s KILL 3f9a0c12`

// listFlag collects a repeatable string flag.
type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

func newFlags(env *cli.Env, name, usage string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	fs.Usage = func() { fmt.Fprintln(env.Stderr, usage) }
	return fs
}

// parseArgs parses flags anywhere among the arguments (stdlib flag stops at
// the first positional one) and returns the positionals. "--" ends flags.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		if args[0] == "--" {
			return append(pos, args[1:]...), nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

func usageExit(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return cli.ExitOK
	}
	return cli.ExitUsage
}

func printJSON(w io.Writer, v any) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

func runShell(ctx context.Context, env *cli.Env, args []string) int {
	fs := newFlags(env, "shell", shellUsage)
	command := fs.String("c", "", "command to run")
	tty := fs.Bool("t", false, "force a PTY / attach without a terminal")
	noStdin := fs.Bool("n", false, "do not read stdin")
	cwd := fs.String("cwd", "", "working directory")
	var envs listFlag
	fs.Var(&envs, "env", "KEY=VALUE")
	timeout := fs.Duration("timeout", 0, "kill after this long")
	name := fs.String("s", "", "session name")
	fresh := fs.Bool("new", false, "start a new session")
	escape := fs.String("e", "~", "escape character")
	if err := fs.Parse(args); err != nil {
		return usageExit(err)
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	rest := fs.Args()

	if !set["c"] {
		if len(rest) > 0 {
			switch rest[0] {
			case "ls", "list":
				return shellList(ctx, env, rest[1:])
			case "kill":
				return shellKill(ctx, env, rest[1:])
			}
			env.Errorf("shell: unexpected argument %q (to run a command use -c '...')", rest[0])
			return cli.ExitUsage
		}
		for _, f := range []string{"n", "cwd", "env", "timeout"} {
			if set[f] {
				env.Errorf("shell: -%s only applies with -c", f)
				return cli.ExitUsage
			}
		}
		esc, err := parseEscape(*escape)
		if err != nil {
			env.Errorf("shell: %v", err)
			return cli.ExitUsage
		}
		return shellInteractive(ctx, env, *name, *fresh, esc, *tty)
	}

	for _, f := range []string{"s", "new", "e"} {
		if set[f] {
			env.Errorf("shell: -%s does not apply with -c", f)
			return cli.ExitUsage
		}
	}
	if len(rest) > 0 {
		env.Errorf("shell: unexpected argument %q after -c (quote the whole command)", rest[0])
		return cli.ExitUsage
	}
	if *command == "" {
		env.Errorf("shell: empty command")
		return cli.ExitUsage
	}
	if err := validEnv(envs); err != nil {
		env.Errorf("shell: %v", err)
		return cli.ExitUsage
	}
	if *timeout < 0 {
		env.Errorf("shell: negative --timeout")
		return cli.ExitUsage
	}
	req := ExecRequest{
		Command: *command, Cwd: *cwd, Env: envs, Timeout: *timeout, TTY: *tty,
		Stdout: env.Stdout, Stderr: env.Stderr,
	}
	if !*noStdin {
		req.Stdin = env.Stdin
	}
	return shellExec(ctx, env, req)
}

// parseEscape accepts a single character, ^X for a control character, or
// "none".
func parseEscape(s string) (byte, error) {
	switch {
	case s == "none":
		return 0, nil
	case len(s) == 1 && s[0] >= 0x20 && s[0] < 0x7f:
		return s[0], nil
	case len(s) == 2 && s[0] == '^' && s[1] >= '@' && s[1] <= '_':
		return s[1] - '@', nil
	case len(s) == 2 && s[0] == '^' && s[1] >= 'a' && s[1] <= 'z':
		return s[1] - 'a' + 1, nil
	}
	return 0, fmt.Errorf("bad escape character %q (use one character, ^X or none)", s)
}

func shellExec(ctx context.Context, env *cli.Env, req ExecRequest) int {
	conn, err := env.Conn(ctx)
	if err != nil {
		env.Errorf("%v", err)
		return cli.ExitConnect
	}
	restore := func() {}
	if lt := openTerm(env.Stdin, env.Stdout); req.TTY && lt != nil {
		req.Rows, req.Cols = lt.size()
		req.Term = os.Getenv("TERM")
		wctx, cancel := context.WithCancel(ctx)
		defer cancel()
		req.Resize = lt.watchSize(wctx)
		if r, err := lt.makeRaw(); err == nil {
			restore = r
		}
	}
	defer restore()
	res, err := Exec(ctx, conn, req)
	restore()
	if err != nil {
		if ctx.Err() != nil {
			env.Errorf("interrupted; remote command killed")
			return exitInterrupted
		}
		env.Errorf("%v", err)
		return cli.ExitConnect
	}
	if res.TimedOut {
		env.Errorf("command timed out after %s and was killed", req.Timeout)
		return exitTimeout
	}
	return clampExit(res.ExitCode)
}

func clampExit(code int) int {
	if code < 0 || code > 255 {
		return cli.ExitError
	}
	return code
}

func shellInteractive(ctx context.Context, env *cli.Env, name string, fresh bool, esc byte, force bool) int {
	lt := openTerm(env.Stdin, env.Stdout)
	if lt == nil && !force {
		env.Errorf("shell: stdin is not a terminal; use -c COMMAND to run a command (or -t to attach anyway)")
		return cli.ExitUsage
	}
	ctx, stop := withHangup(ctx)
	defer stop()
	conn, err := env.Conn(ctx)
	if err != nil {
		env.Errorf("%v", err)
		return cli.ExitConnect
	}
	req := AttachRequest{Name: name, New: fresh, Term: os.Getenv("TERM"), Rows: 24, Cols: 80}
	aio := AttachIO{Stdin: env.Stdin, Stdout: env.Stdout, Escape: esc}
	restore := func() {}
	if lt != nil {
		req.Rows, req.Cols = lt.size()
		wctx, cancel := context.WithCancel(ctx)
		defer cancel()
		aio.Resize = lt.watchSize(wctx)
		r, err := lt.makeRaw()
		if err != nil {
			env.Errorf("shell: cannot put the terminal in raw mode: %v", err)
			return cli.ExitError
		}
		restore = r
	}
	// Restore the terminal even if something below panics.
	defer restore()
	res, err := Attach(ctx, conn, req, aio)
	restore()

	resume := fmt.Sprintf("remohop %s shell", env.Target)
	if res != nil && res.Session != defaultSession {
		resume += " -s " + res.Session
	}
	keep := func() string {
		if res.KeepAlive <= 0 {
			return "the host ends detached sessions immediately"
		}
		return fmt.Sprintf("it keeps running for %s; resume with: %s", fmtDuration(res.KeepAlive), resume)
	}
	if err != nil {
		if res != nil {
			env.Errorf("%v", err)
			env.Errorf("session %q is detached; %s", res.Session, keep())
		} else {
			env.Errorf("%v", err)
		}
		return cli.ExitConnect
	}
	switch {
	case res.Exited:
		return clampExit(res.ExitCode)
	case res.Detached == DetachTakeover:
		env.Errorf("detached: session %q was attached by another client", res.Session)
	default:
		env.Errorf("detached from session %q; %s", res.Session, keep())
	}
	return cli.ExitOK
}

func shellList(ctx context.Context, env *cli.Env, args []string) int {
	fs := newFlags(env, "shell ls", shellUsage)
	asJSON := fs.Bool("json", false, "JSON output")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return usageExit(err)
	}
	if len(pos) > 0 {
		env.Errorf("shell ls: unexpected argument %q", pos[0])
		return cli.ExitUsage
	}
	conn, err := env.Conn(ctx)
	if err != nil {
		env.Errorf("%v", err)
		return cli.ExitConnect
	}
	list, err := Sessions(ctx, conn)
	if err != nil {
		env.Errorf("shell ls: %v", err)
		return cli.ExitError
	}
	if *asJSON {
		if list == nil {
			list = []SessionInfo{}
		}
		printJSON(env.Stdout, list)
		return cli.ExitOK
	}
	if len(list) == 0 {
		fmt.Fprintln(env.Stdout, "no sessions")
		return cli.ExitOK
	}
	fmt.Fprintf(env.Stdout, "%-16s %-8s %-10s %-9s %-10s %s\n", "NAME", "PID", "AGE", "ATTACHED", "IDLE", "EXPIRES IN")
	for _, s := range list {
		att, exp := "no", fmtDuration(seconds(s.ExpiresInSeconds))
		if s.Attached {
			att, exp = "yes", "-"
		}
		fmt.Fprintf(env.Stdout, "%-16s %-8d %-10s %-9s %-10s %s\n", s.Name, s.PID,
			fmtDuration(seconds(s.AgeSeconds)), att, fmtDuration(seconds(s.IdleSeconds)), exp)
	}
	return cli.ExitOK
}

func shellKill(ctx context.Context, env *cli.Env, args []string) int {
	if len(args) == 0 {
		env.Errorf("shell kill: need a session name (see `remohop %s shell ls`)", env.Target)
		return cli.ExitUsage
	}
	conn, err := env.Conn(ctx)
	if err != nil {
		env.Errorf("%v", err)
		return cli.ExitConnect
	}
	code := cli.ExitOK
	for _, name := range args {
		if err := KillSession(ctx, conn, name); err != nil {
			env.Errorf("shell kill: %v", err)
			code = cli.ExitError
		}
	}
	return code
}

func seconds(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }

// fmtDuration renders a duration compactly: 45s, 12m, 3h5m, 2d4h.
func fmtDuration(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		if s := int(d.Seconds()) % 60; s != 0 {
			return fmt.Sprintf("%dm%ds", int(d.Minutes()), s)
		}
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		if m := int(d.Minutes()) % 60; m != 0 {
			return fmt.Sprintf("%dh%dm", int(d.Hours()), m)
		}
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
}

func runSpawn(ctx context.Context, env *cli.Env, args []string) int {
	fs := newFlags(env, "spawn", spawnUsage)
	name := fs.String("name", "", "job name")
	cwd := fs.String("cwd", "", "working directory")
	var envs listFlag
	fs.Var(&envs, "env", "KEY=VALUE")
	asJSON := fs.Bool("json", false, "JSON output")
	if err := fs.Parse(args); err != nil {
		return usageExit(err)
	}
	command := strings.Join(fs.Args(), " ")
	if strings.TrimSpace(command) == "" {
		env.Errorf("spawn: need a command")
		return cli.ExitUsage
	}
	if *name != "" {
		if err := validName(*name); err != nil {
			env.Errorf("spawn: %v", err)
			return cli.ExitUsage
		}
	}
	if err := validEnv(envs); err != nil {
		env.Errorf("spawn: %v", err)
		return cli.ExitUsage
	}
	conn, err := env.Conn(ctx)
	if err != nil {
		env.Errorf("%v", err)
		return cli.ExitConnect
	}
	j, err := Spawn(ctx, conn, SpawnRequest{Command: command, Name: *name, Cwd: *cwd, Env: envs})
	if err != nil {
		env.Errorf("spawn: %v", err)
		return cli.ExitError
	}
	if *asJSON {
		printJSON(env.Stdout, j)
		return cli.ExitOK
	}
	fmt.Fprintln(env.Stdout, j.ID)
	env.Errorf("started job %s (pid %d); follow it with: remohop %s logs -f %s", j.ID, j.PID, env.Target, j.ID)
	return cli.ExitOK
}

func runJobs(ctx context.Context, env *cli.Env, args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "rm", "remove":
			return jobsRemove(ctx, env, args[1:])
		case "wait":
			return jobsWait(ctx, env, args[1:])
		}
	}
	fs := newFlags(env, "jobs", jobsUsage)
	asJSON := fs.Bool("json", false, "JSON output")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return usageExit(err)
	}
	if len(pos) > 0 {
		env.Errorf("jobs: unexpected argument %q", pos[0])
		return cli.ExitUsage
	}
	conn, err := env.Conn(ctx)
	if err != nil {
		env.Errorf("%v", err)
		return cli.ExitConnect
	}
	list, err := Jobs(ctx, conn)
	if err != nil {
		env.Errorf("jobs: %v", err)
		return cli.ExitError
	}
	if *asJSON {
		printJSON(env.Stdout, list)
		return cli.ExitOK
	}
	if len(list) == 0 {
		fmt.Fprintln(env.Stdout, "no jobs")
		return cli.ExitOK
	}
	fmt.Fprintf(env.Stdout, "%-8s  %-12s  %-12s  %-8s  %-19s  %s\n", "ID", "NAME", "STATE", "PID", "STARTED", "COMMAND")
	for _, j := range list {
		name := j.Name
		if name == "" {
			name = "-"
		}
		fmt.Fprintf(env.Stdout, "%-8s  %-12s  %-12s  %-8d  %-19s  %s\n", printable(j.ID), printable(name),
			jobState(&j), j.PID, j.Started.Local().Format("2006-01-02 15:04:05"), printable(j.Command))
	}
	return cli.ExitOK
}

func jobState(j *Job) string {
	if j.State == JobExited {
		return fmt.Sprintf("exited(%d)", j.ExitCode)
	}
	return printable(j.State)
}

// jobExit is the CLI exit code reflecting a finished job.
func jobExit(j *Job) int {
	switch {
	case j == nil:
		return cli.ExitError
	case j.State == JobExited:
		return clampExit(j.ExitCode)
	case j.State == JobUnknown:
		return cli.ExitError
	}
	return cli.ExitOK
}

func jobsWait(ctx context.Context, env *cli.Env, args []string) int {
	fs := newFlags(env, "jobs wait", jobsUsage)
	pos, err := parseArgs(fs, args)
	if err != nil {
		return usageExit(err)
	}
	if len(pos) != 1 {
		env.Errorf("jobs wait: need exactly one job")
		return cli.ExitUsage
	}
	conn, err := env.Conn(ctx)
	if err != nil {
		env.Errorf("%v", err)
		return cli.ExitConnect
	}
	j, err := Wait(ctx, conn, pos[0])
	if err != nil {
		env.Errorf("jobs wait: %v", err)
		return cli.ExitError
	}
	return jobExit(j)
}

func jobsRemove(ctx context.Context, env *cli.Env, args []string) int {
	fs := newFlags(env, "jobs rm", jobsUsage)
	exited := fs.Bool("exited", false, "remove every finished job")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return usageExit(err)
	}
	if len(pos) == 0 && !*exited {
		env.Errorf("jobs rm: name jobs to remove, or use --exited")
		return cli.ExitUsage
	}
	conn, err := env.Conn(ctx)
	if err != nil {
		env.Errorf("%v", err)
		return cli.ExitConnect
	}
	ids, err := RemoveJobs(ctx, conn, RemoveRequest{Jobs: pos, Exited: *exited})
	for _, id := range ids {
		fmt.Fprintf(env.Stdout, "removed %s\n", printable(id))
	}
	if err != nil {
		env.Errorf("jobs rm: %v", err)
		return cli.ExitError
	}
	return cli.ExitOK
}

func runLogs(ctx context.Context, env *cli.Env, args []string) int {
	fs := newFlags(env, "logs", logsUsage)
	follow := fs.Bool("f", false, "follow")
	fs.BoolVar(follow, "follow", false, "follow")
	tail := fs.Int("tail", 0, "last N lines")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return usageExit(err)
	}
	if len(pos) != 1 {
		env.Errorf("logs: need exactly one job")
		return cli.ExitUsage
	}
	if *tail < 0 {
		env.Errorf("logs: negative --tail")
		return cli.ExitUsage
	}
	conn, err := env.Conn(ctx)
	if err != nil {
		env.Errorf("%v", err)
		return cli.ExitConnect
	}
	r, err := Logs(ctx, conn, LogsRequest{Job: pos[0], Follow: *follow, Tail: *tail})
	if err != nil {
		env.Errorf("logs: %v", err)
		return cli.ExitError
	}
	defer r.Close()
	if _, err := io.Copy(env.Stdout, r); err != nil {
		if ctx.Err() != nil {
			return exitInterrupted
		}
		env.Errorf("logs: %v", err)
		return cli.ExitError
	}
	if *follow {
		return jobExit(r.Job())
	}
	return cli.ExitOK
}

func runKill(ctx context.Context, env *cli.Env, args []string) int {
	fs := newFlags(env, "kill", killUsage)
	sig := fs.String("s", "TERM", "signal")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return usageExit(err)
	}
	if len(pos) == 0 {
		env.Errorf("kill: need a job (see `remohop %s jobs`)", env.Target)
		return cli.ExitUsage
	}
	conn, err := env.Conn(ctx)
	if err != nil {
		env.Errorf("%v", err)
		return cli.ExitConnect
	}
	code := cli.ExitOK
	for _, ref := range pos {
		j, err := Kill(ctx, conn, ref, *sig)
		if err != nil {
			env.Errorf("kill: %v", err)
			code = cli.ExitError
			continue
		}
		fmt.Fprintf(env.Stdout, "sent %s to job %s (pid %d)\n", printable(strings.TrimPrefix(strings.ToUpper(*sig), "SIG")), j.ID, j.PID)
	}
	return code
}
