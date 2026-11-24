package forward

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/readori/RemoHop/internal/bg"
	"github.com/readori/RemoHop/internal/cli"
)

const forwardUsage = `Usage: remohop <ID> forward [-b] [--udp] [--json] [-L] SPEC... [-R SPEC]...
       remohop [<ID>] forward [--json]            list the running forwards

Map a service on the remote machine's network to localhost (like ssh -L), or
expose a local service on the remote machine (like ssh -R). It runs in the
FOREGROUND until interrupted or the session ends; add -b to return as soon
as the listeners are up and leave the forward running in the background
(what scripts usually want). Without a SPEC it lists the running forwards,
to <ID> or to every machine; stop one with ` + "`remohop unforward LOCAL-PORT`" + ` (or a
whole process with ` + "`remohop stop PID`" + `).

SPEC forms (default bind address is 127.0.0.1; IPv6 literals in brackets):
  LPORT:RPORT                 target 127.0.0.1:RPORT as seen from the remote
  LPORT:RHOST:RPORT           any host the remote machine can reach
  BIND:LPORT:RHOST:RPORT      listen on BIND ("*" = all interfaces)
LPORT 0 picks a free port; the actual address is always printed.

Flags:
  -b, --background   detach once all listeners are up
  -L SPEC            local forward (the default for bare SPECs); repeatable
  -R SPEC            reverse forward: the REMOTE machine listens on LPORT and
                     connections are carried back here to RHOST:RPORT, which
                     defaults to 127.0.0.1; repeatable (TCP only)
      --udp          the local forwards carry UDP instead of TCP: each local
                     source address is a flow, forgotten after 60 s of silence
      --json         print one JSON object per listener:
                     {"direction":"local|reverse","proto":"tcp|udp",
                      "listen":"ADDR","target":"ADDR"}
                     (when listing: a JSON array with pid, id, direction,
                     proto, listen, target, started)

Examples:
  remohop 123456789 forward -b 18080:8080         # remote :8080 -> localhost:18080
  remohop 123456789 forward 5433:db.internal:5432 8888:8888    # foreground
  remohop 123456789 forward -b 0:3000 --json      # free local port, background
  remohop 123456789 forward -R 9000:3000          # remote :9000 -> my localhost:3000
  remohop 123456789 forward -b --udp 5353:53      # UDP, e.g. the remote's DNS
  remohop forward                                 # what is running
  remohop unforward 18080
`

func init() {
	// A local command with a remote form: `remohop forward` lists every running
	// forward, `remohop <ID> forward ...` starts forwards to (or lists those of)
	// one machine.
	cli.Register(&cli.Command{
		Name:      "forward",
		Summary:   "forward ports to/from a remote machine (-b for background); no SPEC lists them",
		Usage:     forwardUsage,
		Run:       runLocal,
		RemoteRun: run,
	})
	cli.Register(&cli.Command{
		Name:      "unforward",
		Summary:   "stop the forward listening on a local port",
		Usage:     unforwardUsage,
		Run:       runUnforward,
		RemoteRun: runUnforward,
	})
}

// runLocal is `remohop forward` without a target: it can only list.
func runLocal(ctx context.Context, env *cli.Env, args []string) int {
	asJSON := false
	for _, a := range args {
		switch a {
		case "--json", "-json":
			asJSON = true
		case "-h", "--help", "-help":
			fmt.Fprint(env.Stderr, forwardUsage)
			return cli.ExitOK
		default:
			env.Errorf("forward: starting a forward needs a target: remohop <ID> forward %s", strings.Join(args, " "))
			return cli.ExitUsage
		}
	}
	return runList(env, "", asJSON)
}

type specList []Spec

func (l *specList) String() string { return fmt.Sprint([]Spec(*l)) }
func (l *specList) Set(v string) error {
	sp, err := ParseSpec(v)
	if err != nil {
		return err
	}
	*l = append(*l, sp)
	return nil
}

type listenInfo struct {
	Direction string `json:"direction"`
	Proto     string `json:"proto"`
	Listen    string `json:"listen"`
	Target    string `json:"target"`
}

// live is one running listener of this process, TCP or UDP.
type live struct {
	info  listenInfo
	close func() error
	done  <-chan struct{}
	err   func() error
	// closed is set when the listener was closed on request (unforward),
	// which is not a failure.
	closed bool
}

func run(ctx context.Context, env *cli.Env, args []string) int {
	fs := flag.NewFlagSet("forward", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	var locals, reverses specList
	var background, asJSON, udp bool
	fs.BoolVar(&background, "b", false, "")
	fs.BoolVar(&background, "background", false, "")
	fs.BoolVar(&asJSON, "json", false, "")
	fs.BoolVar(&udp, "udp", false, "")
	fs.Var(&locals, "L", "")
	fs.Var(&reverses, "R", "")
	fs.Usage = func() { fmt.Fprint(env.Stderr, forwardUsage) }

	// Flags may follow the specs (`forward 8080:80 -b`); the stdlib parser
	// stops at the first positional, so parse in rounds.
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return cli.ExitOK
			}
			return cli.ExitUsage
		}
		if fs.NArg() == 0 {
			break
		}
		if err := locals.Set(fs.Arg(0)); err != nil {
			env.Errorf("forward: %v", err)
			return cli.ExitUsage
		}
		rest = fs.Args()[1:]
	}
	if len(locals)+len(reverses) == 0 {
		if background || udp {
			env.Errorf("forward: no SPEC given (e.g. `remohop <ID> forward -b 18080:8080`)")
			return cli.ExitUsage
		}
		return runList(env, env.Target, asJSON)
	}
	if udp && len(reverses) > 0 {
		env.Errorf("forward: --udp does not support reverse (-R) forwards")
		return cli.ExitUsage
	}

	if background && !bg.InChild() {
		// Re-exec before connecting: the child owns the session.
		msg, pid, err := bg.Start(60 * time.Second)
		if err != nil {
			env.Errorf("forward: %v", err)
			return cli.ExitError
		}
		fmt.Fprintln(env.Stdout, msg)
		if !asJSON {
			fmt.Fprintf(env.Stdout, "running in the background as pid %d (list with `remohop forward`, stop with `remohop unforward PORT` or `remohop stop %d`)\n", pid, pid)
		}
		return cli.ExitOK
	}

	conn, err := env.Conn(ctx)
	if err != nil {
		env.Errorf("%v", err)
		return cli.ExitConnect
	}

	var (
		mu    sync.Mutex // guards lives[i].closed and the record
		lives []*live
	)
	defer func() {
		for _, l := range lives {
			l.close()
		}
	}()
	proto := "tcp"
	if udp {
		proto = "udp"
	}
	for _, sp := range locals {
		if udp {
			f, err := ListenUDP(ctx, conn, sp)
			if err != nil {
				env.Errorf("forward: %v", err)
				return cli.ExitError
			}
			lives = append(lives, &live{info: listenInfo{"local", proto, f.Addr().String(), sp.Target()},
				close: f.Close, done: f.Done(), err: f.Err})
			continue
		}
		f, err := Listen(ctx, conn, sp)
		if err != nil {
			env.Errorf("forward: %v", err)
			return cli.ExitError
		}
		lives = append(lives, &live{info: listenInfo{"local", proto, f.Addr().String(), sp.Target()},
			close: f.Close, done: f.Done(), err: f.Err})
	}
	for _, sp := range reverses {
		f, err := ListenReverse(ctx, conn, sp)
		if err != nil {
			env.Errorf("forward: %v", err)
			return cli.ExitError
		}
		lives = append(lives, &live{info: listenInfo{"reverse", "tcp", f.Addr().String(), sp.Target()},
			close: f.Close, done: f.Done(), err: f.Err})
	}

	var lines, details []string
	for _, l := range lives {
		in := l.info
		suffix := ""
		if in.Proto == "udp" {
			suffix = " udp"
		}
		if asJSON {
			b, _ := json.Marshal(in)
			lines = append(lines, string(b))
		} else if in.Direction == "local" {
			lines = append(lines, fmt.Sprintf("forwarding %s -> %s (remote)%s", cli.Sanitize(in.Listen), cli.Sanitize(in.Target), suffix))
		} else {
			lines = append(lines, fmt.Sprintf("remote %s -> %s (local)", cli.Sanitize(in.Listen), cli.Sanitize(in.Target)))
		}
		details = append(details, in.Listen+" -> "+in.Target+suffix)
	}
	msg := strings.Join(lines, "\n")

	// The structured record and the control socket behind `remohop forward`
	// (list) and `remohop unforward`. Both are best effort: without them the
	// forward still works and `remohop stop` still stops it.
	rec := record{PID: os.Getpid(), Target: env.Target}
	saveRecord := func() { // mu held
		rec.Forwards = rec.Forwards[:0]
		for _, l := range lives {
			if !l.closed {
				rec.Forwards = append(rec.Forwards, l.info)
			}
		}
		if err := writeRecord(rec); err != nil {
			slog.Debug("forward: writing the forward record", "err", err)
		}
	}
	allClosed := make(chan struct{})
	if ctl, err := net.Listen("tcp", "127.0.0.1:0"); err == nil {
		if tok, err := newToken(); err == nil {
			rec.Control, rec.Token = ctl.Addr().String(), tok
			go serveControl(ctl, tok, func(listen string) (bool, int) {
				mu.Lock()
				defer mu.Unlock()
				found, remaining := false, 0
				for _, l := range lives {
					switch {
					case l.closed:
					case l.info.Listen == listen && l.info.Direction == "local":
						l.closed, found = true, true
						l.close()
					default:
						remaining++
					}
				}
				if found {
					saveRecord()
					if remaining == 0 {
						close(allClosed)
					}
				}
				return found, remaining
			})
		}
		defer ctl.Close()
	}
	mu.Lock()
	saveRecord()
	mu.Unlock()
	defer removeRecord(rec.PID)

	if !bg.InChild() {
		fmt.Fprintln(env.Stdout, msg)
	}
	done := bg.Ready(bg.Info{Kind: "forward", Target: env.Target, Detail: strings.Join(details, ", ")}, msg)
	defer done()

	stopped := make(chan *live, len(lives))
	for _, l := range lives {
		go func() { <-l.done; stopped <- l }()
	}
	for {
		select {
		case <-ctx.Done():
			return cli.ExitOK
		case <-allClosed:
			return cli.ExitOK
		case l := <-stopped:
			if ctx.Err() != nil { // interrupted: the listeners stop with us
				return cli.ExitOK
			}
			mu.Lock()
			intentional := l.closed
			mu.Unlock()
			if intentional {
				continue
			}
			if err := l.err(); errors.Is(err, ErrSessionClosed) {
				env.Errorf("forward: session closed")
			} else {
				env.Errorf("forward %s stopped: %v", l.info.Listen, err)
			}
			return cli.ExitError
		}
	}
}
