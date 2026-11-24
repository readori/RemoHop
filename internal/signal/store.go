package signal

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/config"
)

// idStore maps identity keys to their 9-digit IDs and keeps the mapping on
// disk so IDs survive restarts.
//
// The file is an append-only journal of three kinds of line:
//
//	<key> <id> <created> [<seen>]   an ID was allocated to a key
//	seen <id> <time>                the ID's host registered again
//	drop <id>                       the ID expired and is free again
//
// (times in Unix seconds). An allocation is written with a single write
// call and fsynced before the ID is handed out, so every ID a host was ever
// told about is on disk. A crash can at worst leave a torn final line; it
// belongs to something nobody was told about and is cut off on the next
// start. Appending keeps the cost of a registration independent of how many
// IDs exist.
//
// IDs whose host has not registered for expiry are dropped, so that the
// table cannot be filled for good by keys that are never used again. "seen"
// lines are written at most once per seenEvery per ID, and without fsync:
// losing one only makes an idle ID expire a few days early. The journal is
// rewritten compactly (to a temporary file, renamed into place) when it has
// grown to several times the size of the table.
type idStore struct {
	mu     sync.Mutex
	path   string
	f      *os.File
	byKey  map[[ed25519.PublicKeySize]byte]*idEntry
	byID   map[string]*idEntry
	max    int
	expiry time.Duration
	lines  int // lines in the journal file
	now    func() time.Time
}

type idEntry struct {
	key       [ed25519.PublicKeySize]byte
	id        string
	created   int64
	seen      int64 // last registration (or creation)
	persisted int64 // the value of seen that is on disk
}

// seenEvery bounds how often a "seen" line is written for one ID.
const seenEvery = 7 * 24 * time.Hour

var errStoreFull = errors.New("id store full")

func openStore(path string, max int, expiry time.Duration) (*idStore, error) {
	s := &idStore{
		path:   path,
		byKey:  map[[ed25519.PublicKeySize]byte]*idEntry{},
		byID:   map[string]*idEntry{},
		max:    max,
		expiry: expiry,
		now:    time.Now,
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	good, err := s.load(f)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := f.Truncate(good); err != nil {
		f.Close()
		return nil, err
	}
	if _, err := f.Seek(good, 0); err != nil {
		f.Close()
		return nil, err
	}
	s.f = f
	s.expire(nil)
	if s.lines != len(s.byKey) {
		if err := s.compact(); err != nil {
			if s.f != nil {
				s.f.Close()
			}
			return nil, err
		}
	}
	return s, nil
}

// load replays the journal and returns the offset just past the last
// complete line.
func (s *idStore) load(f *os.File) (int64, error) {
	var good int64
	r := bufio.NewReader(f)
	for lineNo := 1; ; lineNo++ {
		line, err := r.ReadBytes('\n')
		if err != nil {
			// EOF: anything in line is a torn tail and is dropped.
			return good, nil
		}
		if !s.replay(bytes.Fields(line)) {
			return 0, fmt.Errorf("corrupt journal at line %d", lineNo)
		}
		s.lines++
		good += int64(len(line))
	}
}

func (s *idStore) replay(f [][]byte) bool {
	if len(f) < 2 {
		return false
	}
	switch string(f[0]) {
	case "seen":
		if len(f) != 3 {
			return false
		}
		t, err := strconv.ParseInt(string(f[2]), 10, 64)
		if err != nil {
			return false
		}
		// A line for an ID that has since been dropped is history.
		if e := s.byID[string(f[1])]; e != nil && t > e.seen {
			e.seen, e.persisted = t, t
		}
		return true
	case "drop":
		if len(f) != 2 {
			return false
		}
		if e := s.byID[string(f[1])]; e != nil {
			delete(s.byID, e.id)
			delete(s.byKey, e.key)
		}
		return true
	}
	if len(f) != 3 && len(f) != 4 {
		return false
	}
	key, err := auth.DecodeKey(string(f[0]))
	id := string(f[1])
	created, cerr := strconv.ParseInt(string(f[2]), 10, 64)
	if err != nil || cerr != nil || !validID(id) {
		return false
	}
	e := &idEntry{key: [ed25519.PublicKeySize]byte(key), id: id, created: created, seen: created, persisted: created}
	if len(f) == 4 {
		seen, err := strconv.ParseInt(string(f[3]), 10, 64)
		if err != nil {
			return false
		}
		e.seen, e.persisted = seen, seen
	}
	// Later lines win: after an ID expired, it or its key may appear again.
	if old := s.byKey[e.key]; old != nil {
		delete(s.byID, old.id)
	}
	if old := s.byID[id]; old != nil {
		delete(s.byKey, old.key)
	}
	s.byKey[e.key] = e
	s.byID[id] = e
	return true
}

func validID(id string) bool {
	if len(id) != 9 || id[0] == '0' {
		return false
	}
	for i := 0; i < len(id); i++ {
		if id[i] < '0' || id[i] > '9' {
			return false
		}
	}
	return true
}

func (e *idEntry) line() string {
	return fmt.Sprintf("%s %s %d %d\n", auth.EncodeKey(e.key[:]), e.id, e.created, e.seen)
}

// expire drops every ID not seen for the expiry time, except those online
// reports as connected right now. Callers hold s.mu (or are openStore).
func (s *idStore) expire(online func(id string) bool) (dropped int) {
	if s.expiry <= 0 {
		return 0
	}
	cutoff := s.now().Add(-s.expiry).Unix()
	var journal bytes.Buffer
	for _, e := range s.byID {
		if e.seen >= cutoff || (online != nil && online(e.id)) {
			continue
		}
		fmt.Fprintf(&journal, "drop %s\n", e.id)
		delete(s.byID, e.id)
		delete(s.byKey, e.key)
		dropped++
	}
	if dropped > 0 {
		// Not synced: a drop that is lost is simply made again.
		if _, err := s.f.Write(journal.Bytes()); err == nil {
			s.lines += dropped
		}
	}
	return dropped
}

// compact rewrites the journal as one line per live ID. The journal is
// closed while it is replaced: Windows does not let a file be renamed over
// one that is open.
func (s *idStore) compact() error {
	var buf bytes.Buffer
	for _, e := range s.byID {
		buf.WriteString(e.line())
	}
	if s.f != nil {
		s.f.Sync()
		s.f.Close()
		s.f = nil
	}
	werr := config.WriteFileAtomic(s.path, buf.Bytes(), 0o600)
	// Reopen whichever file is there now, the new one or, if the
	// replacement failed, the old one, which is still complete.
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return errors.Join(werr, err)
	}
	s.f = f
	if werr != nil {
		return werr
	}
	for _, e := range s.byID {
		e.persisted = e.seen
	}
	s.lines = len(s.byID)
	return nil
}

// maintain is the periodic housekeeping: IDs that are online count as seen,
// idle ones expire, and an overgrown journal is compacted.
func (s *idStore) maintain(online func(id string) bool) (dropped int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, e := range s.byID {
		if online(id) {
			s.touch(e)
		}
	}
	dropped = s.expire(online)
	if s.lines > 2*len(s.byID)+4096 {
		err = s.compact()
	}
	return dropped, err
}

// touch records that e's host is alive now. Callers hold s.mu.
func (s *idStore) touch(e *idEntry) {
	now := s.now().Unix()
	e.seen = now
	if now-e.persisted < int64(seenEvery/time.Second) {
		return
	}
	if _, err := fmt.Fprintf(s.f, "seen %s %d\n", e.id, now); err == nil {
		e.persisted = now
		s.lines++
	}
}

// lookup returns the ID already assigned to key, if any.
func (s *idStore) lookup(key ed25519.PublicKey) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.byKey[[ed25519.PublicKeySize]byte(key)]; e != nil {
		return e.id, true
	}
	return "", false
}

// assign returns key's ID, allocating a random unused one on first sight,
// and records that the key's host is alive. online is consulted when room
// has to be made by expiring idle IDs.
func (s *idStore) assign(key ed25519.PublicKey, online func(id string) bool) (id string, isNew bool, err error) {
	k := [ed25519.PublicKeySize]byte(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.byKey[k]; e != nil {
		s.touch(e)
		return e.id, false, nil
	}
	if len(s.byKey) >= s.max && s.expire(online) == 0 {
		return "", false, errStoreFull
	}
	// Uniform over 100000000..999999999. IDs are random rather than
	// sequential so that knowing one says nothing about the others.
	span := big.NewInt(900_000_000)
	for {
		n, err := rand.Int(rand.Reader, span)
		if err != nil {
			return "", false, err
		}
		id = strconv.FormatInt(100_000_000+n.Int64(), 10)
		if s.byID[id] == nil {
			break
		}
	}
	now := s.now().Unix()
	e := &idEntry{key: k, id: id, created: now, seen: now, persisted: now}
	if _, err := s.f.WriteString(e.line()); err != nil {
		return "", false, err
	}
	if err := s.f.Sync(); err != nil {
		return "", false, err
	}
	s.lines++
	s.byKey[k] = e
	s.byID[id] = e
	return id, true, nil
}

// keyOf returns the key an ID is assigned to.
func (s *idStore) keyOf(id string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.byID[id]; e != nil {
		return append([]byte(nil), e.key[:]...), true
	}
	return nil, false
}

func (s *idStore) known(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byID[id] != nil
}

func (s *idStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.byID)
}

func (s *idStore) close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f == nil {
		return nil
	}
	s.f.Sync()
	return s.f.Close()
}
