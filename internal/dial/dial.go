// Package dial is the controlling side of connection establishment: it
// turns a target (9-digit ID, saved alias or host:port) into an
// authenticated *peer.Conn.
//
// For an ID that means: open the shared UDP socket, ask the rendezvous
// server for an introduction, punch through the NATs, establish QUIC on the
// first path that works and run the login handshake. For an address the
// rendezvous and punching steps are skipped; the login is the same.
//
// The package also owns the client's saved peers (pinned host keys, pairing
// tokens, aliases) and the commands around them: pair, peers, ping, config.
package dial

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/readori/RemoHop/internal/account"
	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/config"
	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/nat"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/signal"
	"github.com/readori/RemoHop/internal/tailnet"
	"github.com/readori/RemoHop/internal/wsdgram"
)

// DefaultSignal and DefaultSignalPin name the rendezvous server (and
// optionally its public key) used when the user has configured none. They
// are empty in source and meant to be set by whoever builds a distribution
// with its own server:
//
//	go build -ldflags "-X github.com/readori/RemoHop/internal/dial.DefaultSignal=rendezvous.example.net:21116" ./cmd/remohop
//
// With nothing set and nothing configured, dialling an ID fails with
// instructions instead of contacting a host the user never chose.
//
// DefaultSignalWS adds the server's websocket bridge (`remohop signal
// --ws-listen`), for machines where only HTTPS gets out.
var (
	DefaultSignal    string
	DefaultSignalPin string
	DefaultSignalWS  string
)

func init() {
	// The host daemon resolves its rendezvous server through the same
	// defaults; it cannot import this package, so hand them over.
	if DefaultSignal != "" {
		signal.DefaultAddr, signal.DefaultPin, signal.DefaultWS = DefaultSignal, DefaultSignalPin, DefaultSignalWS
	}
	cli.Dialer = func(ctx context.Context, env *cli.Env) (*peer.Conn, error) {
		return Dial(ctx, env.Target, OptionsFromEnv(env))
	}
}

const (
	signalTimeout = 10 * time.Second
	authTimeout   = 15 * time.Second
	// maxPrompts is how many times an interactive user may mistype.
	maxPrompts = 3
	// approvalWait is how long a login waits once the host has said it is
	// asking its owner; the host gives up first, by default after 30 s.
	approvalWait = 3 * time.Minute
	// maxBusyRetries is how often a login the host had no room for is
	// tried again before giving up.
	maxBusyRetries = 3
)

// directTimeout bounds the QUIC handshake with a host dialled by address.
// A variable so tests need not wait it out.
var directTimeout = 10 * time.Second

// PromptRequest describes a password prompt.
type PromptRequest struct {
	// Target is the host as the user named it, formatted for display.
	Target string
	// Attempt counts prompts for this dial, starting at 1.
	Attempt int
	// Reason says why a prompt is needed again ("wrong password", ...);
	// empty on the first one.
	Reason string
}

// Options configures Dial. The zero value dials with saved credentials
// only, using the rendezvous server from the config file.
type Options struct {
	// Signal overrides the rendezvous server ("host[:port]").
	Signal string
	// Password is the host's password. When set it is the only credential
	// tried.
	Password string
	// Pairing is a pairing token held in memory and not saved (the phone
	// apps get one to log in again after a dropped connection). When set
	// it is tried before the saved credentials; a host that no longer
	// accepts it leaves the saved pairing alone.
	Pairing *hostd.Pairing
	// Prompt asks a human for the password when no other credential
	// works. Nil means never ask: the dial fails with an explanation
	// instead, which is what non-interactive callers want.
	Prompt func(ctx context.Context, req PromptRequest) (string, error)
	// Notify, if set, receives one-line notices about a dial in progress
	// that a person would want to see ("waiting for approval on the
	// host", "connected through a relay").
	Notify func(msg string)
	// Dir is the config directory holding the identity key and saved
	// peers; empty means the default.
	Dir string
	// Logger receives progress at debug level; nil means slog.Default().
	Logger *slog.Logger
	// NoRelay refuses a relayed connection even if the rendezvous server
	// offers one. The "relay" setting and REMOHOP_RELAY=0 do the same.
	NoRelay bool
	// PunchTime overrides how long hole punching may take.
	PunchTime time.Duration
	// SignalWS overrides the rendezvous server's websocket bridge URL and
	// Transport the way it is reached (signal.TransportAuto, UDP or WS);
	// empty means the configuration (signal-ws, REMOHOP_TRANSPORT).
	SignalWS  string
	Transport string

	// PacketConn, if set, is used as the shared socket, and Locals
	// replaces interface enumeration. Tests on a virtual network use both.
	// Listen, if set, opens each socket a dial needs instead (a retried
	// punch needs a fresh one); PacketConn allows no retry.
	PacketConn net.PacketConn
	Listen     func() (net.PacketConn, error)
	Locals     func() []netip.Addr
}

// Path says how the session's packets travel.
type Path string

const (
	// PathLAN: straight to a private address the host listed for itself;
	// both machines are on the same network.
	PathLAN Path = "lan"
	// PathDirect: straight to an address given by the user, or to the
	// host's own public address, without a NAT mapping on the host side.
	PathDirect Path = "direct"
	// PathPunched: through NAT mappings opened by hole punching.
	PathPunched Path = "punched"
	// PathRelayed: no direct path exists; the rendezvous server passes
	// the (end-to-end encrypted) packets between the peers.
	PathRelayed Path = "relayed"
	// PathTailnet: to the host's address on a tailnet both machines are
	// on (Tailscale carries the packets; the session is still RemoHop's
	// own, end to end).
	PathTailnet Path = "tailnet"
)

// Transports, for Info.Transport.
const (
	TransportUDP       = "udp"
	TransportWebSocket = "websocket"
)

// Info describes an established session; see InfoOf.
type Info struct {
	// Target is the canonical target: the 9-digit ID or "host:port".
	Target string
	// Path is how the session reaches the host: directly in every case
	// but PathRelayed.
	Path Path
	// Transport is how this machine's packets travel: TransportUDP, or
	// TransportWebSocket through the rendezvous server's websocket bridge
	// (then Path is always PathRelayed).
	Transport string
	// Local and Remote are the UDP addresses of the session on this
	// machine and of the host as reached.
	Local, Remote netip.AddrPort
	// HostKey is the host's identity key.
	HostKey ed25519.PublicKey
	// Auth is the credential kind the session was authenticated with.
	Auth auth.Kind
	// NewHost is true when this was the first successful login to the
	// host and its key has just been pinned.
	NewHost bool
}

type infoKey struct{}

// InfoOf returns the establishment details of a session made by Dial, or
// nil for any other connection.
func InfoOf(c *peer.Conn) *Info {
	v, _ := c.Value(infoKey{})
	info, _ := v.(*Info)
	return info
}

// Reuse, if set, is asked for an existing session to the (canonical) target
// before a new one is established; returning false falls through to a
// normal dial. It is the seam for a local connection-sharing broker in the
// style of ssh's ControlMaster: the broker process keeps one session per
// host and hands short-lived commands a *peer.Conn that proxies streams to
// it over a local socket (peer.Mux exists for that), so they skip
// rendezvous, punching and login.
var Reuse func(ctx context.Context, target string) (*peer.Conn, bool)

// Dial establishes an authenticated session with target: a 9-digit ID
// (spaces and dashes allowed), an alias saved with `remohop <ID> pair --name` or
// `remohop peers alias`, or a direct "host:port".
//
// Credentials are tried in this order: opts.Password; the pairing token
// saved for the host; opts.Prompt. Closing the returned connection releases
// everything Dial opened.
//
// Errors wrap the layer that failed, so callers can classify them with
// signal.CodeOf (unknown ID, host offline), auth.CodeOf (wrong password,
// locked out), errors.Is(err, nat.ErrNoPath) (hole punching failed),
// errors.Is(err, ErrNoCredential) and errors.Is(err, ErrHostKeyChanged).
func Dial(ctx context.Context, target string, opts Options) (*peer.Conn, error) {
	d, err := newDialer(opts)
	if err != nil {
		return nil, err
	}
	t, err := d.resolve(target)
	if err != nil {
		return nil, err
	}
	if Reuse != nil {
		if c, ok := Reuse(ctx, t.canonical); ok {
			return c, nil
		}
	}
	return d.dial(ctx, t)
}

// explained is an error whose text is written for the user, wrapping the
// underlying error for errors.Is/As without repeating its wording.
type explained struct {
	msg string
	err error
}

func (e *explained) Error() string { return e.msg }
func (e *explained) Unwrap() error { return e.err }

func explain(err error, format string, args ...any) error {
	return &explained{msg: fmt.Sprintf(format, args...), err: err}
}

// ErrNoCredential is returned when Dial has no password, no saved pairing
// and no way to ask.
var ErrNoCredential = errors.New("no credential for this host")

// ErrHostKeyChanged is returned when a host presents a different identity
// key than the one pinned at the first login.
var ErrHostKeyChanged = errors.New("host identity has changed")

type dialer struct {
	opts     Options
	log      *slog.Logger
	dir      string
	store    *Store
	identity *auth.Identity
	natType  nat.Type // first measurement on the dial's socket
	// punchRetried is set once a failed punch has been retried; there
	// is only one retry.
	punchRetried bool
	// usedPacketConn is set once Options.PacketConn has been turned into
	// a socket.
	usedPacketConn bool
	// onWS: the dial runs over the rendezvous server's websocket bridge,
	// on the socket made from wc.
	onWS bool
	wc   *wsdgram.Conn
	// signalAddr and sopts are the rendezvous server, resolved once.
	signalAddr string
	sopts      signal.Options
}

// errUseWS is what establish returns when the rendezvous server did not
// answer over UDP and its websocket bridge is to be tried.
type errUseWS struct{ err error }

func (e *errUseWS) Error() string { return e.err.Error() }
func (e *errUseWS) Unwrap() error { return e.err }

// applyTransport puts the Options' transport overrides into sopts.
func (o Options) applyTransport(sopts *signal.Options) {
	if o.SignalWS != "" {
		sopts.WS = o.SignalWS
	}
	if o.Transport != "" {
		sopts.Transport = o.Transport
	}
}

// errRetryPunch is what establish returns when a punch failed in a way a
// second attempt is likely to fix; dial makes that attempt.
type errRetryPunch struct{ err error }

func (e *errRetryPunch) Error() string { return e.err.Error() }
func (e *errRetryPunch) Unwrap() error { return e.err }

// retryPunchTime bounds the second punch attempt. When priming works the
// punch succeeds within a second; the bound keeps the total time of a dial
// whose punch fails twice near what one long attempt used to take.
const retryPunchTime = 4 * time.Second

// beforePunch, when set (by tests), runs before each punch attempt
// (attempt counts from 1).
var beforePunch func(attempt int)

// newSocket opens the UDP socket a dial attempt runs on.
func (d *dialer) newSocket() (*nat.Socket, error) {
	var sock *nat.Socket
	var err error
	switch {
	case d.onWS:
		if d.wc, err = d.sopts.NewBridge(d.log); err == nil {
			if sock, err = nat.NewSocket(d.wc); err != nil {
				d.wc.Close()
			}
		}
		return sock, err
	case d.opts.Listen != nil:
		var pc net.PacketConn
		if pc, err = d.opts.Listen(); err == nil {
			sock, err = nat.NewSocket(pc)
		}
	case d.opts.PacketConn != nil:
		if d.usedPacketConn {
			return nil, errors.New("dial: Options.PacketConn can serve one socket only")
		}
		d.usedPacketConn = true
		sock, err = nat.NewSocket(d.opts.PacketConn)
	default:
		sock, err = nat.Listen("")
	}
	if err != nil {
		return nil, err
	}
	if d.opts.Locals != nil {
		sock.Locals = d.opts.Locals
	}
	return sock, nil
}

// mayRetryPunch reports whether a punch that found no path is worth one
// more attempt: not already retried, a fresh socket can be had, and
// neither NAT is known to be symmetric. Punches between cone NATs fail
// only by losing a race (see "Who sends first" in internal/nat); with a
// symmetric NAT involved they fail every time.
func (d *dialer) mayRetryPunch(local, remote nat.Type) bool {
	return !d.punchRetried && (d.opts.Listen != nil || d.opts.PacketConn == nil) &&
		local != nat.TypeSymmetric && remote != nat.TypeSymmetric
}

func newDialer(opts Options) (*dialer, error) {
	d := &dialer{opts: opts, log: opts.Logger, dir: opts.Dir}
	if d.log == nil {
		d.log = slog.Default()
	}
	var err error
	if d.dir == "" {
		if d.dir, err = config.Dir(); err != nil {
			return nil, err
		}
	}
	if d.store, err = OpenStore(d.dir); err != nil {
		return nil, err
	}
	return d, nil
}

// target is a resolved dial target.
type target struct {
	canonical string // 9-digit ID or host:port
	id        string // set for rendezvous targets
	display   string // for messages
	saved     Peer   // zero if the host is not in the store
	// accountKey: the host is a device of this machine's account, its key
	// (in saved.HostKey) comes from the account's signed device list, and
	// a key login is worth trying in case its owner turned account-trust
	// on. pinFromAccount: that key is not in the store yet.
	accountKey     bool
	pinFromAccount bool
}

func (d *dialer) resolve(name string) (*target, error) {
	if name == "" {
		return nil, errors.New("no target given")
	}
	name = cli.NormalizeTarget(name)
	if !cli.IsID(name) {
		if p, ok, err := d.store.ByAlias(name); err != nil {
			return nil, err
		} else if ok {
			name = p.Target
		}
	}
	t := &target{canonical: name, display: name}
	if cli.IsID(name) {
		t.id = name
		t.display = signal.FormatID(name)
	} else if host, port, err := net.SplitHostPort(name); err != nil || host == "" || port == "" {
		return nil, fmt.Errorf("unknown target %q: expected a 9-digit ID, a saved alias (see `remohop peers`) or HOST:PORT", name)
	}
	var err error
	if t.saved, _, err = d.store.Get(t.canonical); err != nil {
		return nil, err
	}
	return t, nil
}

// credentials yields the credentials to try, in order, one per call.
type credentials struct {
	d        *dialer
	t        *target
	usedMem  bool
	lastMem  bool // the credential last returned is Options.Pairing
	usedPW   bool
	usedTok  bool
	usedKey  bool
	usedAcct bool
	prompts  int
	reason   string
}

// next returns the next credential to try, or an error when there is none
// left. prompted reports that it came from the user just now.
func (c *credentials) next(ctx context.Context) (cred auth.Credential, prompted bool, err error) {
	opts := &c.d.opts
	c.lastMem = false
	if opts.Pairing != nil && !c.usedMem {
		c.usedMem, c.lastMem = true, true
		return auth.TokenCredential(opts.Pairing.TokenID, opts.Pairing.Token), false, nil
	}
	if opts.Password != "" {
		if c.usedPW {
			return cred, false, errors.New("no more credentials")
		}
		c.usedPW = true
		return auth.PasswordCredential(opts.Password), false, nil
	}
	if c.t.saved.Paired() && !c.usedTok {
		c.usedTok = true
		return auth.TokenCredential(c.t.saved.TokenID, c.t.saved.Token), false, nil
	}
	// The host's owner authorised this machine's key, and we hold the
	// host's key to check who answers: no secret needed.
	if c.t.saved.KeyLogin && c.t.saved.HostKey != "" && !c.usedKey {
		c.usedKey = true
		return auth.KeyCredential(), false, nil
	}
	// The host is a device of our account: it accepts our key if its
	// owner turned account-trust on. Its key is pinned from the account's
	// signed device list, so the key login cannot reach an impostor.
	if c.t.accountKey && c.t.saved.HostKey != "" && !c.usedAcct {
		c.usedAcct = true
		return auth.KeyCredential(), false, nil
	}
	if opts.Prompt == nil {
		why := "no password given and no saved pairing"
		if c.usedTok {
			why = "the saved pairing no longer works and no password was given"
		} else if c.usedKey {
			why = "the host does not accept this machine's key (any more) and no password was given"
		} else if c.usedAcct {
			why = "the host is a device of your account but does not accept the account's devices " +
				"(its owner has not run `remohop config account-trust on` there, or this device is no longer in the account) " +
				"and no password was given"
		}
		return cred, false, fmt.Errorf("%w (%s): %s; pass --password PW, set REMOHOP_PASSWORD, "+
			"or run `remohop %s pair` once from a terminal to stop needing one",
			ErrNoCredential, c.t.display, why, c.t.canonical)
	}
	if c.prompts >= maxPrompts {
		return cred, false, errors.New("no more credentials")
	}
	c.prompts++
	pw, err := opts.Prompt(ctx, PromptRequest{Target: c.t.display, Attempt: c.prompts, Reason: c.reason})
	if err != nil {
		return cred, false, err
	}
	if auth.NormalizePassword(pw) == "" {
		return cred, false, errors.New("empty password")
	}
	return auth.PasswordCredential(pw), true, nil
}

// route is an established way to reach the host: redial opens another QUIC
// connection over it without going through rendezvous again.
type route struct {
	remote netip.AddrPort
	path   Path
	nonce  []byte // of the relay session, for releasing it
}

func (d *dialer) dial(ctx context.Context, t *target) (_ *peer.Conn, err error) {
	if d.identity, err = auth.LoadIdentity(d.dir); err != nil {
		return nil, fmt.Errorf("identity: %w", err)
	}
	d.accountPin(ctx, t)
	creds := &credentials{d: d, t: t}
	// Ask for the password before touching the network when it is certain
	// to be needed: a half-open connection would time out on the host
	// while the user types.
	cred, prompted, err := creds.next(ctx)
	if err != nil {
		return nil, err
	}
	if t.id != "" {
		if d.signalAddr, d.sopts, err = signal.Resolve(d.opts.Signal); err != nil {
			return nil, err
		}
		d.opts.applyTransport(&d.sopts)
		d.onWS = d.sopts.UseWS() && (!d.sopts.UseUDP() || d.signalAddr == "")
	}

	sock, err := d.newSocket()
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			sock.Close()
		}
	}()

	q, rt, err := d.establish(ctx, sock, t)
	if useWS, isWS := errors.AsType[*errUseWS](err); isWS {
		// UDP to the rendezvous server is blocked (or the server is
		// down): go through its websocket bridge instead, which can only
		// end in a relayed session.
		d.log.Debug("rendezvous server not reachable over UDP; trying its websocket bridge", "err", useWS.err)
		sock.Close()
		d.onWS = true
		if sock, err = d.newSocket(); err != nil {
			return nil, err
		}
		q, rt, err = d.establish(ctx, sock, t)
		if err != nil && ctx.Err() == nil {
			err = fmt.Errorf("%w (over UDP: %v)", err, useWS.err)
		}
	}
	if retry, isRetry := errors.AsType[*errRetryPunch](err); isRetry {
		// The punch lost a race it can win on a second try. The second
		// try runs on a fresh socket, so neither NAT holds state from the
		// first one for it, and on a fresh introduction, so the host
		// primes its NAT again.
		d.log.Debug("hole punching failed although both NATs look punchable; trying once more", "target", t.canonical, "err", retry.err)
		sock.Close()
		if sock, err = d.newSocket(); err != nil {
			return nil, err
		}
		d.punchRetried = true
		q, rt, err = d.establish(ctx, sock, t)
	}
	if err != nil {
		return nil, err
	}
	busy := 0
	for {
		loginStart := time.Now()
		info, err := d.login(ctx, q, t, cred)
		if err == nil {
			info.Path, info.Remote = rt.path, rt.remote
			info.Transport = TransportUDP
			if d.onWS && t.id != "" {
				info.Transport = TransportWebSocket
			}
			if la, isUDP := q.LocalAddr().(*net.UDPAddr); isUDP {
				ap := la.AddrPort()
				info.Local = netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
			}
			d.log.Debug("session established", "target", t.canonical, "path", info.Path, "remote", info.Remote, "auth", info.Auth,
				"login", time.Since(loginStart).Round(time.Millisecond).String())
			// A relay session is released when the session ends, so the
			// relay frees it at once: synchronously on Close (before the
			// process may exit), and when the host ends it.
			var releaseOnce sync.Once
			release := func() {
				releaseOnce.Do(func() {
					if rt.path == PathRelayed && rt.nonce != nil {
						sock.ReleaseRelay(rt.remote, rt.nonce, nat.RoleClient)
					}
				})
			}
			conn := peer.NewConn(peer.QUICMux(q), peer.Options{Role: peer.RoleClient, RemoteID: t.canonical, OnClose: release})
			d.markSeen(t)
			conn.SetValue(infoKey{}, info)
			// The socket lives exactly as long as the session. A relay
			// session is released first, so the relay frees it at once.
			context.AfterFunc(conn.Context(), func() {
				release()
				sock.Close()
			})
			ok = true
			return conn, nil
		}
		q.CloseWithError(0, "")

		// Decide whether another credential is worth a new connection.
		code := auth.CodeOf(err)
		switch {
		case cred.Kind == auth.KindToken && creds.lastMem && (code == auth.CodeUnknownToken || code == auth.CodeAuthFailed):
			d.log.Debug("in-memory pairing rejected", "target", t.canonical, "code", code)
			creds.reason = "the pairing held for this session is no longer accepted"
		case cred.Kind == auth.KindToken && (code == auth.CodeUnknownToken || code == auth.CodeAuthFailed):
			d.log.Debug("saved pairing rejected", "target", t.canonical, "code", code)
			if code == auth.CodeUnknownToken {
				// Revoked on the host: the token is dead weight. (A
				// failed confirmation is left alone; it may be an
				// interception attempt rather than a stale token.)
				d.dropToken(t)
			}
			creds.reason = "the saved pairing is no longer accepted"
		case cred.Kind == auth.KindKey && code == auth.CodeUnknownKey && creds.usedAcct:
			creds.reason = "the host is a device of your account but does not accept the account's devices " +
				"(account-trust is off there, or this device is no longer in the account)"
		case cred.Kind == auth.KindKey && code == auth.CodeUnknownKey:
			creds.reason = "the host does not accept this machine's key"
		case prompted && code == auth.CodeAuthFailed:
			creds.reason = "wrong password"
		case code == auth.CodeBusy && busy < maxBusyRetries:
			// The host had no room for this login just now (other
			// logins in progress, or it shed connections under load).
			// Nothing was tried; come back with the same credential.
			busy++
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(busy) * 300 * time.Millisecond):
			}
			if q, err = d.redial(ctx, sock, t, &rt); err != nil {
				return nil, err
			}
			continue
		default:
			return nil, d.loginError(t, cred, err)
		}
		next, nextPrompted, nerr := creds.next(ctx)
		if nerr != nil {
			if errors.Is(nerr, ErrNoCredential) || ctx.Err() != nil {
				return nil, nerr
			}
			return nil, d.loginError(t, cred, err)
		}
		cred, prompted = next, nextPrompted
		if q, err = d.redial(ctx, sock, t, &rt); err != nil {
			return nil, err
		}
	}
}

// accountPin looks the host up in this machine's account, when nothing
// else would log in to it without a password. A device of the account has
// its identity key in the account's signed device list: that key is
// pinned for the login (better than trusting it on first use), and a key
// login is tried first.
func (d *dialer) accountPin(ctx context.Context, t *target) {
	if t.id == "" || t.saved.Paired() || t.saved.KeyLogin || d.opts.Password != "" {
		return
	}
	key, ok := account.HostKey(ctx, account.Options{Dir: d.dir, Logger: d.log}, t.id, true)
	if !ok {
		return
	}
	enc := auth.EncodeKey(key)
	switch t.saved.HostKey {
	case "":
		t.saved.HostKey, t.pinFromAccount = enc, true
	case enc:
	default:
		// The key pinned at an earlier login wins; a mismatch is
		// reported by the login as a changed host key.
		return
	}
	t.accountKey = true
}

// markSeen records that the host was just found online, for hosts this
// machine already knows (a first login has just pinned it, or it was saved
// before); it never creates an entry by itself.
func (d *dialer) markSeen(t *target) {
	if t.saved.HostKey == "" {
		return
	}
	now := time.Now().UTC().Truncate(time.Second)
	d.store.Update(t.canonical, func(p *Peer) error {
		p.LastSeen = &now
		return nil
	})
}

func (d *dialer) notify(msg string) {
	if d.opts.Notify != nil {
		d.opts.Notify(msg)
	}
}

func (d *dialer) clientTLS() (*tls.Config, error) { return d.identity.ClientTLS(peer.ALPN, nil) }

// establish finds a path to the host and returns the first QUIC connection
// over it.
func (d *dialer) establish(ctx context.Context, sock *nat.Socket, t *target) (*quic.Conn, route, error) {
	tlsConf, err := d.clientTLS()
	if err != nil {
		return nil, route{}, err
	}
	if t.id == "" {
		dctx, cancel := context.WithTimeout(ctx, directTimeout)
		defer cancel()
		ua, err := nat.ResolveUDP(dctx, sock.LocalAddr(), t.canonical)
		if err != nil {
			if ctx.Err() != nil {
				return nil, route{}, ctx.Err()
			}
			return nil, route{}, fmt.Errorf("cannot resolve %s: %w", t.canonical, err)
		}
		q, err := sock.Transport().Dial(dctx, ua, tlsConf, peer.QUICConfig())
		if err != nil {
			if ctx.Err() != nil {
				return nil, route{}, ctx.Err()
			}
			return nil, route{}, fmt.Errorf("no remohop host answered at %s: %w", t.canonical, err)
		}
		ap := ua.AddrPort()
		return q, route{remote: netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port()), path: PathDirect}, nil
	}

	addr, sopts := d.signalAddr, d.sopts
	sctx, cancel := context.WithTimeout(ctx, signalTimeout)
	defer cancel()
	var sc *signal.Client
	if d.onWS {
		sc, err = signal.DialBridge(sctx, sock, d.wc, sopts)
		addr = signal.Display(addr, sopts)
	} else {
		uctx, ucancel := context.WithTimeout(sctx, sopts.UDPWait(signalTimeout))
		sc, err = signal.Dial(uctx, sock, addr, sopts)
		ucancel()
		if err != nil && ctx.Err() == nil && sopts.UseWS() && !signal.IsWrongServer(err) {
			return nil, route{}, &errUseWS{err}
		}
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, route{}, ctx.Err()
		}
		return nil, route{}, err
	}
	// The server's part ends with the introduction; it is not kept around
	// for the session.
	defer sc.Close()
	// Our NAT type was measured while the rendezvous connection came up.
	// It goes to the host with the request, decides how long punching is
	// worth trying, and explains a failure. Only the first measurement on
	// a socket counts: once the socket has punched, a NAT may answer
	// differently.
	if d.natType == "" || d.natType == nat.TypeUnknown {
		d.natType = sc.NATType()
	}
	natType := d.natType
	intro, err := sc.Connect(sctx, t.id, sock.Candidates(), natType)
	if err != nil {
		if ctx.Err() != nil {
			return nil, route{}, ctx.Err()
		}
		switch signal.CodeOf(err) {
		case signal.CodeUnknownID:
			return nil, route{}, explain(err, "no host with ID %s is known to the rendezvous server %s (check the ID)", t.display, addr)
		case signal.CodeOffline:
			return nil, route{}, explain(err, "host %s is offline: it is not connected to the rendezvous server %s", t.display, addr)
		case signal.CodeNoAnswer:
			return nil, route{}, explain(err, "host %s is registered with the rendezvous server but did not answer; try again", t.display)
		case signal.CodeDeclined:
			return nil, route{}, fmt.Errorf("host %s declined the connection: %w", t.display, err)
		}
		return nil, route{}, fmt.Errorf("rendezvous with %s failed: %w", t.display, err)
	}
	d.log.Debug("introduced", "target", t.canonical, "host_candidates", intro.Peer.Candidates(), "host_nat", intro.Peer.NAT, "nat", natType, "public_addr", intro.Reflexive,
		"bridged", sc.Bridged(), "host_bridged", intro.Peer.Bridged)
	if sc.Bridged() || intro.Peer.Bridged {
		// Through the websocket bridge (ours or the host's) no punch
		// can work: straight to the relay.
		side := "this machine reaches the rendezvous server only through its websocket bridge"
		if !sc.Bridged() {
			side = "host " + t.display + " reaches the rendezvous server only through its websocket bridge"
		}
		switch {
		case !intro.RelayOffered:
			return nil, route{}, fmt.Errorf("could not connect to %s: %s, and the server does not relay", t.display, side)
		case d.opts.NoRelay || !signal.RelayAllowed(d.opts.Dir):
			return nil, route{}, fmt.Errorf("could not connect to %s: %s, which needs the relay, but relaying is turned off on this machine", t.display, side)
		}
		q, relay, err := d.viaRelay(ctx, sock, sc, intro.Nonce, tlsConf)
		if err != nil {
			if ctx.Err() != nil {
				return nil, route{}, ctx.Err()
			}
			return nil, route{}, fmt.Errorf("could not connect to %s through the rendezvous server's relay (%s): %w", t.display, side, err)
		}
		how := "over its websocket bridge"
		if !sc.Bridged() {
			how = "(the host is on its websocket bridge)"
		}
		d.notify("connected to " + t.display + " through the rendezvous server's relay " + how + " (still end-to-end encrypted, but slower)")
		return q, route{remote: relay, path: PathRelayed, nonce: intro.Nonce}, nil
	}

	// Both machines on the same tailnet: the host's tailnet address is a
	// direct path through any NAT. It is one more candidate, preferred
	// over punched paths and tried before the relay; the session over it
	// is authenticated and encrypted exactly as over any other path.
	prefer := tailnet.Prefer(ctx, intro.Peer.Candidates())
	if prefer != nil {
		d.log.Debug("the host offers an address on this machine's tailnet; preferring it", "target", t.canonical)
	}

	if prefer == nil && !d.punchRetried && intro.RelayOffered && !d.opts.NoRelay && signal.RelayAllowed(d.opts.Dir) &&
		relayHinted(d.dir, t.canonical, intro.Reflexive, intro.Peer.Reflexive) {
		// Punching to this host failed moments ago from these same
		// addresses; it would only cost its timeout again.
		q, relay, err := d.viaRelay(ctx, sock, sc, intro.Nonce, tlsConf)
		if err == nil {
			setRelayHint(d.dir, t.canonical, intro.Reflexive, intro.Peer.Reflexive, true)
			d.notify("connected to " + t.display + " through the rendezvous server's relay: hole punching to it failed minutes ago " +
				"from the same networks (still end-to-end encrypted, but slower)")
			return q, route{remote: relay, path: PathRelayed, nonce: intro.Nonce}, nil
		}
		if ctx.Err() != nil {
			return nil, route{}, ctx.Err()
		}
		// The relay is out of use; forget the hint and punch after all
		// (on a fresh introduction, since this one was spent).
		d.log.Debug("relay hint did not work", "target", t.canonical, "err", err)
		setRelayHint(d.dir, t.canonical, intro.Reflexive, intro.Peer.Reflexive, false)
		return d.establish(ctx, sock, t)
	}

	punchTime := d.opts.PunchTime
	if punchTime == 0 {
		punchTime = nat.PunchTime(natType, intro.Peer.NAT)
		if prefer != nil {
			// The tailnet path does not depend on the NATs; give it
			// time to answer even where punching is hopeless.
			punchTime = max(punchTime, tailnetPunchTime)
		}
	}
	attempt := 1
	if d.punchRetried {
		attempt = 2
		punchTime = min(punchTime, retryPunchTime)
	}
	if beforePunch != nil {
		beforePunch(attempt)
	}
	q, remote, err := sock.Connect(ctx, nat.ConnectConfig{
		Nonce:     intro.Nonce,
		Remote:    intro.Peer.Candidates(),
		TLS:       tlsConf,
		QUIC:      peer.QUICConfig(),
		PunchTime: punchTime,
		Prefer:    prefer,
		Logger:    d.log,
	})
	if err != nil {
		if !errors.Is(err, nat.ErrNoPath) {
			return nil, route{}, err
		}
		noPath := fmt.Errorf("could not connect to %s: %s: %w", t.display, nat.Explain(natType, intro.Peer.NAT), err)
		if ctx.Err() == nil && d.mayRetryPunch(natType, intro.Peer.NAT) {
			return nil, route{}, &errRetryPunch{noPath}
		}
		// No direct path. A relay is used only now, only if the server's
		// operator offers one and neither side has turned it down.
		switch {
		case !intro.RelayOffered:
			return nil, route{}, noPath
		case d.opts.NoRelay || !signal.RelayAllowed(d.opts.Dir):
			return nil, route{}, fmt.Errorf("%w (the rendezvous server offers a relay, but relaying is turned off on this machine)", noPath)
		}
		q, relay, rerr := d.viaRelay(ctx, sock, sc, intro.Nonce, tlsConf)
		if rerr != nil {
			if ctx.Err() != nil {
				return nil, route{}, ctx.Err()
			}
			return nil, route{}, fmt.Errorf("%w; the relay could not be used either: %v", noPath, rerr)
		}
		d.notify("no direct path to " + t.display + ": connected through the rendezvous server's relay (still end-to-end encrypted, but slower)")
		setRelayHint(d.dir, t.canonical, intro.Reflexive, intro.Peer.Reflexive, true)
		return q, route{remote: relay, path: PathRelayed, nonce: intro.Nonce}, nil
	}
	setRelayHint(d.dir, t.canonical, intro.Reflexive, intro.Peer.Reflexive, false)
	path := classify(remote, intro.Peer)
	if prefer != nil && prefer(remote) {
		path = PathTailnet
	}
	return q, route{remote: remote, path: path}, nil
}

// tailnetPunchTime is the least time a punch gets when the host offers a
// tailnet address: Tailscale may first carry the probes through its own
// relays while it sets up a direct path.
const tailnetPunchTime = 3 * time.Second

// viaRelay connects to the host through a relay session on the rendezvous
// server. The connection is the same QUIC and the login that follows the
// same as on a direct path; the relay is only where the packets go.
func (d *dialer) viaRelay(ctx context.Context, sock *nat.Socket, sc *signal.Client, nonce []byte, tlsConf *tls.Config) (*quic.Conn, netip.AddrPort, error) {
	rctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	relay, err := sc.RelayAs(rctx, nonce, d.accountIdentity(sc))
	if err != nil {
		return nil, netip.AddrPort{}, err
	}
	if err := sock.BindRelay(rctx, relay, nonce, nat.RoleClient); err != nil {
		return nil, netip.AddrPort{}, err
	}
	q, err := sock.Transport().Dial(rctx, net.UDPAddrFromAddrPort(relay), tlsConf, peer.QUICConfig())
	if err != nil {
		return nil, netip.AddrPort{}, fmt.Errorf("no answer from the host through the relay: %w", err)
	}
	return q, relay, nil
}

// accountIdentity is the identity to prove in a relay request: only when
// this device is a member of an account on exactly this server (by its
// pinned key), where it may earn a better relay tier. Otherwise the relay
// request stays anonymous.
func (d *dialer) accountIdentity(sc *signal.Client) *auth.Identity {
	st, err := account.Load(d.dir)
	if err != nil || st.Status != account.StatusMember || st.ServerKey == "" || st.ServerKey != auth.EncodeKey(sc.ServerKey()) {
		return nil
	}
	return d.identity
}

// classify names the kind of path a winning candidate represents.
func classify(remote netip.AddrPort, host signal.Peer) Path {
	listed := false
	for _, l := range host.Local {
		listed = listed || l == remote
	}
	switch ip := remote.Addr(); {
	case listed && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()):
		return PathLAN
	case listed:
		// A public address on one of the host's own interfaces.
		return PathDirect
	case ip.IsLoopback():
		return PathLAN
	default:
		return PathPunched
	}
}

// redial opens another QUIC connection to the host for a further login
// attempt. The path found earlier normally still works; if it has gone
// stale (the user took a while to type), the whole establishment is redone.
func (d *dialer) redial(ctx context.Context, sock *nat.Socket, t *target, rt *route) (*quic.Conn, error) {
	tlsConf, err := d.clientTLS()
	if err != nil {
		return nil, err
	}
	qctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	q, err := sock.Transport().Dial(qctx, net.UDPAddrFromAddrPort(rt.remote), tlsConf, peer.QUICConfig())
	cancel()
	if err == nil {
		return q, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	q, fresh, err := d.establish(ctx, sock, t)
	if err != nil {
		return nil, err
	}
	*rt = fresh
	return q, nil
}

// login authenticates one QUIC connection with one credential.
func (d *dialer) login(ctx context.Context, q *quic.Conn, t *target, cred auth.Credential) (*Info, error) {
	cs := q.ConnectionState().TLS
	binding, err := auth.ClientBinding(cs, d.identity.Public())
	if err != nil {
		return nil, err
	}
	// Check the pin before saying anything: a host that is not the one we
	// know gets no handshake message from us at all.
	pinned := t.saved.HostKey != ""
	if pinned {
		want, err := auth.DecodeKey(t.saved.HostKey)
		if err != nil {
			return nil, fmt.Errorf("saved key for %s is corrupt (run `remohop peers forget %s`): %w", t.display, t.canonical, err)
		}
		if !want.Equal(binding.HostKey) {
			return nil, fmt.Errorf("%w: %s now presents key %s but %s was pinned on first login. "+
				"Someone may be impersonating the host. If the host was reinstalled, run `remohop peers forget %s` and connect again",
				ErrHostKeyChanged, t.display, auth.Fingerprint(binding.HostKey), auth.Fingerprint(want), t.canonical)
		}
	}

	actx, cancel := context.WithTimeout(ctx, authTimeout)
	defer cancel()
	st, err := q.OpenStreamSync(actx)
	if err != nil {
		return nil, connError(q, err)
	}
	defer st.Close()
	defer st.CancelRead(0)
	st.SetDeadline(time.Now().Add(authTimeout))
	stop := context.AfterFunc(ctx, func() { st.CancelRead(0); st.CancelWrite(0) })
	defer stop()
	if cred.Kind == auth.KindKey && !pinned {
		// Unreachable through credentials.next, and essential: without a
		// pin a key login would authenticate us to anybody.
		return nil, errors.New("key login needs the host's key to be known first (`remohop peers trust`)")
	}
	hopts := auth.ClientOptions{Name: hostd.DeviceName(), OnWait: func() {
		st.SetDeadline(time.Now().Add(approvalWait))
		d.notify("waiting for approval on the host…")
	}}
	if err := auth.ClientHandshakeWith(st, binding, cred, t.id, hopts); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if auth.CodeOf(err) == "" {
			err = connError(q, err)
		}
		return nil, err
	}

	info := &Info{Target: t.canonical, HostKey: binding.HostKey, Auth: cred.Kind}
	if t.pinFromAccount {
		// The account vouched for this key and the host has now logged
		// us in with it: keep it as the pin.
		t.pinFromAccount = false
		if err := d.store.Update(t.canonical, func(p *Peer) error {
			if p.HostKey == "" {
				p.HostKey = t.saved.HostKey
			}
			return nil
		}); err != nil {
			d.log.Warn("could not save the host's key", "err", err)
		}
	}
	if !pinned {
		// Trust on first use: the host just proved it knows the
		// credential on this very TLS connection, so this key is the
		// host's. From now on a different key is refused before login.
		info.NewHost = true
		key := auth.EncodeKey(binding.HostKey)
		if err := d.store.Update(t.canonical, func(p *Peer) error {
			if p.HostKey == "" {
				p.HostKey = key
			}
			return nil
		}); err != nil {
			d.log.Warn("could not save the host's key", "err", err)
		} else {
			t.saved.HostKey = key
		}
	}
	return info, nil
}

// connError prefers the reason the host closed the connection with over a
// bare stream error. A refusal arrives both as a reply on the login stream
// and in the close frame; when the close wins the race, this recovers it.
func connError(q *quic.Conn, err error) error {
	if e := hostd.LoginRefusal(err); e != nil {
		return e
	}
	cause := context.Cause(q.Context())
	if e := hostd.LoginRefusal(cause); e != nil {
		return e
	}
	// The stream can report the close before the connection's context
	// does, so look in both.
	for _, e := range []error{err, cause} {
		if ae, ok := errors.AsType[*quic.ApplicationError](e); ok && ae.Remote {
			// Text chosen by a host that has not authenticated.
			return fmt.Errorf("the host closed the connection: %s", auth.CleanText(ae.ErrorMessage, 120))
		}
	}
	if cause != nil {
		return cause
	}
	return err
}

func (d *dialer) dropToken(t *target) {
	t.saved.TokenID, t.saved.Token = "", nil
	err := d.store.Update(t.canonical, func(p *Peer) error {
		p.TokenID, p.Token, p.PairedAt = "", nil, nil
		return nil
	})
	if err != nil {
		d.log.Warn("could not remove the rejected pairing", "err", err)
	}
}

// loginError words a final login failure for the user.
func (d *dialer) loginError(t *target, cred auth.Credential, err error) error {
	if errors.Is(err, ErrHostKeyChanged) {
		return err
	}
	switch auth.CodeOf(err) {
	case auth.CodeAuthFailed:
		if cred.Kind == auth.KindToken {
			return explain(err, "login to %s failed: the saved pairing was not accepted "+
				"(re-pair with `remohop --password PW %s pair`, or the connection is being intercepted)", t.display, t.canonical)
		}
		return explain(err, "login to %s failed: wrong password (or the connection is being intercepted)", t.display)
	case auth.CodeLockedOut:
		return fmt.Errorf("login to %s refused: %w", t.display, err)
	case auth.CodeBusy:
		return fmt.Errorf("login to %s did not get through: %w", t.display, err)
	case auth.CodeDenied:
		if cred.Kind == auth.KindToken {
			return explain(err, "login to %s refused: the connection was not approved at the host (denied, or nobody answered in time), "+
				"or the grant this machine redeemed has expired, been revoked or suspended, or has all its sessions in use", t.display)
		}
		return explain(err, "login to %s refused: the connection was not approved at the host (denied, or nobody answered in time)", t.display)
	case auth.CodeUnknownKey:
		if t.accountKey {
			return explain(err, "login to %s failed: it is a device of your account, but it does not accept the account's devices; "+
				"its owner can run `remohop config account-trust on` there, or log in with its password", t.display)
		}
		return explain(err, "login to %s failed: the host has not authorised this machine's key; "+
			"its owner can with `remohop host authorize \"$(remohop key export)\"`", t.display)
	case auth.CodeUnknownToken:
		return explain(err, "login to %s failed: the host no longer accepts the saved pairing; "+
			"connect with the password and run `remohop %s pair` again", t.display, t.canonical)
	}
	return fmt.Errorf("login to %s failed: %w", t.display, err)
}
