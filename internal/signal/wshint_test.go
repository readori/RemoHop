package signal

import (
	"net/netip"
	"testing"
)

func TestUDPBlockedHint(t *testing.T) {
	home := []netip.Addr{netip.MustParseAddr("192.168.1.5")}
	cafe := []netip.Addr{netip.MustParseAddr("10.20.30.40")}
	for _, dir := range []string{t.TempDir(), ""} {
		const srv = "rendezvous.example:21116"
		if UDPBlocked(dir, srv, home) {
			t.Fatal("hinted before anything was recorded")
		}
		SetUDPBlocked(dir, srv, home, true)
		if !UDPBlocked(dir, srv, home) {
			t.Fatalf("dir %q: hint not remembered", dir)
		}
		if UDPBlocked(dir, srv, cafe) || UDPBlocked(dir, "other.example:21116", home) {
			t.Fatal("hint applied to another network or server")
		}
		SetUDPBlocked(dir, srv, home, false)
		if UDPBlocked(dir, srv, home) {
			t.Fatal("hint not forgotten")
		}
	}
}
