// Package wsdgram carries UDP datagrams over a WebSocket, so that a peer in
// a network that lets only HTTPS out (CI runners, cloud containers,
// corporate proxies) can still reach a rendezvous server and its relay.
//
// The client side is a net.PacketConn: the rendezvous and relay code, which
// is QUIC over a UDP socket, runs on it unchanged. End-to-end encryption,
// the PAKE and server pinning are exactly as on UDP; the WebSocket and
// whatever TLS terminates it in front of the server (a Cloudflare Tunnel,
// say) only ever see QUIC ciphertext.
//
// The server end (see internal/signal) forwards each datagram to one of the
// rendezvous server's own UDP ports from a UDP socket of its own per
// WebSocket connection, and never anywhere else: it is not a UDP proxy.
//
// # Wire format
//
// One binary WebSocket message per datagram:
//
//	type(1)  port(2, big endian)  payload
//
// Type 0x01 is a datagram. From client to server, port is the server UDP
// port it is for; from server to client, the port it came from. The
// address part of every datagram is the server itself, so the client side
// presents the server under one placeholder address, ServerIP.
//
// Type 0x02 is the server's hello, its first message: port is 0 and the
// payload holds the rendezvous port and the NAT-probe port (2 bytes each,
// 0 if there is none), so a client needs to know nothing but the URL.
//
// The WebSocket subprotocol is Subprotocol. The client sends the Origin
// OriginValue; the server refuses any other origin, which is what keeps a
// web page from using visitors' browsers to talk to the bridge.
package wsdgram

import (
	"encoding/binary"
	"errors"
	"net/netip"
)

const (
	// Subprotocol is the WebSocket subprotocol both ends require.
	Subprotocol = "remohop-dgram.1"
	// DefaultPath is the URL path the server serves by default.
	DefaultPath = "/remohop/v1/udp"
	// OriginValue is the Origin header remohop clients send. Browsers always
	// send the origin of the page, so a server that insists on this one
	// cannot be driven from a web page.
	OriginValue = "remohop:remohop"

	// MaxDatagram bounds a datagram's payload; QUIC packets are at most
	// about 1500 bytes and the relay drops anything beyond 2048.
	MaxDatagram = 2048
	// MaxMessage bounds a whole WebSocket message.
	MaxMessage = headerLen + MaxDatagram

	msgData  = 0x01
	msgHello = 0x02

	headerLen = 3
)

// ServerIP is the placeholder address under which the client side presents
// the server: datagrams to any port at it go over the WebSocket, and
// datagrams from the server arrive from it. It is TEST-NET-1 (RFC 5737),
// which is never routed, so it cannot be mistaken for a real peer, and a
// peer that is told about it by an older server sends nothing anywhere.
var ServerIP = netip.AddrFrom4([4]byte{192, 0, 2, 1})

// AppendData appends a datagram message to b.
func AppendData(b []byte, port uint16, payload []byte) []byte {
	b = append(b, msgData, byte(port>>8), byte(port))
	return append(b, payload...)
}

// Hello encodes the server's hello.
func Hello(mainPort, altPort uint16) []byte {
	b := []byte{msgHello, 0, 0, 0, 0, 0, 0}
	binary.BigEndian.PutUint16(b[3:], mainPort)
	binary.BigEndian.PutUint16(b[5:], altPort)
	return b
}

// ErrMalformed is returned for a message that is not one of ours.
var ErrMalformed = errors.New("wsdgram: malformed message")

// ParseData splits a datagram message. Messages of any other type, and
// oversized ones, are ErrMalformed.
func ParseData(m []byte) (port uint16, payload []byte, err error) {
	if len(m) < headerLen || len(m) > MaxMessage || m[0] != msgData {
		return 0, nil, ErrMalformed
	}
	return binary.BigEndian.Uint16(m[1:]), m[headerLen:], nil
}

func parseHello(m []byte) (mainPort, altPort uint16, err error) {
	if len(m) < 7 || m[0] != msgHello {
		return 0, 0, ErrMalformed
	}
	mainPort, altPort = binary.BigEndian.Uint16(m[3:]), binary.BigEndian.Uint16(m[5:])
	if mainPort == 0 {
		return 0, 0, ErrMalformed
	}
	return mainPort, altPort, nil
}
