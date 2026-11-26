package dial

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/config"
)

// PeersFile is the client's saved-peers file inside the config directory.
const PeersFile = "peers.json"

// Peer is what the client remembers about one host.
type Peer struct {
	// Target is how the host is dialled: its 9-digit ID or "host:port".
	Target string `json:"target"`
	// Alias is an optional name usable in place of Target on the command
	// line.
	Alias string `json:"alias,omitempty"`
	// HostKey is the host's identity key (auth.EncodeKey), pinned at the
	// first successful login. A host that later presents another key is
	// refused.
	HostKey string `json:"host_key,omitempty"`
	// KeyLogin says the host's owner has authorised this machine's
	// identity key (`remohop host authorize`), so logins need no secret. Set
	// by `remohop peers trust`, which also pins HostKey.
	KeyLogin bool `json:"key_login,omitempty"`
	// TokenID and Token are the pairing credential from `remohop <ID> pair`.
	TokenID  string     `json:"token_id,omitempty"`
	Token    []byte     `json:"token,omitempty"`
	PairedAt *time.Time `json:"paired_at,omitempty"`
	// Password is the host's password, saved only when the user asked a
	// gateway (the desktop app, a mobile app, `remohop web`) to remember
	// the login and the host does not hand out pairings (quick support,
	// the desktop app's own host). It is sent only to the pinned HostKey
	// and is dropped when the host rejects it. Like the tokens, it is a
	// login credential: the file is 0600.
	Password string `json:"password,omitempty"`
	// FirstSeen is when the host key was pinned.
	FirstSeen time.Time `json:"first_seen"`
	// LastSeen is when this machine last found the host online: a
	// successful login, or `remohop devices` seeing it registered. It is this
	// machine's own observation; the rendezvous server is not asked when
	// a host was last around.
	LastSeen *time.Time `json:"last_seen,omitempty"`
}

// Paired reports whether a pairing token is stored for the peer.
func (p Peer) Paired() bool { return p.TokenID != "" && len(p.Token) > 0 }

// SavedPassword reports whether a usable saved password is stored: one
// with a pinned host key to send it to.
func (p Peer) SavedPassword() bool { return p.Password != "" && p.HostKey != "" }

// Store is the saved-peers file: pinned host keys, pairing tokens and
// aliases. It is mode 0600 because the tokens are login credentials. All
// operations go to the file, with a cross-process lock around changes, so
// concurrent remohop commands do not lose each other's updates.
type Store struct {
	path string
}

// OpenStore returns the store in dir (the remohop config directory if empty).
func OpenStore(dir string) (*Store, error) {
	if dir == "" {
		var err error
		if dir, err = config.Dir(); err != nil {
			return nil, err
		}
	}
	return &Store{path: filepath.Join(dir, PeersFile)}, nil
}

// List returns every saved peer in the order they were first seen.
func (s *Store) List() ([]Peer, error) {
	b, err := config.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var peers []Peer
	if err := json.Unmarshal(b, &peers); err != nil {
		return nil, fmt.Errorf("%s: %w", s.path, err)
	}
	return peers, nil
}

// Get returns the saved peer for a target.
func (s *Store) Get(target string) (Peer, bool, error) {
	peers, err := s.List()
	if err != nil {
		return Peer{}, false, err
	}
	for _, p := range peers {
		if p.Target == target {
			return p, true, nil
		}
	}
	return Peer{}, false, nil
}

// ByAlias returns the saved peer with the given alias.
func (s *Store) ByAlias(alias string) (Peer, bool, error) {
	peers, err := s.List()
	if err != nil {
		return Peer{}, false, err
	}
	for _, p := range peers {
		if p.Alias != "" && p.Alias == alias {
			return p, true, nil
		}
	}
	return Peer{}, false, nil
}

func (s *Store) modify(fn func([]Peer) ([]Peer, error)) error {
	unlock, err := config.Lock(s.path)
	if err != nil {
		return err
	}
	defer unlock()
	peers, err := s.List()
	if err != nil {
		return err
	}
	if peers, err = fn(peers); err != nil {
		return err
	}
	b, err := json.MarshalIndent(peers, "", "  ")
	if err != nil {
		return err
	}
	return config.WriteFileAtomic(s.path, append(b, '\n'), 0o600)
}

// Update applies fn to the saved peer for target, creating the entry if
// needed.
func (s *Store) Update(target string, fn func(*Peer) error) error {
	return s.modify(func(peers []Peer) ([]Peer, error) {
		for i := range peers {
			if peers[i].Target == target {
				return peers, fn(&peers[i])
			}
		}
		p := Peer{Target: target, FirstSeen: time.Now().UTC().Truncate(time.Second)}
		if err := fn(&p); err != nil {
			return nil, err
		}
		return append(peers, p), nil
	})
}

// SetAlias names target. An alias is unique: it is taken away from any
// other peer that had it. An empty alias removes target's name (and adds
// no entry for a target that is not saved).
func (s *Store) SetAlias(target, alias string) error {
	if alias == "" {
		return s.modify(func(peers []Peer) ([]Peer, error) {
			for i := range peers {
				if peers[i].Target == target {
					peers[i].Alias = ""
				}
			}
			return peers, nil
		})
	}
	if err := CheckAlias(alias); err != nil {
		return err
	}
	return s.modify(func(peers []Peer) ([]Peer, error) {
		found := false
		for i := range peers {
			switch {
			case peers[i].Target == target:
				peers[i].Alias = alias
				found = true
			case peers[i].Alias == alias:
				peers[i].Alias = ""
			}
		}
		if !found {
			peers = append(peers, Peer{Target: target, Alias: alias, FirstSeen: time.Now().UTC().Truncate(time.Second)})
		}
		return peers, nil
	})
}

// SetPassword saves (or, when password is empty, removes) the password of
// the saved peer target. A peer that is not saved is an error: the password
// only ever goes with a pinned host key.
func (s *Store) SetPassword(target, password string) error {
	return s.modify(func(peers []Peer) ([]Peer, error) {
		for i := range peers {
			if peers[i].Target == target {
				if password != "" && peers[i].HostKey == "" {
					return nil, fmt.Errorf("%s: no pinned host key to save a password for", target)
				}
				peers[i].Password = password
				return peers, nil
			}
		}
		if password == "" {
			return peers, nil
		}
		return nil, fmt.Errorf("%s is not a saved host", target)
	})
}

// Forget removes the peers whose target or alias equals ref, with their
// pinned key and pairing token, and reports how many were removed.
func (s *Store) Forget(ref string) (int, error) {
	n := 0
	err := s.modify(func(peers []Peer) ([]Peer, error) {
		kept := peers[:0]
		for _, p := range peers {
			if p.Target == ref || (p.Alias != "" && p.Alias == ref) {
				n++
				continue
			}
			kept = append(kept, p)
		}
		return kept, nil
	})
	return n, err
}

// aliasRE: a letter (any script, so "办公室电脑" works), then letters,
// combining marks, digits, '.', '_', '-' and single inner spaces; at most
// 63 characters. No ':' or '/', and it starts with a letter, so it cannot
// be mistaken for an ID or an address.
var aliasRE = regexp.MustCompile(`^\p{L}(?:[\p{L}\p{M}\p{N}._-]| [\p{L}\p{M}\p{N}._-]){0,62}$`)

// CheckAlias validates an alias: a short name that cannot be mistaken for
// an ID, an address or a remohop command.
func CheckAlias(alias string) error {
	if !aliasRE.MatchString(alias) || utf8.RuneCountInString(alias) > 63 {
		return fmt.Errorf("invalid alias %q: start with a letter; then use letters, digits, single spaces, '.', '_' or '-' (at most 63 characters)", alias)
	}
	switch alias {
	case "help", "version":
		return fmt.Errorf("alias %q would shadow a remohop command", alias)
	}
	if cli.Lookup(alias) != nil {
		return fmt.Errorf("alias %q would shadow the remohop command of that name", alias)
	}
	return nil
}
