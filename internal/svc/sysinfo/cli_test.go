package sysinfo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/peer/peertest"
)

func TestHumanBytes(t *testing.T) {
	tests := []struct {
		in   uint64
		want string
	}{
		{0, "0 B"},
		{1023, "1023 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{16 << 30, "16.0 GiB"},
		{15<<30 + 639<<20, "15.6 GiB"},
		{1 << 50, "1.0 PiB"},
		{^uint64(0), "16.0 EiB"},
	}
	for _, tc := range tests {
		if got := humanBytes(tc.in); got != tc.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestHumanDuration(t *testing.T) {
	tests := []struct {
		in   int64
		want string
	}{
		{-5, "0s"},
		{0, "0s"},
		{45, "45s"},
		{60, "1m"},
		{12*60 + 30, "12m"},
		{3600, "1h 0m"},
		{4*3600 + 12*60, "4h 12m"},
		{3*86400 + 4*3600 + 12*60 + 59, "3d 4h 12m"},
	}
	for _, tc := range tests {
		if got := humanDuration(tc.in); got != tc.want {
			t.Errorf("humanDuration(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func sampleInfo() *Info {
	return &Info{
		Hostname:       "build-07",
		OS:             "linux",
		Arch:           "amd64",
		DistroName:     "Ubuntu",
		DistroVersion:  "24.04",
		Kernel:         "6.8.0-40-generic",
		CPUModel:       "Intel(R) Xeon(R) CPU",
		CPUCores:       8,
		MemTotalBytes:  16 << 30,
		MemAvailBytes:  10 << 30,
		UptimeSeconds:  3*86400 + 4*3600 + 12*60,
		LoadAverage:    [3]float64{0.52, 0.41, 0.38},
		User:           "root",
		UID:            "0",
		Home:           "/root",
		Shell:          "/bin/bash",
		Cwd:            "/srv/app",
		LocalTime:      "2026-11-19T10:00:00Z",
		Timezone:       "UTC",
		RootTotalBytes: 240 << 30,
		RootFreeBytes:  120 << 30,
		Display:        true,
		RemoHopVersion: "1.2.3",
	}
}

func runInfoCmd(t *testing.T, args []string, dial func(context.Context, *cli.Env) (*peer.Conn, error)) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	env := &cli.Env{
		Target:   "test",
		Stdin:    strings.NewReader(""),
		Stdout:   &out,
		Stderr:   &errb,
		DialFunc: dial,
	}
	code := cli.Lookup("info").Run(context.Background(), env, args)
	return code, out.String(), errb.String()
}

func TestInfoCommandSummary(t *testing.T) {
	client, _ := peertest.Pair(t)
	code, out, errs := runInfoCmd(t, nil, func(context.Context, *cli.Env) (*peer.Conn, error) { return client, nil })
	if code != cli.ExitOK {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
	for _, want := range []string{"Host:", "OS:", "Kernel:", "Memory:", "Uptime:", "Load:", "Disk /:", "remohop:"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("summary contains raw escape sequences:\n%s", out)
	}
}

func TestInfoCommandJSON(t *testing.T) {
	client, _ := peertest.Pair(t)
	code, out, errs := runInfoCmd(t, []string{"--json"}, func(context.Context, *cli.Env) (*peer.Conn, error) { return client, nil })
	if code != cli.ExitOK {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
	var info Info
	if err := json.Unmarshal([]byte(out), &info); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if info.OS == "" || info.Arch == "" {
		t.Errorf("JSON lacks os/arch: %+v", info)
	}
	if !strings.Contains(out, `"mem_total_bytes"`) || !strings.Contains(out, `"load_average"`) {
		t.Errorf("JSON not snake_case as documented:\n%s", out)
	}
}

func TestHumanSummaryFormatting(t *testing.T) {
	var buf bytes.Buffer
	writeSummary(&buf, sampleInfo())
	got := buf.String()
	for _, want := range []string{
		"Host:     build-07",
		"Distro:   Ubuntu 24.04",
		"CPU:      Intel(R) Xeon(R) CPU (8 cores)",
		"Memory:   16.0 GiB total, 10.0 GiB available",
		"Uptime:   3d 4h 12m",
		"Load:     0.52 0.41 0.38 (1, 5, 15 min)",
		"User:     root (uid 0)",
		"Disk /:   120.0 GiB free of 240.0 GiB",
		"Display:  yes",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary lacks %q:\n%s", want, got)
		}
	}
}

func TestHumanSummaryShowsDashForUnknown(t *testing.T) {
	var buf bytes.Buffer
	writeSummary(&buf, &Info{OS: "darwin", Arch: "arm64", CPUCores: 10})
	got := buf.String()
	for _, want := range []string{"Memory:   -", "Uptime:   -", "Disk /:   -", "Kernel:   -", "CPU:      10 cores"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary lacks %q:\n%s", want, got)
		}
	}
}

func TestHumanSummarySanitisesHostStrings(t *testing.T) {
	info := sampleInfo()
	info.Hostname = "pwn\x1b]0;title\x07"
	info.CPUModel = "\x1b[2J"
	var buf bytes.Buffer
	writeSummary(&buf, info)
	if strings.ContainsAny(buf.String(), "\x1b\x07") {
		t.Errorf("summary passes control characters through:\n%q", buf.String())
	}
}

func TestInfoCommandUsageErrors(t *testing.T) {
	dialed := false
	dial := func(context.Context, *cli.Env) (*peer.Conn, error) { dialed = true; return nil, errors.New("unused") }

	if code, _, _ := runInfoCmd(t, []string{"--bogus"}, dial); code != cli.ExitUsage {
		t.Errorf("unknown flag: exit %d, want %d", code, cli.ExitUsage)
	}
	if code, _, errs := runInfoCmd(t, []string{"extra"}, dial); code != cli.ExitUsage || !strings.Contains(errs, "unexpected argument") {
		t.Errorf("extra argument: exit %d, stderr %q", code, errs)
	}
	if code, _, _ := runInfoCmd(t, []string{"-h"}, dial); code != cli.ExitOK {
		t.Errorf("-h: exit %d, want %d", code, cli.ExitOK)
	}
	if dialed {
		t.Error("usage errors must not dial the target")
	}
}

func TestInfoCommandConnectFailure(t *testing.T) {
	code, _, errs := runInfoCmd(t, nil, func(context.Context, *cli.Env) (*peer.Conn, error) {
		return nil, errors.New("no route to host")
	})
	if code != cli.ExitConnect {
		t.Errorf("exit %d, want %d", code, cli.ExitConnect)
	}
	if !strings.Contains(errs, "no route to host") {
		t.Errorf("stderr %q lacks the dial error", errs)
	}
}

// A session whose host has gone away must fail the command, not hang it.
func TestGetFailsWhenSessionEnds(t *testing.T) {
	client, host := peertest.Pair(t)
	host.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := Get(ctx, client); err == nil {
		t.Fatal("Get on a closed session succeeded")
	}
}

func TestHumanSummaryWindowsHasNoLoad(t *testing.T) {
	var buf bytes.Buffer
	writeSummary(&buf, &Info{OS: "windows", Arch: "amd64"})
	if !strings.Contains(buf.String(), "Load:     -\n") {
		t.Errorf("windows summary should print - for load:\n%s", buf.String())
	}
}
