package clip

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/readori/RemoHop/internal/cli"
)

// historyValueFlags are the history flags that take a value; splitArgs needs
// them to tell a flag's value from a positional argument.
var historyValueFlags = map[string]bool{"n": true, "since": true, "o": true, "max-bytes": true}

// splitArgs separates flags from positional arguments so flags may come
// before or after the entry numbers (`history list 3 1 --md`), which the
// flag package alone does not allow.
func splitArgs(args []string, valueFlags map[string]bool) (flags, pos []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if len(a) < 2 || a[0] != '-' {
			pos = append(pos, a)
			continue
		}
		flags = append(flags, a)
		name := strings.TrimLeft(a, "-")
		if !strings.Contains(name, "=") && valueFlags[name] && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return flags, pos
}

type historyOpts struct {
	n        int
	md, json bool
	since    string
	all      bool
	clear    bool
	remote   bool
	host     bool
	maxBytes int
	out      string
}

var historySubs = map[string]bool{"list": true, "show": true, "copy": true, "rm": true, "clear": true,
	"on": true, "off": true, "send": true, "take": true, "restore": true}

func runHistory(ctx context.Context, env *cli.Env, args []string) int {
	flags, pos := splitArgs(args, historyValueFlags)
	var o historyOpts
	_, code, ok := parse(env, "history", flags, func(fs *flag.FlagSet) {
		fs.IntVar(&o.n, "n", 20, "")
		fs.BoolVar(&o.md, "md", false, "")
		fs.BoolVar(&o.json, "json", false, "")
		fs.StringVar(&o.since, "since", "", "")
		fs.BoolVar(&o.all, "all", false, "")
		fs.BoolVar(&o.clear, "clear", false, "")
		fs.BoolVar(&o.remote, "remote", false, "")
		fs.BoolVar(&o.host, "host", false, "")
		fs.IntVar(&o.maxBytes, "max-bytes", DefaultMarkdownBytes, "")
		fs.StringVar(&o.out, "o", "", "")
	})
	if !ok {
		return code
	}
	sub := "list"
	if len(pos) > 0 && historySubs[pos[0]] {
		sub, pos = pos[0], pos[1:]
	}
	if o.clear {
		if sub != "list" && sub != "clear" {
			env.Errorf("clip history: --clear cannot be combined with %s", sub)
			return cli.ExitUsage
		}
		sub = "clear"
	}
	usageErr := func(format string, a ...any) int {
		env.Errorf("clip history "+sub+": "+format, a...)
		return cli.ExitUsage
	}
	if o.md && o.json {
		env.Errorf("clip history: --md and --json are mutually exclusive")
		return cli.ExitUsage
	}
	if o.n < 0 || o.maxBytes < 0 {
		env.Errorf("clip history: -n and --max-bytes must be >= 0")
		return cli.ExitUsage
	}
	needsTarget := sub == "send" || sub == "take" || sub == "restore" || o.remote
	if needsTarget && env.Target == "" {
		return usageErr("needs a target: remohop <ID> clip history %s", strings.TrimSpace(sub+" "+strings.Join(pos, " ")))
	}
	if o.remote && sub != "list" && sub != "show" {
		return usageErr("--remote only applies to list and show")
	}
	var nums []int
	if sub == "on" || sub == "off" || sub == "clear" {
		if len(pos) > 0 {
			return usageErr("unexpected argument %q", pos[0])
		}
	} else {
		for _, p := range pos {
			n, err := strconv.Atoi(p)
			if err != nil || n < 1 {
				return usageErr("%q is not an entry number (entries are numbered from 1, the newest)", p)
			}
			nums = append(nums, n)
		}
		switch {
		case sub == "rm" && len(nums) == 0:
			return usageErr("needs at least one entry number")
		case sub != "list" && sub != "rm" && len(nums) != 1:
			return usageErr("needs exactly one entry number")
		}
	}

	hc := &historyCmd{env: env, o: o, sub: sub}
	if !o.all {
		hc.peer = env.Target
	}
	if o.since != "" {
		if sub != "list" || len(nums) > 0 {
			return usageErr("--since only applies to listing")
		}
		d, err := parseDuration(o.since)
		if err != nil {
			env.Errorf("clip history: bad --since %q: %v", o.since, err)
			return cli.ExitUsage
		}
		hc.since = time.Now().Add(-d)
	}
	if sub == "on" || sub == "off" {
		return hc.toggle(sub == "on")
	}
	var err error
	if hc.h, err = OpenHistory(""); err != nil {
		env.Errorf("clip history: %v", err)
		return cli.ExitError
	}
	switch sub {
	case "list":
		return hc.list(ctx, nums)
	case "show":
		return hc.show(ctx, nums[0])
	case "copy":
		return hc.copy(ctx, nums[0])
	case "rm":
		return hc.rm(nums)
	case "clear":
		return hc.clearScope()
	case "send":
		return hc.send(ctx, nums[0])
	case "take":
		return hc.take(ctx, nums[0])
	default:
		return hc.restore(ctx, nums[0])
	}
}

type historyCmd struct {
	env   *cli.Env
	o     historyOpts
	sub   string
	h     *History
	peer  string // scope: one peer, or "" for all
	since time.Time
}

func (hc *historyCmd) fail(err error) int {
	hc.env.Errorf("clip history %s: %v", hc.sub, err)
	return cli.ExitError
}

func (hc *historyCmd) scopeName() string {
	if hc.peer == "" {
		return "all peers"
	}
	return hc.peer
}

// describeEntry is the line that tells the user which entry a command acted
// on.
func describeEntry(e Entry) string {
	dir := "pull from"
	if e.Dir == DirPush {
		dir = "push to"
	}
	return fmt.Sprintf("#%d %s, %s, sha256 %s, %s %s, %s", e.N, e.Kind, size(e.Size), shortHash(e.Hash), dir, meta(e.Peer),
		e.Time.Local().Format("2006-01-02 15:04:05"))
}

// pick resolves entry numbers against entries (as listed for this scope).
func pick(entries []Entry, nums []int, scope string) ([]Entry, error) {
	byN := map[int]Entry{}
	for _, e := range entries {
		byN[e.N] = e
	}
	var out []Entry
	seen := map[int]bool{}
	for _, n := range nums {
		e, ok := byN[n]
		if !ok {
			return nil, fmt.Errorf("no entry #%d (%s has %d %s)", n, scope, len(entries), plural(len(entries), "entry", "entries"))
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, e)
		}
	}
	return out, nil
}

func (hc *historyCmd) localEntry(n int) (Entry, error) {
	all, err := hc.h.List(Filter{Peer: hc.peer})
	if err != nil {
		return Entry{}, err
	}
	es, err := pick(all, []int{n}, "the history of "+hc.scopeName())
	if err != nil {
		return Entry{}, err
	}
	return es[0], nil
}

func (hc *historyCmd) remoteEntry(ctx context.Context, n int) (Entry, int) {
	conn, code := connect(ctx, hc.env)
	if conn == nil {
		return Entry{}, code
	}
	all, err := RemoteHistory(ctx, conn, 0)
	if err != nil {
		return Entry{}, hc.fail(err)
	}
	es, err := pick(all, []int{n}, "the remote history")
	if err != nil {
		return Entry{}, hc.fail(err)
	}
	return es[0], 0
}

// remoteContent fetches a remote entry and checks it is the one listed.
func (hc *historyCmd) remoteContent(ctx context.Context, e Entry) (Content, error) {
	conn, err := hc.env.Conn(ctx)
	if err != nil {
		return Content{}, err
	}
	c, err := RemoteHistoryGet(ctx, conn, e.ID)
	if err != nil {
		return Content{}, err
	}
	if c.Hash() != e.Hash {
		return Content{}, errors.New("the remote history changed; list it again")
	}
	return c, nil
}

func (hc *historyCmd) toggle(on bool) int {
	set, which := SetRecording, "this machine's clipboard history"
	if hc.o.host {
		set, which = SetHostRecording, "the host-side clipboard history of this machine"
	}
	if err := set(on); err != nil {
		return hc.fail(err)
	}
	state := "off: nothing is written until it is turned on again"
	if on {
		state = "on"
	}
	fmt.Fprintf(hc.env.Stdout, "recording of %s is %s\n", which, state)
	return cli.ExitOK
}

func (hc *historyCmd) list(ctx context.Context, nums []int) int {
	env := hc.env
	var entries []Entry
	var err error
	scope := "the history of " + hc.scopeName()
	if hc.o.remote {
		conn, code := connect(ctx, env)
		if conn == nil {
			return code
		}
		scope = "the remote history"
		limit := hc.o.n
		if len(nums) > 0 {
			limit = 0
		}
		if entries, err = RemoteHistory(ctx, conn, limit); err != nil {
			return hc.fail(err)
		}
		if !hc.since.IsZero() {
			for len(entries) > 0 && entries[0].Time.Before(hc.since) {
				entries = entries[1:]
			}
		}
	} else {
		f := Filter{Peer: hc.peer, Since: hc.since, Limit: hc.o.n}
		if len(nums) > 0 {
			f = Filter{Peer: hc.peer}
		}
		if entries, err = hc.h.List(f); err != nil {
			return hc.fail(err)
		}
		if !RecordingEnabled() {
			fmt.Fprintln(env.Stderr, "clip: history recording is off (remohop clip history on)")
		}
	}
	if len(nums) > 0 {
		if entries, err = pick(entries, nums, scope); err != nil {
			return hc.fail(err)
		}
		sort.SliceStable(entries, func(i, j int) bool { return entries[i].N > entries[j].N }) // oldest first
	}
	switch {
	case hc.o.json:
		if entries == nil {
			entries = []Entry{}
		}
		printJSON(env, entries)
	case hc.o.md:
		md, shown := RenderMarkdownLimit(entries, hc.o.maxBytes)
		fmt.Fprint(env.Stdout, md)
		if shown < len(entries) {
			fmt.Fprintf(env.Stderr, "clip: Markdown output truncated at %d bytes: %d of %d entries shown (raise --max-bytes)\n",
				hc.o.maxBytes, shown, len(entries))
		}
	default:
		if len(entries) == 0 {
			fmt.Fprintln(env.Stdout, "no clipboard history")
			return cli.ExitOK
		}
		for i := len(entries) - 1; i >= 0; i-- { // newest first
			e := entries[i]
			fmt.Fprintf(env.Stdout, "#%-3d %s  %-4s  %-12s  %-5s  %-9s  %s\n", e.N, e.Time.Local().Format("2006-01-02 15:04:05"),
				e.Dir, meta(e.Peer), e.Kind, size(e.Size), preview(e))
		}
	}
	return cli.ExitOK
}

func (hc *historyCmd) show(ctx context.Context, n int) int {
	env := hc.env
	var e Entry
	var c Content
	var err error
	if hc.o.remote {
		var code int
		if e, code = hc.remoteEntry(ctx, n); code != 0 {
			return code
		}
		c, err = hc.remoteContent(ctx, e)
	} else {
		if e, err = hc.localEntry(n); err != nil {
			return hc.fail(err)
		}
		c, err = e.Content()
	}
	if err != nil {
		return hc.fail(fmt.Errorf("entry %s: %w", describeEntry(e), err))
	}
	fmt.Fprintf(env.Stderr, "clip: entry %s\n", describeEntry(e))
	switch {
	case hc.o.out == "" && c.Kind == KindImage:
		env.Errorf("entry #%d is an image (%s PNG); use -o FILE to save it or -o - to write it to stdout", n, size(len(c.Data)))
		return cli.ExitUsage
	case hc.o.out == "" || hc.o.out == "-":
		env.Stdout.Write(c.Data)
	default:
		if err := os.WriteFile(hc.o.out, c.Data, 0o600); err != nil {
			return hc.fail(err)
		}
		fmt.Fprintf(env.Stdout, "wrote %s (%s, %s)\n", hc.o.out, c.Kind, size(len(c.Data)))
	}
	return cli.ExitOK
}

func (hc *historyCmd) copy(ctx context.Context, n int) int {
	e, err := hc.localEntry(n)
	if err != nil {
		return hc.fail(err)
	}
	c, err := e.Content()
	if err != nil {
		return hc.fail(fmt.Errorf("entry %s: %w", describeEntry(e), err))
	}
	board := localBoard(ctx)
	if err := board.Write(ctx, c); err != nil {
		return hc.fail(fmt.Errorf("writing local clipboard (%s): %w", board.Name(), err))
	}
	fmt.Fprintf(hc.env.Stdout, "local clipboard set from entry %s (backend: %s)\n", describeEntry(e), board.Name())
	return cli.ExitOK
}

func (hc *historyCmd) remove(entries []Entry) ([]Entry, error) {
	ids := make([]string, len(entries))
	byID := map[string]Entry{}
	for i, e := range entries {
		ids[i] = e.ID
		byID[e.ID] = e
	}
	removed, err := hc.h.Remove(ids...)
	for i := range removed {
		removed[i].N = byID[removed[i].ID].N
	}
	return removed, err
}

func (hc *historyCmd) rm(nums []int) int {
	all, err := hc.h.List(Filter{Peer: hc.peer})
	if err != nil {
		return hc.fail(err)
	}
	chosen, err := pick(all, nums, "the history of "+hc.scopeName())
	if err != nil {
		return hc.fail(err) // nothing was removed
	}
	removed, err := hc.remove(chosen)
	for _, e := range removed {
		fmt.Fprintf(hc.env.Stdout, "removed %s\n", describeEntry(e))
	}
	if err != nil {
		return hc.fail(err)
	}
	return cli.ExitOK
}

func (hc *historyCmd) clearScope() int {
	all, err := hc.h.List(Filter{Peer: hc.peer})
	if err != nil {
		return hc.fail(err)
	}
	removed, err := hc.remove(all)
	if err != nil {
		return hc.fail(err)
	}
	perPeer := map[string]int{}
	var imgs int
	for _, e := range removed {
		perPeer[e.Peer]++
		if e.Kind == KindImage {
			imgs++
		}
	}
	fmt.Fprintf(hc.env.Stdout, "cleared %d history %s of %s (%d text, %d image)\n", len(removed),
		plural(len(removed), "entry", "entries"), hc.scopeName(), len(removed)-imgs, imgs)
	if hc.peer == "" {
		peers := make([]string, 0, len(perPeer))
		for p := range perPeer {
			peers = append(peers, p)
		}
		sort.Strings(peers)
		for _, p := range peers {
			fmt.Fprintf(hc.env.Stdout, "  %s: %d\n", meta(p), perPeer[p])
		}
	}
	return cli.ExitOK
}

func (hc *historyCmd) send(ctx context.Context, n int) int {
	e, err := hc.localEntry(n)
	if err != nil {
		return hc.fail(err)
	}
	c, err := e.Content()
	if err != nil {
		return hc.fail(fmt.Errorf("entry %s: %w", describeEntry(e), err))
	}
	conn, code := connect(ctx, hc.env)
	if conn == nil {
		return code
	}
	backend, err := set(ctx, conn, CallOptions{Peer: hc.env.Target, Source: e.Source}, "send", c)
	if err != nil {
		return hc.fail(err)
	}
	if RecordingEnabled() {
		if err := hc.h.MarkSent(e.ID, hc.env.Target); err != nil {
			return hc.fail(err)
		}
	}
	fmt.Fprintf(hc.env.Stdout, "remote clipboard set from entry %s (backend: %s)\n", describeEntry(e), backend)
	return cli.ExitOK
}

func (hc *historyCmd) take(ctx context.Context, n int) int {
	e, code := hc.remoteEntry(ctx, n)
	if code != 0 {
		return code
	}
	c, err := hc.remoteContent(ctx, e)
	if err != nil {
		return hc.fail(fmt.Errorf("remote entry %s: %w", describeEntry(e), err))
	}
	board := localBoard(ctx)
	if err := board.Write(ctx, c); err != nil {
		return hc.fail(fmt.Errorf("writing local clipboard (%s): %w", board.Name(), err))
	}
	recordEntry(hc.h, Entry{Dir: DirPull, Via: "take", Peer: hc.env.Target}, c)
	fmt.Fprintf(hc.env.Stdout, "local clipboard set from remote entry %s (backend: %s)\n", describeEntry(e), board.Name())
	return cli.ExitOK
}

func (hc *historyCmd) restore(ctx context.Context, n int) int {
	e, code := hc.remoteEntry(ctx, n)
	if code != 0 {
		return code
	}
	conn, err := hc.env.Conn(ctx)
	if err == nil {
		err = RemoteHistoryRestore(ctx, conn, e.ID)
	}
	if err != nil {
		return hc.fail(fmt.Errorf("remote entry %s: %w", describeEntry(e), err))
	}
	fmt.Fprintf(hc.env.Stdout, "remote clipboard set from remote entry %s\n", describeEntry(e))
	return cli.ExitOK
}
