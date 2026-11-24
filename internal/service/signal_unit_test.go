package service

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/readori/RemoHop/internal/cli"
)

func TestSignalUnit(t *testing.T) {
	o := Options{Role: RoleSignal, Exe: "/usr/local/bin/remohop", SignalArgs: []string{"--relay", "--relay-ports", "40000-40099"}}
	if err := o.validate(); err != nil {
		t.Fatal(err)
	}
	text := signalText(o, o.Exe)
	for _, want := range []string{
		"Description=RemoHop rendezvous server",
		"ExecStart=/usr/local/bin/remohop signal --data /var/lib/remohop-signal --relay --relay-ports 40000-40099\n",
		"DynamicUser=yes\n", "StateDirectory=remohop-signal\n", "StateDirectoryMode=0700\n",
		"Environment=REMOHOP_CONFIG_DIR=/var/lib/remohop-signal\n",
		"NoNewPrivileges=yes\n", "ProtectSystem=strict\n", "ProtectHome=yes\n", "PrivateTmp=yes\n",
		"RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX\n", "CapabilityBoundingSet=\n",
		"MemoryMax=512M\n", "LimitNOFILE=65536\n", "Restart=on-failure\n", "WantedBy=multi-user.target\n",
		"After=network-online.target\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("unit lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "AmbientCapabilities") || strings.Contains(text, "\nUser=") {
		t.Errorf("unexpected privilege in unit:\n%s", text)
	}
	// A port below 1024 needs exactly CAP_NET_BIND_SERVICE.
	for _, args := range [][]string{{"--listen", ":443"}, {"--listen=0.0.0.0:53"}, {"--alt", "1.2.3.4:500"}, {"--relay-ports", "500-600"}} {
		low := signalText(Options{Role: RoleSignal, SignalArgs: args}, "/remohop")
		if !strings.Contains(low, "CapabilityBoundingSet=CAP_NET_BIND_SERVICE\nAmbientCapabilities=CAP_NET_BIND_SERVICE\n") {
			t.Errorf("%v: no CAP_NET_BIND_SERVICE:\n%s", args, low)
		}
	}
	// The name decides the unit and state directory.
	named := signalText(Options{Role: RoleSignal, Name: "rdv2"}, "/remohop")
	if !strings.Contains(named, "--data /var/lib/rdv2") || !strings.Contains(named, "StateDirectory=rdv2\n") {
		t.Errorf("named instance:\n%s", named)
	}
}

func TestSignalRoleValidation(t *testing.T) {
	for name, o := range map[string]Options{
		"run-as":        {Role: RoleSignal, RunAs: "nobody"},
		"config-dir":    {Role: RoleSignal, ConfigDir: "/srv/x"},
		"host signal":   {Role: RoleSignal, Signal: "x.example"},
		"linger":        {Role: RoleSignal, Linger: true},
		"data override": {Role: RoleSignal, SignalArgs: []string{"--data", "/tmp"}},
		"data= form":    {Role: RoleSignal, SignalArgs: []string{"--data=/tmp"}},
		"control char":  {Role: RoleSignal, SignalArgs: []string{"--listen", ":1\n[Service]"}},
		"unknown role":  {Role: "relay"},
		"host w/ args":  {SignalArgs: []string{"--relay"}},
	} {
		if err := o.validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if (Options{Role: RoleSignal}).name() != "remohop-signal" || (Options{}).name() != "remohop" {
		t.Error("default names")
	}
}

func TestSignalRoleInstall(t *testing.T) {
	log := fakeBin(t, "systemctl")
	user := newTestSystemd(t, 1000)
	if _, err := user.Install(context.Background(), Options{Role: RoleSignal, Exe: "/usr/bin/remohop"}); err == nil || !strings.Contains(err.Error(), "--system needs root") {
		t.Fatalf("non-root install: %v", err)
	}
	b := newTestSystemd(t, 0)
	if _, err := b.Install(context.Background(), Options{Role: RoleSignal, Scope: ScopeUser, Exe: "/usr/bin/remohop"}); err == nil || !strings.Contains(err.Error(), "system service") {
		t.Fatalf("--user: %v", err)
	}
	inst, err := b.Install(context.Background(), Options{Role: RoleSignal, Exe: "/usr/bin/remohop", SignalArgs: []string{"--relay"}})
	if err != nil {
		t.Fatal(err)
	}
	if inst.Scope != ScopeSystem || inst.Path != filepath.Join(b.systemDir, "remohop-signal.service") || inst.ConfigDir != "/var/lib/remohop-signal" {
		t.Errorf("%+v", inst)
	}
	c := calls(t, log)
	if len(c) < 3 || c[1] != "systemctl enable remohop-signal.service" || c[2] != "systemctl restart remohop-signal.service" {
		t.Errorf("calls %v", c)
	}
	// Status, control and uninstall address it by role.
	if err := b.Control(context.Background(), Options{Role: RoleSignal}, "restart"); err != nil {
		t.Error(err)
	}
	if removed, err := b.Uninstall(context.Background(), Options{Role: RoleSignal}); err != nil || !removed {
		t.Errorf("uninstall: %v %v", removed, err)
	}
}

func TestSignalRoleCLI(t *testing.T) {
	b := &systemd{euid: 0}
	code, out, errOut := runCLI(t, b, "print", "--role", "signal", "--exe", "/usr/bin/remohop", "--listen", ":21116", "--relay", "--relay-ports", "40000-40099", "--", "--relay-kbps", "1000")
	if code != cli.ExitOK || !strings.Contains(out, "ExecStart=/usr/bin/remohop signal --data /var/lib/remohop-signal --listen :21116 --relay-ports 40000-40099 --relay --relay-kbps 1000\n") {
		t.Fatalf("print: %d\n%s%s", code, out, errOut)
	}
	if code, _, errOut := runCLI(t, b, "print", "--listen", ":1"); code != cli.ExitUsage || !strings.Contains(errOut, "--role signal") {
		t.Errorf("signal flags without the role: %d %q", code, errOut)
	}
	if code, _, _ := runCLI(t, b, "print", "--role", "signal", "--", "--data", "/x"); code != cli.ExitUsage {
		t.Error("--data passed through")
	}
	if code, _, _ := runCLI(t, b, "status", "--role", "bogus"); code != cli.ExitUsage {
		t.Error("unknown role accepted by status")
	}
	if !strings.Contains(usage, "--role signal") || !strings.Contains(usage, "MemoryMax=512M") {
		t.Error("usage does not describe the signal role")
	}
}

func TestSignalExeHiddenBySandbox(t *testing.T) {
	for _, exe := range []string{"/home/runner/work/_temp/remohop", "/root/go/bin/remohop", "/tmp/remohop", "/var/tmp/x/remohop", "/run/user/1000/remohop"} {
		if hiddenFromSignal(exe) == "" {
			t.Errorf("%s: not reported as hidden from the sandboxed unit", exe)
		}
	}
	for _, exe := range []string{"/usr/local/bin/remohop", "/usr/bin/remohop", "/opt/remohop/remohop", "/homestead/remohop", "/tmpfs/remohop"} {
		if why := hiddenFromSignal(exe); why != "" {
			t.Errorf("%s: wrongly refused: %s", exe, why)
		}
	}
}
