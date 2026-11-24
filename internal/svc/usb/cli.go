package usb

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/readori/RemoHop/internal/bg"
	"github.com/readori/RemoHop/internal/cli"
)

const usbUsage = `Usage: remohop <ID> usb list [--json]
       remohop <ID> usb attach [-b] [--json] BUSID
       remohop usb [list] [--json]
       remohop usb detach N|BUSID
       remohop usb allow [--force] BUSID     (on the host, by its owner)
       remohop usb disallow BUSID | remohop usb allowed [--json]
       remohop usb release BUSID             (on the host: recover after a crash)

Use a USB device plugged into the remote machine as if it were plugged in
here (USB/IP over the session). Linux on both sides, root on both sides.

"usb list" shows the remote machine's devices and whether its owner allowed
exporting each one. "usb attach" asks the host to export BUSID and imports it
here through the vhci_hcd driver; it stays in the foreground (or with -b in
the background) and the device is detached when it is stopped, when you run
"remohop usb detach", or when the session ends. "remohop usb" lists the devices
attached on this machine.

Exporting detaches a device from the host's own drivers (a keyboard stops
typing, a disk disappears), so the host owner must allow each device first:
"remohop usb allow 1-2" on the host records the busid and its VID:PID. Keyboards,
mice, storage and hubs need --force, and hubs are never exported. The host's
original driver is restored when the client detaches or the session ends.

Needed kernel modules: usbip-host on the host (sudo modprobe usbip-host) and
vhci-hcd on the client (sudo modprobe vhci-hcd). Missing ones are reported
by name and side. The host must allow the "usb" feature (remohop config feature
usb on) and each device (remohop usb allow BUSID).

Examples:
  remohop 123456789 usb list
  remohop usb allow 1-2                 # on the host
  sudo remohop 123456789 usb attach -b 1-2
  remohop usb
  sudo remohop usb detach 1-2`

func init() {
	cli.Register(&cli.Command{
		Name:      "usb",
		Summary:   "use a remote USB device as a local one (USB/IP; list attached ones without a target)",
		Usage:     usbUsage,
		Run:       runLocal,
		RemoteRun: runRemote,
	})
}

func isJSON(a string) bool { return a == "--json" || a == "-json" }

func printJSON(env *cli.Env, v any) {
	enc := json.NewEncoder(env.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

// ---- local: attached devices, detach, host owner commands ----

// Attachment is an imported device with its owning remohop process, if any.
type Attachment struct {
	Port       int    `json:"port"`
	LocalBusID string `json:"local_busid"`
	BusID      string `json:"busid,omitempty"` // the host's busid
	Target     string `json:"target,omitempty"`
	PID        int    `json:"pid,omitempty"`
	Device     string `json:"device,omitempty"`
}

var detailRE = regexp.MustCompile(`^(\S+) -> vhci port (\d+) \((.*)\)$`)

func detail(busid string, port int, dev Device) string {
	return fmt.Sprintf("%s -> vhci port %d (%04x:%04x)", busid, port, dev.VendorID, dev.ProductID)
}

// attachments merges the kernel's view with the remohop processes that own ports.
func attachments() ([]Attachment, error) {
	ports, err := ListAttached()
	if err != nil {
		return nil, err
	}
	infos, _ := bg.List()
	var out []Attachment
	for _, p := range ports {
		a := Attachment{Port: p.Port, LocalBusID: p.LocalBusID}
		for _, in := range infos {
			if in.Kind != "usb" {
				continue
			}
			if m := detailRE.FindStringSubmatch(in.Detail); m != nil && m[2] == strconv.Itoa(p.Port) {
				a.BusID, a.Target, a.PID, a.Device = m[1], in.Target, in.PID, m[3]
			}
		}
		out = append(out, a)
	}
	return out, nil
}

func runLocal(ctx context.Context, env *cli.Env, args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "detach":
			return runDetach(env, args[1:])
		case "allow":
			return runAllow(env, args[1:])
		case "disallow":
			return runDisallow(env, args[1:])
		case "allowed":
			return runAllowed(env, args[1:])
		case "release":
			return runRelease(env, args[1:])
		case "list":
			args = args[1:]
		case "-h", "--help":
			fmt.Fprintln(env.Stdout, usbUsage)
			return cli.ExitOK
		}
	}
	asJSON := false
	for _, a := range args {
		if !isJSON(a) {
			env.Errorf("usb: unexpected argument %q (remote devices need a target: remohop <ID> usb list)", a)
			return cli.ExitUsage
		}
		asJSON = true
	}
	list, err := attachments()
	if err != nil {
		env.Errorf("usb: %v", err)
		return cli.ExitError
	}
	if asJSON {
		if list == nil {
			list = []Attachment{}
		}
		printJSON(env, list)
		return cli.ExitOK
	}
	if len(list) == 0 {
		fmt.Fprintln(env.Stdout, "no USB devices attached")
		return cli.ExitOK
	}
	fmt.Fprintf(env.Stdout, "%-5s %-10s %-12s %-10s %-8s %s\n", "PORT", "LOCAL", "TARGET", "BUSID", "PID", "DEVICE")
	for _, a := range list {
		fmt.Fprintf(env.Stdout, "%-5d %-10s %-12s %-10s %-8d %s\n", a.Port, cli.Sanitize(a.LocalBusID), cli.Sanitize(a.Target), cli.Sanitize(a.BusID), a.PID, cli.Sanitize(a.Device))
	}
	return cli.ExitOK
}

func runDetach(env *cli.Env, args []string) int {
	if len(args) != 1 {
		env.Errorf("usb detach: need a port number or bus id (see `remohop usb`)")
		return cli.ExitUsage
	}
	list, err := attachments()
	if err != nil {
		env.Errorf("usb detach: %v", err)
		return cli.ExitError
	}
	port := -1
	if n, err := strconv.Atoi(args[0]); err == nil {
		port = n
	}
	var hit *Attachment
	for i, a := range list {
		if a.Port == port || (port < 0 && (a.BusID == args[0] || a.LocalBusID == args[0])) {
			hit = &list[i]
			break
		}
	}
	if hit == nil {
		env.Errorf("usb detach: nothing attached as %q (see `remohop usb`)", cli.Sanitize(args[0]))
		return cli.ExitError
	}
	if hit.PID > 0 {
		// The owning process detaches on the way out.
		if err := bg.Stop(hit.PID); err != nil && !errors.Is(err, bg.ErrNotFound) {
			env.Errorf("usb detach: %v", err)
			return cli.ExitError
		}
	}
	if err := DetachPort(hit.Port); err != nil {
		// Already gone because the owner finished first is fine.
		if still, _ := ListAttached(); containsPort(still, hit.Port) {
			env.Errorf("usb detach: %v", err)
			return cli.ExitError
		}
	}
	fmt.Fprintf(env.Stdout, "detached port %d\n", hit.Port)
	return cli.ExitOK
}

func containsPort(l []AttachedPort, n int) bool {
	for _, p := range l {
		if p.Port == n {
			return true
		}
	}
	return false
}

func runAllow(env *cli.Env, args []string) int {
	force := false
	var ids []string
	for _, a := range args {
		switch {
		case a == "--force":
			force = true
		case strings.HasPrefix(a, "-"):
			env.Errorf("usb allow: unknown flag %q", a)
			return cli.ExitUsage
		default:
			ids = append(ids, a)
		}
	}
	if len(ids) != 1 || !ValidBusID(ids[0]) {
		env.Errorf("usb allow: want one BUSID like 1-2 (see `remohop usb allowed` / the host's lsusb)")
		return cli.ExitUsage
	}
	h := getHooks()
	d, ifs, err := h.sys.device(ids[0])
	if err != nil {
		env.Errorf("usb allow: no USB device %s on this machine", ids[0])
		return cli.ExitError
	}
	if what := carriesRisk(d, ifs); what != "" {
		if what == "hub" {
			env.Errorf("usb allow: %s is a hub; hubs are never exported", ids[0])
			return cli.ExitError
		}
		if !force {
			env.Errorf("usb allow: %s is a %s device: exporting it takes it away from this machine. Use --force if you mean it", ids[0], what)
			return cli.ExitError
		}
	}
	e := AllowEntry{BusID: ids[0], VID: fmt.Sprintf("%04x", d.VendorID), PID: fmt.Sprintf("%04x", d.ProductID)}
	if err := Allow(e); err != nil {
		env.Errorf("usb allow: %v", err)
		return cli.ExitError
	}
	fmt.Fprintf(env.Stdout, "allowed %s (%s:%s): a client can now export it from this machine\n", e.BusID, e.VID, e.PID)
	return cli.ExitOK
}

func runDisallow(env *cli.Env, args []string) int {
	if len(args) != 1 || !ValidBusID(args[0]) {
		env.Errorf("usb disallow: want one BUSID like 1-2")
		return cli.ExitUsage
	}
	was, err := Disallow(args[0])
	if err != nil {
		env.Errorf("usb disallow: %v", err)
		return cli.ExitError
	}
	if !was {
		env.Errorf("usb disallow: %s was not allowed", args[0])
		return cli.ExitError
	}
	fmt.Fprintf(env.Stdout, "no longer allowed: %s\n", args[0])
	return cli.ExitOK
}

func runAllowed(env *cli.Env, args []string) int {
	asJSON := len(args) == 1 && isJSON(args[0])
	if len(args) > 0 && !asJSON {
		env.Errorf("usb allowed: unexpected argument %q", args[0])
		return cli.ExitUsage
	}
	l, err := Allowed()
	if err != nil {
		env.Errorf("usb allowed: %v", err)
		return cli.ExitError
	}
	if asJSON {
		if l == nil {
			l = []AllowEntry{}
		}
		printJSON(env, l)
		return cli.ExitOK
	}
	if len(l) == 0 {
		fmt.Fprintln(env.Stdout, "no USB devices allowed")
	}
	for _, e := range l {
		fmt.Fprintf(env.Stdout, "%-10s %s:%s\n", e.BusID, e.VID, e.PID)
	}
	return cli.ExitOK
}

func runRelease(env *cli.Env, args []string) int {
	if len(args) != 1 {
		env.Errorf("usb release: want one BUSID (devices still on usbip-host: %v)", ExportedBusIDs())
		return cli.ExitUsage
	}
	if err := Release(args[0]); err != nil {
		env.Errorf("usb release: %v", err)
		return cli.ExitError
	}
	fmt.Fprintf(env.Stdout, "released %s to its normal driver\n", args[0])
	return cli.ExitOK
}

// ---- remote ----

func runRemote(ctx context.Context, env *cli.Env, args []string) int {
	if len(args) == 0 {
		env.Errorf("usb: want `list` or `attach BUSID` (remohop <ID> usb list)")
		return cli.ExitUsage
	}
	switch args[0] {
	case "list":
		return runList(ctx, env, args[1:])
	case "attach":
		return runAttach(ctx, env, args[1:])
	case "detach", "allow", "disallow", "allowed", "release":
		env.Errorf("usb %s runs on the machine it concerns, without a target: remohop usb %s ...", args[0], args[0])
		return cli.ExitUsage
	}
	env.Errorf("usb: unknown subcommand %q", args[0])
	return cli.ExitUsage
}

func runList(ctx context.Context, env *cli.Env, args []string) int {
	asJSON := false
	for _, a := range args {
		if !isJSON(a) {
			env.Errorf("usb list: unexpected argument %q", a)
			return cli.ExitUsage
		}
		asJSON = true
	}
	conn, err := env.Conn(ctx)
	if err != nil {
		env.Errorf("%v", err)
		return cli.ExitConnect
	}
	devs, note, err := List(ctx, conn)
	if err != nil {
		env.Errorf("usb list: %v", err)
		return cli.ExitError
	}
	if note != "" {
		env.Errorf("note: %s", note)
	}
	if asJSON {
		if devs == nil {
			devs = []DeviceInfo{}
		}
		printJSON(env, devs)
		return cli.ExitOK
	}
	if len(devs) == 0 {
		fmt.Fprintln(env.Stdout, "no USB devices on the remote machine")
		return cli.ExitOK
	}
	fmt.Fprintf(env.Stdout, "%-9s %-9s %-14s %-8s %-8s %s\n", "BUSID", "VID:PID", "CLASS", "SPEED", "STATE", "DEVICE")
	for _, d := range devs {
		state := "-"
		switch {
		case d.InUse:
			state = "in use"
		case d.Exported:
			state = "exported"
		case d.Allowed:
			state = "allowed"
		}
		name := strings.TrimSpace(d.Manufacturer + " " + d.Product)
		fmt.Fprintf(env.Stdout, "%-9s %-9s %-14s %-8s %-8s %s\n", cli.Sanitize(d.BusID), cli.Sanitize(d.VID+":"+d.PID),
			cli.Sanitize(d.Class), cli.Sanitize(d.Speed), state, cli.Sanitize(name))
	}
	return cli.ExitOK
}

func runAttach(ctx context.Context, env *cli.Env, args []string) int {
	fs := flag.NewFlagSet("usb attach", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	var background, asJSON bool
	fs.BoolVar(&background, "b", false, "")
	fs.BoolVar(&background, "background", false, "")
	fs.BoolVar(&asJSON, "json", false, "")
	fs.Usage = func() { fmt.Fprintln(env.Stderr, usbUsage) }
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
	if len(pos) != 1 || !ValidBusID(pos[0]) {
		env.Errorf("usb attach: want one BUSID like 1-2 (see `remohop <ID> usb list`)")
		return cli.ExitUsage
	}
	busid := pos[0]
	// Fail on this machine's own problems before bothering the host.
	if err := clientCheck(getHooks()); err != nil {
		env.Errorf("usb attach: %v", err)
		return cli.ExitError
	}
	if background && !bg.InChild() {
		msg, pid, err := bg.Start(60 * time.Second)
		if err != nil {
			env.Errorf("usb attach: %v", err)
			return cli.ExitError
		}
		fmt.Fprintln(env.Stdout, msg)
		if !asJSON {
			fmt.Fprintf(env.Stdout, "running in the background as pid %d (detach with `remohop usb detach %s`)\n", pid, busid)
		}
		return cli.ExitOK
	}
	conn, err := env.Conn(ctx)
	if err != nil {
		env.Errorf("%v", err)
		return cli.ExitConnect
	}
	a, err := Attach(ctx, conn, busid)
	if err != nil {
		env.Errorf("usb attach: %v", err)
		return cli.ExitError
	}
	defer a.Close()
	var msg string
	if asJSON {
		b, _ := json.Marshal(map[string]any{"busid": busid, "port": a.Port,
			"vid": fmt.Sprintf("%04x", a.Device.VendorID), "pid": fmt.Sprintf("%04x", a.Device.ProductID)})
		msg = string(b)
	} else {
		msg = fmt.Sprintf("attached %s (%04x:%04x) as vhci port %d", busid, a.Device.VendorID, a.Device.ProductID, a.Port)
	}
	if !bg.InChild() {
		fmt.Fprintln(env.Stdout, msg)
	}
	done := bg.Ready(bg.Info{Kind: "usb", Target: env.Target, Detail: detail(busid, a.Port, a.Device)}, msg)
	defer done()
	select {
	case <-ctx.Done():
		return cli.ExitOK
	case <-a.Done():
		if ctx.Err() != nil {
			return cli.ExitOK
		}
		env.Errorf("usb: device detached (session closed or the host released it)")
		return cli.ExitError
	}
}
