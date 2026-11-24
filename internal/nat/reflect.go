package nat

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"

	"github.com/quic-go/quic-go"
)

const (
	pktProbe       = 0x01
	pktProbeAck    = 0x02
	pktReflectReq  = 0x03
	pktReflectResp = 0x04

	probeLen       = 36
	reflectReqLen  = 40
	reflectRespLen = 36
)

var magic = [3]byte{'S', 'D', 'K'}

func hasHeader(p []byte, typ byte) bool {
	return len(p) >= 4 && p[0] == typ && p[1] == magic[0] && p[2] == magic[1] && p[3] == magic[2]
}

func putHeader(p []byte, typ byte) {
	p[0] = typ
	copy(p[1:4], magic[:])
}

func newReflectReq() (pkt []byte, txid [12]byte) {
	rand.Read(txid[:])
	pkt = make([]byte, reflectReqLen)
	putHeader(pkt, pktReflectReq)
	copy(pkt[4:16], txid[:])
	return pkt, txid
}

// reflection is what a reflect server tells a socket about itself.
type reflection struct {
	// addr is the public address the request arrived from.
	addr netip.AddrPort
	// altPort is the port of the server's second reflect service, or 0.
	// Only a rendezvous server's main port reports one.
	altPort uint16
}

func parseReflectResp(p []byte) (txid [12]byte, r reflection, ok bool) {
	if len(p) != reflectRespLen || !hasHeader(p, pktReflectResp) {
		return txid, r, false
	}
	copy(txid[:], p[4:16])
	port := binary.BigEndian.Uint16(p[16:18])
	ip := netip.AddrFrom16([16]byte(p[18:34])).Unmap()
	r.addr = netip.AddrPortFrom(ip, port)
	r.altPort = binary.BigEndian.Uint16(p[34:36])
	return txid, r, r.addr.IsValid()
}

// reflectResponse builds the answer to req, or returns nil if req is not a
// well-formed request. The answer is always smaller than the request: the
// service is stateless and unauthenticated, and this keeps it useless to
// anyone spoofing source addresses.
func reflectResponse(req []byte, from netip.AddrPort, altPort uint16) []byte {
	if len(req) != reflectReqLen || !hasHeader(req, pktReflectReq) {
		return nil
	}
	resp := make([]byte, reflectRespLen)
	putHeader(resp, pktReflectResp)
	copy(resp[4:16], req[4:16])
	binary.BigEndian.PutUint16(resp[16:18], from.Port())
	ip := from.Addr().As16()
	copy(resp[18:34], ip[:])
	binary.BigEndian.PutUint16(resp[34:36], altPort)
	return resp
}

// ServeReflect answers reflect requests on pc with the source address each
// request arrived from, until pc is closed. A rendezvous server runs it on
// a second UDP port: a peer that compares the address reported there with
// the one reported on the main port learns whether its NAT maps per
// destination.
func ServeReflect(pc net.PacketConn) error {
	buf := make([]byte, 128)
	for {
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return err
		}
		ap, ok := addrPortOf(from)
		if !ok {
			continue
		}
		if resp := reflectResponse(buf[:n], ap, 0); resp != nil {
			pc.WriteTo(resp, from)
		}
	}
}

// ServeReflectQUIC answers reflect requests arriving on a QUIC transport's
// socket (they are not QUIC packets, see the package comment) until ctx
// ends, also telling each requester altPort, the port ServeReflect runs on.
// A rendezvous server runs it on its main port, so a peer can learn its
// public address there, find the second service and measure its NAT without
// a round trip on the rendezvous connection, and before it has asked for
// any introduction.
func ServeReflectQUIC(ctx context.Context, tr *quic.Transport, altPort uint16) error {
	buf := make([]byte, 128)
	for {
		n, from, err := tr.ReadNonQUICPacket(ctx, buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		ap, ok := addrPortOf(from)
		if !ok {
			continue
		}
		if resp := reflectResponse(buf[:n], ap, altPort); resp != nil {
			tr.WriteTo(resp, from)
		}
	}
}
