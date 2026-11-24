package signal

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/readori/RemoHop/internal/account/ledger"
	"github.com/readori/RemoHop/internal/config"
)

// accountStore keeps the server's side of accounts on disk, in the same
// way idStore keeps IDs: an append-only journal, one JSON object per line,
//
//	{"put": ACCOUNT}                     the account's whole record, as it is now
//	{"del": EMAILHASH}                   the account was deleted
//	{"seen": {"a": EMAILHASH, "k": KEY, "t": UNIX}}   a device was around
//
// A put or del is written with a single write and fsynced before the
// client is answered; a crash can at worst leave a torn last line, which
// belongs to an answer nobody received and is cut off on the next start.
// "seen" lines are written at most once per seenAccountEvery per device and
// not synced. The journal is rewritten compactly (temporary file, renamed
// into place) when it has grown to several times the number of accounts.
//
// Nothing secret is stored in the clear. Accounts are indexed by
// ledger.EmailHash of the address, not the address; sessions by the
// SHA-256 of their token. Login codes are never written at all: they live
// in memory as MACs under a key that is made at start and never stored.
//
// Everything is bounded: maxAccounts accounts, and per account the ledger's
// limits, maxSessions sessions, maxPending join requests.
type accountStore struct {
	mu    sync.Mutex
	path  string
	f     *os.File
	lines int
	max   int
	now   func() time.Time

	byEmail  map[string]*acctRec // hex of the email hash
	byToken  map[[32]byte]*acctRec
	byMember map[[32]byte][]*acctRec
}

const (
	maxSessions      = 64
	maxPending       = 8
	pendingTTL       = time.Hour
	seenAccountEvery = time.Hour
)

type acctRec struct {
	Email    []byte         `json:"email"`
	Created  int64          `json:"created"`
	Log      []ledger.Entry `json:"log,omitempty"`
	Sessions []sessRec      `json:"sessions,omitempty"`
	Pending  []pendRec      `json:"pending,omitempty"`
	Devices  []devRec       `json:"devices,omitempty"`

	state *ledger.State // the verified log; nil while it is empty
}

type sessRec struct {
	Hash    []byte `json:"hash"` // SHA-256 of the token
	Key     []byte `json:"key"`  // the device the session belongs to
	Created int64  `json:"created"`
}

type pendRec struct {
	Key      []byte `json:"key"`
	Name     string `json:"name,omitempty"`
	Platform string `json:"platform,omitempty"`
	From     string `json:"from,omitempty"`
	At       int64  `json:"at"`
}

// devRec is what a device last said about itself; display only.
type devRec struct {
	Key       []byte `json:"key"`
	Name      string `json:"name,omitempty"`
	Platform  string `json:"platform,omitempty"`
	Seen      int64  `json:"seen,omitempty"`
	persisted int64
}

type journalLine struct {
	Put  *acctRec  `json:"put,omitempty"`
	Del  string    `json:"del,omitempty"`
	Seen *seenLine `json:"seen,omitempty"`
}

type seenLine struct {
	A string `json:"a"`
	K []byte `json:"k"`
	T int64  `json:"t"`
}

var errAccountsFull = errors.New("account store full")

func tokenHash(token []byte) [32]byte { return sha256.Sum256(token) }

func openAccountStore(path string, max int) (*accountStore, error) {
	s := &accountStore{path: path, max: max, now: time.Now, byEmail: map[string]*acctRec{}, byToken: map[[32]byte]*acctRec{},
		byMember: map[[32]byte][]*acctRec{}}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	good, err := s.load(f)
	if err == nil {
		err = f.Truncate(good)
	}
	if err == nil {
		_, err = f.Seek(good, 0)
	}
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	s.f = f
	for k, r := range s.byEmail {
		if len(r.Log) > 0 {
			st, err := ledger.Verify(r.Log)
			if err != nil {
				f.Close()
				return nil, fmt.Errorf("%s: account %s has an invalid log: %w", path, k[:12], err)
			}
			r.state = st
		}
		s.index(r)
	}
	if s.lines != len(s.byEmail) {
		if err := s.compact(); err != nil {
			if s.f != nil {
				s.f.Close()
			}
			return nil, err
		}
	}
	return s, nil
}

func (s *accountStore) load(f *os.File) (int64, error) {
	var good int64
	r := bufio.NewReaderSize(f, 64<<10)
	for lineNo := 1; ; lineNo++ {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return good, nil // a torn tail is dropped
		}
		var jl journalLine
		if err := json.Unmarshal(line, &jl); err != nil {
			return 0, fmt.Errorf("corrupt journal at line %d", lineNo)
		}
		switch {
		case jl.Put != nil && len(jl.Put.Email) == sha256.Size:
			s.byEmail[hex.EncodeToString(jl.Put.Email)] = jl.Put
		case jl.Del != "":
			delete(s.byEmail, jl.Del)
		case jl.Seen != nil:
			if r := s.byEmail[jl.Seen.A]; r != nil {
				if d := r.device(jl.Seen.K); d != nil && jl.Seen.T > d.Seen {
					d.Seen, d.persisted = jl.Seen.T, jl.Seen.T
				}
			}
		default:
			return 0, fmt.Errorf("corrupt journal at line %d", lineNo)
		}
		s.lines++
		good += int64(len(line))
	}
}

// index adds r's sessions and members to the indexes; unindex takes them
// out. A change to either goes between the two. Callers hold s.mu.
func (s *accountStore) index(r *acctRec) {
	for _, ss := range r.Sessions {
		if len(ss.Hash) == 32 {
			s.byToken[[32]byte(ss.Hash)] = r
		}
	}
	if r.state != nil {
		for _, m := range r.state.Members {
			k := [32]byte(m.Key)
			s.byMember[k] = append(s.byMember[k], r)
		}
	}
}

func (s *accountStore) unindex(r *acctRec) {
	for _, ss := range r.Sessions {
		if len(ss.Hash) == 32 {
			delete(s.byToken, [32]byte(ss.Hash))
		}
	}
	if r.state != nil {
		for _, m := range r.state.Members {
			k := [32]byte(m.Key)
			if l := slices.DeleteFunc(s.byMember[k], func(x *acctRec) bool { return x == r }); len(l) > 0 {
				s.byMember[k] = l
			} else {
				delete(s.byMember, k)
			}
		}
	}
}

func (s *accountStore) writeLine(jl journalLine, sync bool) error {
	b, err := json.Marshal(jl)
	if err != nil {
		return err
	}
	if _, err := s.f.Write(append(b, '\n')); err != nil {
		return err
	}
	s.lines++
	if sync {
		return s.f.Sync()
	}
	return nil
}

// put persists r. Callers hold s.mu.
func (s *accountStore) put(r *acctRec) error {
	for i := range r.Devices {
		r.Devices[i].persisted = r.Devices[i].Seen
	}
	if err := s.writeLine(journalLine{Put: r}, true); err != nil {
		return err
	}
	if s.lines > 2*len(s.byEmail)+1024 {
		return s.compact()
	}
	return nil
}

func (s *accountStore) compact() error {
	var buf bytes.Buffer
	for _, r := range s.byEmail {
		b, err := json.Marshal(journalLine{Put: r})
		if err != nil {
			return err
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	if s.f != nil {
		s.f.Sync()
		s.f.Close()
		s.f = nil
	}
	werr := config.WriteFileAtomic(s.path, buf.Bytes(), 0o600)
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return errors.Join(werr, err)
	}
	s.f = f
	if werr != nil {
		return werr
	}
	s.lines = len(s.byEmail)
	return nil
}

func (s *accountStore) close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f == nil {
		return nil
	}
	s.f.Sync()
	return s.f.Close()
}

func (r *acctRec) device(key []byte) *devRec {
	for i := range r.Devices {
		if bytes.Equal(r.Devices[i].Key, key) {
			return &r.Devices[i]
		}
	}
	return nil
}

func (r *acctRec) isMember(key []byte) bool {
	return r.state != nil && r.state.IsMember(ed25519.PublicKey(key))
}

func (r *acctRec) isPending(key []byte) bool {
	return slices.ContainsFunc(r.Pending, func(p pendRec) bool { return bytes.Equal(p.Key, key) })
}

// prune drops what has outlived its use: expired join requests, and
// display records of devices that are neither members, nor waiting, nor
// logged in.
func (r *acctRec) prune(now time.Time) {
	r.Pending = slices.DeleteFunc(r.Pending, func(p pendRec) bool {
		return now.Sub(time.Unix(p.At, 0)) > pendingTTL || r.isMember(p.Key)
	})
	r.Devices = slices.DeleteFunc(r.Devices, func(d devRec) bool {
		return !r.isMember(d.Key) && !r.isPending(d.Key) &&
			!slices.ContainsFunc(r.Sessions, func(ss sessRec) bool { return bytes.Equal(ss.Key, d.Key) })
	})
}

// setDevice records what a device says about itself and reports whether
// that changed anything.
func (r *acctRec) setDevice(key []byte, name, platform string, now time.Time) bool {
	d := r.device(key)
	if d == nil {
		r.Devices = append(r.Devices, devRec{Key: key, Name: name, Platform: platform, Seen: now.Unix()})
		return true
	}
	d.Seen = now.Unix()
	if d.Name == name && d.Platform == platform {
		return false
	}
	d.Name, d.Platform = name, platform
	return true
}

// session finds the account and session of a token. Callers hold s.mu.
func (s *accountStore) session(token []byte) (*acctRec, *sessRec) {
	h := tokenHash(token)
	r := s.byToken[h]
	if r == nil {
		return nil, nil
	}
	for i := range r.Sessions {
		if bytes.Equal(r.Sessions[i].Hash, h[:]) {
			return r, &r.Sessions[i]
		}
	}
	return nil, nil
}

// login binds key to the account for emailHash, creating the account if
// needed, and returns the new session token and the account. Callers hold
// s.mu.
func (s *accountStore) login(emailHash, key []byte, token []byte, name, platform, from string) (*acctRec, error) {
	k := hex.EncodeToString(emailHash)
	now := s.now()
	r := s.byEmail[k]
	if r == nil {
		if len(s.byEmail) >= s.max {
			return nil, errAccountsFull
		}
		r = &acctRec{Email: emailHash, Created: now.Unix()}
		s.byEmail[k] = r
	}
	s.unindex(r)
	// One session per device: logging in again replaces the old one.
	r.Sessions = slices.DeleteFunc(r.Sessions, func(ss sessRec) bool { return bytes.Equal(ss.Key, key) })
	if len(r.Sessions) >= maxSessions {
		r.Sessions = r.Sessions[len(r.Sessions)-maxSessions+1:]
	}
	h := tokenHash(token)
	r.Sessions = append(r.Sessions, sessRec{Hash: h[:], Key: key, Created: now.Unix()})
	if r.state != nil && !r.isMember(key) {
		r.Pending = slices.DeleteFunc(r.Pending, func(p pendRec) bool { return bytes.Equal(p.Key, key) })
		if len(r.Pending) >= maxPending {
			r.Pending = r.Pending[1:]
		}
		r.Pending = append(r.Pending, pendRec{Key: key, Name: name, Platform: platform, From: from, At: now.Unix()})
	}
	r.setDevice(key, name, platform, now)
	r.prune(now)
	s.index(r)
	return r, s.put(r)
}

// revokeKey ends every session of key in r. Callers hold s.mu.
func (s *accountStore) revokeKey(r *acctRec, key []byte) {
	s.unindex(r)
	r.Sessions = slices.DeleteFunc(r.Sessions, func(ss sessRec) bool { return bytes.Equal(ss.Key, key) })
	s.index(r)
}

// append adds a verified entry to r's log. Callers hold s.mu.
func (s *accountStore) appendEntry(r *acctRec, e ledger.Entry) error {
	st := &ledger.State{}
	if r.state != nil {
		cp := *r.state
		cp.Members = slices.Clone(r.state.Members)
		st = &cp
	}
	if e.Seq == 0 && r.state != nil {
		return errConflict
	}
	if e.Seq != st.Len {
		return errConflict
	}
	if err := st.Apply(e); err != nil {
		return err
	}
	if e.Seq == 0 && !bytes.Equal(e.Email, r.Email) {
		return errors.New("the create entry names another email address")
	}
	s.unindex(r)
	if e.Seq == 0 {
		r.Log = nil
		r.Pending = nil
	}
	r.Log = append(r.Log, e)
	r.state = st
	if e.Op == ledger.OpRemove {
		r.Sessions = slices.DeleteFunc(r.Sessions, func(ss sessRec) bool { return bytes.Equal(ss.Key, e.Device) })
	}
	if len(st.Members) == 0 {
		// Everybody left: the account starts over with whoever logs in
		// next, under a new account ID.
		r.Log, r.state = nil, nil
	}
	s.index(r)
	r.prune(s.now())
	return s.put(r)
}

var errConflict = errors.New("the account's log has changed")

func (s *accountStore) del(r *acctRec) error {
	s.unindex(r)
	k := hex.EncodeToString(r.Email)
	delete(s.byEmail, k)
	return s.writeLine(journalLine{Del: k}, true)
}

// seen records that key, a device of r, is around now. Callers hold s.mu.
func (s *accountStore) seen(r *acctRec, key []byte) {
	d := r.device(key)
	if d == nil {
		return
	}
	now := s.now().Unix()
	d.Seen = now
	if now-d.persisted < int64(seenAccountEvery/time.Second) {
		return
	}
	if s.writeLine(journalLine{Seen: &seenLine{A: hex.EncodeToString(r.Email), K: key, T: now}}, false) == nil {
		d.persisted = now
	}
}

// accountsOf returns the accounts key is a member of. Callers hold s.mu.
func (s *accountStore) accountsOf(key []byte) []*acctRec {
	if len(key) != 32 {
		return nil
	}
	return s.byMember[[32]byte(key)]
}
