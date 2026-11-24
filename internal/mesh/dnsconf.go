package mesh

import (
	"bytes"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
)

// System DNS is touched conservatively: by default (net-dns auto) only
// where the system routes one domain to one interface's resolver and drops
// that setting with the interface (systemd-resolved), so nothing can be
// left behind. net-dns hosts writes a clearly marked block into the hosts
// file instead, which `remohop net down` removes and the next start rewrites.

const (
	hostsBegin = "# BEGIN remohop-net (managed by remohop; removed by `remohop net down`)"
	hostsEnd   = "# END remohop-net"
)

func hostsPath() string {
	if p := os.Getenv("REMOHOP_HOSTS_FILE"); p != "" {
		return p
	}
	if runtime.GOOS == "windows" {
		return os.Getenv("SystemRoot") + `\System32\drivers\etc\hosts`
	}
	return "/etc/hosts"
}

// withBlock returns hosts with the mesh block replaced by lines (removed
// when lines is empty).
func withBlock(hosts []byte, lines []string) []byte {
	var out bytes.Buffer
	in := false
	for _, l := range strings.SplitAfter(string(hosts), "\n") {
		t := strings.TrimRight(l, "\r\n")
		switch {
		case t == hostsBegin:
			in = true
		case t == hostsEnd && in:
			in = false
		case !in && l != "":
			out.WriteString(l)
		}
	}
	if len(lines) > 0 {
		if out.Len() > 0 && !bytes.HasSuffix(out.Bytes(), []byte("\n")) {
			out.WriteByte('\n')
		}
		out.WriteString(hostsBegin + "\n")
		for _, l := range lines {
			out.WriteString(l + "\n")
		}
		out.WriteString(hostsEnd + "\n")
	}
	return out.Bytes()
}

func hostsLines(v *view) []string {
	var lines []string
	for name, addrs := range v.names() {
		for _, a := range addrs {
			lines = append(lines, fmt.Sprintf("%s\t%s", a, strings.TrimSuffix(name, ".")))
		}
	}
	sort.Strings(lines)
	return lines
}

// writeHosts puts the block for v into the hosts file (v nil: removes it).
// The file is rewritten in place, not replaced: it is often a bind mount.
func writeHosts(v *view) error {
	p := hostsPath()
	old, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	var lines []string
	if v != nil {
		lines = hostsLines(v)
	}
	next := withBlock(old, lines)
	if bytes.Equal(old, next) {
		return nil
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	if _, err := f.Write(next); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// setupDNS points the system at the mesh's names as net-dns says, and
// returns notes for the user.
func (n *Node) setupDNS(v *view) []string {
	switch n.cfg.dns {
	case DNSOff:
		return []string{fmt.Sprintf("DNS: names like %s are answered at %s:53 only (net-dns off)", v.self.FQDN(), v.self.Addr4)}
	case DNSHosts:
		if err := writeHosts(v); err != nil {
			return []string{fmt.Sprintf("DNS: could not write %s: %v", hostsPath(), err)}
		}
		return []string{"DNS: mesh names are in " + hostsPath()}
	}
	writeHosts(nil) // a block left by an earlier hosts-mode run
	if note, ok := sysSplitDNS(n.dev.Name(), v.self.Addr4); ok || note != "" {
		return []string{note}
	}
	return []string{fmt.Sprintf("DNS: this system has no per-interface DNS that remohop uses (systemd-resolved); names like %s "+
		"are answered at %s:53, or use `remohop config set net-dns hosts`", v.self.FQDN(), v.self.Addr4)}
}

// rewriteHosts keeps the hosts block current when devices come and go.
func (n *Node) rewriteHosts(v *view) {
	if n.cfg.dns == DNSHosts {
		writeHosts(v)
	}
}

func (n *Node) cleanupDNS() {
	if n.cfg.dns == DNSHosts {
		writeHosts(nil)
	}
	// Per-interface settings go with the interface.
}
