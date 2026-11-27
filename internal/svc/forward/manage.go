package forward

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/readori/RemoHop/internal/bg"
	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/config"
)

// record is the structured description a running `remohop forward` process
// keeps next to its internal/bg record (run/forward-<pid>.json, mode 0600),
// so `remohop forward` (list) and `remohop unforward` need not parse the
// human-readable bg detail. Control and Token give access to the process's
// control socket, a loopback TCP listener that closes single listeners; the
// token keeps other local users out.
type record struct {
	PID      int          `json:"pid"`
	Target   string       `json:"target"`
	Forwards []listenInfo `json:"forwards"`
	Control  string       `json:"control,omitempty"`
	Token    string       `json:"token,omitempty"`
}

func recordPath(pid int) (string, error) {
	dir, err := config.SubDir("run")
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, fmt.Sprintf("forward-%d.json", pid)), nil
}

func writeRecord(r record) error {
	p, err := recordPath(r.PID)
	if err != nil {
		return err
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	// Write then rename so readers never see a partial record.
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func readRecord(pid int) (record, bool) {
	var r record
	p, err := recordPath(pid)
	if err != nil {
		return r, false
	}
	b, err := os.ReadFile(p)
	if err != nil || json.Unmarshal(b, &r) != nil || r.PID != pid {
		return r, false
	}
	return r, true
}

func removeRecord(pid int) {
	if p, err := recordPath(pid); err == nil {
		os.Remove(p)
	}
}

type controlReq struct {
	Token  string `json:"token"`
	Op     string `json:"op"`
	Listen string `json:"listen"`
}

type controlReply struct {
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
	Remaining int    `json:"remaining"`
}

// serveControl answers control requests until ln is closed. closeListener
// closes the forward listening on the given address and reports how many
// forwards remain.
func serveControl(ln net.Listener, token string, closeListener func(listen string) (found bool, remaining int)) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			c.SetDeadline(time.Now().Add(5 * time.Second))
			var req controlReq
			line, err := bufio.NewReaderSize(c, 4096).ReadSlice('\n')
			if err != nil || json.Unmarshal(line, &req) != nil {
				return
			}
			var rep controlReply
			switch {
			case subtle.ConstantTimeCompare([]byte(req.Token), []byte(token)) != 1:
				rep.Error = "bad token"
			case req.Op != "close":
				rep.Error = "unknown operation"
			default:
				found, remaining := closeListener(req.Listen)
				rep.OK, rep.Remaining = found, remaining
				if !found {
					rep.Error = "no such listener"
				}
			}
			b, _ := json.Marshal(rep)
			c.Write(append(b, '\n'))
		}()
	}
}

// controlClose asks the process described by r to close one listener.
func controlClose(r record, listen string) (controlReply, error) {
	var rep controlReply
	if r.Control == "" {
		return rep, errors.New("no control socket")
	}
	c, err := net.DialTimeout("tcp", r.Control, 3*time.Second)
	if err != nil {
		return rep, err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	b, _ := json.Marshal(controlReq{Token: r.Token, Op: "close", Listen: listen})
	if _, err := c.Write(append(b, '\n')); err != nil {
		return rep, err
	}
	line, err := bufio.NewReaderSize(c, 4096).ReadSlice('\n')
	if err != nil {
		return rep, err
	}
	if err := json.Unmarshal(line, &rep); err != nil {
		return rep, err
	}
	if !rep.OK {
		return rep, errors.New(rep.Error)
	}
	return rep, nil
}

func newToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// running is one listener of a running forward process.
type running struct {
	PID       int       `json:"pid"`
	ID        string    `json:"id"` // the remote machine
	Direction string    `json:"direction"`
	Proto     string    `json:"proto"`
	Listen    string    `json:"listen"`
	Target    string    `json:"target"`
	Started   time.Time `json:"started"`

	rec    record
	hasRec bool
}

// listRunning returns the listeners of every running `remohop forward`
// process, optionally only those to one target.
func listRunning(target string) ([]running, error) {
	infos, err := bg.List()
	if err != nil {
		return nil, err
	}
	sweepRecords(infos)
	var out []running
	for _, in := range infos {
		if in.Kind != "forward" || (target != "" && in.Target != target) {
			continue
		}
		rec, ok := readRecord(in.PID)
		if !ok {
			// A process from before records existed: show what bg knows.
			out = append(out, running{PID: in.PID, ID: in.Target, Listen: in.Detail, Started: in.Started})
			continue
		}
		for _, f := range rec.Forwards {
			out = append(out, running{PID: in.PID, ID: in.Target, Direction: f.Direction, Proto: f.Proto,
				Listen: f.Listen, Target: f.Target, Started: in.Started, rec: rec, hasRec: true})
		}
	}
	return out, nil
}

// sweepRecords removes records left behind by processes that were killed.
// A young record without a bg entry may belong to a process that is still
// starting, so only old ones go.
func sweepRecords(alive []bg.Info) {
	dir, err := config.SubDir("run")
	if err != nil {
		return
	}
	pids := map[string]bool{}
	for _, in := range alive {
		pids[strconv.Itoa(in.PID)] = true
	}
	files, _ := filepath.Glob(filepath.Join(dir, "forward-*.json"))
	for _, f := range files {
		pid := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), "forward-"), ".json")
		if st, err := os.Stat(f); err == nil && !pids[pid] && time.Since(st.ModTime()) > time.Minute {
			os.Remove(f)
		}
	}
}

func (r running) describe() string {
	if r.Target == "" {
		return cli.Sanitize(r.Listen)
	}
	return cli.Sanitize(r.Listen) + " -> " + cli.Sanitize(r.Target)
}

// runList prints the running forwards (`remohop forward`, `remohop <ID> forward`).
func runList(env *cli.Env, target string, asJSON bool) int {
	rows, err := listRunning(target)
	if err != nil {
		env.Errorf("forward: %v", err)
		return cli.ExitError
	}
	if asJSON {
		if rows == nil {
			rows = []running{}
		}
		enc := json.NewEncoder(env.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(rows)
		return cli.ExitOK
	}
	if len(rows) == 0 {
		if target != "" {
			fmt.Fprintf(env.Stdout, "no forwards running to %s\n", cli.Sanitize(target))
		} else {
			fmt.Fprintln(env.Stdout, "no forwards running")
		}
		return cli.ExitOK
	}
	fmt.Fprintf(env.Stdout, "%-8s %-12s %-8s %-5s %-10s %s\n", "PID", "TARGET", "KIND", "PROTO", "AGE", "FORWARD")
	for _, r := range rows {
		fmt.Fprintf(env.Stdout, "%-8d %-12s %-8s %-5s %-10s %s\n", r.PID, cli.Sanitize(r.ID), r.Direction, r.Proto,
			time.Since(r.Started).Round(time.Second), r.describe())
	}
	return cli.ExitOK
}

const unforwardUsage = `Usage: remohop [<ID>] unforward [--json] LOCAL-PORT...

Stop the running forward(s) listening on the given local port(s), as shown
by ` + "`remohop forward`" + `. With <ID> only forwards to that machine are considered.
A process that holds several forwards keeps the others running; when that is
not possible the whole process is stopped and the other forwards that went
with it are reported.

  --json   print one JSON object per stopped listener:
           {"pid":N,"listen":"ADDR","target":"ADDR","proto":"tcp|udp",
            "also_stopped":["ADDR -> ADDR",...]}

Examples:
  remohop unforward 18080
  remohop 123456789 unforward 5433 8888
`

type unforwarded struct {
	PID         int      `json:"pid"`
	Listen      string   `json:"listen"`
	Target      string   `json:"target"`
	Proto       string   `json:"proto"`
	AlsoStopped []string `json:"also_stopped,omitempty"`
}

func runUnforward(_ context.Context, env *cli.Env, args []string) int {
	fs := cli.NewFlagSet(env, "unforward", unforwardUsage)
	asJSON := fs.Bool("json", false, "")
	var ports []int
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
		p, err := strconv.Atoi(fs.Arg(0))
		if err != nil || p < 1 || p > 65535 {
			env.Errorf("unforward: bad local port %q", fs.Arg(0))
			return cli.ExitUsage
		}
		ports = append(ports, p)
		rest = fs.Args()[1:]
	}
	if len(ports) == 0 {
		env.Errorf("unforward: no local port given (see `remohop forward` for the running forwards)")
		return cli.ExitUsage
	}

	code := cli.ExitOK
	killed := map[int]bool{} // processes stopped as a whole
	for _, port := range ports {
		rows, err := listRunning(env.Target)
		if err != nil {
			env.Errorf("unforward: %v", err)
			return cli.ExitError
		}
		matched := false
		for _, r := range rows {
			if !r.hasRec || r.Direction != "local" || killed[r.PID] || portOf(r.Listen) != port {
				continue
			}
			matched = true
			res := unforwarded{PID: r.PID, Listen: r.Listen, Target: r.Target, Proto: r.Proto}
			if _, err := controlClose(r.rec, r.Listen); err != nil {
				// No way to close just this listener: stop the process.
				if serr := bg.Stop(r.PID); serr != nil {
					env.Errorf("unforward: stopping pid %d: %v", r.PID, serr)
					code = cli.ExitError
					continue
				}
				killed[r.PID] = true
				for _, o := range rows {
					if o.PID == r.PID && o.Listen != r.Listen {
						res.AlsoStopped = append(res.AlsoStopped, o.Listen+" -> "+o.Target)
					}
				}
			}
			if *asJSON {
				b, _ := json.Marshal(res)
				fmt.Fprintln(env.Stdout, string(b))
				continue
			}
			fmt.Fprintf(env.Stdout, "stopped %s -> %s (pid %d)\n", cli.Sanitize(r.Listen), cli.Sanitize(r.Target), r.PID)
			if len(res.AlsoStopped) > 0 {
				fmt.Fprintf(env.Stdout, "  the process could not close one listener only; also stopped: %s\n",
					cli.Sanitize(strings.Join(res.AlsoStopped, ", ")))
			}
		}
		if !matched {
			env.Errorf("unforward: no running forward listens on local port %d", port)
			code = cli.ExitError
		}
	}
	return code
}

func portOf(addr string) int {
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return -1
	}
	n, _ := strconv.Atoi(p)
	return n
}
