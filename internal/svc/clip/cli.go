package clip

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/readori/RemoHop/internal/bg"
	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/peer"
)

// localBoard picks the local clipboard; tests replace it.
var localBoard = Detect

func init() {
	cli.Register(&cli.Command{
		Name:    "clip",
		Summary: "sync or transfer the clipboard (text and images), with history",
		Usage:   usage,
		// `remohop clip history ...` works on this machine's history without a
		// target; everything else addresses a remote clipboard.
		Run:       runLocal,
		RemoteRun: run,
	})
}

const usage = `Usage: remohop <ID> clip [sync] [-b] [--push-only | --pull-only] [--no-images]
       remohop <ID> clip get [-o FILE] [--json]
       remohop <ID> clip set [--image FILE] [--json] [TEXT...]
       remohop <ID> clip push [--json]
       remohop <ID> clip pull [--json]
       remohop <ID> clip status [--json]
       remohop [<ID>] clip history [list [N...]] [-n N] [--since DUR] [--all]
                               [--json | --md [--max-bytes B]] [--remote]
       remohop [<ID>] clip history show N [-o FILE] [--remote]
       remohop [<ID>] clip history copy N
       remohop [<ID>] clip history rm N...
       remohop [<ID>] clip history clear [--all]
       remohop [<ID>] clip history on | off [--host]
       remohop <ID> clip history send N | take N | restore N

With no subcommand (or "sync"), keep the local and remote clipboards in sync
in both directions (text and images) until interrupted with Ctrl-C, logging
each transfer on stderr. Only changes made after the sync starts are copied.
If both sides change at once, the later change wins on both. The same content
is never bounced back. To print the remote clipboard use "clip get".

  -b, --background   run the sync in the background (see remohop ps / remohop stop)
  --push-only        send local changes, ignore remote ones
  --pull-only        apply remote changes, send nothing
  --no-images        text only

Subcommands:
  sync      the same as no subcommand.
  get       print the remote clipboard. Text goes to stdout; an image needs
            -o FILE (or -o - for stdout). --json adds kind, size, hash and the
            host's clipboard backend (image data as base64 when no -o).
  set       set the remote clipboard from the arguments, or from stdin when
            there are none. --image FILE sets a PNG/JPEG/GIF image (- reads
            stdin); it is stored as PNG.
  push      copy the local clipboard to the remote one, once.
  pull      copy the remote clipboard to the local one, once.
  status    show the local and remote clipboard backends.
  history   the clipboard history, see below.

History. Everything that passes through sync/get/set/push/pull is recorded on
this machine, with its time, direction, peer, the source it came from and the
peers it was sent to. "remohop <ID> clip history" shows the entries of that
target; "remohop clip history" (no target) or --all shows every peer. Entries
are numbered from the newest (#1); a number means the same entry in every
history command with the same scope, and commands print the entry they acted
on (kind, size, short hash) so a wrong number is noticed.

  list [N...]  list entries, newest first (default; the word "list" is
               optional). -n N shows the newest N (default 20, 0 = all),
               --since DUR the last DUR (90m, 2h, 7d). With numbers, exactly
               those entries. --json prints entries as JSON (oldest first,
               field "n" is the number). --md exports Markdown for an LLM
               (oldest first); it contains the clipboard text verbatim (only
               the per-entry metadata is cleaned) and is capped at
               --max-bytes (default 2097152, 0 = no cap): the oldest entries
               are left out and the output says so.
  show N       print the text of entry N, or write its image with -o FILE
               (-o - for stdout).
  copy N       put entry N on the LOCAL clipboard.
  rm N...      delete entries; prints each one removed.
  clear        delete every entry in scope: this target's, or all peers'
               with --all or without a target. Prints what was removed.
               (--clear is the same.)
  on | off     turn recording on or off for this machine (persisted). While
               off nothing is written. --host switches the history this
               machine keeps when it is the controlled host.
  send N       put local history entry N on the REMOTE clipboard.
  take N       fetch entry N of the REMOTE machine's history to the local
               clipboard.
  restore N    make the remote machine put its own history entry N back on
               its clipboard.
  --remote     list/show the remote machine's history instead (the host
               records what passes through its clipboard during sessions;
               take and restore use these numbers). At most 300 entries are
               listed, with a text preview.

Limits: items over 32 MiB are refused. History keeps up to 1000 entries and
64 MiB of text plus images per peer (8 times that in total), text up to
1 MiB per entry (longer text is cut and marked), oldest entries pruned first.
History lives under the remohop config directory (clip/), readable only by you.

A machine without a system clipboard (a typical server) uses a virtual
in-memory clipboard that lives as long as the host process; status and --json
output name the backend in use ("x11/xclip", "wayland/wl-clipboard", "macos",
"windows/powershell", "memory").

Examples:
  remohop 123456789 clip sync                    # two-way sync in the foreground
  remohop 123456789 clip -b --pull-only          # remote -> local, in the background
  remohop 123456789 clip set "hello"
  remohop 123456789 clip set --image a.png
  remohop 123456789 clip get -o shot.png
  remohop clip history -n 5 --md                 # every peer, for an LLM
  remohop 123456789 clip history send 2
  remohop 123456789 clip history --remote
`

// runLocal is `remohop clip ...` without a target: only the history lives here.
func runLocal(ctx context.Context, env *cli.Env, args []string) int {
	if len(args) > 0 && args[0] == "history" {
		return runHistory(ctx, env, args[1:])
	}
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		fmt.Fprint(env.Stderr, usage)
		return cli.ExitOK
	}
	env.Errorf("clip needs a target: remohop <ID> clip ... (only `remohop clip history` works without one)")
	return cli.ExitUsage
}

func run(ctx context.Context, env *cli.Env, args []string) int {
	if len(args) > 0 {
		sub, rest := args[0], args[1:]
		switch sub {
		case "get":
			return runGet(ctx, env, rest)
		case "set":
			return runSet(ctx, env, rest)
		case "push":
			return runPushPull(ctx, env, rest, true)
		case "pull":
			return runPushPull(ctx, env, rest, false)
		case "history":
			return runHistory(ctx, env, rest)
		case "status":
			return runStatus(ctx, env, rest)
		case "sync":
			return runSync(ctx, env, rest)
		}
	}
	return runSync(ctx, env, args)
}

// parse handles flag errors uniformly: -h prints the usage, anything else is
// a usage error.
func parse(env *cli.Env, name string, args []string, define func(*flag.FlagSet)) (*flag.FlagSet, int, bool) {
	fs := flag.NewFlagSet("clip "+name, flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	fs.Usage = func() { fmt.Fprint(env.Stderr, usage) }
	define(fs)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, cli.ExitOK, false
		}
		return nil, cli.ExitUsage, false
	}
	return fs, 0, true
}

func connect(ctx context.Context, env *cli.Env) (*peer.Conn, int) {
	conn, err := env.Conn(ctx)
	if err != nil {
		env.Errorf("%v", err)
		return nil, cli.ExitConnect
	}
	return conn, 0
}

func printJSON(env *cli.Env, v any) {
	enc := json.NewEncoder(env.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

func size(n int) string {
	switch {
	case n < 1<<10:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	}
}

func runSync(ctx context.Context, env *cli.Env, args []string) int {
	var background, pushOnly, pullOnly, noImages bool
	_, code, ok := parse(env, "", args, func(fs *flag.FlagSet) {
		fs.BoolVar(&background, "b", false, "")
		fs.BoolVar(&background, "background", false, "")
		fs.BoolVar(&pushOnly, "push-only", false, "")
		fs.BoolVar(&pullOnly, "pull-only", false, "")
		fs.BoolVar(&noImages, "no-images", false, "")
	})
	if !ok {
		return code
	}
	if pushOnly && pullOnly {
		env.Errorf("clip: --push-only and --pull-only are mutually exclusive")
		return cli.ExitUsage
	}
	if background && !bg.InChild() {
		msg, _, err := bg.Start(30 * time.Second)
		if err != nil {
			env.Errorf("clip: %v", err)
			return cli.ExitError
		}
		fmt.Fprintln(env.Stdout, msg)
		return cli.ExitOK
	}
	conn, code := connect(ctx, env)
	if conn == nil {
		return code
	}
	mode := "two-way"
	switch {
	case pushOnly:
		mode = "push-only"
	case pullOnly:
		mode = "pull-only"
	}
	if noImages {
		mode += ", text only"
	}
	done := func() {}
	defer func() { done() }()
	err := Sync(ctx, conn, SyncOptions{
		Local:    localBoard(ctx),
		PushOnly: pushOnly, PullOnly: pullOnly, NoImages: noImages,
		Peer: env.Target,
		OnReady: func(i SyncInfo) {
			msg := fmt.Sprintf("clipboard sync with %s (%s; local: %s, remote: %s)", env.Target, mode, i.LocalBackend, i.RemoteBackend)
			done = bg.Ready(bg.Info{Kind: "clip", Target: env.Target, Detail: mode + ", local " + i.LocalBackend}, msg)
			fmt.Fprintf(env.Stderr, "clip: %s\n", msg)
			if i.LocalBackend == "memory" {
				fmt.Fprintln(env.Stderr, "clip: no system clipboard found here; using a virtual in-memory one that only this process sees")
			}
		},
		OnTransfer: func(t Transfer) {
			verb := "pushed"
			if t.Dir == DirPull {
				verb = "pulled"
			}
			if t.Err != nil {
				fmt.Fprintf(env.Stderr, "clip: refused %s %s: %v\n", t.Dir, t.Kind, t.Err)
				return
			}
			fmt.Fprintf(env.Stderr, "clip: %s %s (%s)\n", verb, t.Kind, size(t.Size))
		},
	})
	if err != nil && ctx.Err() == nil {
		env.Errorf("clip: %v", err)
		return cli.ExitError
	}
	return cli.ExitOK
}

type itemJSON struct {
	Kind    Kind   `json:"kind"`
	MIME    string `json:"mime"`
	Size    int    `json:"size"`
	Hash    string `json:"hash"`
	Backend string `json:"backend,omitempty"`
	Text    string `json:"text,omitempty"`
	File    string `json:"file,omitempty"`
	Data    string `json:"data_base64,omitempty"`
}

func describe(c Content, backend string) itemJSON {
	return itemJSON{Kind: c.Kind, MIME: c.MIME, Size: len(c.Data), Hash: c.Hash(), Backend: backend}
}

func runGet(ctx context.Context, env *cli.Env, args []string) int {
	var out string
	var asJSON bool
	_, code, ok := parse(env, "get", args, func(fs *flag.FlagSet) {
		fs.StringVar(&out, "o", "", "")
		fs.BoolVar(&asJSON, "json", false, "")
	})
	if !ok {
		return code
	}
	conn, code := connect(ctx, env)
	if conn == nil {
		return code
	}
	c, backend, err := get(ctx, conn, CallOptions{Peer: env.Target}, "get")
	if errors.Is(err, ErrEmpty) {
		if asJSON {
			printJSON(env, map[string]any{"empty": true, "backend": backend})
			return cli.ExitOK
		}
		env.Errorf("remote clipboard is empty (backend: %s)", backend)
		return cli.ExitError
	}
	if err != nil {
		env.Errorf("clip get: %v", err)
		return cli.ExitError
	}
	item := describe(c, backend)
	if c.Kind == KindImage && out == "" && !asJSON {
		env.Errorf("remote clipboard holds an image (%s PNG); use -o FILE to save it or -o - to write it to stdout", size(len(c.Data)))
		return cli.ExitUsage
	}
	switch {
	case out == "-":
		if !asJSON {
			env.Stdout.Write(c.Data)
			return cli.ExitOK
		}
		// stdout carries the JSON document; the data goes inline.
		fallthrough
	case out == "":
		if c.Kind == KindText {
			item.Text = c.String()
		} else {
			item.Data = base64.StdEncoding.EncodeToString(c.Data)
		}
	default:
		// 0600: clipboards hold secrets.
		if err := os.WriteFile(out, c.Data, 0o600); err != nil {
			env.Errorf("clip get: %v", err)
			return cli.ExitError
		}
		item.File = out
	}
	switch {
	case asJSON:
		printJSON(env, item)
	case out == "":
		env.Stdout.Write(c.Data)
	default:
		fmt.Fprintf(env.Stdout, "wrote %s (%s, %s)\n", out, c.Kind, size(len(c.Data)))
	}
	return cli.ExitOK
}

func readLimited(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, MaxSize+1))
	if err == nil && len(b) > MaxSize {
		err = ErrTooLarge
	}
	return b, err
}

func runSet(ctx context.Context, env *cli.Env, args []string) int {
	var image string
	var asJSON bool
	fs, code, ok := parse(env, "set", args, func(fs *flag.FlagSet) {
		fs.StringVar(&image, "image", "", "")
		fs.BoolVar(&asJSON, "json", false, "")
	})
	if !ok {
		return code
	}
	var c Content
	switch {
	case image != "":
		if fs.NArg() > 0 {
			env.Errorf("clip set: --image takes no text arguments")
			return cli.ExitUsage
		}
		var data []byte
		var err error
		if image == "-" {
			data, err = readLimited(env.Stdin)
		} else {
			data, err = readFileLimited(image)
		}
		if err == nil {
			c, err = ImageFromBytes(data)
		}
		if err != nil {
			env.Errorf("clip set: %v", err)
			return cli.ExitError
		}
	case fs.NArg() > 0:
		c = Text(strings.Join(fs.Args(), " "))
	default:
		if f, ok := env.Stdin.(*os.File); ok {
			if st, err := f.Stat(); err == nil && st.Mode()&os.ModeCharDevice != 0 {
				fmt.Fprintln(env.Stderr, "clip: reading text from stdin, finish with Ctrl-D")
			}
		}
		data, err := readLimited(env.Stdin)
		if err != nil {
			env.Errorf("clip set: %v", err)
			return cli.ExitError
		}
		c = Text(string(data))
	}
	return pushContent(ctx, env, c, "set", asJSON)
}

func readFileLimited(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readLimited(f)
}

// pushContent sends c to the remote clipboard and reports it.
func pushContent(ctx context.Context, env *cli.Env, c Content, via string, asJSON bool) int {
	if err := c.Validate(); err != nil {
		env.Errorf("clip %s: %v", via, err)
		return cli.ExitError
	}
	conn, code := connect(ctx, env)
	if conn == nil {
		return code
	}
	backend, err := set(ctx, conn, CallOptions{Peer: env.Target}, via, c)
	if err != nil {
		env.Errorf("clip %s: %v", via, err)
		return cli.ExitError
	}
	if asJSON {
		item := describe(c, backend)
		printJSON(env, map[string]any{"ok": true, "item": item})
	} else {
		fmt.Fprintf(env.Stdout, "remote clipboard set: %s, %s (backend: %s)\n", c.Kind, size(len(c.Data)), backend)
	}
	return cli.ExitOK
}

func runPushPull(ctx context.Context, env *cli.Env, args []string, push bool) int {
	name := "pull"
	if push {
		name = "push"
	}
	var asJSON bool
	_, code, ok := parse(env, name, args, func(fs *flag.FlagSet) {
		fs.BoolVar(&asJSON, "json", false, "")
	})
	if !ok {
		return code
	}
	board := localBoard(ctx)
	if push {
		c, err := board.Read(ctx)
		if errors.Is(err, ErrEmpty) {
			env.Errorf("clip push: local clipboard is empty (backend: %s)", board.Name())
			return cli.ExitError
		}
		if err != nil {
			env.Errorf("clip push: %v", err)
			return cli.ExitError
		}
		return pushContent(ctx, env, c, "push", asJSON)
	}
	conn, code := connect(ctx, env)
	if conn == nil {
		return code
	}
	c, backend, err := get(ctx, conn, CallOptions{Peer: env.Target}, "pull")
	if errors.Is(err, ErrEmpty) {
		env.Errorf("clip pull: remote clipboard is empty (backend: %s)", backend)
		return cli.ExitError
	}
	if err != nil {
		env.Errorf("clip pull: %v", err)
		return cli.ExitError
	}
	if err := board.Write(ctx, c); err != nil {
		env.Errorf("clip pull: writing local clipboard (%s): %v", board.Name(), err)
		return cli.ExitError
	}
	if asJSON {
		printJSON(env, map[string]any{"ok": true, "item": describe(c, backend), "local_backend": board.Name()})
	} else {
		fmt.Fprintf(env.Stdout, "local clipboard set: %s, %s (backend: %s)\n", c.Kind, size(len(c.Data)), board.Name())
	}
	return cli.ExitOK
}

func runStatus(ctx context.Context, env *cli.Env, args []string) int {
	var asJSON bool
	_, code, ok := parse(env, "status", args, func(fs *flag.FlagSet) {
		fs.BoolVar(&asJSON, "json", false, "")
	})
	if !ok {
		return code
	}
	conn, code := connect(ctx, env)
	if conn == nil {
		return code
	}
	info, err := RemoteInfo(ctx, conn)
	if err != nil {
		env.Errorf("clip status: %v", err)
		return cli.ExitError
	}
	local := localBoard(ctx).Name()
	if asJSON {
		printJSON(env, map[string]any{"local_backend": local, "remote_backend": info.Backend, "max_size": MaxSize})
	} else {
		fmt.Fprintf(env.Stdout, "local clipboard:  %s\nremote clipboard: %s\nsize limit:       %d MiB\n", local, info.Backend, MaxSize>>20)
	}
	return cli.ExitOK
}

func preview(e Entry) string {
	if e.Kind == KindImage {
		if e.File == "" {
			return "(image)"
		}
		return e.File
	}
	s := strings.Join(strings.Fields(e.Text), " ")
	if r := []rune(s); len(r) > 60 {
		s = string(r[:60]) + "..."
	}
	return strconv.Quote(s)
}

// parseDuration is time.ParseDuration plus a "d" (24 h) suffix.
func parseDuration(s string) (time.Duration, error) {
	if days, ok := strings.CutSuffix(s, "d"); ok {
		v, err := strconv.ParseFloat(days, 64)
		if err != nil || v < 0 {
			return 0, errors.New("expected a duration like 90m, 2h or 7d")
		}
		return time.Duration(v * float64(24*time.Hour)), nil
	}
	d, err := time.ParseDuration(s)
	if err == nil && d < 0 {
		err = errors.New("must not be negative")
	}
	return d, err
}
