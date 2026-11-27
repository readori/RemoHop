package serial

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/readori/RemoHop/internal/bg"
	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/config"
)

const serialUsage = `Usage: remohop <ID> serial list [--json]
       remohop <ID> serial PORT [BAUD] [--local PATH] [--format 8N1] [--dtr on|off]
                            [--rts on|off] [--rtscts] [-b] [--json]
       remohop serial [--json]

Use a serial port of the remote machine as if it were plugged in here.

"serial list" shows the ports on the remote machine. "serial PORT" opens that
port there (exclusively; 115200 baud 8N1 unless told otherwise), creates a
LOCAL virtual serial port connected to it and prints its path. Point any
terminal program (picocom, screen, minicom, an IDE, a flasher) at that path.
When a local program changes the baud rate or the 8N1 framing of the virtual
port, the change follows to the remote port. The port closes, and the local
path disappears, when you press Ctrl-C, when the session ends, or with -b
when you run ` + "`remohop unserial PATH`" + ` or ` + "`remohop stop PID`" + `.

"remohop serial" with no target lists the virtual ports active on this machine.

PORT is a path (/dev/ttyUSB0, COM3), a short name (ttyUSB0), or a
/dev/serial/by-id name, as shown by "serial list". The remote host must allow
the "serial" feature (remohop config feature serial on; a serial console can be a
login, so grant it like a shell). Creating virtual ports needs
Linux or macOS on this side; the remote side may be Linux, macOS or Windows.

Flags:
  --local PATH     where the virtual port appears (default: in the serial/
                   directory of the remohop config directory)
  --format FMT     data bits, parity, stop bits, e.g. 8N1 (default), 7E1, 8N2
  --dtr on|off     set DTR when opening (default: left as the driver does)
  --rts on|off     set RTS when opening
  --rtscts         RTS/CTS hardware flow control
  -b, --background detach once the port is ready (see remohop ps / remohop unserial)
  --json           machine-readable output

Examples:
  remohop 123456789 serial list
  remohop 123456789 serial /dev/ttyUSB0
  remohop 123456789 serial ttyACM0 9600 --local /tmp/arduino
  remohop 123456789 serial -b COM3 921600 --json
  picocom /tmp/arduino
  remohop serial
  remohop unserial /tmp/arduino`

const unserialUsage = `Usage: remohop unserial PATH [--json]

Close the local virtual serial port at PATH (as printed by "remohop <ID> serial"
and listed by "remohop serial") and remove it. It stops the remohop process that owns
the port, whether started with -b or in another terminal.

Example:
  remohop unserial /tmp/arduino`

func init() {
	cli.Register(&cli.Command{
		Name:      "serial",
		Summary:   "use a remote serial port as a local virtual one (list local ones without a target)",
		Usage:     serialUsage,
		Run:       runLocalList,
		RemoteRun: runRemote,
	})
	cli.Register(&cli.Command{
		Name:    "unserial",
		Summary: "close a local virtual serial port",
		Usage:   unserialUsage,
		Run:     runUnserial,
	})
}

func isJSONFlag(a string) bool { return a == "--json" || a == "-json" }

// ---- remohop serial / remohop unserial (local) ----

// Active is one virtual port running on this machine.
type Active struct {
	PID    int    `json:"pid"`
	Target string `json:"target"`
	Local  string `json:"local"`
	Port   string `json:"port"`
	Config string `json:"config"`
}

func detail(local, port string, cfg Config) string {
	return local + " -> " + port + " @ " + cfg.String()
}

// ListActive returns the virtual ports run by remohop processes on this machine.
func ListActive() ([]Active, error) {
	infos, err := bg.List()
	if err != nil {
		return nil, err
	}
	var out []Active
	for _, in := range infos {
		if in.Kind != "serial" {
			continue
		}
		local, rest, ok := strings.Cut(in.Detail, " -> ")
		if !ok {
			continue
		}
		port, cfg, _ := strings.Cut(rest, " @ ")
		out = append(out, Active{PID: in.PID, Target: in.Target, Local: local, Port: port, Config: cfg})
	}
	return out, nil
}

func runLocalList(ctx context.Context, env *cli.Env, args []string) int {
	asJSON := false
	for _, a := range args {
		if isJSONFlag(a) {
			asJSON = true
		} else {
			env.Errorf("serial: unexpected argument %q (a remote port needs a target: remohop <ID> serial PORT)", a)
			return cli.ExitUsage
		}
	}
	act, err := ListActive()
	if err != nil {
		env.Errorf("serial: %v", err)
		return cli.ExitError
	}
	if asJSON {
		if act == nil {
			act = []Active{}
		}
		enc := json.NewEncoder(env.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(act)
		return cli.ExitOK
	}
	if len(act) == 0 {
		fmt.Fprintln(env.Stdout, "no virtual serial ports active")
		return cli.ExitOK
	}
	fmt.Fprintf(env.Stdout, "%-8s %-12s %-24s %s\n", "PID", "TARGET", "REMOTE PORT", "LOCAL PATH")
	for _, a := range act {
		fmt.Fprintf(env.Stdout, "%-8d %-12s %-24s %s\n", a.PID, cli.Sanitize(a.Target), cli.Sanitize(a.Port+" ("+a.Config+")"), cli.Sanitize(a.Local))
	}
	return cli.ExitOK
}

func runUnserial(ctx context.Context, env *cli.Env, args []string) int {
	var paths []string
	for _, a := range args {
		if a == "-h" || a == "--help" {
			fmt.Fprintln(env.Stdout, unserialUsage)
			return cli.ExitOK
		}
		if strings.HasPrefix(a, "-") {
			env.Errorf("unserial: unknown flag %q", a)
			return cli.ExitUsage
		}
		paths = append(paths, a)
	}
	if len(paths) == 0 {
		env.Errorf("unserial: need the local path of a virtual port (see `remohop serial`)")
		return cli.ExitUsage
	}
	act, err := ListActive()
	if err != nil {
		env.Errorf("unserial: %v", err)
		return cli.ExitError
	}
	code := cli.ExitOK
	for _, p := range paths {
		abs, _ := filepath.Abs(p)
		found := false
		for _, a := range act {
			if a.Local != p && a.Local != abs {
				continue
			}
			found = true
			if err := bg.Stop(a.PID); err != nil {
				env.Errorf("unserial %s: %v", p, err)
				code = cli.ExitError
				break
			}
			// The owner removes its own link on the way out; make sure.
			if st, err := os.Lstat(a.Local); err == nil && st.Mode()&os.ModeSymlink != 0 {
				os.Remove(a.Local)
			}
			fmt.Fprintf(env.Stdout, "closed %s\n", cli.Sanitize(a.Local))
			break
		}
		if !found {
			env.Errorf("unserial: no active virtual serial port at %s (see `remohop serial`)", cli.Sanitize(p))
			code = cli.ExitError
		}
	}
	return code
}

// ---- remohop <ID> serial ... ----

func runRemote(ctx context.Context, env *cli.Env, args []string) int {
	if len(args) > 0 && args[0] == "list" {
		return runList(ctx, env, args[1:])
	}
	return runAttach(ctx, env, args)
}

func runList(ctx context.Context, env *cli.Env, args []string) int {
	asJSON := false
	for _, a := range args {
		if isJSONFlag(a) {
			asJSON = true
		} else {
			env.Errorf("serial list: unexpected argument %q", a)
			return cli.ExitUsage
		}
	}
	conn, err := env.Conn(ctx)
	if err != nil {
		env.Errorf("%v", err)
		return cli.ExitConnect
	}
	ports, err := List(ctx, conn)
	if err != nil {
		env.Errorf("serial list: %v", err)
		return cli.ExitError
	}
	if asJSON {
		if ports == nil {
			ports = []PortInfo{}
		}
		enc := json.NewEncoder(env.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(ports)
		return cli.ExitOK
	}
	if len(ports) == 0 {
		fmt.Fprintln(env.Stdout, "no serial ports on the remote machine")
		return cli.ExitOK
	}
	fmt.Fprintf(env.Stdout, "%-16s %-9s %-12s %s\n", "PORT", "USB ID", "DRIVER", "DESCRIPTION")
	for _, p := range ports {
		id := "-"
		if p.VID != "" {
			id = p.VID + ":" + p.PID
		}
		desc := p.Description
		if p.Serial != "" {
			desc += " [" + p.Serial + "]"
		}
		fmt.Fprintf(env.Stdout, "%-16s %-9s %-12s %s\n", cli.Sanitize(p.Path), cli.Sanitize(id), cli.Sanitize(p.Driver), cli.Sanitize(desc))
		for _, id := range p.ByID {
			fmt.Fprintf(env.Stdout, "  alias %s\n", cli.Sanitize(id))
		}
	}
	return cli.ExitOK
}

type onOff struct{ v **bool }

func (o onOff) String() string { return "" }
func (o onOff) Set(s string) error {
	var b bool
	switch strings.ToLower(s) {
	case "on", "1", "true", "yes":
		b = true
	case "off", "0", "false", "no":
	default:
		return fmt.Errorf("want on or off, got %q", s)
	}
	*o.v = &b
	return nil
}

var unsafeName = func(r rune) rune {
	if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_' {
		return r
	}
	return '_'
}

// DefaultLocalPath is where a virtual port for target's port lives by default.
func DefaultLocalPath(target, port string) (string, error) {
	dir, err := config.SubDir("serial")
	if err != nil {
		return "", err
	}
	name := strings.TrimLeft(strings.Map(unsafeName, target+"-"+strings.TrimPrefix(port, "/dev/")), ".")
	return filepath.Join(dir, name), nil
}

func runAttach(ctx context.Context, env *cli.Env, args []string) int {
	fs := cli.NewFlagSet(env, "serial", serialUsage)
	var local, format string
	var background, asJSON, rtscts bool
	var dtr, rts *bool
	fs.StringVar(&local, "local", "", "")
	fs.StringVar(&format, "format", "", "")
	fs.BoolVar(&background, "b", false, "")
	fs.BoolVar(&background, "background", false, "")
	fs.BoolVar(&asJSON, "json", false, "")
	fs.BoolVar(&rtscts, "rtscts", false, "")
	fs.Var(onOff{&dtr}, "dtr", "")
	fs.Var(onOff{&rts}, "rts", "")

	var pos []string
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
		pos = append(pos, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	if len(pos) == 0 || len(pos) > 2 {
		env.Errorf("serial: want PORT [BAUD] (see `remohop <ID> serial list`)")
		return cli.ExitUsage
	}
	cfg := DefaultConfig()
	if len(pos) == 2 {
		b, err := strconv.Atoi(pos[1])
		if err != nil {
			env.Errorf("serial: bad baud rate %q", pos[1])
			return cli.ExitUsage
		}
		cfg.Baud = b
	}
	if format != "" {
		if err := cfg.ParseFormat(format); err != nil {
			env.Errorf("serial: %v", err)
			return cli.ExitUsage
		}
	}
	cfg.RTSCTS = rtscts
	if err := cfg.Validate(); err != nil {
		env.Errorf("serial: %v", err)
		return cli.ExitUsage
	}
	port := pos[0]
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		env.Errorf("serial: creating a virtual serial port is not supported on %s (the controlling machine must be Linux or macOS); `remohop <ID> serial list` still works", runtime.GOOS)
		return cli.ExitError
	}
	if local == "" {
		p, err := DefaultLocalPath(env.Target, port)
		if err != nil {
			env.Errorf("serial: %v", err)
			return cli.ExitError
		}
		local = p
	}
	if abs, err := filepath.Abs(local); err == nil {
		local = abs
	}

	if background && !bg.InChild() {
		msg, pid, err := bg.Start(60 * time.Second)
		if err != nil {
			env.Errorf("serial: %v", err)
			return cli.ExitError
		}
		fmt.Fprintln(env.Stdout, msg)
		if !asJSON {
			fmt.Fprintf(env.Stdout, "running in the background as pid %d (stop with `remohop unserial %s`)\n", pid, local)
		}
		return cli.ExitOK
	}

	conn, err := env.Conn(ctx)
	if err != nil {
		env.Errorf("%v", err)
		return cli.ExitConnect
	}
	opts := OpenOptions{DTR: dtr, RTS: rts,
		OnNotice: func(s string) { env.Errorf("serial: host: %s", s) },
		OnModem: func(m Modem) {
			if env.Globals.Verbose {
				env.Errorf("serial: modem lines CTS=%v DSR=%v DCD=%v RI=%v", m.CTS, m.DSR, m.DCD, m.RI)
			}
		}}
	v, err := Attach(ctx, conn, port, cfg, AttachOptions{OpenOptions: opts, LocalPath: local})
	if err != nil {
		env.Errorf("serial: %v", err)
		return cli.ExitError
	}
	defer v.Close()

	var msg string
	if asJSON {
		b, _ := json.Marshal(map[string]any{"local": local, "slave": v.Slave, "port": v.Remote.Path,
			"baud": cfg.Baud, "format": fmt.Sprintf("%d%c%d", cfg.Data, cfg.Parity, cfg.Stop)})
		msg = string(b)
	} else {
		msg = fmt.Sprintf("virtual serial port %s -> %s (%s)", local, cli.Sanitize(v.Remote.Path), cfg)
	}
	if !bg.InChild() {
		fmt.Fprintln(env.Stdout, msg)
	}
	done := bg.Ready(bg.Info{Kind: "serial", Target: env.Target, Detail: detail(local, v.Remote.Path, cfg)}, msg)
	defer done()

	select {
	case <-ctx.Done():
		return cli.ExitOK
	case <-v.Done():
		if ctx.Err() != nil {
			return cli.ExitOK
		}
		if e := v.Err(); e != nil {
			env.Errorf("serial: %v", e)
		} else {
			env.Errorf("serial: session closed or remote port closed")
		}
		return cli.ExitError
	}
}
