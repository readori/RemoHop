//go:build linux

package nat

import (
	"net"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// The per-packet TTL really is per packet: the control message sets the TTL
// of that one datagram, on IPv4 and on dual-stack sockets, and the socket's
// other packets keep the default.
func TestUDPTTLWriter(t *testing.T) {
	recv, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer recv.Close()
	rc, _ := recv.SyscallConn()
	rc.Control(func(fd uintptr) { err = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_RECVTTL, 1) })
	if err != nil {
		t.Skipf("IP_RECVTTL not available: %v", err)
	}
	to := recv.LocalAddr().(*net.UDPAddr).AddrPort()

	readTTL := func() int {
		t.Helper()
		buf, oob := make([]byte, 64), make([]byte, 128)
		recv.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, oobn, _, _, err := recv.ReadMsgUDP(buf, oob)
		if err != nil {
			t.Fatalf("receive: %v", err)
		}
		msgs, err := unix.ParseSocketControlMessage(oob[:oobn])
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range msgs {
			if m.Header.Level == unix.IPPROTO_IP && m.Header.Type == unix.IP_TTL {
				return int(m.Data[0])
			}
		}
		t.Fatal("no TTL reported for the received packet")
		return 0
	}

	for _, network := range []string{"udp4", "udp"} {
		t.Run(network, func(t *testing.T) {
			var laddr *net.UDPAddr
			if network == "udp4" {
				laddr = &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}
			}
			send, err := net.ListenUDP(network, laddr)
			if err != nil {
				t.Skipf("cannot open a %s socket: %v", network, err)
			}
			defer send.Close()
			write := udpTTLWriter(send)
			if write == nil {
				t.Fatal("no TTL writer for a UDP socket")
			}
			for _, ttl := range []int{2, 3, 17} {
				if err := write([]byte("probe"), to, ttl); err != nil {
					t.Fatalf("write with TTL %d: %v", ttl, err)
				}
				if got := readTTL(); got != ttl {
					t.Fatalf("packet arrived with TTL %d, want %d", got, ttl)
				}
			}
			// An ordinary write afterwards is unaffected.
			if _, err := send.WriteToUDPAddrPort([]byte("normal"), to); err != nil {
				t.Fatal(err)
			}
			if got := readTTL(); got < 30 {
				t.Fatalf("ordinary packet arrived with TTL %d: the socket's TTL was changed", got)
			}
		})
	}

	// Listen's socket is wrapped (softsend.go); priming must still work.
	sock, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer sock.Close()
	if sock.ttl == nil {
		t.Fatal("no TTL writer on a socket from Listen")
	}
	if err := sock.ttl([]byte("probe"), to, 2); err != nil {
		t.Fatal(err)
	}
	if got := readTTL(); got != 2 {
		t.Fatalf("packet from Listen's socket arrived with TTL %d, want 2", got)
	}

	if udpTTLWriter(plainOnly{recv}) != nil {
		t.Error("a TTL writer was returned for something that is not a UDP socket")
	}
}

type plainOnly struct{ net.PacketConn }
