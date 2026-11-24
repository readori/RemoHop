package auth

import (
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"sync"
	"time"

	"filippo.io/cpace"
)

// The handshake runs on the first stream of a freshly established P2P QUIC
// connection, client speaking first. Messages are a 4-byte big-endian length
// followed by JSON, at most maxMsg bytes.
//
//	C→H  hello   {v, cred: "password"|"token", token_id?, target?, msg_a}
//	H→C  reply   {v, ok: true, msg_b, confirm}          (or ok: false, code, ...)
//	C→H  finish  {confirm, alt?}
//	H→C  wait    {v, ok: true, wait: true}              (only if the host asks its owner)
//	H→C  welcome {v, ok: true}                          (or ok: false, code: "denied")
//
// Two passwords. A host can accept its one-time password and a fixed one
// side by side. A balanced PAKE proves one password per exchange, so the
// host then answers the client's single msg_a twice, once per password,
// the second answer in reply.alt; the client confirms against whichever
// verifies and says which in finish.alt. The client still committed to one
// guess in msg_a, so an attacker tests that one guess against both
// passwords and no more: one guess per connection, as before.
//
// Key logins. With cred "key" there is no PAKE: msg_a, msg_b and the
// confirms are absent. The host accepts if the identity key the client
// proved in TLS is one its owner authorised. What authenticates the host to
// the client in that case is the host's identity key, which the client must
// already hold (pinned) and checks before it sends the hello. Both ends'
// keys signed this TLS handshake, so nobody can sit in the middle.
//
// The welcome carries no secret. It exists so that the client knows the
// host has received its confirmation before it does anything else with the
// connection: a client that closed right after sending finish could
// otherwise have the close overtake the finish, and the host would have to
// count a correct login as a failed guess.
//
// msg_a / msg_b are the two CPace messages. CPace turns the shared
// credential into a session key K without revealing anything an attacker
// could test guesses against offline. Its context covers
//
//   - the protocol label and version,
//   - the credential kind and token id from the hello,
//   - the target the client dialled (the host's 9-digit ID, or empty),
//   - the TLS exporter of the QUIC connection the stream runs on,
//   - both identity keys as proven in that TLS handshake.
//
// The two confirm values are HMACs under K (with distinct labels per
// direction) over that context and both CPace messages.
//
// Why this defeats a man in the middle, including a malicious rendezvous
// server that routes the client to itself: the attacker has to terminate TLS
// on both legs, so client and host see different exporters (and different
// peer keys), derive different K, and the confirmation fails. Relaying the
// handshake messages verbatim does not help, and neither does knowing one of
// the identity keys.
//
// Why the host confirms first: the party that needs protecting from an
// active attacker it cannot rate-limit is the client (anyone on the path, or
// the rendezvous server, can pose as the host). With this order an impostor
// must produce a valid confirm before it has seen anything derived from K,
// which it cannot do without the credential, and the client then aborts
// without sending its own confirm. An impostor therefore gets no password
// guess at all out of a client. An attacking client gets exactly one guess
// per connection (the host's confirm verifies or refutes the single password
// it committed to in msg_a), and the host's Limiter counts that guess as
// failed from the moment the hello arrives until the client's confirm
// proves it was right. Hanging up early does not avoid the count.
const (
	// Version is the handshake version this code speaks.
	Version = 1

	maxMsg        = 2048
	protoLabel    = "remohop-auth/1"
	exporterLabel = "EXPORTER-remohop-auth-v1"
	maxTargetLen  = 128
	maxTokenIDLen = 64
	maxDetailLen  = 120

	// MaxRetryAfter bounds the wait a host can ask of a client. It is the
	// longest lockout the default limiter policy ever imposes; a reply
	// claiming more is a peer trying to park us, and is cut down to this.
	MaxRetryAfter = 10 * time.Minute
)

// Kind is the type of credential a handshake authenticates with.
type Kind string

const (
	// KindPassword is the host's temporary or fixed password.
	KindPassword Kind = "password"
	// KindToken is a pairing token minted by the host.
	KindToken Kind = "token"
	// KindKey is the client's identity key, authorised by the host's owner.
	KindKey Kind = "key"
)

// Credential is what the client proves knowledge of.
type Credential struct {
	Kind    Kind
	TokenID string // public identifier of the token; empty for passwords
	secret  string
}

// PasswordCredential makes a password credential; pw is normalised.
func PasswordCredential(pw string) Credential {
	return Credential{Kind: KindPassword, secret: NormalizePassword(pw)}
}

// TokenCredential makes a pairing-token credential.
func TokenCredential(id string, secret []byte) Credential {
	return Credential{Kind: KindToken, TokenID: id, secret: tokenSecret(secret)}
}

// KeyCredential logs in with the client's identity key alone. Use it only
// on a connection whose host key has been checked against a pin: nothing in
// a key login proves the host to the client except that key.
func KeyCredential() Credential { return Credential{Kind: KindKey} }

// tokenSecret is the form in which a 256-bit token enters CPace.
func tokenSecret(secret []byte) string { return hex.EncodeToString(secret) }

// Binding ties a handshake to the one TLS connection it runs over.
type Binding struct {
	Exporter  []byte
	ClientKey ed25519.PublicKey
	HostKey   ed25519.PublicKey
}

// ClientBinding derives the binding on the dialling side from the QUIC
// connection's TLS state and the client's own identity key.
func ClientBinding(cs tls.ConnectionState, self ed25519.PublicKey) (Binding, error) {
	b, peer, err := binding(cs)
	b.ClientKey, b.HostKey = self, peer
	return b, err
}

// HostBinding derives the binding on the accepting side.
func HostBinding(cs tls.ConnectionState, self ed25519.PublicKey) (Binding, error) {
	b, peer, err := binding(cs)
	b.ClientKey, b.HostKey = peer, self
	return b, err
}

func binding(cs tls.ConnectionState) (Binding, ed25519.PublicKey, error) {
	if cs.Version != tls.VersionTLS13 {
		return Binding{}, nil, errors.New("auth: connection is not TLS 1.3")
	}
	peer, err := PeerKey(cs)
	if err != nil {
		return Binding{}, nil, err
	}
	ekm, err := cs.ExportKeyingMaterial(exporterLabel, nil, 32)
	if err != nil {
		return Binding{}, nil, fmt.Errorf("auth: tls exporter: %w", err)
	}
	return Binding{Exporter: ekm}, peer, nil
}

// Code classifies a handshake failure.
type Code string

const (
	// CodeAuthFailed: the key confirmation did not verify. The credential
	// is wrong, or someone is in the middle of the connection.
	CodeAuthFailed Code = "auth_failed"
	// CodeLockedOut: too many failed attempts; try again after RetryAfter.
	CodeLockedOut Code = "locked_out"
	// CodeUnknownToken: the host does not know the pairing token (revoked,
	// or issued to a different client identity).
	CodeUnknownToken Code = "unknown_token"
	// CodeNoPassword: the host does not accept password logins.
	CodeNoPassword Code = "password_disabled"
	// CodeWrongHost: the client dialled an ID this host does not hold.
	CodeWrongHost Code = "wrong_host"
	// CodeBadVersion: no handshake version in common.
	CodeBadVersion Code = "bad_version"
	// CodeUnknownKey: the client's identity key is not authorised.
	CodeUnknownKey Code = "unknown_key"
	// CodeDenied: the person at the host declined, or did not answer.
	CodeDenied Code = "denied"
	// CodeBusy: the host has too many logins in progress; try again.
	CodeBusy Code = "busy"
	// CodeProtocol: malformed or unexpected message.
	CodeProtocol Code = "protocol"
)

// Error is a handshake failure with a machine-readable cause.
type Error struct {
	Code       Code
	RetryAfter time.Duration // for CodeLockedOut
	Detail     string
}

func (e *Error) Error() string {
	switch e.Code {
	case CodeAuthFailed:
		return "authentication failed: wrong password or token (or the connection is being intercepted)"
	case CodeLockedOut:
		return fmt.Sprintf("locked out after too many failed attempts; retry in %s", e.RetryAfter.Round(time.Second))
	case CodeUnknownToken:
		return "the host does not recognise this pairing (revoked?)"
	case CodeNoPassword:
		return "the host does not accept password logins"
	case CodeWrongHost:
		return "reached a host that does not hold the requested ID"
	case CodeBadVersion:
		return "incompatible handshake version: " + e.Detail
	case CodeBusy:
		return "the host is busy with other logins; try again in a moment"
	case CodeUnknownKey:
		return "the host has not authorised this machine's key"
	case CodeDenied:
		return "the connection was not approved at the host"
	}
	if e.Detail != "" {
		return "handshake failed: " + e.Detail
	}
	return "handshake failed"
}

// CodeOf returns the Code of a handshake error, or "" for other errors.
func CodeOf(err error) Code {
	if e, ok := errors.AsType[*Error](err); ok {
		return e.Code
	}
	return ""
}

type hello struct {
	V       int    `json:"v"`
	Kind    Kind   `json:"cred"`
	TokenID string `json:"token_id,omitempty"`
	Target  string `json:"target,omitempty"`
	// Name is the device name the client's owner chose (`remohop config
	// name`), shown at the host in approval prompts and status. It is a
	// claim, not an identity: the client's key is what identifies it.
	Name string `json:"name,omitempty"`
	MsgA []byte `json:"msg_a"`
}

type reply struct {
	V            int    `json:"v"`
	OK           bool   `json:"ok"`
	Code         Code   `json:"code,omitempty"`
	Detail       string `json:"detail,omitempty"`
	RetryAfterMS int64  `json:"retry_after_ms,omitempty"`
	MsgB         []byte `json:"msg_b,omitempty"`
	Confirm      []byte `json:"confirm,omitempty"`
	// Alt is the host's answer for its second password, if it has two.
	Alt *altReply `json:"alt,omitempty"`
	// Wait announces that the host is asking its owner; the welcome (or
	// a refusal) follows.
	Wait bool `json:"wait,omitempty"`
}

type altReply struct {
	MsgB    []byte `json:"msg_b"`
	Confirm []byte `json:"confirm"`
}

type finish struct {
	Confirm []byte `json:"confirm,omitempty"`
	// Alt says the confirm is for the host's alternative answer.
	Alt bool `json:"alt,omitempty"`
}

// failure turns a refusal received from the host into an Error. Everything
// in it is the host's say-so and the host has not authenticated: only codes
// this version knows are passed on, free text is kept solely where the code
// calls for it and is cleaned and bounded, and the retry time is clamped.
func (r *reply) failure() *Error {
	e := &Error{Code: r.Code}
	switch r.Code {
	case CodeLockedOut:
		e.RetryAfter = time.Duration(min(max(r.RetryAfterMS, 0), MaxRetryAfter.Milliseconds())) * time.Millisecond
	case CodeBadVersion:
		e.Detail = CleanText(r.Detail, maxDetailLen)
	case CodeAuthFailed, CodeUnknownToken, CodeUnknownKey, CodeDenied, CodeNoPassword, CodeWrongHost, CodeBusy, CodeProtocol:
	default:
		e.Code = CodeProtocol
		e.Detail = "the host refused the login for a reason this version does not know"
	}
	return e
}

func writeMsg(w io.Writer, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(body) > maxMsg {
		return &Error{Code: CodeProtocol, Detail: "message too large"}
	}
	buf := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(buf, uint32(len(body)))
	copy(buf[4:], body)
	_, err = w.Write(buf)
	return err
}

// readMsg reads one message, refusing anything over maxMsg before
// allocating for it: the peer is not authenticated yet.
func readMsg(r io.Reader, v any) error {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > maxMsg {
		return &Error{Code: CodeProtocol, Detail: "message too large"}
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return err
	}
	if err := json.Unmarshal(body, v); err != nil {
		return &Error{Code: CodeProtocol, Detail: "malformed message"}
	}
	return nil
}

// context builds the CPace context for a hello on a given connection and
// returns it with the digest the confirmations cover.
func (h *hello) context(b Binding) (*cpace.ContextInfo, []byte) {
	d := sha256.New()
	put := func(p []byte) {
		var l [4]byte
		binary.BigEndian.PutUint32(l[:], uint32(len(p)))
		d.Write(l[:])
		d.Write(p)
	}
	put([]byte(protoLabel))
	put([]byte{byte(h.V)})
	put([]byte(h.Kind))
	put([]byte(h.TokenID))
	put([]byte(h.Target))
	put(b.Exporter)
	put(b.ClientKey)
	put(b.HostKey)
	// Covered by the confirmation, so nobody on the path can change it;
	// left out when empty so clients that send none compute what they
	// always did.
	if h.Name != "" {
		put([]byte(h.Name))
	}
	ad := d.Sum(nil)
	return cpace.NewContextInfo("client:"+EncodeKey(b.ClientKey), "host:"+EncodeKey(b.HostKey), ad), ad
}

// confirmTag is the key-confirmation MAC for one direction. Every input has
// a fixed length, and the role label keeps a tag from being reflected back
// at its sender.
func confirmTag(key []byte, role string, ad, msgA, msgB []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(protoLabel + " confirm " + role + "\x00"))
	m.Write(ad)
	m.Write(msgA)
	m.Write(msgB)
	return m.Sum(nil)
}

// ClientHandshake authenticates to a host over rw (the first stream of the
// connection b was derived from). target is the 9-digit ID that was dialled,
// or "" for a direct address. The caller owns deadlines on rw.
//
// On CodeAuthFailed the host has counted a failed attempt and will close the
// connection; a retry needs a new connection.
func ClientHandshake(rw io.ReadWriter, b Binding, cred Credential, target string) error {
	return ClientHandshakeWith(rw, b, cred, target, ClientOptions{})
}

// ClientOptions tunes ClientHandshakeWith.
type ClientOptions struct {
	// Name is this device's name, shown at the host (at most
	// MaxTokenName bytes of plain text; anything else is cleaned away).
	Name string
	// OnWait is called when the host says it is asking its owner to
	// approve the connection. The answer can take a while: this is where
	// the caller tells the user and extends its deadlines.
	OnWait func()
}

// ClientHandshakeWith is ClientHandshake with options.
func ClientHandshakeWith(rw io.ReadWriter, b Binding, cred Credential, target string, opts ClientOptions) error {
	h := hello{V: Version, Kind: cred.Kind, TokenID: cred.TokenID, Target: target, Name: CleanText(opts.Name, MaxTokenName)}
	var fin finish
	if cred.Kind == KindKey {
		if err := writeMsg(rw, h); err != nil {
			return err
		}
		var rep reply
		if err := readMsg(rw, &rep); err != nil {
			return fmt.Errorf("auth: reading host reply: %w", err)
		}
		if !rep.OK {
			return rep.failure()
		}
	} else {
		if cred.secret == "" {
			return errors.New("auth: empty credential")
		}
		ci, ad := h.context(b)
		msgA, state, err := cpace.Start(cred.secret, ci)
		if err != nil {
			return err
		}
		h.MsgA = msgA
		if err := writeMsg(rw, h); err != nil {
			return err
		}
		var rep reply
		if err := readMsg(rw, &rep); err != nil {
			return fmt.Errorf("auth: reading host reply: %w", err)
		}
		if !rep.OK {
			return rep.failure()
		}
		// The host may have answered for two passwords; ours is at most
		// one of them.
		answers := []altReply{{rep.MsgB, rep.Confirm}}
		if rep.Alt != nil {
			answers = append(answers, *rep.Alt)
		}
		matched := false
		for i, a := range answers {
			key, err := state.Finish(a.MsgB)
			if err != nil {
				return &Error{Code: CodeProtocol, Detail: "invalid key exchange message"}
			}
			if hmac.Equal(a.Confirm, confirmTag(key, "host", ad, msgA, a.MsgB)) {
				fin = finish{Confirm: confirmTag(key, "client", ad, msgA, a.MsgB), Alt: i == 1}
				matched = true
				break
			}
		}
		if !matched {
			// Stop here: sending a confirm would hand an impostor the
			// means to test one password guess.
			return &Error{Code: CodeAuthFailed}
		}
	}
	if err := writeMsg(rw, fin); err != nil {
		return err
	}
	for {
		var welcome reply
		if err := readMsg(rw, &welcome); err != nil {
			return fmt.Errorf("auth: waiting for the host to accept: %w", err)
		}
		if !welcome.OK {
			return welcome.failure()
		}
		if !welcome.Wait {
			return nil
		}
		if opts.OnWait != nil {
			opts.OnWait()
		}
	}
}

// Server is the host side of the handshake.
type Server struct {
	// Password returns the password currently accepted (normalised), or ""
	// when password logins are disabled.
	Password func() string
	// PasswordGen, if set, is used instead of Password. It also returns
	// the password's generation: a number, never 0, that the host changes
	// whenever it replaces the password (0 for a password it never
	// replaces). A login that proves this password reports the generation
	// in Result.PasswordGen, so that the host can check, when it finally
	// accepts the login, that the password is still the one in force, and
	// spend it if it is single-use.
	PasswordGen func() (pw string, gen uint64)
	// AltPassword, if set and returning a non-empty password different
	// from Password's, is accepted as well (see "Two passwords" above).
	AltPassword func() string
	// Tokens holds the pairing tokens and authorised keys; nil disables
	// token and key logins.
	Tokens *TokenStore
	// Limiter throttles password guessing. Nil means a limiter with the
	// default policy, created on first use.
	Limiter *Limiter
	// HostID returns the 9-digit ID this host currently holds, or "".
	HostID func() string
	// ScopedKey, if set, is asked about a key login whose key Tokens does
	// not list. It may admit the key for a limited scope (for example
	// "net": the mesh network only) under a display name; the host
	// restricts such a session to that scope.
	ScopedKey func(key ed25519.PublicKey) (name, scope string, ok bool)

	limiterOnce sync.Once
}

// Result describes a successfully authenticated client.
type Result struct {
	Kind Kind
	// TokenID and TokenName identify the pairing or authorised key used.
	TokenID   string
	TokenName string
	ClientKey ed25519.PublicKey
	// AltPassword is true when a password login matched AltPassword.
	AltPassword bool
	// PasswordGen is the generation of the password a password login
	// proved, as Server.PasswordGen gave it; 0 for AltPassword and for
	// other kinds of login.
	PasswordGen uint64
	// ClientName is the name the client gave itself, cleaned; empty if
	// it gave none.
	ClientName string
	// Scope is set when a key login was admitted by Server.ScopedKey: the
	// session may use that scope only.
	Scope string
	// Grant is the grant the token redeems (internal/grant); empty for
	// full access.
	Grant string
}

// Approver is called by Complete once the client has proven its credential
// and before it is welcomed. It may take as long as a person needs:
// announce tells the client that it has to wait (call it at most once, and
// only if the answer is not immediate). A non-nil error refuses the client:
// with its code if it is an *Error, otherwise with CodeDenied. It is the
// host's last word before the client is welcomed, so it is also where the
// host makes its final admission checks (a one-time password still unspent,
// a session limit not reached).
type Approver func(res *Result, announce func() error) error

func (s *Server) limiter() *Limiter {
	s.limiterOnce.Do(func() {
		if s.Limiter == nil {
			s.Limiter = NewLimiter(LimiterConfig{})
		}
	})
	return s.Limiter
}

// Hello is a client's opening message, read but not yet acted on.
type Hello struct {
	h hello
}

// Kind is the credential type the client wants to log in with. It is the
// client's claim; nothing has been verified.
func (h *Hello) Kind() Kind { return h.h.Kind }

// Reject answers a client with a refusal. It is best effort: the client
// may already be gone.
func Reject(w io.Writer, e *Error) {
	writeMsg(w, reply{V: Version, Code: e.Code, Detail: e.Detail, RetryAfterMS: e.RetryAfter.Milliseconds()})
}

// ReadHello reads and validates the client's hello. Splitting the handshake
// here lets the caller account for the connection differently once it knows
// the client has spoken and which kind of login it wants. On a *Error the
// client has been sent a refusal.
func (s *Server) ReadHello(rw io.ReadWriter) (*Hello, error) {
	reject := func(e *Error) (*Hello, error) {
		Reject(rw, e)
		return nil, e
	}
	var h hello
	if err := readMsg(rw, &h); err != nil {
		if e, ok := errors.AsType[*Error](err); ok {
			return reject(e)
		}
		return nil, fmt.Errorf("auth: reading hello: %w", err)
	}
	if h.V != Version {
		return reject(&Error{Code: CodeBadVersion, Detail: fmt.Sprintf("host speaks version %d", Version)})
	}
	if len(h.Target) > maxTargetLen || len(h.TokenID) > maxTokenIDLen || len(h.Name) > 4*MaxTokenName {
		return reject(&Error{Code: CodeProtocol, Detail: "field too long"})
	}
	if h.Kind != KindPassword && h.Kind != KindToken && h.Kind != KindKey {
		return reject(&Error{Code: CodeProtocol, Detail: "unknown credential type"})
	}
	if h.Kind == KindPassword && h.TokenID != "" {
		return reject(&Error{Code: CodeProtocol, Detail: "token id on a password login"})
	}
	return &Hello{h: h}, nil
}

// Handshake authenticates one client: ReadHello followed by Complete.
func (s *Server) Handshake(ctx context.Context, rw io.ReadWriter, b Binding, src netip.Addr) (*Result, error) {
	hello, err := s.ReadHello(rw)
	if err != nil {
		return nil, err
	}
	return s.Complete(ctx, rw, hello, b, src, nil)
}

// Complete runs the login for a hello obtained from ReadHello. src is the
// client's network address, used for throttling; ctx bounds how long the
// login may queue behind other logins from the same source (reads and
// writes on rw are bounded by the caller's deadlines). Exactly one
// credential is tried per connection: after any error the caller must close
// it, so that every guess costs the client a new one. approve may be nil.
func (s *Server) Complete(ctx context.Context, rw io.ReadWriter, hello *Hello, b Binding, src netip.Addr, approve Approver) (*Result, error) {
	h := hello.h
	reject := func(e *Error) (*Result, error) {
		Reject(rw, e)
		return nil, e
	}
	// A client that dialled an ID must reach the host holding that ID. The
	// target is in the PAKE context as well, so this check only turns a
	// misrouted connection into a clear error instead of a failed attempt.
	if h.Target != "" && s.HostID != nil {
		if id := s.HostID(); id != "" && id != h.Target {
			return reject(&Error{Code: CodeWrongHost})
		}
	}

	res := &Result{Kind: h.Kind, ClientKey: b.ClientKey, ClientName: CleanText(h.Name, MaxTokenName)}
	var secret, altSecret string
	var attempt *Attempt
	bind := false
	switch h.Kind {
	case KindKey:
		// No shared secret: the TLS handshake proved the client holds
		// this key, and the host's owner has listed it.
		var tok Token
		var ok bool
		if s.Tokens != nil {
			var err error
			if tok, ok, err = s.Tokens.AuthorizedKey(b.ClientKey); err != nil {
				return reject(&Error{Code: CodeProtocol, Detail: "key store unavailable"})
			}
		}
		if !ok && s.ScopedKey != nil {
			if name, scope, admitted := s.ScopedKey(b.ClientKey); admitted && scope != "" {
				ok, tok.Name, res.Scope = true, CleanTokenName(name), scope
			}
		}
		if !ok {
			return reject(&Error{Code: CodeUnknownKey})
		}
		res.TokenID, res.TokenName = tok.ID, tok.Name
		if err := writeMsg(rw, reply{V: Version, OK: true}); err != nil {
			return nil, err
		}
		var fin finish
		if err := readMsg(rw, &fin); err != nil {
			return nil, err
		}
		return s.welcome(rw, res, approve)
	case KindPassword:
		var gen uint64
		if s.PasswordGen != nil {
			secret, gen = s.PasswordGen()
		} else if s.Password != nil {
			secret = s.Password()
		}
		if s.AltPassword != nil {
			altSecret = s.AltPassword()
		}
		if secret == "" {
			secret, altSecret, gen = altSecret, "", 0
		}
		res.PasswordGen = gen
		if secret == "" {
			return reject(&Error{Code: CodeNoPassword})
		}
		if altSecret == secret {
			altSecret = ""
		}
		var err error
		if attempt, err = s.limiter().Begin(ctx, src); err != nil {
			e, _ := errors.AsType[*Error](err)
			return reject(e)
		}
		// Anything short of a verified confirm below is a failed guess.
		defer attempt.Fail()
	case KindToken:
		// Token logins are not throttled: a 256-bit secret cannot be
		// guessed, and exempting them keeps paired clients working while
		// someone hammers the password.
		var tok Token
		var ok bool
		if s.Tokens != nil {
			var err error
			if tok, ok, err = s.Tokens.Lookup(h.TokenID); err != nil {
				return reject(&Error{Code: CodeProtocol, Detail: "token store unavailable"})
			}
		}
		// A token only works from the identity it was issued to, so a
		// leaked token file is useless without that client's key. A grant
		// token issued unbound is bound below, once the client has proven
		// it holds the secret.
		if !ok || (len(tok.ClientKey) != 0 || tok.Grant == "") && !hmac.Equal(tok.ClientKey, b.ClientKey) {
			return reject(&Error{Code: CodeUnknownToken})
		}
		secret = tokenSecret(tok.Secret)
		res.TokenID, res.TokenName, res.Grant = tok.ID, tok.Name, tok.Grant
		bind = len(tok.ClientKey) == 0
	}

	ci, ad := h.context(b)
	msgB, key, err := cpace.Exchange(secret, ci, h.MsgA)
	if err != nil {
		return reject(&Error{Code: CodeProtocol, Detail: "invalid key exchange message"})
	}
	rep := reply{V: Version, OK: true, MsgB: msgB, Confirm: confirmTag(key, "host", ad, h.MsgA, msgB)}
	var altB, altKey []byte
	if altSecret != "" {
		if altB, altKey, err = cpace.Exchange(altSecret, ci, h.MsgA); err != nil {
			return reject(&Error{Code: CodeProtocol, Detail: "invalid key exchange message"})
		}
		rep.Alt = &altReply{MsgB: altB, Confirm: confirmTag(altKey, "host", ad, h.MsgA, altB)}
	}
	if err := writeMsg(rw, rep); err != nil {
		return nil, err
	}
	var fin finish
	if err := readMsg(rw, &fin); err != nil {
		// The usual way a wrong guess ends: the client saw our confirm
		// fail to verify and hung up.
		return nil, &Error{Code: CodeAuthFailed, Detail: err.Error()}
	}
	if fin.Alt && altKey != nil {
		key, msgB = altKey, altB
		res.AltPassword, res.PasswordGen = true, 0
	}
	if !hmac.Equal(fin.Confirm, confirmTag(key, "client", ad, h.MsgA, msgB)) {
		return nil, &Error{Code: CodeAuthFailed}
	}
	if attempt != nil {
		attempt.Success()
	}
	if bind {
		// First redemption of an unbound grant token: from now on it
		// works only from this client's identity. Two clients racing
		// with the same secret: the store lets exactly one win.
		if err := s.Tokens.Bind(res.TokenID, b.ClientKey); err != nil {
			return reject(&Error{Code: CodeUnknownToken})
		}
	}
	return s.welcome(rw, res, approve)
}

// welcome ends a login whose credential checked out: ask the owner if the
// caller wants that, then tell the client it is in.
func (s *Server) welcome(rw io.ReadWriter, res *Result, approve Approver) (*Result, error) {
	if approve != nil {
		announce := func() error { return writeMsg(rw, reply{V: Version, OK: true, Wait: true}) }
		if err := approve(res, announce); err != nil {
			e, ok := errors.AsType[*Error](err)
			if !ok {
				e = &Error{Code: CodeDenied}
			}
			Reject(rw, e)
			return nil, e
		}
	}
	if err := writeMsg(rw, reply{V: Version, OK: true}); err != nil {
		return nil, err
	}
	return res, nil
}
