package sysinfo

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/readori/RemoHop/internal/cli"
)

const infoUsage = `Usage: remohop <ID> info [--json]

Describe the remote machine: hostname, OS and distribution, kernel, CPU,
memory, uptime and load, the user the host runs as, its working directory,
local time, root filesystem space and whether a display is available.

Run it first when you do not yet know what you are connected to.

Flags:
  --json    print the raw JSON document instead of the summary

Examples:
  remohop 123456789 info
  remohop 123456789 info --json
  remohop 123456789 info --json | jq .mem_total_bytes`

func init() {
	cli.Register(&cli.Command{
		Name:    "info",
		Summary: "describe the remote machine (OS, CPU, memory, user, disk)",
		Usage:   infoUsage,
		Remote:  true,
		Run:     runInfo,
	})
}

func runInfo(ctx context.Context, env *cli.Env, args []string) int {
	fs := cli.NewFlagSet(env, "info", "")
	asJSON := fs.Bool("json", false, "print the raw JSON document")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return cli.ExitOK
		}
		return cli.ExitUsage
	}
	if fs.NArg() > 0 {
		env.Errorf("info: unexpected argument %q (see `remohop help info`)", fs.Arg(0))
		return cli.ExitUsage
	}

	conn, err := env.Conn(ctx)
	if err != nil {
		env.Errorf("%v", err)
		return cli.ExitConnect
	}
	info, err := Get(ctx, conn)
	if err != nil {
		env.Errorf("info: %v", err)
		return cli.ExitError
	}

	if *asJSON {
		enc := json.NewEncoder(env.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(info); err != nil {
			env.Errorf("info: %v", err)
			return cli.ExitError
		}
		return cli.ExitOK
	}
	writeSummary(env.Stdout, info)
	return cli.ExitOK
}

// writeSummary prints the human-readable report. Every string comes from the
// remote host, so it goes through safe before it reaches the terminal.
func writeSummary(w io.Writer, i *Info) {
	rows := [][2]string{
		{"Host", orDash(safe(i.Hostname))},
		{"OS", fmt.Sprintf("%s/%s", safe(i.OS), safe(i.Arch))},
		{"Distro", orDash(strings.TrimSpace(safe(i.DistroName) + " " + safe(i.DistroVersion)))},
		{"Kernel", orDash(safe(i.Kernel))},
		{"CPU", cpuLine(i)},
		{"Memory", memLine(i)},
		{"Uptime", uptimeLine(i)},
		{"Load", loadLine(i)},
		{"User", userLine(i)},
		{"Home", orDash(safe(i.Home))},
		{"Shell", orDash(safe(i.Shell))},
		{"Cwd", orDash(safe(i.Cwd))},
		{"Time", orDash(strings.TrimSpace(safe(i.LocalTime) + " " + safe(i.Timezone)))},
		{"Disk /", diskLine(i)},
		{"Display", yesNo(i.Display)},
		{"remohop", orDash(safe(i.RemoHopVersion))},
	}
	for _, r := range rows {
		fmt.Fprintf(w, "%-9s %s\n", r[0]+":", r[1])
	}
}

func cpuLine(i *Info) string {
	model := safe(i.CPUModel)
	switch {
	case model != "" && i.CPUCores > 0:
		return fmt.Sprintf("%s (%d cores)", model, i.CPUCores)
	case model != "":
		return model
	case i.CPUCores > 0:
		return fmt.Sprintf("%d cores", i.CPUCores)
	}
	return "-"
}

func memLine(i *Info) string {
	if i.MemTotalBytes == 0 {
		return "-"
	}
	return fmt.Sprintf("%s total, %s available", humanBytes(i.MemTotalBytes), humanBytes(i.MemAvailBytes))
}

func uptimeLine(i *Info) string {
	if i.UptimeSeconds <= 0 {
		return "-"
	}
	return humanDuration(i.UptimeSeconds)
}

// loadLine prints "-" for Windows hosts: they have no load average, and
// zeros would read as an idle machine.
func loadLine(i *Info) string {
	if i.OS == "windows" {
		return "-"
	}
	return fmt.Sprintf("%.2f %.2f %.2f (1, 5, 15 min)", i.LoadAverage[0], i.LoadAverage[1], i.LoadAverage[2])
}

func userLine(i *Info) string {
	if i.User == "" {
		return "-"
	}
	if i.UID == "" {
		return safe(i.User)
	}
	return fmt.Sprintf("%s (uid %s)", safe(i.User), safe(i.UID))
}

func diskLine(i *Info) string {
	if i.RootTotalBytes == 0 {
		return "-"
	}
	return fmt.Sprintf("%s free of %s", humanBytes(i.RootFreeBytes), humanBytes(i.RootTotalBytes))
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
