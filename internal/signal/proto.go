// Package signal is RemoHop's rendezvous service: the server behind
// `remohop signal` and the client hosts and dialling peers use to talk to it.
//
// The server does three things and nothing else:
//
//   - it gives every host identity key a stable 9-digit ID,
//   - it knows which hosts are online (a host is online while its QUIC
//     connection to the server is open) and what public address each
//     connection comes from,
//   - on request it introduces a client to a host: both get the other's
//     candidate addresses and a shared random nonce for hole punching.
//
// It never carries session traffic, and it is not trusted: a session is
// authenticated end to end by the PAKE in internal/auth, bound to the TLS
// connection between the peers. The worst a hostile rendezvous server can
// do is refuse service or introduce a client to the wrong machine, which
// then fails that handshake.
//
// # Wire protocol
//
// QUIC with ALPN "remohop-signal/1". Peers connect from the UDP socket they
// will use for the P2P session, which is how the server learns the
// reflexive address of exactly that socket. Each request is its own
// bidirectional stream carrying length-prefixed JSON messages of at most
// maxMsg bytes:
//
//	register (host → server)
//	  → {t:"register", v:1, key}
//	  ← {ok, challenge}
//	  → {sig}                   Ed25519 over label ‖ challenge ‖ TLS exporter
//	  ← {ok, id, you, alt_port}
//
//	connect (client → server)
//	  → {t:"connect", v:1, id, candidates, nat}
//	  ← {ok, nonce, peer:{reflexive, local, nat, via}, you, alt_port}
//
//	offer (server → host, on a stream the server opens)
//	  → {t:"offer", nonce, peer:{reflexive, local, nat, via}}
//	  ← {ok, candidates, nat}
//
//	relay (client → server, after a punch has failed, or at once when
//	either peer is bridged; only if the server was started with relaying
//	on)
//	  → {t:"relay", v:1, nonce [, key, proof]}   the nonce of the introduction;
//	                                     key and proof (Ed25519 over label ‖
//	                                     nonce ‖ TLS exporter) only from a
//	                                     device that wants its account tier
//	  ← {ok, relay_port}
//	and, in between, server → host on a stream the server opens:
//	  → {t:"relay", nonce, relay_port}
//	  ← {ok}
//
//	presence (client → server)
//	  → {t:"presence", v:1, ids:[...]}     at most 64 IDs
//	  ← {ok, presence:["online"|"offline"|"unknown"|"rate_limited", ...]}
//	Each ID costs what a connect request for it would and reveals the
//	same: the per-source and per-network connect budgets are charged per
//	ID, and answers other than "online" draw on the global budget for
//	such answers. When a budget runs out, the remaining IDs are answered
//	"rate_limited". No host is contacted.
//
//	outcome (client → server, after a connection attempt; see outcome.go)
//	  → {t:"outcome", v:1, outcome:{path, ms, nat, peer_nat}}
//	  ← {ok}
//	Older servers answer bad_request. The server keeps only aggregate
//	counts, nothing per report.
//
//	account (client → server; only if the server was started with
//	--accounts, see accounts.go for the operations)
//	  → {t:"account", v:1, op, ...}
//	  ← {ok, account:{...}}     at most maxAccountMsg bytes
//
//	via is "ws" for a peer that reaches the server through its websocket
//	bridge (see ws.go): its addresses are placeholders, and the two
//	peers go straight to the relay instead of punching.
//
//	A peer whose nat is "symmetric" may end its candidates with a port
//	prediction: two addresses on its observed address (nat.Prediction.Hint),
//	which the receiving peer reads back (nat.ReadHint). To the server they
//	are candidates like any other; it needs to know nothing about them.
//
// Failures are {ok:false, code, error, retry_after_ms}. "you" is the
// requester's own reflexive address; alt_port is the UDP port of the
// server's NAT-probe (reflect) service, see internal/nat. The main port
// answers reflect requests as well (they are not QUIC packets), which is how
// a peer measures its NAT while the QUIC handshake is still under way and
// before any introduction is asked for.
package signal

import (
	"crypto/ed25519"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"time"

	"github.com/readori/RemoHop/internal/account/ledger"
	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/nat"
)

const (
	// ALPN identifies the rendezvous protocol in the QUIC handshake.
	ALPN = "remohop-signal/1"
	// DefaultPort is the rendezvous server's UDP port; the NAT-probe
	// service listens on the next one by default.
	DefaultPort = 21116

	protoVersion = 1
	// maxMsg bounds every message in either direction. The largest honest
	// message is a candidate list of a few hundred bytes.
	maxMsg = 2048

	registerLabel  = "remohop-signal-register/1"
	exporterLabel  = "EXPORTER-remohop-signal-register"
	challengeSize  = 32
	maxNATTypeLen  = 16
	maxCandidates  = nat.MaxCandidates + 2
	maxCandidateLn = 64
	maxErrorText   = 120

	// MaxRetryAfter bounds the wait a rendezvous server can ask of us. A
	// server (or someone impersonating it) that names a longer one is cut
	// down to this; otherwise a single forged reply could keep a host
	// away from its rendezvous server indefinitely.
	MaxRetryAfter = 10 * time.Minute
)

// Failure codes.
const (
	CodeUnknownID   = "unknown_id"   // no host has ever registered this ID
	CodeOffline     = "offline"      // the ID exists but its host is not connected
	CodeNoAnswer    = "no_answer"    // the host is connected but did not answer the offer
	CodeDeclined    = "declined"     // the host refused the offer
	CodeRateLimited = "rate_limited" // slow down; see RetryAfter
	CodeFull        = "full"         // the server will not allocate more IDs
	CodeBadRequest  = "bad_request"
	CodeNoRelay     = "no_relay" // relaying is not offered, or the host refuses it
	// CodeIdentityInUse: another machine keeps registering with the same
	// identity key (a cloned disk image, a copied configuration
	// directory); this one waits RetryAfter. Hosts before 1.0.1 log the
	// message and retry with their own backoff (up to a minute).
	CodeIdentityInUse = "identity_in_use"

	// Account failures.
	CodeNoAccounts  = "no_accounts"   // the server does not offer accounts
	CodeNotAllowed  = "not_allowed"   // the server does not take accounts for this address
	CodeBadCode     = "bad_code"      // wrong, expired or used-up login code
	CodeNotLoggedIn = "not_logged_in" // the session is unknown or was revoked
	CodeForbidden   = "forbidden"     // the device is not a member of the account
	CodeConflict    = "conflict"      // the account changed meanwhile; fetch and retry
)

// Error is a failure reported by the rendezvous server.
type Error struct {
	Code       string
	Message    string
	RetryAfter time.Duration
}

func (e *Error) Error() string {
	switch e.Code {
	case CodeUnknownID:
		return "the rendezvous server does not know this ID"
	case CodeOffline:
		return "the host is offline (not connected to the rendezvous server)"
	case CodeNoAnswer:
		return "the host is registered but did not answer"
	case CodeNoAccounts:
		return "this rendezvous server does not offer accounts (its operator can enable them with `remohop signal --accounts`)"
	case CodeBadCode:
		return "wrong or expired login code"
	case CodeNotLoggedIn:
		return "not logged in to the account on the rendezvous server (logged out, removed, or the account was deleted)"
	case CodeForbidden:
		return "this device is not a member of the account (yet)"
	case CodeRateLimited:
		if e.RetryAfter > 0 {
			return fmt.Sprintf("rate limited by the rendezvous server; retry in %s", e.RetryAfter.Round(time.Second))
		}
		return "rate limited by the rendezvous server"
	}
	if e.Message != "" {
		return "rendezvous server: " + e.Message
	}
	return "rendezvous server: " + e.Code
}

// CodeOf returns the failure code of a rendezvous error (one of the Code*
// constants, including CodeIsHost and CodeWrongProtocol), or "".
func CodeOf(err error) string {
	if e, ok := errors.AsType[*Error](err); ok {
		return e.Code
	}
	if e, ok := errors.AsType[*WrongServerError](err); ok {
		return e.Code()
	}
	return ""
}

type request struct {
	T          string   `json:"t"`
	V          int      `json:"v,omitempty"`
	Key        []byte   `json:"key,omitempty"`
	ID         string   `json:"id,omitempty"`
	Candidates []string `json:"candidates,omitempty"`
	NAT        string   `json:"nat,omitempty"`
	// offer only
	Nonce []byte    `json:"nonce,omitempty"`
	Peer  *peerInfo `json:"peer,omitempty"`
	// relay only (server → host)
	RelayPort int `json:"relay_port,omitempty"`
	// presence only
	IDs []string `json:"ids,omitempty"`
	// account only; Key is the device's identity key
	Op       string        `json:"op,omitempty"`
	Email    string        `json:"email,omitempty"`
	Code     string        `json:"code,omitempty"`
	Token    []byte        `json:"token,omitempty"`
	Proof    []byte        `json:"proof,omitempty"`
	Name     string        `json:"name,omitempty"`
	Platform string        `json:"platform,omitempty"`
	Entry    *ledger.Entry `json:"entry,omitempty"`
	Device   []byte        `json:"device,omitempty"`
	// outcome only (outcome.go)
	Outcome *Outcome `json:"outcome,omitempty"`
}

// MaxPresenceIDs bounds the IDs of one presence request.
const MaxPresenceIDs = 64

// Presence states, the same three a connect request can find out.
const (
	PresenceOnline  = "online"
	PresenceOffline = "offline" // the ID exists but its host is not connected
	PresenceUnknown = "unknown" // no host has this ID
	// PresenceLimited: the answer was not given because a rate limit was
	// reached; ask again later.
	PresenceLimited = "rate_limited"
)

type proof struct {
	Sig []byte `json:"sig"`
}

type response struct {
	OK           bool   `json:"ok"`
	Code         string `json:"code,omitempty"`
	Error        string `json:"error,omitempty"`
	RetryAfterMS int64  `json:"retry_after_ms,omitempty"`

	Challenge []byte    `json:"challenge,omitempty"`
	ID        string    `json:"id,omitempty"`
	You       string    `json:"you,omitempty"`
	AltPort   int       `json:"alt_port,omitempty"`
	Nonce     []byte    `json:"nonce,omitempty"`
	Peer      *peerInfo `json:"peer,omitempty"`
	// Relay says the server would relay this introduction on request;
	// RelayPort is the answer to such a request.
	Relay     bool `json:"relay,omitempty"`
	RelayPort int  `json:"relay_port,omitempty"`
	// Presence answers a presence request, ID by ID in request order.
	Presence []string `json:"presence,omitempty"`
	// offer answer only
	Candidates []string `json:"candidates,omitempty"`
	NAT        string   `json:"nat,omitempty"`
	// Account answers an account request.
	Account *accountReply `json:"account,omitempty"`
}

type peerInfo struct {
	Reflexive string   `json:"reflexive"`
	Local     []string `json:"local,omitempty"`
	NAT       string   `json:"nat,omitempty"`
	// Via is "ws" for a peer that reaches the server through its
	// websocket bridge: no address of it can be punched, only the relay
	// reaches it. Reflexive is then a placeholder.
	Via string `json:"via,omitempty"`
}

// err turns a failure received from the server into an Error. The server
// is not trusted: its code is held to the known vocabulary, its text is
// cleaned and bounded, and its retry time is clamped.
func (r *response) err() error {
	if r.OK {
		return nil
	}
	e := &Error{Code: r.Code, Message: auth.CleanText(r.Error, maxErrorText)}
	switch r.Code {
	case CodeUnknownID, CodeOffline, CodeNoAnswer, CodeDeclined, CodeFull, CodeBadRequest, CodeNoRelay,
		CodeNoAccounts, CodeNotAllowed, CodeBadCode, CodeNotLoggedIn, CodeForbidden, CodeConflict:
	case CodeRateLimited, CodeIdentityInUse:
		e.RetryAfter = time.Duration(min(max(r.RetryAfterMS, 0), MaxRetryAfter.Milliseconds())) * time.Millisecond
	default:
		e.Code = CodeBadRequest
	}
	return e
}

func writeMsg(w io.Writer, v any) error { return writeMsgN(w, v, maxMsg) }

func readMsg(r io.Reader, v any) error { return readMsgN(r, v, maxMsg) }

// writeMsgN and readMsgN are writeMsg and readMsg with another bound; only
// answers to account requests, which carry an account's log, use a larger
// one.
func writeMsgN(w io.Writer, v any, limit int) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(body) > limit {
		return errors.New("signal: message too large")
	}
	buf := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(buf, uint32(len(body)))
	copy(buf[4:], body)
	_, err = w.Write(buf)
	return err
}

func readMsgN(r io.Reader, v any, limit int) error {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > uint32(limit) {
		return errors.New("signal: message too large")
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return err
	}
	if err := json.Unmarshal(body, v); err != nil {
		return errors.New("signal: malformed message")
	}
	return nil
}

// registerMessage is what a host signs to prove it holds its identity key.
// The server's random challenge makes the signature fresh; the TLS exporter
// ties it to this connection, so a server cannot replay the proof to
// another rendezvous server to register the host there.
func registerMessage(challenge, exporter []byte) []byte {
	msg := make([]byte, 0, len(registerLabel)+len(challenge)+len(exporter))
	msg = append(msg, registerLabel...)
	msg = append(msg, challenge...)
	return append(msg, exporter...)
}

func verifyRegistration(key ed25519.PublicKey, challenge, exporter, sig []byte) bool {
	return len(key) == ed25519.PublicKeySize && ed25519.Verify(key, registerMessage(challenge, exporter), sig)
}

// parseCandidates validates the addresses a peer reports for itself.
// observed is the address that peer's packets actually come from.
//
// A self-reported address is only passed on if probing it cannot bother a
// stranger much: an address from private, unique-local, link-local or
// carrier-NAT space, or one on the peer's own observed address (the same
// IPv4 address, the same IPv6 /64). Otherwise a peer could name a third
// party and have the other side send it probes.
//
// One exception: a peer that reaches the server over IPv4 may name one
// global IPv6 address, which nothing here can check. Without it IPv6 would
// never be used, because both peers nearly always reach a dual-stack
// rendezvous name over IPv4, and an IPv6 punch between two stateful
// firewalls is the one direct path two carrier-grade or symmetric IPv4 NATs
// leave. The cost is bounded: one address per introduction, introductions
// are rate-limited, and what reaches it is a few dozen 36-byte probes over
// a few seconds (internal/nat bounds the routable addresses a punch probes
// too), never more than the requester itself sent to ask for them.
func parseCandidates(in []string, observed netip.Addr) ([]netip.AddrPort, error) {
	if len(in) > maxCandidates {
		return nil, errors.New("too many candidates")
	}
	observed = observed.Unmap()
	out := make([]netip.AddrPort, 0, len(in))
	unverified6 := 0
	for _, s := range in {
		if len(s) > maxCandidateLn {
			return nil, errors.New("malformed candidate")
		}
		ap, err := netip.ParseAddrPort(s)
		if err != nil || ap.Addr().Zone() != "" {
			return nil, errors.New("malformed candidate")
		}
		ap = netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
		ip := ap.Addr()
		if ap.Port() == 0 || ip.IsUnspecified() || ip.IsMulticast() {
			continue
		}
		switch {
		case sameSite(ip, observed) || (!nat.Routable(ip) && !ip.IsLoopback() && !(ip.Is6() && ip.IsLinkLocalUnicast())):
			out = append(out, ap)
		case ip.Is6() && observed.Is4() && nat.Routable(ip) && unverified6 == 0:
			unverified6++
			out = append(out, ap)
		}
	}
	return out, nil
}

// sameSite reports whether a self-reported address belongs to the place the
// peer was observed at.
func sameSite(ip, observed netip.Addr) bool {
	if !observed.IsValid() || ip.Is4() != observed.Is4() {
		return false
	}
	if ip.Is4() {
		return ip == observed
	}
	a, _ := ip.Prefix(64)
	b, _ := observed.Prefix(64)
	return a == b
}

func formatCandidates(in []netip.AddrPort) []string {
	out := make([]string, 0, len(in))
	for _, ap := range in {
		if len(out) == maxCandidates {
			break
		}
		out = append(out, ap.String())
	}
	return out
}

func cleanNAT(s string) string {
	if len(s) > maxNATTypeLen {
		return string(nat.TypeUnknown)
	}
	return string(nat.ParseType(s))
}

// FormatID renders a 9-digit ID the way it is shown to people: "123 456 789".
func FormatID(id string) string {
	if len(id) != 9 {
		return id
	}
	return id[:3] + " " + id[3:6] + " " + id[6:]
}
