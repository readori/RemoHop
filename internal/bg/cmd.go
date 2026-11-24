package bg

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/readori/RemoHop/internal/cli"
)

func init() {
	cli.Register(&cli.Command{
		Name:    "ps",
		Summary: "list running forwards, mounts and other background commands",
		Usage: `Usage: remohop ps [--json]

List the long-lived remohop commands running on this machine (port forwards,
mounts, clipboard sync), whether started with -b or in another terminal.`,
		Run: runPS,
	})
	cli.Register(&cli.Command{
		Name:    "stop",
		Summary: "stop a background command by PID (or all)",
		Usage: `Usage: remohop stop <PID>... | --all

Stop long-lived remohop commands listed by ` + "`remohop ps`" + `.`,
		Run: runStop,
	})
}

func runPS(ctx context.Context, env *cli.Env, args []string) int {
	asJSON := false
	for _, a := range args {
		if a == "--json" {
			asJSON = true
		} else {
			env.Errorf("ps: unexpected argument %q", a)
			return cli.ExitUsage
		}
	}
	infos, err := List()
	if err != nil {
		env.Errorf("ps: %v", err)
		return cli.ExitError
	}
	if asJSON {
		if infos == nil {
			infos = []Info{}
		}
		enc := json.NewEncoder(env.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(infos)
		return cli.ExitOK
	}
	if len(infos) == 0 {
		fmt.Fprintln(env.Stdout, "no background commands running")
		return cli.ExitOK
	}
	fmt.Fprintf(env.Stdout, "%-8s %-9s %-12s %-10s %s\n", "PID", "KIND", "TARGET", "AGE", "DETAIL")
	for _, in := range infos {
		fmt.Fprintf(env.Stdout, "%-8d %-9s %-12s %-10s %s\n", in.PID, in.Kind, in.Target,
			time.Since(in.Started).Round(time.Second), in.Detail)
	}
	return cli.ExitOK
}

func runStop(ctx context.Context, env *cli.Env, args []string) int {
	if len(args) == 0 {
		env.Errorf("stop: need a PID or --all")
		return cli.ExitUsage
	}
	var pids []int
	if len(args) == 1 && args[0] == "--all" {
		infos, err := List()
		if err != nil {
			env.Errorf("stop: %v", err)
			return cli.ExitError
		}
		for _, in := range infos {
			pids = append(pids, in.PID)
		}
	} else {
		for _, a := range args {
			pid, err := strconv.Atoi(a)
			if err != nil {
				env.Errorf("stop: bad PID %q", a)
				return cli.ExitUsage
			}
			pids = append(pids, pid)
		}
	}
	code := cli.ExitOK
	for _, pid := range pids {
		if err := Stop(pid); err != nil {
			env.Errorf("stop %d: %v", pid, err)
			code = cli.ExitError
			continue
		}
		fmt.Fprintf(env.Stdout, "stopped %d\n", pid)
	}
	return code
}
