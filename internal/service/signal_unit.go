package service

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// signalMemoryMax caps a rendezvous server service. The server sizes its
// connection limit to it (about 3200 online hosts at 512 MB) and keeps its
// garbage collector inside it, so on a 1 GB machine it can never take
// memory the system needs. `systemctl edit` raises it.
const signalMemoryMax = "512M"

// signalArgs are the arguments after the binary for a rendezvous server.
func (o Options) signalArgs() []string {
	return append([]string{"signal", "--data", stateDir(o)}, o.SignalArgs...)
}

// lowPorts reports whether the server will bind a port below 1024, which
// needs CAP_NET_BIND_SERVICE.
func (o Options) lowPorts() bool {
	for i, a := range o.SignalArgs {
		v := ""
		switch {
		case (a == "--listen" || a == "--alt" || a == "--relay-ports" || a == "--panel") && i+1 < len(o.SignalArgs):
			v = o.SignalArgs[i+1]
		case strings.HasPrefix(a, "--listen=") || strings.HasPrefix(a, "--alt=") || strings.HasPrefix(a, "--relay-ports=") ||
			strings.HasPrefix(a, "--panel="):
			_, v, _ = strings.Cut(a, "=")
		default:
			continue
		}
		port := v
		if _, p, err := net.SplitHostPort(v); err == nil {
			port = p
		}
		port, _, _ = strings.Cut(port, "-")
		if n, err := strconv.Atoi(port); err == nil && n > 0 && n < 1024 {
			return true
		}
	}
	return false
}

// panelListen is the --panel address among the signal arguments, or "".
func (o Options) panelListen() string {
	for i, a := range o.SignalArgs {
		if a == "--panel" && i+1 < len(o.SignalArgs) {
			return o.SignalArgs[i+1]
		}
		if v, ok := strings.CutPrefix(a, "--panel="); ok {
			return v
		}
	}
	return ""
}

// panelEnvFile holds the panel's settings (its password, Cloudflare
// Access): environment variables, never on the command line or in the
// world-readable unit.
func (o Options) panelEnvFile() string { return "/etc/remohop/" + o.name() + "-panel.env" }

// sshForward is the ssh -L argument that reaches a panel on addr.
func sshForward(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return port + ":" + net.JoinHostPort(host, port)
}

// hiddenFromSignal says why the sandbox of the rendezvous server unit
// would hide exe from the service, or "". ProtectHome=yes makes /home,
// /root and /run/user empty for the service and PrivateTmp=yes gives it
// its own /tmp and /var/tmp, so a binary there fails to start with
// status 203/EXEC and nothing but a terse journal line to explain it.
func hiddenFromSignal(exe string) string {
	for _, d := range []string{"/home", "/root", "/run/user", "/tmp", "/var/tmp"} {
		if exe == d || strings.HasPrefix(exe, d+"/") {
			what := "home directories (ProtectHome=yes)"
			if d == "/tmp" || d == "/var/tmp" {
				what = "the system's temporary directories (PrivateTmp=yes)"
			}
			return fmt.Sprintf("the rendezvous server service cannot run %s: its sandbox hides %s. "+
				"Install the binary system-wide first, e.g. `sudo install -m 755 %s /usr/local/bin/remohop`, "+
				"then run `sudo /usr/local/bin/remohop service install --role signal ...` (or pass --exe /usr/local/bin/remohop)",
				exe, what, exe)
		}
	}
	return ""
}

// signalText is the systemd unit of a rendezvous server. It is a network
// daemon that only needs UDP sockets and its state directory, so unlike
// the host it is fully sandboxed: a transient unprivileged user
// (DynamicUser), a read-only system, no home directories, no devices, no
// capabilities (unless it must bind a port below 1024), and memory and
// file-descriptor limits sized for a small VM.
func signalText(o Options, exe string) string {
	words := []string{quote(exe)}
	for _, a := range o.signalArgs() {
		words = append(words, quote(a))
	}
	var u strings.Builder
	u.WriteString("# Written by `remohop service install --role signal`. Customise with `systemctl edit " + o.name() + ".service`.\n")
	u.WriteString("[Unit]\nDescription=RemoHop rendezvous server\nDocumentation=https://github.com/readori/RemoHop\n")
	u.WriteString("After=network-online.target\nWants=network-online.target\n")
	u.WriteString("\n[Service]\nType=simple\n")
	u.WriteString("ExecStart=" + strings.Join(words, " ") + "\n")
	u.WriteString("Restart=on-failure\nRestartSec=5\n")
	// State (server key, ID journal) in /var/lib/NAME, owned by the
	// transient user; REMOHOP_CONFIG_DIR points there so nothing else is
	// written anywhere.
	u.WriteString("DynamicUser=yes\n")
	u.WriteString("StateDirectory=" + o.name() + "\nStateDirectoryMode=0700\n")
	u.WriteString("Environment=REMOHOP_CONFIG_DIR=" + stateDir(o) + "\n")
	if o.panelListen() != "" {
		// The web panel's password and Cloudflare Access settings; the
		// leading "-" lets the service start without the file.
		u.WriteString("EnvironmentFile=-" + o.panelEnvFile() + "\n")
	}
	u.WriteString("UMask=0077\n")
	u.WriteString("NoNewPrivileges=yes\n")
	u.WriteString("ProtectSystem=strict\nProtectHome=yes\nPrivateTmp=yes\nPrivateDevices=yes\n")
	u.WriteString("ProtectKernelTunables=yes\nProtectKernelModules=yes\nProtectKernelLogs=yes\nProtectControlGroups=yes\n")
	u.WriteString("ProtectClock=yes\nProtectHostname=yes\nProtectProc=invisible\n")
	u.WriteString("RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX\n")
	u.WriteString("RestrictNamespaces=yes\nRestrictRealtime=yes\nRestrictSUIDSGID=yes\nLockPersonality=yes\n")
	u.WriteString("MemoryDenyWriteExecute=yes\nSystemCallArchitectures=native\nSystemCallFilter=@system-service\n")
	if o.lowPorts() {
		u.WriteString("CapabilityBoundingSet=CAP_NET_BIND_SERVICE\nAmbientCapabilities=CAP_NET_BIND_SERVICE\n")
	} else {
		u.WriteString("CapabilityBoundingSet=\n")
	}
	u.WriteString("MemoryMax=" + signalMemoryMax + "\n")
	// One descriptor per relayed session plus the server's own.
	u.WriteString("LimitNOFILE=65536\n")
	u.WriteString("\n[Install]\nWantedBy=multi-user.target\n")
	return u.String()
}
