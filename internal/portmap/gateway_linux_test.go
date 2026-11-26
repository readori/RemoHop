package portmap

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"strings"
	"testing"
)

func TestParseProcRoute(t *testing.T) {
	// The kernel prints each address as its 32-bit word, so the digits
	// depend on the machine's byte order.
	word := func(ip string) string {
		a := netip.MustParseAddr(ip).As4()
		return fmt.Sprintf("%08X", binary.NativeEndian.Uint32(a[:]))
	}
	table := "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n" +
		"wg0\t" + word("10.8.0.0") + "\t00000000\t0001\t0\t0\t0\t" + word("255.255.255.0") + "\t0\t0\t0\n" +
		"wlan0\t00000000\t" + word("192.168.1.1") + "\t0003\t0\t0\t600\t00000000\t0\t0\t0\n" +
		"eth0\t00000000\t" + word("10.0.0.138") + "\t0003\t0\t0\t100\t00000000\t0\t0\t0\n" +
		"eth1\t00000000\t" + word("10.9.9.9") + "\t0002\t0\t0\t1\t00000000\t0\t0\t0\n" + // not up
		"eth0\t" + word("10.0.0.0") + "\t00000000\t0001\t0\t0\t100\t" + word("255.255.255.0") + "\t0\t0\t0\n"
	got, err := parseProcRoute(strings.NewReader(table))
	if err != nil || got != netip.MustParseAddr("10.0.0.138") {
		t.Fatalf("parseProcRoute = %s, %v; want the default route with the lowest metric", got, err)
	}
	if _, err := parseProcRoute(strings.NewReader("Iface\tDestination\n")); err == nil {
		t.Error("a table without a default route gave a gateway")
	}
}
