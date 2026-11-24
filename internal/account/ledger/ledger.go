// Package ledger is the signed membership log of a RemoHop account: the one
// piece of account state that does not depend on trusting the rendezvous
// server.
//
// An account's membership is an append-only, hash-chained list of entries.
// Each entry is signed with the identity key of a device that is a member
// at that point in the log:
//
//	seq 0  create  device = signer = the first device; email = EmailHash
//	seq n  add     device = a new member, signed by an existing member
//	seq n  remove  device = a member that leaves, signed by any member
//
// Every entry after the first names the hash of its predecessor, so the log
// can only be extended, never rewritten, without a member's key. The hash
// of the create entry is the account's ID; people compare its fingerprint
// (Fingerprint) between devices when a device joins, and every device keeps
// the ID it confirmed. A rendezvous server stores and relays the log but
// holds no member key, so it cannot add a device, and a device checks every
// log it receives against the ID it holds and against the longest log it has
// already verified (Extends).
//
// What a server can still do is withhold the newest entries, for example a
// removal, from a device; see the account package for how hosts bound that.
package ledger

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/readori/RemoHop/internal/auth"
)

// Operations.
const (
	OpCreate = "create"
	OpAdd    = "add"
	OpRemove = "remove"
)

const (
	// MaxEntries bounds a log. An account that reaches it has to be
	// deleted and created again; at a few changes a month that is decades.
	MaxEntries = 256
	// MaxMembers bounds the devices in an account at one time.
	MaxMembers = 64
	// MaxName bounds a device name in an entry.
	MaxName = auth.MaxTokenName
	// MaxEmail bounds an email address.
	MaxEmail = 254

	entryLabel = "remohop-account-entry/1"
	emailLabel = "remohop-account-email/1"
)

// Entry is one signed change to an account's membership.
type Entry struct {
	Seq    int    `json:"seq"`
	Prev   []byte `json:"prev,omitempty"`
	Op     string `json:"op"`
	Device []byte `json:"device"`
	// Name is the device's name when the entry was made; display only.
	Name string `json:"name,omitempty"`
	// Email is EmailHash of the account's address, in the create entry.
	Email  []byte `json:"email,omitempty"`
	Time   int64  `json:"time"`
	Signer []byte `json:"signer"`
	Sig    []byte `json:"sig"`
}

// NormalizeEmail checks an address and returns its canonical form: the
// bare address, lower case.
func NormalizeEmail(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > MaxEmail {
		return "", errors.New("not an email address")
	}
	a, err := mail.ParseAddress(s)
	if err != nil || a.Name != "" || a.Address != s {
		return "", fmt.Errorf("%q is not a plain email address (like name@example.com)", s)
	}
	at := strings.LastIndexByte(s, '@')
	if at <= 0 || at == len(s)-1 || !strings.Contains(s[at+1:], ".") {
		return "", fmt.Errorf("%q is not a plain email address (like name@example.com)", s)
	}
	for _, r := range s {
		if r <= ' ' || r == 0x7f {
			return "", errors.New("not an email address")
		}
	}
	return strings.ToLower(s), nil
}

// Domain returns the part of a normalised address after the @.
func Domain(email string) string {
	return email[strings.LastIndexByte(email, '@')+1:]
}

// EmailHash names an account's address without spelling it out: the
// rendezvous server indexes accounts by it and the create entry commits to
// it.
func EmailHash(normalized string) []byte {
	h := sha256.Sum256([]byte(emailLabel + "\x00" + normalized))
	return h[:]
}

// signedBytes is what a signature covers: every field but the signature,
// each length-prefixed, after a domain label.
func (e *Entry) signedBytes() []byte {
	var b bytes.Buffer
	put := func(p []byte) {
		var l [4]byte
		binary.BigEndian.PutUint32(l[:], uint32(len(p)))
		b.Write(l[:])
		b.Write(p)
	}
	putInt := func(v int64) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(v))
		b.Write(n[:])
	}
	put([]byte(entryLabel))
	putInt(int64(e.Seq))
	put(e.Prev)
	put([]byte(e.Op))
	put(e.Device)
	put([]byte(e.Name))
	put(e.Email)
	putInt(e.Time)
	put(e.Signer)
	return b.Bytes()
}

// SignedBytes is what an entry's signature covers. Other implementations
// (mobile apps, a web gateway) verify entries with it.
func SignedBytes(e *Entry) []byte { return e.signedBytes() }

// Hash identifies an entry, signature included. The next entry's Prev is
// this; the create entry's hash is the account ID.
func (e *Entry) Hash() []byte {
	h := sha256.New()
	h.Write(e.signedBytes())
	h.Write(e.Sig)
	return h.Sum(nil)
}

// sign fills in Signer and Sig.
func (e *Entry) sign(id *auth.Identity) {
	e.Signer = id.Public()
	e.Sig = id.Sign(e.signedBytes())
}

// Member is a device in the account.
type Member struct {
	Key ed25519.PublicKey
	// Name is the name recorded when the device was added.
	Name string
	// Seq is the entry that added it; AddedBy the device that signed it.
	Seq     int
	AddedBy ed25519.PublicKey
	Added   time.Time
}

// State is the result of replaying a log.
type State struct {
	// ID is the account ID: the hash of the create entry.
	ID        []byte
	EmailHash []byte
	// Len is the number of entries; Head the hash of the last one.
	Len  int
	Head []byte
	// Members in the order they were added.
	Members []Member
}

func keyOf(b []byte) ([32]byte, bool) {
	if len(b) != ed25519.PublicKeySize {
		return [32]byte{}, false
	}
	return [32]byte(b), true
}

// IsMember reports whether key is a member.
func (s *State) IsMember(key ed25519.PublicKey) bool {
	_, ok := s.member(key)
	return ok
}

// Member returns the member with key.
func (s *State) Member(key ed25519.PublicKey) (Member, bool) {
	i, ok := s.member(key)
	if !ok {
		return Member{}, false
	}
	return s.Members[i], true
}

func (s *State) member(key []byte) (int, bool) {
	for i, m := range s.Members {
		if bytes.Equal(m.Key, key) {
			return i, true
		}
	}
	return 0, false
}

// Apply checks e as the next entry and, if it is valid, applies it.
func (s *State) Apply(e Entry) error {
	if s.Len >= MaxEntries {
		return fmt.Errorf("the account's log is full (%d entries)", MaxEntries)
	}
	if e.Seq != s.Len {
		return fmt.Errorf("entry %d is out of order (expected %d)", e.Seq, s.Len)
	}
	if _, ok := keyOf(e.Device); !ok {
		return errors.New("entry names a malformed device key")
	}
	if _, ok := keyOf(e.Signer); !ok {
		return errors.New("entry has a malformed signer key")
	}
	if auth.CleanText(e.Name, MaxName) != e.Name {
		return errors.New("entry has a malformed device name")
	}
	if !ed25519.Verify(e.Signer, e.signedBytes(), e.Sig) {
		return fmt.Errorf("entry %d has an invalid signature", e.Seq)
	}
	if e.Seq == 0 {
		if e.Op != OpCreate || len(e.Prev) != 0 || !bytes.Equal(e.Device, e.Signer) || len(e.Email) != sha256.Size {
			return errors.New("the log does not start with a valid create entry")
		}
		h := e.Hash()
		*s = State{ID: h, EmailHash: e.Email, Len: 1, Head: h,
			Members: []Member{{Key: ed25519.PublicKey(e.Device), Name: e.Name, Seq: 0, AddedBy: ed25519.PublicKey(e.Signer), Added: time.Unix(e.Time, 0)}}}
		return nil
	}
	if !bytes.Equal(e.Prev, s.Head) {
		return fmt.Errorf("entry %d does not follow entry %d", e.Seq, e.Seq-1)
	}
	if len(e.Email) != 0 {
		return errors.New("only the create entry names an email")
	}
	if !s.IsMember(e.Signer) {
		return fmt.Errorf("entry %d is signed by a device that is not a member", e.Seq)
	}
	switch e.Op {
	case OpAdd:
		if s.IsMember(e.Device) {
			return fmt.Errorf("entry %d adds a device that is already a member", e.Seq)
		}
		if len(s.Members) >= MaxMembers {
			return fmt.Errorf("an account has at most %d devices", MaxMembers)
		}
		s.Members = append(s.Members, Member{Key: ed25519.PublicKey(e.Device), Name: e.Name, Seq: e.Seq,
			AddedBy: ed25519.PublicKey(e.Signer), Added: time.Unix(e.Time, 0)})
	case OpRemove:
		i, ok := s.member(e.Device)
		if !ok {
			return fmt.Errorf("entry %d removes a device that is not a member", e.Seq)
		}
		s.Members = append(s.Members[:i:i], s.Members[i+1:]...)
	default:
		return fmt.Errorf("entry %d has unknown operation %q", e.Seq, e.Op)
	}
	s.Len++
	s.Head = e.Hash()
	return nil
}

// Verify replays a whole log.
func Verify(log []Entry) (*State, error) {
	if len(log) == 0 {
		return nil, errors.New("empty account log")
	}
	if len(log) > MaxEntries {
		return nil, fmt.Errorf("account log longer than %d entries", MaxEntries)
	}
	s := &State{}
	for _, e := range log {
		if err := s.Apply(e); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// Extends reports whether next is old with zero or more entries appended:
// the only way an honest log changes. Both must already be verified.
func Extends(old, next []Entry) bool {
	if len(next) < len(old) {
		return false
	}
	for i := range old {
		if !bytes.Equal(old[i].Hash(), next[i].Hash()) {
			return false
		}
	}
	return true
}

// Create makes the first entry of a new account, signed by its first
// device.
func Create(id *auth.Identity, normalizedEmail, name string) Entry {
	e := Entry{Seq: 0, Op: OpCreate, Device: id.Public(), Name: auth.CleanText(name, MaxName),
		Email: EmailHash(normalizedEmail), Time: time.Now().Unix()}
	e.sign(id)
	return e
}

// Next makes the next entry of the log s describes, signed by id, which
// must be a member.
func (s *State) Next(id *auth.Identity, op string, device ed25519.PublicKey, name string) (Entry, error) {
	e := Entry{Seq: s.Len, Prev: s.Head, Op: op, Device: device, Name: auth.CleanText(name, MaxName), Time: time.Now().Unix()}
	e.sign(id)
	probe := *s
	probe.Members = append([]Member(nil), s.Members...)
	if err := probe.Apply(e); err != nil {
		return Entry{}, err
	}
	return e, nil
}

// Fingerprint is how people compare an account ID between devices.
func Fingerprint(id []byte) string { return auth.Fingerprint(id) }

// NormalizeFingerprint reduces a typed fingerprint (any case, with or
// without colons or spaces) to the form Fingerprint prints, or "" if it is
// not a complete one.
func NormalizeFingerprint(s string) string {
	var hex []byte
	for _, r := range strings.ToLower(s) {
		switch {
		case r == ':' || r == ' ' || r == '-':
		case (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f'):
			hex = append(hex, byte(r))
		default:
			return ""
		}
	}
	if len(hex) != 20 {
		return ""
	}
	out := make([]byte, 0, 24)
	for i := 0; i < 20; i += 4 {
		if i > 0 {
			out = append(out, ':')
		}
		out = append(out, hex[i:i+4]...)
	}
	return string(out)
}
