package hostd

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"time"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/wire"
)

// Service names.
const (
	ServicePair = "pair"
	ServicePing = "ping"
)

func init() {
	peer.Handle(ServicePair, servePair)
	peer.Handle(ServicePing, servePing)
}

// PairRequest asks the host to mint a pairing token for the calling client.
type PairRequest struct {
	// Name labels the pairing on the host (`remohop host paired`), typically
	// the client's hostname.
	Name string `json:"name"`
}

// Pairing is a minted token: a credential the client stores and later
// presents instead of the password.
type Pairing struct {
	TokenID string `json:"token_id"`
	Token   []byte `json:"token"`
}

type pairReply struct {
	Error string `json:"error,omitempty"`
	Pairing
}

// servePair: the client sends its request in the stream header; the host
// answers with one JSON message. The token is bound to the identity key the
// client presented on this connection.
func servePair(_ context.Context, c *peer.Conn, s peer.Stream, req json.RawMessage) error {
	refuse := func(msg string) error {
		wire.WriteJSON(s, pairReply{Error: msg})
		return errors.New(msg)
	}
	sess := SessionOf(c)
	if sess == nil {
		return refuse("pairing is not available on this connection")
	}
	if sess.host.cfg.NoPairing {
		return refuse("this host does not allow pairing")
	}
	if sess.grant != "" {
		// A pairing would outlive the grant and carry none of its limits.
		return refuse("pairing is not allowed by grant " + sess.grant)
	}
	var r PairRequest
	if err := peer.DecodeReq(req, &r); err != nil {
		return refuse("malformed request")
	}
	if auth.CleanTokenName(r.Name) == "" {
		r.Name = "key " + auth.Fingerprint(sess.Auth.ClientKey)
	}
	if sess.Auth.Scope != "" {
		return refuse("pairing is not part of a login limited to " + sess.Auth.Scope)
	}
	// A pairing minted on an authorised key's login is recorded as that
	// key's child and goes when the key is revoked (or leaves the account),
	// so that it cannot be used to outlive the revocation.
	tok, err := sess.host.tokens.MintFrom(r.Name, sess.Auth.ClientKey, sess.currentTokenID())
	if errors.Is(err, auth.ErrCredentialGone) {
		return refuse(err.Error())
	}
	if err != nil {
		sess.host.log.Error("pairing failed", "err", err)
		return refuse("the host could not store the pairing")
	}
	// A client that re-pairs from a token session has just had its old
	// token replaced by Mint; the session now stands on the new one.
	sess.setTokenID(tok.ID)
	sess.host.log.Info("paired", "client", tok.Name, "identity", auth.Fingerprint(sess.Auth.ClientKey), "from", sess.Remote)
	return wire.WriteJSON(s, pairReply{Pairing: Pairing{TokenID: tok.ID, Token: tok.Secret}})
}

// Pair asks the host on c for a pairing token. The caller stores it; see
// internal/dial for the command that does.
func Pair(ctx context.Context, c *peer.Conn, req PairRequest) (*Pairing, error) {
	s, err := c.Open(ctx, ServicePair, req)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	stop := context.AfterFunc(ctx, s.Reset)
	defer stop()
	var rep pairReply
	if err := wire.ReadJSONLimit(s, &rep, 1024); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("pair: %w", err)
	}
	if rep.Error != "" {
		return nil, fmt.Errorf("the host refused to pair: %s", auth.CleanText(rep.Error, maxRemoteText))
	}
	// The token id is stored and later shown; hold it to what a host
	// actually mints.
	if !isHex(rep.TokenID) || len(rep.TokenID) > 64 || len(rep.Token) != auth.TokenSize {
		return nil, errors.New("pair: host returned a malformed token")
	}
	return &rep.Pairing, nil
}

// PingInfo is what the host reports when a ping stream opens.
type PingInfo struct {
	// Observed is the client's address as the host sees it on the P2P
	// path: the client's public address when a NAT is in between.
	Observed string `json:"observed"`
	// Version is the host's remohop version.
	Version string `json:"version"`
	// Name is the host's device name (`remohop config name`), if it has one.
	Name string `json:"name,omitempty"`
}

const pingSize = 8

// maxRemoteText bounds free text from the other side that ends up in an
// error message.
const maxRemoteText = 120

func isHex(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// servePing sends PingInfo, then echoes fixed-size probes until the client
// closes the stream.
func servePing(_ context.Context, c *peer.Conn, s peer.Stream, _ json.RawMessage) error {
	if err := wire.WriteJSON(s, PingInfo{Observed: c.RemoteAddr().String(), Version: cli.Version, Name: DeviceName()}); err != nil {
		return err
	}
	var buf [pingSize]byte
	for {
		if _, err := io.ReadFull(s, buf[:]); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if _, err := s.Write(buf[:]); err != nil {
			return err
		}
	}
}

// PingOptions configures Ping.
type PingOptions struct {
	// Count is the number of probes, default 4.
	Count int
	// Interval is the pause between probes, default 200 ms.
	Interval time.Duration
	// OnReply, if set, is called after each probe's reply.
	OnReply func(seq int, rtt time.Duration)
}

// PingResult is the outcome of Ping.
type PingResult struct {
	Info PingInfo
	RTTs []time.Duration
}

// Ping measures round-trip times over the session by echoing small probes
// on one stream. It measures the path the session really uses, including
// QUIC's processing on both ends.
func Ping(ctx context.Context, c *peer.Conn, opts PingOptions) (*PingResult, error) {
	if opts.Count <= 0 {
		opts.Count = 4
	}
	if opts.Interval <= 0 {
		opts.Interval = 200 * time.Millisecond
	}
	s, err := c.Open(ctx, ServicePing, nil)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	stop := context.AfterFunc(ctx, s.Reset)
	defer stop()
	fail := func(err error) (*PingResult, error) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("ping: %w", err)
	}

	res := &PingResult{}
	if err := wire.ReadJSONLimit(s, &res.Info, 1024); err != nil {
		return fail(err)
	}
	// What the host says about us and itself is shown to the user: keep
	// only an address that parses and a short clean version string.
	if ap, err := netip.ParseAddrPort(res.Info.Observed); err == nil {
		res.Info.Observed = ap.String()
	} else {
		res.Info.Observed = ""
	}
	res.Info.Version = auth.CleanText(res.Info.Version, 32)
	res.Info.Name = auth.CleanText(res.Info.Name, auth.MaxTokenName)
	var out, in [pingSize]byte
	for seq := 0; seq < opts.Count; seq++ {
		if seq > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(opts.Interval):
			}
		}
		binary.BigEndian.PutUint64(out[:], uint64(seq))
		start := time.Now()
		if _, err := s.Write(out[:]); err != nil {
			return fail(err)
		}
		if _, err := io.ReadFull(s, in[:]); err != nil {
			return fail(err)
		}
		rtt := time.Since(start)
		if in != out {
			return fail(errors.New("reply does not match probe"))
		}
		res.RTTs = append(res.RTTs, rtt)
		if opts.OnReply != nil {
			opts.OnReply(seq, rtt)
		}
	}
	return res, nil
}
