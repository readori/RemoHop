package wake

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/readori/RemoHop/internal/perm"
	"strings"
	"time"

	"github.com/readori/RemoHop/internal/cli"
)

const wakeUsage = `Usage: remohop wake [--broadcast ADDR] [--port N] [--json] MAC|NAME
       remohop wake --save NAME MAC
       remohop wake --list [--json]
       remohop wake --forget NAME

Send a Wake-on-LAN magic packet from THIS machine to a sleeping computer on
the same network. By default the packet goes out on every up IPv4 interface
(to its subnet broadcast address). To wake a machine from far away, use a
small always-on box on its LAN instead: remohop <ID> wol MAC.

MAC may be written aa:bb:cc:dd:ee:ff, aa-bb-cc-dd-ee-ff, aabb.ccdd.eeff or
aabbccddeeff. NAME is a name saved earlier with --save (kept in
wake.json in the config directory).

Flags:
  --broadcast ADDR  send to this IPv4 address only (e.g. 192.168.1.255)
  --port N          UDP port (default 9; some machines use 7)
  --save NAME MAC   remember MAC under NAME (does not send anything)
  --list            show saved names
  --forget NAME     delete a saved name
  --json            machine-readable output

The target machine must have Wake-on-LAN enabled in its firmware and network
driver, and normally has to be on the wired network.

Examples:
  remohop wake aa:bb:cc:dd:ee:ff
  remohop wake --broadcast 192.168.1.255 aabbccddeeff
  remohop wake --save nas aa:bb:cc:dd:ee:ff
  remohop wake nas`

const wolUsage = `Usage: remohop <ID> wol [--port N] [--json] MAC|NAME

Ask the remote host to send a Wake-on-LAN magic packet on ITS local network,
to wake a sleeping computer next to it. Run it against a small always-on
machine (a Raspberry Pi, a NAS, a mini PC) that runs ` + "`remohop host`" + ` on the same LAN
as the sleeping computer.

The host picks the destinations (a broadcast on each of its networks); a
remote peer cannot aim the packet elsewhere. A host accepts a limited number
of wake requests per minute. NAME is resolved on THIS machine from the names
saved with ` + "`remohop wake --save`" + `.

Flags:
  --port N    UDP port (default 9)
  --json      machine-readable output

Examples:
  remohop 123456789 wol aa:bb:cc:dd:ee:ff
  remohop 123456789 wol nas`

func init() {
	// Sending packets onto the host's LAN on a client's behalf is the same
	// kind of reach as a port forward, so the same switch governs it.
	perm.Register("wake", perm.Forward)
	// `remohop wake` sends from this machine and `remohop <ID> wake` asks that host
	// to; "wol" is kept as a second name for the remote form.
	cli.Register(&cli.Command{
		Name:      "wake",
		Summary:   "send a Wake-on-LAN magic packet from this machine",
		Usage:     wakeUsage,
		Run:       runWake,
		RemoteRun: runWol,
	})
	cli.Register(&cli.Command{
		Name:    "wol",
		Summary: "wake a sleeping PC on the remote host's LAN",
		Hidden:  true,
		Usage:   wolUsage,
		Remote:  true,
		Run:     runWol,
	})
}

// parseInterspersed parses flags that may follow positional arguments, so
// both `wake --port 7 MAC` and `wake MAC --port 7` work.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
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

// resolve turns a MAC or a saved name into a MAC.
func resolve(env *cli.Env, arg string) (string, int) {
	if _, err := ParseMAC(arg); err == nil {
		return arg, 0
	}
	if nameRE.MatchString(arg) {
		st, err := OpenStore()
		if err != nil {
			env.Errorf("wake: %v", err)
			return "", cli.ExitError
		}
		mac, ok, err := st.Lookup(arg)
		switch {
		case err != nil:
			env.Errorf("wake: %v", err)
			return "", cli.ExitError
		case ok:
			return mac, 0
		}
		env.Errorf("wake: %q is neither a MAC address nor a saved name (see `remohop wake --list`)", clip(arg))
		return "", cli.ExitUsage
	}
	_, err := ParseMAC(arg)
	env.Errorf("wake: %v", err)
	return "", cli.ExitUsage
}

func printResult(env *cli.Env, res *Result, asJSON bool, how string) {
	if asJSON {
		b, _ := json.MarshalIndent(res, "", "  ")
		fmt.Fprintln(env.Stdout, string(b))
		return
	}
	fmt.Fprintf(env.Stdout, "sent magic packet for %s to %s (udp/%d)%s\n",
		res.MAC, cli.Sanitize(strings.Join(res.Targets, ", ")), res.Port, how)
}

func runWake(ctx context.Context, env *cli.Env, args []string) int {
	fs := flag.NewFlagSet("wake", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	fs.Usage = func() { fmt.Fprintln(env.Stderr, wakeUsage) }
	bcast := fs.String("broadcast", "", "")
	port := fs.Int("port", 0, "")
	save := fs.String("save", "", "")
	list := fs.Bool("list", false, "")
	forget := fs.String("forget", "", "")
	asJSON := fs.Bool("json", false, "")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return usageExit(err)
	}

	switch {
	case *list:
		return runList(env, *asJSON, pos)
	case *forget != "":
		return runForget(env, *forget, pos)
	case *save != "":
		return runSave(env, *save, pos)
	}
	if len(pos) != 1 {
		env.Errorf("usage: remohop wake [--broadcast ADDR] [--port N] MAC|NAME (see `remohop help wake`)")
		return cli.ExitUsage
	}
	mac, code := resolve(env, pos[0])
	if code != 0 {
		return code
	}
	res, err := Send(ctx, mac, Options{Broadcast: *bcast, Port: *port})
	if err != nil {
		env.Errorf("%v", err)
		return cli.ExitError
	}
	printResult(env, res, *asJSON, "")
	return cli.ExitOK
}

func runSave(env *cli.Env, name string, pos []string) int {
	if len(pos) != 1 {
		env.Errorf("usage: remohop wake --save NAME MAC")
		return cli.ExitUsage
	}
	if err := ValidName(name); err != nil {
		env.Errorf("wake: %v", err)
		return cli.ExitUsage
	}
	st, err := OpenStore()
	if err != nil {
		env.Errorf("wake: %v", err)
		return cli.ExitError
	}
	if err := st.Save(name, pos[0]); err != nil {
		env.Errorf("wake: %v", err)
		return cli.ExitUsage
	}
	hw, _ := ParseMAC(pos[0])
	fmt.Fprintf(env.Stdout, "saved %s = %s\n", name, hw)
	return cli.ExitOK
}

func runForget(env *cli.Env, name string, pos []string) int {
	if len(pos) != 0 {
		env.Errorf("usage: remohop wake --forget NAME")
		return cli.ExitUsage
	}
	st, err := OpenStore()
	if err != nil {
		env.Errorf("wake: %v", err)
		return cli.ExitError
	}
	ok, err := st.Forget(name)
	switch {
	case err != nil:
		env.Errorf("wake: %v", err)
		return cli.ExitError
	case !ok:
		env.Errorf("wake: no saved name %q", clip(name))
		return cli.ExitError
	}
	fmt.Fprintf(env.Stdout, "forgot %s\n", name)
	return cli.ExitOK
}

func runList(env *cli.Env, asJSON bool, pos []string) int {
	if len(pos) != 0 {
		env.Errorf("usage: remohop wake --list [--json]")
		return cli.ExitUsage
	}
	st, err := OpenStore()
	if err != nil {
		env.Errorf("wake: %v", err)
		return cli.ExitError
	}
	entries, err := st.List()
	if err != nil {
		env.Errorf("wake: %v", err)
		return cli.ExitError
	}
	if asJSON {
		b, _ := json.MarshalIndent(entries, "", "  ")
		fmt.Fprintln(env.Stdout, string(b))
		return cli.ExitOK
	}
	if len(entries) == 0 {
		fmt.Fprintln(env.Stdout, "no saved names (remohop wake --save NAME MAC)")
	}
	for _, e := range entries {
		fmt.Fprintf(env.Stdout, "%-20s %s\n", cli.Sanitize(e.Name), cli.Sanitize(e.MAC))
	}
	return cli.ExitOK
}

func runWol(ctx context.Context, env *cli.Env, args []string) int {
	fs := flag.NewFlagSet("wol", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	fs.Usage = func() { fmt.Fprintln(env.Stderr, wolUsage) }
	port := fs.Int("port", 0, "")
	asJSON := fs.Bool("json", false, "")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return usageExit(err)
	}
	if len(pos) != 1 {
		env.Errorf("usage: remohop <ID> wol [--port N] MAC|NAME (see `remohop help wol`)")
		return cli.ExitUsage
	}
	mac, code := resolve(env, pos[0])
	if code != 0 {
		return code
	}
	conn, err := env.Conn(ctx)
	if err != nil {
		env.Errorf("%v", err)
		return cli.ExitConnect
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, err := Remote(ctx, conn, mac, Options{Port: *port})
	if err != nil {
		env.Errorf("%v", err)
		return cli.ExitError
	}
	printResult(env, res, *asJSON, " from the remote host")
	return cli.ExitOK
}
