package nat

import (
	"errors"
	"net"
	"os"
	"syscall"
	"testing"
)

func sendErr(errno syscall.Errno) error {
	return &net.OpError{Op: "write", Net: "udp", Err: os.NewSyscallError("sendmsg", errno)}
}

func TestSoftSendErrors(t *testing.T) {
	var s softSend
	unreach := sendErr(unreachableErrno())
	// Before anything got out, the real error: a dial on a machine without
	// a network says so at once.
	if _, err := s.result(100, 0, unreach); err == nil {
		t.Fatal("transient error swallowed although nothing was ever sent")
	}
	if n, err := s.result(100, 100, nil); n != 100 || err != nil {
		t.Fatalf("success reported as %d, %v", n, err)
	}
	// Then a moment without a route is packet loss.
	if n, err := s.result(100, 0, unreach); n != 100 || err != nil {
		t.Fatalf("no route right after a successful send: %d, %v; want the packet taken as lost", n, err)
	}
	// Anything else still ends the connection, as quic-go decides.
	other := sendErr(syscall.EBADF)
	if _, err := s.result(100, 0, other); !errors.Is(err, syscall.EBADF) {
		t.Fatalf("EBADF: %v", err)
	}
	// After softGrace without a successful send the error is real again.
	s.lastOK.Add(-int64(softGrace) - 1)
	if _, err := s.result(100, 0, unreach); err == nil {
		t.Fatal("transient error swallowed long after the last successful send")
	}
}

// Listen's socket must keep what quic-go needs for batching, GSO, ECN and DF.
func TestListenKeepsUDPCapabilities(t *testing.T) {
	sock, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer sock.Close()
	if _, ok := sock.pc.(interface {
		net.PacketConn
		SyscallConn() (syscall.RawConn, error)
		SetReadBuffer(int) error
		ReadMsgUDP(b, oob []byte) (n, oobn, flags int, addr *net.UDPAddr, err error)
		WriteMsgUDP(b, oob []byte, addr *net.UDPAddr) (n, oobn int, err error)
	}); !ok {
		t.Fatalf("%T is not an OOB-capable packet conn", sock.pc)
	}
	if !transientSendError(sendErr(unreachableErrno())) {
		t.Fatal("network unreachable not recognised")
	}
}
