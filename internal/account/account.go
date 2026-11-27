// Package account is the client side of RemoHop accounts: `remohop login`,
// `remohop logout`, `remohop account`, the account part of `remohop devices`, and the
// host-side "account-trust" that lets a host accept the account's other
// devices. The server side is in internal/signal (accounts are an optional
// service of a rendezvous server); the signed membership log both sides
// verify is internal/account/ledger.
//
// # What an account is
//
// An account groups devices under an email address, on one rendezvous
// server. Every device of the account can list the others with their
// 9-digit ID, name, platform, whether they are online and when they were
// last seen. That is all: an account never grants access to a device.
// Connecting still takes a credential the host accepts (its password, a
// pairing, or a key its owner authorised).
//
// One host setting changes that, on that host only: with
// `remohop config account-trust on`, a host accepts the identity keys of its
// account's devices like authorised keys. It is off by default.
//
// # Threat model
//
// The rendezvous server is not trusted (see docs/security.md), and the
// operator who enables accounts is the party best placed to attack them:
// it sends the login codes, so it can log in to any account on its server
// as far as the server is concerned. Account-trust must therefore not rest
// on anything the server says. It rests on two things:
//
//   - Membership is a log of entries each signed by the identity key of a
//     device that is already a member (ledger). The first entry is signed
//     by the device that created the account, and its hash is the account
//     ID. The server stores and relays the log but holds no member key, so
//     it cannot write an entry.
//
//   - A device that joins an existing account has no way to tell the real
//     log from one the server made up with its own keys, and an existing
//     device cannot tell a real new device from the server's key. So
//     joining takes two comparisons by the person who owns both devices:
//     the existing device approves the new one only by its full identity
//     fingerprint (`remohop account approve FINGERPRINT`, which the new device
//     prints), and the new device accepts the account only after its owner
//     has confirmed the account fingerprint shown by an existing device
//     (asked at `remohop login`, or --account-fingerprint). Both fingerprints
//     are 80 bits of SHA-256: the server cannot grind a key that matches.
//
// A host with account-trust on accepts a client when the client proves its
// identity key in the TLS handshake of the session (as for any key login)
// and that key is a member in the log the host verified from the account
// ID it confirmed. The host keeps the longest log it has verified and
// accepts only extensions of it, so a server cannot roll a removal back
// once the host has seen it. What a malicious server can do: refuse
// service; withhold new entries from a host, which delays a removal from
// taking effect there (a host that cannot refresh the log for MaxStale
// stops trusting the account until it can; `remohop config account-trust off`
// on the host takes effect at once); see which devices belong together;
// and, by the login codes, show itself as "pending" in the account, which
// nobody should approve. It cannot add a device, log in to a host, or read
// a session.
//
// Mechanically, a host keeps its account's member keys in its own list of
// authorised keys (paired.json), named "account:NAME". The host's normal
// key login then accepts them, `remohop host paired` lists them, and removing a
// device from the account (or turning account-trust off) removes them and
// ends their sessions within seconds. `remohop host revoke` of such an entry
// keeps that device out of this host until account-trust is turned off and
// on again.
package account

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/readori/RemoHop/internal/account/ledger"
	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/config"
	"github.com/readori/RemoHop/internal/nat"
	"github.com/readori/RemoHop/internal/signal"
)

// File is the account session inside the config directory (mode 0600: it
// holds the session token).
const File = "account.json"

// Statuses of this device in its account.
const (
	// StatusPending: logged in, waiting for an existing device to approve.
	StatusPending = "pending"
	// StatusMember: a device of the account.
	StatusMember = "member"
	// StatusLoggedOut: the server no longer accepts the session (the
	// device was removed, logged out elsewhere, or the account deleted).
	StatusLoggedOut = "logged_out"
)

// State is what this device keeps about its account.
type State struct {
	// Server is the rendezvous server the account lives on, and ServerKey
	// its key, pinned at login.
	Server    string `json:"server"`
	ServerKey string `json:"server_key"`
	// ServerWS is the server's websocket bridge as configured at login,
	// for networks without UDP; Server is empty when it was the only way
	// configured.
	ServerWS string `json:"server_ws,omitempty"`
	Email    string `json:"email"`
	Token    []byte `json:"token"`
	Status   string `json:"status"`
	// AccountID is the account this device created or whose fingerprint
	// its owner confirmed. Every log is checked against it.
	AccountID []byte `json:"account_id,omitempty"`
	// Log is the longest membership log verified so far.
	Log []ledger.Entry `json:"log,omitempty"`
	// Devices is the device list as last fetched.
	Devices []Device `json:"devices,omitempty"`
	// Verified is when a log was last fetched from the server and
	// verified.
	Verified time.Time `json:"verified,omitzero"`
	LoggedIn time.Time `json:"logged_in"`
}

// Device is a device of the account.
type Device struct {
	// ID is its 9-digit ID, if it has ever registered as a host on the
	// account's rendezvous server.
	ID       string `json:"id,omitempty"`
	Name     string `json:"name,omitempty"`
	Platform string `json:"platform,omitempty"`
	// Identity is the fingerprint of its identity key; Key the key.
	Identity string `json:"identity"`
	Key      string `json:"key"`
	// Online: registered with the rendezvous server right now, so it can
	// be connected to (if it accepts this device's credential).
	Online   bool       `json:"online"`
	LastSeen *time.Time `json:"last_seen,omitempty"`
	// This is the device running the command.
	This bool `json:"this_device,omitempty"`
}

// JoinRequest is a device waiting to be approved.
type JoinRequest struct {
	Identity string    `json:"identity"`
	Key      string    `json:"key"`
	Name     string    `json:"name,omitempty"`
	Platform string    `json:"platform,omitempty"`
	From     string    `json:"from,omitempty"`
	At       time.Time `json:"at"`
}

// Options configures the functions of this package.
type Options struct {
	// Dir is the config directory; empty means the default.
	Dir string
	// Signal is the rendezvous server for Login and RequestCode (empty:
	// the configured one). Everything after login uses the server, and
	// the server key, recorded at login.
	Signal string
	// Listen opens the UDP socket for one exchange with the server;
	// default an ephemeral port on all interfaces.
	Listen func() (net.PacketConn, error)
	// Logger receives debug output; nil discards it.
	Logger *slog.Logger
}

const requestTimeout = 15 * time.Second

// ErrNotLoggedIn is returned when this device has no account session.
var ErrNotLoggedIn = errors.New("not logged in to an account (log in with `remohop login EMAIL`)")

func (o Options) dir() (string, error) {
	if o.Dir != "" {
		return o.Dir, nil
	}
	return config.Dir()
}

func (o Options) logger() *slog.Logger {
	if o.Logger != nil {
		return o.Logger
	}
	return slog.New(slog.DiscardHandler)
}

// Load returns this device's account state, or ErrNotLoggedIn.
func Load(dir string) (*State, error) {
	b, err := os.ReadFile(filepath.Join(dir, File))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotLoggedIn
	}
	if err != nil {
		return nil, err
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(dir, File), err)
	}
	return &st, nil
}

// LoadOpts is Load on the directory of opts.
func LoadOpts(opts Options) (*State, error) {
	dir, err := opts.dir()
	if err != nil {
		return nil, err
	}
	return Load(dir)
}

func save(dir string, st *State) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return config.WriteFileAtomic(filepath.Join(dir, File), append(b, '\n'), 0o600)
}

// commit applies fn to the stored state and saves it, provided the stored
// session is still the one identified by token: a logout or new login by
// another process in the meantime wins.
func commit(dir string, token []byte, fn func(*State) error) error {
	path := filepath.Join(dir, File)
	unlock, err := config.Lock(path)
	if err != nil {
		return err
	}
	defer unlock()
	st, err := Load(dir)
	if err != nil {
		return err
	}
	if !bytes.Equal(st.Token, token) {
		return errors.New("the account session changed while this command ran; run it again")
	}
	if err := fn(st); err != nil {
		return err
	}
	return save(dir, st)
}

func remove(dir string) error {
	path := filepath.Join(dir, File)
	unlock, err := config.Lock(path)
	if err != nil {
		return err
	}
	defer unlock()
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// Fingerprint is the account fingerprint people compare.
func (st *State) Fingerprint() string {
	if len(st.AccountID) == 0 {
		return ""
	}
	return ledger.Fingerprint(st.AccountID)
}

// Ledger verifies the stored log.
func (st *State) Ledger() (*ledger.State, error) {
	if len(st.Log) == 0 {
		return nil, errors.New("no verified account log yet")
	}
	ls, err := ledger.Verify(st.Log)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(ls.ID, st.AccountID) {
		return nil, errors.New("the stored account log belongs to another account")
	}
	return ls, nil
}

// adopt verifies a log received from the server and makes it the stored
// one. It must belong to the confirmed account and extend what was
// verified before.
func (st *State) adopt(log []ledger.Entry) (*ledger.State, error) {
	if len(log) == 0 {
		return nil, errors.New("the rendezvous server sent no account log")
	}
	ls, err := ledger.Verify(log)
	if err != nil {
		return nil, fmt.Errorf("the rendezvous server sent an invalid account log: %w", err)
	}
	if !bytes.Equal(ls.ID, st.AccountID) {
		return nil, fmt.Errorf("the rendezvous server sent the log of account %s, but this device belongs to account %s; refusing it",
			ledger.Fingerprint(ls.ID), ledger.Fingerprint(st.AccountID))
	}
	if st.Email != "" && !bytes.Equal(ls.EmailHash, ledger.EmailHash(st.Email)) {
		return nil, errors.New("the rendezvous server sent an account log for another email address; refusing it")
	}
	if !ledger.Extends(st.Log, log) {
		return nil, errors.New("the rendezvous server sent an account log that does not extend the one this device " +
			"has already verified (an attempt to undo changes?); refusing it")
	}
	st.Log = log
	st.Verified = time.Now().UTC().Truncate(time.Second)
	return ls, nil
}

// settingName is the device-name setting (`remohop config name`), declared by
// internal/hostd, which imports this package and so cannot be imported.
const settingName = "name"

// CheckLog reports whether this device would accept log as its account's
// log: valid signatures from members all the way, the account it
// confirmed, and an extension of what it verified before. Nothing is
// stored.
func CheckLog(dir string, log []ledger.Entry) error {
	st, err := Load(dir)
	if err != nil {
		return err
	}
	_, err = st.adopt(log)
	return err
}

// deviceInfo is what this device tells the server about itself.
func deviceInfo(dir string) (name, platform string) {
	values, _ := config.AllIn(dir)
	return DeviceName(values), runtime.GOOS + "/" + runtime.GOARCH
}

// DeviceName is the name a device joins its account with: the "name"
// setting from values, or else the computer's host name (as
// hostd.DeviceName).
func DeviceName(values map[string]string) string {
	if n := auth.CleanText(values[settingName], ledger.MaxName); n != "" {
		return n
	}
	h, _ := os.Hostname()
	return auth.CleanText(h, ledger.MaxName)
}

// session is one connection to the rendezvous server.
type session struct {
	sc   *signal.Client
	sock *nat.Socket
}

func (s *session) close() {
	s.sc.Close()
	s.sock.Close()
}

// dialServer connects to the rendezvous server over UDP or, where UDP does
// not get through and a websocket bridge is configured, over the bridge.
func dialServer(ctx context.Context, opts Options, addr string, sopts signal.Options) (*session, error) {
	sc, sock, err := signal.Connect(ctx, addr, sopts, func() (*nat.Socket, error) {
		if opts.Listen != nil {
			pc, err := opts.Listen()
			if err != nil {
				return nil, err
			}
			return nat.NewSocket(pc)
		}
		return nat.Listen("")
	}, opts.Logger)
	if err != nil {
		return nil, err
	}
	return &session{sc: sc, sock: sock}, nil
}

// connect opens a connection to the server the account lives on.
func connect(ctx context.Context, opts Options, st *State) (*session, error) {
	return dialServer(ctx, opts, st.Server, serverOptions(st))
}

// serverOptions are the options for reaching the account's server: its
// pinned key and the bridge recorded at login. The bridge's access token
// and the transport come from the current settings when they still name
// the same server (they are not stored with the account).
func serverOptions(st *State) signal.Options {
	o := signal.Options{Pin: st.ServerKey, WS: st.ServerWS}
	addr, cur, err := signal.Resolve("")
	if err != nil {
		return o
	}
	if (st.Server != "" && addr == st.Server) || (st.Server == "" && cur.WS == st.ServerWS) {
		o.WS, o.WSHeader, o.Transport = cur.WS, cur.WSHeader, cur.Transport
	} else if st.Server == "" {
		o.Transport = signal.TransportWS
	}
	return o
}

// call runs one authenticated account operation for st.
func call(ctx context.Context, opts Options, dir string, st *State, op string, args signal.AccountArgs) (*signal.AccountReply, error) {
	id, err := auth.LoadIdentity(dir)
	if err != nil {
		return nil, fmt.Errorf("identity: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	s, err := connect(ctx, opts, st)
	if err != nil {
		return nil, err
	}
	defer s.close()
	if args.Name == "" && args.Platform == "" {
		args.Name, args.Platform = deviceInfo(dir)
	}
	r, err := s.sc.AccountCall(ctx, id, st.Token, op, args)
	if signal.CodeOf(err) == signal.CodeNotLoggedIn {
		commit(dir, st.Token, func(cur *State) error { cur.Status = StatusLoggedOut; return nil })
		st.Status = StatusLoggedOut
	}
	return r, err
}

func encodeKey(k []byte) string { return auth.EncodeKey(ed25519.PublicKey(k)) }
