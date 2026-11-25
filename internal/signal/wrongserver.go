package signal

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/readori/RemoHop/internal/nat"
)

// Codes of a WrongServerError, as CodeOf returns them. They are stable: the
// web page and the apps translate them.
const (
	// CodeIsHost: the address is a RemoHop host (`remohop host`), not a
	// rendezvous server. Users put a host's address into the rendezvous
	// setting by mistake; the host can be reached by that address directly.
	CodeIsHost = "signal_is_host"
	// CodeWrongProtocol: something answered QUIC at the address but speaks
	// neither the rendezvous nor the host protocol.
	CodeWrongProtocol = "signal_wrong_protocol"
)

// ErrPinMismatch is in the error of a Dial whose server presented another
// key than Options.Pin.
var ErrPinMismatch = errors.New("the rendezvous server's key is not the pinned one")

// peerALPN is internal/peer.ALPN, the protocol a RemoHop host speaks (peer
// imports this package, so it cannot be imported here; a test keeps the two
// equal: TestDialHostAsRendezvousServer).
const peerALPN = "remohop/1"

// WrongServerError is Dial's error when the address answered but is not a
// rendezvous server: its TLS handshake refused the rendezvous protocol
// (QUIC CRYPTO_ERROR 0x178, "no application protocol").
type WrongServerError struct {
	Addr string
	// Host: the address accepted the RemoHop host protocol instead.
	Host bool
	Err  error
}

func (e *WrongServerError) Error() string {
	if e.Host {
		return fmt.Sprintf("%s is a RemoHop host, not a rendezvous server: connect to that host by its address (%s), "+
			"and set a rendezvous server's address in the settings", e.Addr, e.Addr)
	}
	return fmt.Sprintf("%s is not a RemoHop rendezvous server (it does not speak the rendezvous protocol): check the rendezvous server address in the settings", e.Addr)
}

func (e *WrongServerError) Unwrap() error { return e.Err }

// Code is CodeIsHost or CodeWrongProtocol.
func (e *WrongServerError) Code() string {
	if e.Host {
		return CodeIsHost
	}
	return CodeWrongProtocol
}

// noALPN reports whether err is the peer refusing every protocol we offered
// in the TLS handshake (alert 120, no_application_protocol).
func noALPN(err error) bool {
	te, ok := errors.AsType[*quic.TransportError](err)
	return ok && te.Remote && te.ErrorCode == quic.TransportErrorCode(0x100+120)
}

// IsWrongServer reports whether err says the rendezvous address is not a
// rendezvous server (so trying its websocket bridge or again is pointless).
func IsWrongServer(err error) bool {
	_, ok := errors.AsType[*WrongServerError](err)
	return ok
}

// wrongServer builds the error for a server that refused our protocol,
// asking it once (bounded to three seconds) whether it speaks the host
// protocol. That probe only gets as far as the TLS handshake: no login is
// attempted, and the host does not record it.
func wrongServer(ctx context.Context, tr *quic.Transport, ua *net.UDPAddr, addr string, err error) error {
	e := &WrongServerError{Addr: addr, Err: err}
	pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	q, perr := tr.Dial(pctx, ua, &tls.Config{
		NextProtos:         []string{peerALPN},
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, // only asking which protocol it speaks; nothing is sent
	}, &quic.Config{HandshakeIdleTimeout: 3 * time.Second, InitialPacketSize: nat.InitialPacketSize})
	switch {
	case perr == nil:
		e.Host = true
		q.CloseWithError(0, "")
	case noALPN(perr):
	default:
		// A host wants a client certificate, which this probe does not
		// present: it refuses the client only after agreeing on the
		// protocol, with certificate_required (or bad_certificate).
		if te, ok := errors.AsType[*quic.TransportError](perr); ok && te.Remote &&
			(te.ErrorCode == quic.TransportErrorCode(0x100+116) || te.ErrorCode == quic.TransportErrorCode(0x100+42)) {
			e.Host = true
		}
	}
	return e
}
