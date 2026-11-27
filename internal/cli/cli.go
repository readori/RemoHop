// Package cli is the `remohop` command framework: a registry that feature
// packages add their subcommands to from init(), and the dispatcher that
// turns `remohop [global flags] [<ID>] <command> [args]` into a call.
package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/term"

	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/snapenv"
)

// Exit codes shared by every command.
const (
	ExitOK      = 0
	ExitError   = 1   // the command ran and failed
	ExitUsage   = 2   // bad flags or arguments
	ExitConnect = 255 // could not reach or authenticate to the remote peer
)

// Version is the build version, set with -ldflags "-X .../internal/cli.Version=...".
var Version = "dev"

// Globals are the flags accepted before the target or command.
type Globals struct {
	Verbose  bool
	Signal   string // rendezvous server address override
	Password string // password for the target, overriding saved credentials
}

// Env is what a command runs with.
type Env struct {
	// Target is the remote peer the command addresses: a 9-digit ID, a saved
	// alias, or a direct host:port. Empty for local commands.
	Target  string
	Globals Globals

	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	// DialFunc connects to a target. The dispatcher fills it with Dialer;
	// tests substitute a function returning a peertest connection.
	DialFunc func(ctx context.Context, env *Env) (*peer.Conn, error)

	once    sync.Once
	conn    *peer.Conn
	connErr error
}

// Dialer is the production implementation of Env.DialFunc. internal/dial
// installs it from init().
var Dialer func(ctx context.Context, env *Env) (*peer.Conn, error)

// Conn returns the session with Target, establishing it on first use. Every
// call returns the same session.
func (e *Env) Conn(ctx context.Context) (*peer.Conn, error) {
	e.once.Do(func() {
		switch {
		case e.Target == "":
			e.connErr = fmt.Errorf("no target: usage is `remohop <ID> <command>`")
		case e.DialFunc == nil:
			e.connErr = fmt.Errorf("no dialer configured")
		default:
			e.conn, e.connErr = e.DialFunc(ctx, e)
		}
	})
	return e.conn, e.connErr
}

// Close ends the session opened by Conn, if any.
func (e *Env) Close() {
	if e.conn != nil {
		e.conn.Close()
	}
}

// Errorf prints a "remohop: ..." diagnostic to Stderr. The message is passed
// through Sanitize, because diagnostics routinely quote text that came from
// the remote peer or the rendezvous server.
func (e *Env) Errorf(format string, args ...any) {
	fmt.Fprintf(e.Stderr, "remohop: %s\n", Sanitize(fmt.Sprintf(format, args...)))
}

// Sanitize makes text from an untrusted peer safe to print on a terminal. It
// replaces control characters (keeping newline and tab), the C1 range, and
// the Unicode bidirectional overrides with '?', so the text cannot move the
// cursor, retitle the window, write the clipboard through OSC 52, or
// disguise what is displayed. Commands must apply it to any peer-supplied
// string they print themselves; raw payloads the user asked for (terminal
// output, file contents, clipboard text) are of course left alone.
func Sanitize(s string) string {
	clean := true
	for _, r := range s {
		if unsafeRune(r) {
			clean = false
			break
		}
	}
	if clean && utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s { // invalid bytes decode as RuneError
		if unsafeRune(r) || r == utf8.RuneError {
			b.WriteByte('?')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// OneLine shows text from an untrusted peer on one line, with nothing
// hidden: line breaks, tabs, other control characters, characters that
// do not print (bidirectional overrides, zero-width and format
// characters, line and paragraph separators) and invalid bytes are
// written as Go escapes (\n, \t, \x1b, \u202e). Use it where the reader
// decides on what the text says, such as an approval prompt: Sanitize
// keeps line breaks, which can push the start of a request off the screen.
func OneLine(s string) string {
	clean := utf8.ValidString(s)
	for _, r := range s {
		if r != ' ' && !strconv.IsPrint(r) {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	var b strings.Builder
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		switch {
		case r == utf8.RuneError && size == 1:
			fmt.Fprintf(&b, `\x%02x`, s[0])
		case r == ' ' || strconv.IsPrint(r):
			b.WriteString(s[:size])
		default:
			q := strconv.QuoteRune(r) // '\n', '\u202e'
			b.WriteString(q[1 : len(q)-1])
		}
		s = s[size:]
	}
	return b.String()
}

func unsafeRune(r rune) bool {
	switch {
	case r == '\n' || r == '\t':
		return false
	case r < 0x20 || r == 0x7f:
		return true
	case r >= 0x80 && r <= 0x9f: // C1 controls, including the 8-bit CSI/OSC
		return true
	case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069: // bidi overrides and isolates
		return true
	case r == 0x200e || r == 0x200f || r == 0x061c: // directional marks
		return true
	}
	return false
}

// Command is one `remohop` subcommand.
type Command struct {
	Name    string
	Summary string // one line, shown in `remohop help`
	Usage   string // full usage text, shown by `remohop help <name>`
	// Remote commands are invoked as `remohop <ID> <name> ...` and use env.Conn.
	Remote bool
	Hidden bool
	// RemoteRun, on a local command, is what `remohop <ID> <name> ...` runs: the
	// same verb done by the remote machine (`remohop wake MAC` sends the packet
	// from here, `remohop <ID> wake MAC` asks that host to send it).
	RemoteRun func(ctx context.Context, env *Env, args []string) int
	// Run executes the command and returns the process exit code. args are
	// the arguments after the command name.
	Run func(ctx context.Context, env *Env, args []string) int
}

var (
	regMu    sync.RWMutex
	registry = map[string]*Command{}
)

// Register adds a command. It panics on a duplicate name so collisions show
// up at startup rather than as a silently shadowed command.
func Register(c *Command) {
	regMu.Lock()
	defer regMu.Unlock()
	if _, dup := registry[c.Name]; dup {
		panic("cli: duplicate command " + c.Name)
	}
	registry[c.Name] = c
}

// Lookup finds a command by name.
func Lookup(name string) *Command {
	regMu.RLock()
	defer regMu.RUnlock()
	return registry[name]
}

// Commands lists every registered command sorted by name.
func Commands() []*Command {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]*Command, 0, len(registry))
	for _, c := range registry {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Main parses args (without the program name), runs the selected command and
// returns the exit code.
func Main(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	// From the snap: drop snapd's SNAP_* variables before anything starts a
	// child process (they would leak into remote shells).
	snapenv.Setup()
	env := &Env{Stdin: stdin, Stdout: stdout, Stderr: stderr, DialFunc: Dialer}
	env.Globals.Signal = os.Getenv("REMOHOP_SIGNAL")
	env.Globals.Password = os.Getenv("REMOHOP_PASSWORD")
	if os.Getenv("REMOHOP_DEBUG") != "" {
		env.Globals.Verbose = true
	}

	args, err := parseGlobals(&env.Globals, args)
	if err != nil {
		env.Errorf("%v", err)
		return ExitUsage
	}
	level := slog.LevelWarn
	if env.Globals.Verbose {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level})))

	if len(args) == 0 {
		printHelp(stderr)
		return ExitUsage
	}
	switch args[0] {
	case "help", "-h", "--help":
		return runHelp(env, args[1:])
	case "version", "--version":
		fmt.Fprintf(stdout, "remohop %s\n", Version)
		return ExitOK
	}

	// `remohop <command> ...` for local commands, `remohop <target> <command> ...`
	// for remote ones. A local command name always wins, so a saved alias can
	// never shadow one.
	// `--help` (or -h) right after a command shows that command's help on
	// stdout and succeeds, whichever way the command parses its flags.
	if c := Lookup(args[0]); c != nil && wantsHelp(args[1:]) {
		return printUsage(env.Stdout, c)
	}
	if c := Lookup(args[0]); c != nil && !c.Remote {
		return c.Run(ctx, env, args[1:])
	}
	if c := Lookup(args[0]); c != nil && c.Remote {
		env.Errorf("%q needs a target: remohop <ID> %s ...", c.Name, c.Name)
		return ExitUsage
	}
	if len(args) < 2 {
		return targetOnly(ctx, env, args[0])
	}
	c := Lookup(args[1])
	if c == nil {
		env.Errorf("unknown command %q (see `remohop help`)", args[1])
		return ExitUsage
	}
	if wantsHelp(args[2:]) {
		return printUsage(env.Stdout, c)
	}
	run := c.Run
	if !c.Remote {
		if c.RemoteRun == nil {
			env.Errorf("%q does not take a target: remohop %s ...", c.Name, c.Name)
			return ExitUsage
		}
		run = c.RemoteRun
	}
	env.Target = NormalizeTarget(args[0])
	defer env.Close()
	return run(ctx, env, args[2:])
}

// wantsHelp reports whether the arguments ask for help before anything
// else: -h, -help or --help among the leading flags. It stops at the first
// argument that is not a flag and at "--", so `remohop <ID> shell -c 'ls
// --help'` still runs ls.
func wantsHelp(args []string) bool {
	for _, a := range args {
		switch {
		case a == "-h" || a == "-help" || a == "--help":
			return true
		case a == "--" || !strings.HasPrefix(a, "-"):
			return false
		}
	}
	return false
}

func printUsage(w io.Writer, c *Command) int {
	if c.Usage != "" {
		fmt.Fprintln(w, strings.TrimRight(c.Usage, "\n"))
	} else if c.Remote {
		fmt.Fprintf(w, "usage: remohop <ID> %s ...\n\n%s\n", c.Name, c.Summary)
	} else {
		fmt.Fprintf(w, "usage: remohop %s ...\n\n%s\n", c.Name, c.Summary)
	}
	return ExitOK
}

// KnownTarget reports whether name is a saved host's alias. internal/dial
// sets it (this package cannot import the peer store).
var KnownTarget func(name string) bool

// LooksLikeTarget reports whether s names a host rather than a command: a
// 9-digit ID, HOST:PORT, or a saved alias.
func LooksLikeTarget(s string) bool {
	n := NormalizeTarget(s)
	if IsID(n) {
		return true
	}
	if h, p, err := net.SplitHostPort(n); err == nil && h != "" && p != "" {
		return true
	}
	return KnownTarget != nil && KnownTarget(n)
}

// targetOnly handles `remohop <ID>` with no command. On a terminal it opens
// a shell there, as ssh does; otherwise (a script) it says what can be done
// with a host instead of guessing.
func targetOnly(ctx context.Context, env *Env, arg string) int {
	if !LooksLikeTarget(arg) {
		env.Errorf("unknown command %q (see `remohop help`; a host is named by its 9-digit ID, HOST:PORT or a saved name)", arg)
		return ExitUsage
	}
	shell := Lookup("shell")
	if shell != nil && shell.Remote && isTerminal(env.Stdin) && isTerminal(env.Stdout) {
		env.Errorf("no command given: opening a shell on %s (`remohop help` lists what else you can do with a host)", arg)
		env.Target = NormalizeTarget(arg)
		defer env.Close()
		return shell.Run(ctx, env, nil)
	}
	fmt.Fprintf(env.Stderr, "remohop: what should %s do? Usage: remohop %s <command> [args]\n\nRemote commands:\n", Sanitize(arg), Sanitize(arg))
	for _, c := range Commands() {
		if c.Remote && !c.Hidden {
			fmt.Fprintf(env.Stderr, "  %-12s %s\n", c.Name, c.Summary)
		}
	}
	return ExitUsage
}

func isTerminal(v any) bool {
	f, ok := v.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

func parseGlobals(g *Globals, args []string) ([]string, error) {
	for len(args) > 0 {
		a := args[0]
		name, val, hasVal := strings.Cut(a, "=")
		takeVal := func() (string, error) {
			if hasVal {
				return val, nil
			}
			if len(args) < 2 {
				return "", fmt.Errorf("flag %s needs a value", name)
			}
			v := args[1]
			args = args[1:]
			return v, nil
		}
		var err error
		switch name {
		case "-v", "--verbose":
			g.Verbose = true
		case "--signal":
			g.Signal, err = takeVal()
		case "--password":
			g.Password, err = takeVal()
		default:
			return args, nil
		}
		if err != nil {
			return nil, err
		}
		args = args[1:]
	}
	return args, nil
}

// NormalizeTarget strips the spaces and dashes people put in a 9-digit ID
// ("123 456 789", "123-456-789"). Anything that is not such an ID is
// returned unchanged.
func NormalizeTarget(t string) string {
	digits := strings.Map(func(r rune) rune {
		if r == ' ' || r == '-' {
			return -1
		}
		return r
	}, t)
	if IsID(digits) {
		return digits
	}
	return t
}

// IsID reports whether s is a well-formed 9-digit RemoHop ID.
func IsID(s string) bool {
	if len(s) != 9 || s[0] == '0' {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func runHelp(env *Env, args []string) int {
	if len(args) == 0 {
		printHelp(env.Stdout)
		return ExitOK
	}
	c := Lookup(args[0])
	if c == nil {
		env.Errorf("unknown command %q", args[0])
		return ExitUsage
	}
	return printUsage(env.Stdout, c)
}

func printHelp(w io.Writer) {
	fmt.Fprint(w, `RemoHop - lightweight P2P remote control

Usage:
  remohop [flags] <ID> <command> [args]   act on a remote machine
  remohop [flags] <command> [args]        local commands

Flags:
  -v, --verbose        debug logging on stderr (or REMOHOP_DEBUG=1)
      --signal ADDR    rendezvous server (or REMOHOP_SIGNAL)
      --password PW    password for <ID> (or REMOHOP_PASSWORD)

`)
	section := func(title string, remote bool) {
		fmt.Fprintln(w, title)
		for _, c := range Commands() {
			if c.Remote == remote && !c.Hidden {
				fmt.Fprintf(w, "  %-12s %s\n", c.Name, c.Summary)
			}
		}
		fmt.Fprintln(w)
	}
	section("Remote commands (remohop <ID> ...):", true)
	section("Local commands:", false)
	fmt.Fprintln(w, "Run `remohop help <command>` for details.")
}
