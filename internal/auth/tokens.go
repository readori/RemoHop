package auth

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"time"

	"github.com/readori/RemoHop/internal/config"
)

const (
	// TokensFile is the host's pairing-token file inside the config directory.
	TokensFile = "paired.json"
	// TokenSize is the length of a pairing token's secret in bytes.
	TokenSize = 32
	// maxTokens bounds how many clients can be paired with one host.
	maxTokens = 256
	// MaxTokenName bounds the label a client may attach to its pairing.
	MaxTokenName = 64
)

// Token is one pairing: a 256-bit secret a specific client identity can log
// in with instead of the password.
type Token struct {
	ID        string    `json:"id"`               // public identifier, sent in the clear inside TLS
	Name      string    `json:"name"`             // label chosen by the client
	Secret    []byte    `json:"secret,omitempty"` // the credential itself; empty for an authorised key
	ClientKey []byte    `json:"client_key"`
	Created   time.Time `json:"created"`
	// Grant names the grant (internal/grant) this token redeems; empty for
	// an ordinary pairing or authorised key. A grant token may be issued
	// without a ClientKey: the first client to log in with it is bound to
	// it (see Bind).
	Grant string `json:"grant,omitempty"`
	// Parent is the ID of the authorised key a session logged in with
	// when it minted this pairing; empty for a pairing a password login
	// minted. A pairing is no more durable than the credential its
	// session stood on: it is removed with that key, and is not accepted
	// while the key is missing from the store (see valid).
	Parent string `json:"parent,omitempty"`
}

// TokenStore is the host's set of pairing tokens, kept in a 0600 JSON file.
// Every operation goes to the file, under a cross-process lock for changes,
// so `remohop host revoke` run from another process takes effect on the running
// host's very next login.
//
// The secrets are stored as they are: a balanced PAKE needs the same value
// on both ends, so there is nothing weaker to keep. Whoever can read this
// file can also read the identity key next to it.
type TokenStore struct {
	path string
}

// OpenTokenStore returns the store backed by path. The file is created on
// the first Mint.
func OpenTokenStore(path string) *TokenStore { return &TokenStore{path: path} }

func (s *TokenStore) load() ([]Token, error) {
	b, err := config.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var toks []Token
	if err := json.Unmarshal(b, &toks); err != nil {
		return nil, fmt.Errorf("%s: %w", s.path, err)
	}
	for i := range toks {
		// Names were supplied by clients; a file written by an older
		// version may hold ones that are not safe to print.
		toks[i].Name = CleanTokenName(toks[i].Name)
		toks[i].ID = CleanText(toks[i].ID, maxTokenIDLen)
	}
	return toks, nil
}

func (s *TokenStore) update(fn func([]Token) ([]Token, error)) error {
	unlock, err := config.Lock(s.path)
	if err != nil {
		return err
	}
	defer unlock()
	toks, err := s.load()
	if err != nil {
		return err
	}
	if toks, err = fn(toks); err != nil {
		return err
	}
	toks = pruneOrphans(toks)
	b, err := json.MarshalIndent(toks, "", "  ")
	if err != nil {
		return err
	}
	return config.WriteFileAtomic(s.path, append(b, '\n'), 0o600)
}

// CleanTokenName makes a client-supplied label safe to store and print: see
// CleanText.
func CleanTokenName(name string) string { return CleanText(name, MaxTokenName) }

// isKey reports whether t is an authorised key (no secret, no grant).
func (t Token) isKey() bool { return len(t.Secret) == 0 && t.Grant == "" }

// valid reports whether t may be used: a pairing minted from an authorised
// key's session is valid only while that key is still authorised for the
// same client.
func valid(toks []Token, t Token) bool {
	if t.Parent == "" {
		return true
	}
	for _, p := range toks {
		if p.ID == t.Parent && p.isKey() && bytes.Equal(p.ClientKey, t.ClientKey) {
			return true
		}
	}
	return false
}

// pruneOrphans drops the pairings whose parent key is gone, so that every
// way of removing an authorised key (revoke, re-authorisation, account
// trust) removes what its sessions minted too.
func pruneOrphans(toks []Token) []Token {
	kept := make([]Token, 0, len(toks))
	for _, t := range toks {
		if valid(toks, t) {
			kept = append(kept, t)
		}
	}
	return kept
}

// ErrCredentialGone is returned by MintFrom when the credential the
// requesting session logged in with has been removed.
var ErrCredentialGone = errors.New("the credential this session logged in with has been revoked")

// Mint creates a token for clientKey, replacing any token that key already
// has: a client holds one pairing per host, and pairing again rotates it.
// It is MintFrom for a session that logged in with a password.
func (s *TokenStore) Mint(name string, clientKey ed25519.PublicKey) (Token, error) {
	return s.MintFrom(name, clientKey, "")
}

// MintFrom is Mint for a session that logged in with the store entry via
// ("" for a password login). A pairing minted from an authorised key's
// session records that key as its parent and is removed with it; one minted
// from a pairing's session inherits that pairing's parent. Grant tokens
// cannot mint pairings.
func (s *TokenStore) MintFrom(name string, clientKey ed25519.PublicKey, via string) (Token, error) {
	var raw [8 + TokenSize]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return Token{}, err
	}
	tok := Token{
		ID:        hex.EncodeToString(raw[:8]),
		Name:      CleanTokenName(name),
		Secret:    raw[8:],
		ClientKey: clientKey,
		Created:   time.Now().UTC().Truncate(time.Second),
	}
	err := s.update(func(toks []Token) ([]Token, error) {
		if via != "" {
			i := slices.IndexFunc(toks, func(t Token) bool { return t.ID == via })
			switch {
			case i < 0 || !valid(toks, toks[i]) || !bytes.Equal(toks[i].ClientKey, clientKey):
				return nil, ErrCredentialGone
			case toks[i].Grant != "":
				return nil, errors.New("a grant cannot mint a pairing")
			case toks[i].isKey():
				tok.Parent = via
			default:
				tok.Parent = toks[i].Parent
			}
		}
		return replaceFor(toks, tok, false)
	})
	if err != nil {
		return Token{}, err
	}
	return tok, nil
}

// replaceFor adds tok, dropping the entry of the same sort (pairing token
// or authorised key) that tok's client key already had.
func replaceFor(toks []Token, tok Token, keyOnly bool) ([]Token, error) {
	kept := toks[:0]
	for _, t := range toks {
		// Grant tokens live beside a client's ordinary pairing.
		if t.Grant == "" && bytes.Equal(t.ClientKey, tok.ClientKey) && (len(t.Secret) == 0) == keyOnly {
			continue
		}
		kept = append(kept, t)
	}
	if len(kept) >= maxTokens {
		return nil, fmt.Errorf("too many paired clients (%d); revoke some with `remohop host revoke`", maxTokens)
	}
	return append(kept, tok), nil
}

// MintGrant creates the token of a grant, replacing an earlier token of the
// same grant. clientKey may be nil: the token is then bound to the first
// client that logs in with it.
func (s *TokenStore) MintGrant(grant string, clientKey ed25519.PublicKey) (Token, error) {
	var raw [8 + TokenSize]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return Token{}, err
	}
	tok := Token{
		ID:        hex.EncodeToString(raw[:8]),
		Name:      CleanTokenName(grant),
		Secret:    raw[8:],
		ClientKey: clientKey,
		Created:   time.Now().UTC().Truncate(time.Second),
		Grant:     grant,
	}
	err := s.update(func(toks []Token) ([]Token, error) {
		kept := toks[:0]
		for _, t := range toks {
			if t.Grant != grant {
				kept = append(kept, t)
			}
		}
		if len(kept) >= maxTokens {
			return nil, fmt.Errorf("too many paired clients (%d); revoke some with `remohop host revoke`", maxTokens)
		}
		return append(kept, tok), nil
	})
	if err != nil {
		return Token{}, err
	}
	return tok, nil
}

// Bind binds an unbound token to clientKey. It succeeds if the token is
// (now) bound to clientKey, and fails if it is gone or bound to another key.
func (s *TokenStore) Bind(id string, clientKey ed25519.PublicKey) error {
	return s.update(func(toks []Token) ([]Token, error) {
		for i := range toks {
			if toks[i].ID != id || len(toks[i].Secret) != TokenSize {
				continue
			}
			switch {
			case len(toks[i].ClientKey) == 0:
				toks[i].ClientKey = append([]byte(nil), clientKey...)
				return toks, nil
			case bytes.Equal(toks[i].ClientKey, clientKey):
				return toks, nil
			}
			return nil, errors.New("token already redeemed by another client")
		}
		return nil, errors.New("no such token")
	})
}

// RevokeGrant removes the tokens of a grant and reports how many.
func (s *TokenStore) RevokeGrant(grant string) (int, error) {
	n := 0
	err := s.update(func(toks []Token) ([]Token, error) {
		kept := toks[:0]
		for _, t := range toks {
			if t.Grant == grant {
				n++
				continue
			}
			kept = append(kept, t)
		}
		return kept, nil
	})
	return n, err
}

// Authorize lets clientKey log in with its identity key alone, no password
// and no token: the equivalent of a line in ssh's authorized_keys.
func (s *TokenStore) Authorize(name string, clientKey ed25519.PublicKey) (Token, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return Token{}, err
	}
	tok := Token{
		ID:        hex.EncodeToString(raw[:]),
		Name:      CleanTokenName(name),
		ClientKey: clientKey,
		Created:   time.Now().UTC().Truncate(time.Second),
	}
	err := s.update(func(toks []Token) ([]Token, error) { return replaceFor(toks, tok, true) })
	if err != nil {
		return Token{}, err
	}
	return tok, nil
}

// AuthorizedKey finds the authorisation for a client key.
func (s *TokenStore) AuthorizedKey(clientKey ed25519.PublicKey) (Token, bool, error) {
	toks, err := s.load()
	if err != nil {
		return Token{}, false, err
	}
	for _, t := range toks {
		if t.isKey() && bytes.Equal(t.ClientKey, clientKey) {
			return t, true, nil
		}
	}
	return Token{}, false, nil
}

// Exists reports whether an entry (pairing token or authorised key) with
// this ID is still in the store.
func (s *TokenStore) Exists(id string) (bool, error) {
	toks, err := s.load()
	if err != nil {
		return false, err
	}
	for _, t := range toks {
		if t.ID == id {
			return valid(toks, t), nil
		}
	}
	return false, nil
}

// Lookup finds a token by ID.
func (s *TokenStore) Lookup(id string) (Token, bool, error) {
	toks, err := s.load()
	if err != nil {
		return Token{}, false, err
	}
	for _, t := range toks {
		if t.ID == id && len(t.Secret) == TokenSize && valid(toks, t) {
			return t, true, nil
		}
	}
	return Token{}, false, nil
}

// List returns every token, oldest first.
func (s *TokenStore) List() ([]Token, error) { return s.load() }

// Revoke removes the tokens whose ID or name equals ref, or, when ref is an
// identity fingerprint, every pairing and authorised key of that identity
// (not its grant tokens: those go with their grant). The pairings minted by
// sessions of a removed authorised key go too. It reports how many entries
// were removed in all.
func (s *TokenStore) Revoke(ref string) (int, error) {
	n := 0
	err := s.update(func(toks []Token) ([]Token, error) {
		before := len(toks)
		kept := toks[:0]
		for _, t := range toks {
			if t.ID == ref || t.Name == ref || t.Grant == "" && len(t.ClientKey) != 0 && Fingerprint(t.ClientKey) == ref {
				continue
			}
			kept = append(kept, t)
		}
		kept = pruneOrphans(kept)
		n = before - len(kept)
		return kept, nil
	})
	return n, err
}
