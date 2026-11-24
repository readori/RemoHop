// Package nat gets two peers behind NATs talking directly: it gathers the
// addresses a peer might be reachable at (candidates), discovers how the
// local NAT behaves, and punches UDP holes so that a QUIC connection can be
// established peer to peer. There is no relay; when no direct path exists
// the caller gets ErrNoPath and Explain says why that is likely.
//
// Everything runs on one UDP socket per process (Socket). The same socket
// carries the QUIC connection to the rendezvous server, the punch probes
// and finally the P2P QUIC session, because a NAT mapping belongs to a
// socket: the public address the rendezvous server observed is only useful
// to the peer if the session later leaves from that very socket.
//
// # Sharing the socket with QUIC
//
// quic-go's Transport demultiplexes on the first byte of each datagram: it
// treats a packet as QUIC when bit 0x80 (long header) or bit 0x40 (the QUIC
// "fixed bit") is set and hands everything else to ReadNonQUICPacket. All
// packets defined here therefore start with a type byte below 0x40. They
// are sent with Transport.WriteTo.
//
//	probe / probe-ack (36 bytes)
//	  type(1)=0x01|0x02  magic(3)="SDK"  tag(8)  txid(8)  mac(16)
//	reflect request (40 bytes) / response (36 bytes)
//	  type(1)=0x03  magic(3)  txid(12)  zero padding(24)
//	  type(1)=0x04  magic(3)  txid(12)  port(2)  ip(16)  alt-port(2)
//
// # Punching
//
// The rendezvous server gives both peers the same fresh 32-byte nonce and
// each other's candidates. Both then send probes to every candidate of the
// other side, repeatedly, for a few seconds. The first probes usually die
// at the receiving NAT, but each one opens (or refreshes) the sender's own
// mapping, so that the other side's next probe gets through. A probe is
// answered with a probe-ack to the address it came from; receiving an ack
// proves the path works in both directions, and the client then dials QUIC
// to that address.
//
// tag and mac are derived from the nonce (HMAC-SHA256), so a peer only
// reacts to probes of a session it was told about, and the mac covers the
// sender's role so a probe cannot be bounced back at its sender. This is
// not what authenticates the session (the rendezvous server knows the
// nonce): it only keeps strangers from steering our probes. A valid probe
// from an address that is not among the candidates (peer-reflexive: the
// peer's NAT picked a new port for this destination, as symmetric NATs do)
// is answered and adopted as a further candidate, a bounded number of
// times per session.
//
// # Who sends first
//
// Many NATs keep state for packets they refuse. A Linux router whose own
// input chain accepts unsolicited packets is the common case: a probe that
// arrives at its public address before the machine behind it has sent
// anything to the prober is recorded as a flow to the router itself, and
// when the inside machine then sends its own probe, the router finds the
// obvious translation taken and moves the packet to a different source
// port. A cone NAT has turned symmetric for exactly this peer, and the
// punch is lost.
//
// The host learns about a connection attempt before the client does (the
// rendezvous server asks the host first), so it is the host's probes that
// would arrive early. A host therefore starts by sending its probes to the
// client's public candidates with a very low TTL: enough to cross its own
// NAT and open the mapping there, not enough to reach the client's NAT.
// Normal probes follow once the host has heard from the client, whose own
// first probe has then already opened the client's NAT, or after hostHold
// at the latest. The client just sends. Where a per-packet TTL is not
// available (not Linux, or not a kernel UDP socket) the host sends normal
// probes from the start, as it would have to anyway to open its own NAT.
package nat

import (
	"errors"
	"fmt"
	"time"
)

// Role is the side of a punch session, mixed into probe MACs.
type Role byte

const (
	// RoleClient is the side that will dial QUIC.
	RoleClient Role = 1
	// RoleHost is the side that listens for QUIC.
	RoleHost Role = 2
)

func (r Role) peer() Role {
	if r == RoleClient {
		return RoleHost
	}
	return RoleClient
}

// Type describes a NAT's mapping behaviour, which decides whether hole
// punching can work.
type Type string

const (
	// TypeUnknown: not measured (no second server port, or no answer).
	TypeUnknown Type = "unknown"
	// TypeNone: the socket's public address is a local interface address.
	TypeNone Type = "none"
	// TypeCone: endpoint-independent mapping. The public port is the same
	// whatever the destination, so the address the rendezvous server saw
	// is where the peer can reach us. Punchable.
	TypeCone Type = "cone"
	// TypeSymmetric: the public port depends on the destination, so the
	// peer cannot know which port our packets to it will come from. It
	// only works when the peer accepts packets from unexpected ports.
	TypeSymmetric Type = "symmetric"
)

// ParseType validates a NAT type received from the network.
func ParseType(s string) Type {
	switch t := Type(s); t {
	case TypeNone, TypeCone, TypeSymmetric:
		return t
	}
	return TypeUnknown
}

// ErrNoPath is returned when no candidate answered: the peers could not be
// connected directly.
var ErrNoPath = errors.New("no direct path to the peer")

// Explain turns the two sides' NAT types into the most likely reason a
// punch failed, for the error shown to the user.
func Explain(local, remote Type) string {
	types := fmt.Sprintf("NAT here: %s, NAT at the peer: %s", local, remote)
	switch {
	case local == TypeSymmetric && remote == TypeSymmetric:
		return "both sides are behind symmetric NATs, which cannot be hole-punched (each picks an unpredictable port per destination); " +
			"one side needs a different network, a forwarded UDP port, or IPv6 (" + types + ")"
	case local == TypeSymmetric || remote == TypeSymmetric:
		side := "this side is"
		if remote == TypeSymmetric {
			side = "the peer is"
		}
		return side + " behind a symmetric NAT and the other side's NAT only accepts packets from ports it has sent to (port-restricted), " +
			"a combination that cannot be hole-punched; one side needs a different network, a forwarded UDP port, or IPv6 (" + types + ")"
	case local == TypeUnknown || remote == TypeUnknown:
		return "no probe got through in either direction; a firewall may be blocking UDP, or a NAT on the path is not punchable (" + types + ")"
	default:
		return "no probe got through although both NATs look punchable. Likely causes: a firewall on either side that blocks UDP, " +
			"or a NAT that keeps state for unsolicited inbound packets (some Linux-based routers and VM or container hosts do) " +
			"and then remaps the port for this one peer; retrying sometimes helps, a forwarded UDP port or IPv6 always does (" + types + ")"
	}
}

// PunchTime is how long it is worth probing given what both sides measured.
// Two symmetric NATs cannot be punched, so only a moment is spent on the
// chance that a LAN or IPv6 candidate answers; with one symmetric NAT it
// either works within the first few probes (the other NAT does not filter
// by port) or never.
func PunchTime(local, remote Type) time.Duration {
	switch {
	case local == TypeSymmetric && remote == TypeSymmetric:
		return 1200 * time.Millisecond
	case local == TypeSymmetric || remote == TypeSymmetric:
		return 4 * time.Second
	}
	return DefaultPunchTime
}
