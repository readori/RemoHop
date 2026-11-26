package portmap

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"time"
)

// PCP (RFC 6887) and NAT-PMP (RFC 6886) share the gateway's UDP port 5351
// and their first byte: PCP is version 2, NAT-PMP version 0. A router that
// speaks only NAT-PMP answers a PCP request with a NAT-PMP "unsupported
// version" reply, which ends the PCP attempt early.

const (
	pcpVersion    = 2
	pcpOpMap      = 1
	pcpResponse   = 0x80
	pcpMapLen     = 60 // header (24) + MAP opcode data (36)
	pcpMaxLen     = 1100
	protoUDP      = 17
	pmpVersion    = 0
	pmpOpExtAddr  = 0
	pmpOpMapUDP   = 1
	pmpResponse   = 128
	pmpExtAddrLen = 12
	pmpMapLen     = 16
)

var pcpResults = [...]string{
	"success", "unsupported version", "not authorized", "malformed request", "unsupported opcode",
	"unsupported option", "malformed option", "network failure", "no resources", "unsupported protocol",
	"user exceeded quota", "cannot provide external address", "address mismatch", "excessive remote peers",
}

func pcpResultText(code byte) string {
	if int(code) < len(pcpResults) {
		return pcpResults[code]
	}
	return fmt.Sprintf("result code %d", code)
}

var pmpResults = [...]string{"success", "unsupported version", "not authorized or refused", "network failure", "out of resources", "unsupported opcode"}

func pmpResultText(code uint16) string {
	if int(code) < len(pmpResults) {
		return pmpResults[code]
	}
	return fmt.Sprintf("result code %d", code)
}

// pcpMapRequest encodes a PCP MAP request for UDP from client (the
// request's source address) for internal port, suggesting extPort on
// extIP (zero values: any).
func pcpMapRequest(client netip.Addr, nonce [12]byte, internal, extPort uint16, extIP netip.Addr, lifetime uint32) []byte {
	b := make([]byte, pcpMapLen)
	b[0] = pcpVersion
	b[1] = pcpOpMap
	binary.BigEndian.PutUint32(b[4:8], lifetime)
	c := client.Unmap().As16() // an IPv4 address goes as ::ffff:a.b.c.d
	copy(b[8:24], c[:])
	copy(b[24:36], nonce[:])
	b[36] = protoUDP
	binary.BigEndian.PutUint16(b[40:42], internal)
	binary.BigEndian.PutUint16(b[42:44], extPort)
	if !extIP.IsValid() {
		extIP = netip.IPv4Unspecified() // ::ffff:0.0.0.0, "an IPv4 address, any"
	}
	e := extIP.Unmap().As16()
	copy(b[44:60], e[:])
	return b
}

type pcpMapResult struct {
	result   byte
	lifetime uint32
	epoch    uint32
	nonce    [12]byte
	proto    byte
	internal uint16
	extPort  uint16
	extIP    netip.Addr
}

// parsePCPMap decodes a PCP MAP response (options after the opcode data
// are ignored).
func parsePCPMap(b []byte) (pcpMapResult, error) {
	if len(b) < pcpMapLen || len(b) > pcpMaxLen || len(b)%4 != 0 {
		return pcpMapResult{}, errors.New("malformed PCP response")
	}
	if b[0] != pcpVersion || b[1] != pcpResponse|pcpOpMap {
		return pcpMapResult{}, errors.New("not a PCP MAP response")
	}
	r := pcpMapResult{
		result:   b[3],
		lifetime: binary.BigEndian.Uint32(b[4:8]),
		epoch:    binary.BigEndian.Uint32(b[8:12]),
		nonce:    [12]byte(b[24:36]),
		proto:    b[36],
		internal: binary.BigEndian.Uint16(b[40:42]),
		extPort:  binary.BigEndian.Uint16(b[42:44]),
		extIP:    netip.AddrFrom16([16]byte(b[44:60])).Unmap(),
	}
	return r, nil
}

// pcpLease is a PCP mapping. A renewal is the same request again, with
// the same nonce (the server's key for the mapping), suggesting what was
// granted.
type pcpLease struct {
	m        *Mapper
	server   netip.AddrPort
	client   netip.Addr
	nonce    [12]byte
	internal uint16
	external netip.AddrPort
}

func (m *Mapper) mapPCP(ctx context.Context, server netip.AddrPort) (Mapping, lease, error) {
	l := &pcpLease{m: m, server: server, internal: m.cfg.Port, external: netip.AddrPortFrom(netip.Addr{}, m.cfg.Port)}
	rand.Read(l.nonce[:])
	cur, err := l.request(ctx, uint32(m.env.lifetime/time.Second))
	if err != nil {
		return Mapping{}, nil, err
	}
	return cur, l, nil
}

func (l *pcpLease) renew(ctx context.Context) (Mapping, error) {
	return l.request(ctx, uint32(l.m.env.lifetime/time.Second))
}

func (l *pcpLease) remove(ctx context.Context) error {
	_, err := l.request(ctx, 0)
	return err
}

func (l *pcpLease) request(ctx context.Context, lifetime uint32) (Mapping, error) {
	c, local, err := dialGateway(ctx, l.server)
	if err != nil {
		return Mapping{}, err
	}
	defer c.Close()
	if !l.client.IsValid() {
		l.client = local
	} else if local != l.client {
		return Mapping{}, fmt.Errorf("this machine's address changed from %s to %s", l.client, local)
	}
	req := pcpMapRequest(l.client, l.nonce, l.internal, l.external.Port(), l.external.Addr(), lifetime)
	resp, err := exchange(ctx, c, req, l.m.env.pmpWait, func(b []byte) bool {
		if len(b) >= 4 && b[0] == pmpVersion {
			return true // a NAT-PMP router saying it does not speak PCP
		}
		r, err := parsePCPMap(b)
		return err == nil && r.nonce == l.nonce
	})
	if err != nil {
		return Mapping{}, err
	}
	if resp[0] == pmpVersion {
		return Mapping{}, errors.New("the gateway speaks only NAT-PMP")
	}
	r, _ := parsePCPMap(resp)
	if r.result != 0 {
		return Mapping{}, fmt.Errorf("gateway refused: %s", pcpResultText(r.result))
	}
	if lifetime == 0 {
		return Mapping{}, nil
	}
	if r.proto != protoUDP || r.internal != l.internal {
		return Mapping{}, errors.New("gateway answered for another mapping")
	}
	if r.lifetime == 0 || r.extPort == 0 || !r.extIP.Is4() || r.extIP.IsUnspecified() {
		return Mapping{}, errors.New("gateway granted an unusable mapping")
	}
	l.external = netip.AddrPortFrom(r.extIP, r.extPort)
	return Mapping{
		Protocol: ProtoPCP,
		External: l.external,
		Internal: netip.AddrPortFrom(l.client, l.internal),
		Lifetime: time.Duration(r.lifetime) * time.Second,
	}, nil
}

// pmpMapRequest encodes a NAT-PMP UDP mapping request.
func pmpMapRequest(internal, extPort uint16, lifetime uint32) []byte {
	b := make([]byte, 12)
	b[0] = pmpVersion
	b[1] = pmpOpMapUDP
	binary.BigEndian.PutUint16(b[4:6], internal)
	binary.BigEndian.PutUint16(b[6:8], extPort)
	binary.BigEndian.PutUint32(b[8:12], lifetime)
	return b
}

// parsePMPExtAddr decodes a NAT-PMP external address response.
func parsePMPExtAddr(b []byte) (result uint16, ip netip.Addr, err error) {
	if len(b) < pmpExtAddrLen || b[0] != pmpVersion || b[1] != pmpResponse|pmpOpExtAddr {
		return 0, netip.Addr{}, errors.New("not a NAT-PMP external address response")
	}
	return binary.BigEndian.Uint16(b[2:4]), netip.AddrFrom4([4]byte(b[8:12])), nil
}

type pmpMapResult struct {
	result   uint16
	epoch    uint32
	internal uint16
	extPort  uint16
	lifetime uint32
}

// parsePMPMap decodes a NAT-PMP UDP mapping response.
func parsePMPMap(b []byte) (pmpMapResult, error) {
	if len(b) < pmpMapLen || b[0] != pmpVersion || b[1] != pmpResponse|pmpOpMapUDP {
		return pmpMapResult{}, errors.New("not a NAT-PMP mapping response")
	}
	return pmpMapResult{
		result:   binary.BigEndian.Uint16(b[2:4]),
		epoch:    binary.BigEndian.Uint32(b[4:8]),
		internal: binary.BigEndian.Uint16(b[8:10]),
		extPort:  binary.BigEndian.Uint16(b[10:12]),
		lifetime: binary.BigEndian.Uint32(b[12:16]),
	}, nil
}

// pmpLease is a NAT-PMP mapping. NAT-PMP has no notion of the client's
// address in the request: the router maps for whoever sent it, so a
// renewal also checks that this machine's address has not changed.
type pmpLease struct {
	m        *Mapper
	server   netip.AddrPort
	client   netip.Addr
	internal uint16
	external netip.AddrPort
}

func (m *Mapper) mapNATPMP(ctx context.Context, server netip.AddrPort) (Mapping, lease, error) {
	l := &pmpLease{m: m, server: server, internal: m.cfg.Port, external: netip.AddrPortFrom(netip.Addr{}, m.cfg.Port)}
	cur, err := l.request(ctx, uint32(m.env.lifetime/time.Second))
	if err != nil {
		return Mapping{}, nil, err
	}
	return cur, l, nil
}

func (l *pmpLease) renew(ctx context.Context) (Mapping, error) {
	return l.request(ctx, uint32(l.m.env.lifetime/time.Second))
}

func (l *pmpLease) remove(ctx context.Context) error {
	_, err := l.request(ctx, 0)
	return err
}

func (l *pmpLease) request(ctx context.Context, lifetime uint32) (Mapping, error) {
	c, local, err := dialGateway(ctx, l.server)
	if err != nil {
		return Mapping{}, err
	}
	defer c.Close()
	if !l.client.IsValid() {
		l.client = local
	} else if local != l.client {
		return Mapping{}, fmt.Errorf("this machine's address changed from %s to %s", l.client, local)
	}
	wait := l.m.env.pmpWait

	var extIP netip.Addr
	if lifetime != 0 {
		resp, err := exchange(ctx, c, []byte{pmpVersion, pmpOpExtAddr}, wait, func(b []byte) bool {
			_, _, err := parsePMPExtAddr(b)
			return err == nil
		})
		if err != nil {
			return Mapping{}, err
		}
		code, ip, _ := parsePMPExtAddr(resp)
		if code != 0 {
			return Mapping{}, fmt.Errorf("gateway refused: %s", pmpResultText(code))
		}
		if ip.IsUnspecified() {
			return Mapping{}, errors.New("the gateway has no public address")
		}
		extIP = ip
	}

	suggest := l.external.Port()
	if lifetime == 0 {
		suggest = 0 // RFC 6886 3.4: a deletion names no external port
	}
	resp, err := exchange(ctx, c, pmpMapRequest(l.internal, suggest, lifetime), wait, func(b []byte) bool {
		r, err := parsePMPMap(b)
		return err == nil && r.internal == l.internal
	})
	if err != nil {
		return Mapping{}, err
	}
	r, _ := parsePMPMap(resp)
	if r.result != 0 {
		return Mapping{}, fmt.Errorf("gateway refused: %s", pmpResultText(r.result))
	}
	if lifetime == 0 {
		return Mapping{}, nil
	}
	if r.extPort == 0 || r.lifetime == 0 {
		return Mapping{}, errors.New("gateway granted an unusable mapping")
	}
	l.external = netip.AddrPortFrom(extIP, r.extPort)
	return Mapping{
		Protocol: ProtoNATPMP,
		External: l.external,
		Internal: netip.AddrPortFrom(l.client, l.internal),
		Lifetime: time.Duration(r.lifetime) * time.Second,
	}, nil
}
