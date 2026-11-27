package signal

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/readori/RemoHop/internal/auth"
)

// Extensions let code built on the rendezvous server (the team server of
// RemoHop Teams) add request types and a say in who gets the relay's
// member tier, without a fork. Register them from init().
//
// An extension request is
//
//	→ {t:"x-NAME", v:1, key?, proof?}       the usual small header
//	→ {ext: PAYLOAD}                         at most MaxExtMsg bytes
//	← {ok, ext: REPLY} or {ok:false, code, error}
//
// With a key, proof is an Ed25519 signature by it over extProofLabel ‖
// type ‖ the TLS exporter of this connection (extExporterLabel), so the
// handler learns a key that is proven for this connection; without one it
// gets none.

const (
	// ExtPrefix starts every extension request type, so that none can
	// collide with a type the server itself has or will have.
	ExtPrefix = "x-"
	// MaxExtMsg bounds an extension payload and its reply.
	MaxExtMsg = 1 << 20

	extProofLabel    = "remohop-signal-ext/1"
	extExporterLabel = "EXPORTER-remohop-signal-ext"
)

// DataDir is the server's data directory (Config.DataDir), where an
// extension keeps its own files, in a subdirectory named after it.
func (s *Server) DataDir() string { return s.cfg.DataDir }

// Logger is the server's logger.
func (s *Server) Logger() *slog.Logger { return s.log }

// ExtRequest is one extension request as its handler sees it.
type ExtRequest struct {
	Type   string
	Source netip.Addr        // the client's address
	Key    ed25519.PublicKey // proven on this connection, or nil
	// Payload is the request's body, as the client sent it.
	Payload json.RawMessage
}

// ExtError is an error a handler returns to give the client a code.
type ExtError struct {
	Code, Msg  string
	RetryAfter time.Duration
}

func (e *ExtError) Error() string { return e.Msg }

// ExtHandler answers an extension request: its reply (marshalled as JSON)
// or an error. It runs on the request's stream, within requestTimeout.
type ExtHandler func(ctx context.Context, s *Server, r ExtRequest) (any, error)

// RelayPolicy gives an extension a say in the relay.
type RelayPolicy struct {
	Name string
	// Member reports whether a proven identity key gets the relay's member
	// tier, as members of an account on this server do (the account rate,
	// and a relay that --relay-for accounts keeps for members). Must be
	// quick.
	Member func(key ed25519.PublicKey) bool
	// Ended is told about every relay session that ran, with the proven
	// keys of its client and host (nil when not proven) and the bytes it
	// carried, for metering. Must not block.
	Ended func(client, host ed25519.PublicKey, bytes int64, d time.Duration)
}

var ext struct {
	mu       sync.Mutex
	handlers map[string]ExtHandler
	relay    []RelayPolicy
}

// RegisterRequest adds the request type t, which must start with
// ExtPrefix. Call it from init().
func RegisterRequest(t string, h ExtHandler) {
	if !strings.HasPrefix(t, ExtPrefix) || len(t) > 64 {
		panic(fmt.Sprintf("signal: extension request type %q must start with %q", t, ExtPrefix))
	}
	ext.mu.Lock()
	defer ext.mu.Unlock()
	if ext.handlers == nil {
		ext.handlers = map[string]ExtHandler{}
	}
	if _, dup := ext.handlers[t]; dup {
		panic("signal: extension request type " + t + " registered twice")
	}
	ext.handlers[t] = h
}

// RegisterRelayPolicy adds a relay policy. Call it from init().
func RegisterRelayPolicy(p RelayPolicy) {
	ext.mu.Lock()
	defer ext.mu.Unlock()
	ext.relay = append(ext.relay, p)
}

func extHandler(t string) (ExtHandler, bool) {
	ext.mu.Lock()
	defer ext.mu.Unlock()
	h, ok := ext.handlers[t]
	return h, ok
}

func relayPolicies() []RelayPolicy {
	ext.mu.Lock()
	defer ext.mu.Unlock()
	return slices.Clone(ext.relay)
}

// extMember reports whether a relay policy gives key the member tier.
func extMember(key []byte) bool {
	if len(key) != ed25519.PublicKeySize {
		return false
	}
	for _, p := range relayPolicies() {
		if p.Member != nil && p.Member(ed25519.PublicKey(key)) {
			return true
		}
	}
	return false
}

// relayEnded tells the relay policies about a finished session.
func relayEnded(client, host []byte, bytes int64, d time.Duration) {
	k := func(b []byte) ed25519.PublicKey {
		if len(b) != ed25519.PublicKeySize {
			return nil
		}
		return ed25519.PublicKey(b)
	}
	for _, p := range relayPolicies() {
		if p.Ended != nil {
			p.Ended(k(client), k(host), bytes, d)
		}
	}
}

func extProofMessage(t string, exporter []byte) []byte {
	msg := make([]byte, 0, len(extProofLabel)+1+len(t)+len(exporter))
	msg = append(msg, extProofLabel...)
	msg = append(msg, 0)
	msg = append(msg, t...)
	return append(msg, exporter...)
}

// extBody carries an extension request's payload or reply.
type extBody struct {
	Ext json.RawMessage `json:"ext,omitempty"`
}

// handleExt serves an extension request.
func (s *Server) handleExt(c *serverConn, st *quic.Stream, req *request, fail failFunc) {
	h, ok := extHandler(req.T)
	if !ok {
		fail(CodeBadRequest, "unknown request", 0)
		return
	}
	r := ExtRequest{Type: req.T, Source: c.real}
	if len(req.Key) > 0 {
		cs := c.q.ConnectionState().TLS
		exp, err := cs.ExportKeyingMaterial(extExporterLabel, nil, 32)
		if err != nil || len(req.Key) != ed25519.PublicKeySize ||
			!ed25519.Verify(ed25519.PublicKey(req.Key), extProofMessage(req.T, exp), req.Proof) {
			fail(CodeBadRequest, "proof of key possession failed", 0)
			return
		}
		r.Key = ed25519.PublicKey(req.Key)
	}
	var body extBody
	if err := readMsgN(st, &body, MaxExtMsg+64); err != nil {
		return
	}
	r.Payload = body.Ext
	ctx, cancel := context.WithTimeout(c.q.Context(), requestTimeout)
	defer cancel()
	reply, err := h(ctx, s, r)
	if err != nil {
		var xe *ExtError
		if errors.As(err, &xe) {
			code := xe.Code
			if code == "" {
				code = CodeBadRequest
			}
			fail(code, xe.Msg, xe.RetryAfter)
			return
		}
		fail(CodeBadRequest, err.Error(), 0)
		return
	}
	b, err := json.Marshal(reply)
	if err != nil || len(b) > MaxExtMsg {
		fail(CodeBadRequest, "internal error", 0)
		return
	}
	writeMsgN(st, extReply{OK: true, Ext: b}, MaxExtMsg+64)
}

// extReply is the answer to an extension request.
type extReply struct {
	OK  bool            `json:"ok"`
	Ext json.RawMessage `json:"ext,omitempty"`
}

// Ext sends the extension request t with payload and returns the reply's
// body. With id, the request carries id's key and a proof for this
// connection; without, it is anonymous.
func (c *Client) Ext(ctx context.Context, t string, id *auth.Identity, payload any) (json.RawMessage, error) {
	if !strings.HasPrefix(t, ExtPrefix) {
		return nil, fmt.Errorf("signal: %q is not an extension request type", t)
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if len(b) > MaxExtMsg {
		return nil, errors.New("signal: extension payload too large")
	}
	req := request{T: t, V: protoVersion}
	if id != nil {
		cs := c.q.ConnectionState().TLS
		exp, err := cs.ExportKeyingMaterial(extExporterLabel, nil, 32)
		if err != nil {
			return nil, err
		}
		req.Key, req.Proof = id.Public(), id.Sign(extProofMessage(t, exp))
	}
	var out json.RawMessage
	err = c.roundTrip(ctx, func(st *quic.Stream) error {
		if err := writeMsg(st, req); err != nil {
			return err
		}
		if err := writeMsgN(st, extBody{Ext: b}, MaxExtMsg+64); err != nil {
			return err
		}
		// The reply is either an extReply or, on failure, a response.
		var raw json.RawMessage
		if err := readMsgN(st, &raw, MaxExtMsg+64); err != nil {
			return err
		}
		var resp response
		if err := json.Unmarshal(raw, &resp); err != nil {
			return err
		}
		if !resp.OK {
			return resp.err()
		}
		var r extReply
		if err := json.Unmarshal(raw, &r); err != nil {
			return err
		}
		out = r.Ext
		return nil
	})
	return out, err
}
