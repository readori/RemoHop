package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"path/filepath"
	"testing"
)

func newKey(t *testing.T) ed25519.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub
}

// A pairing minted on an authorised key's login is that key's child: it is
// refused and removed once the key is gone, however the key goes.
func TestPairingParent(t *testing.T) {
	s := OpenTokenStore(filepath.Join(t.TempDir(), TokensFile))
	key, other := newKey(t), newKey(t)
	auth, err := s.Authorize("account:laptop", key)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := s.MintFrom("vm", key, auth.ID)
	if err != nil {
		t.Fatal(err)
	}
	if tok.Parent != auth.ID {
		t.Fatalf("parent %q, want %q", tok.Parent, auth.ID)
	}
	// Re-pairing from the pairing's own session keeps the parent.
	tok2, err := s.MintFrom("vm2", key, tok.ID)
	if err != nil || tok2.Parent != auth.ID {
		t.Fatalf("re-pair: %+v %v", tok2, err)
	}
	if ok, _ := s.Exists(tok.ID); ok {
		t.Fatal("the replaced pairing still exists")
	}
	// Another client cannot mint on this key's entry.
	if _, err := s.MintFrom("x", other, auth.ID); !errors.Is(err, ErrCredentialGone) {
		t.Fatalf("mint on another client's entry: %v", err)
	}
	// A password-minted pairing of another client is independent.
	free, err := s.Mint("desk", other)
	if err != nil || free.Parent != "" {
		t.Fatalf("password pairing: %+v %v", free, err)
	}

	// Another process drops the key without knowing about children (an
	// older version, a hand edit): the child is not accepted any more.
	if err := s.update(func(toks []Token) ([]Token, error) {
		var kept []Token
		for _, t := range toks {
			if t.ID != auth.ID {
				kept = append(kept, t)
			}
		}
		return kept, nil
	}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.Exists(tok2.ID); ok {
		t.Fatal("pairing of a removed key still exists")
	}
	if _, ok, _ := s.Lookup(tok2.ID); ok {
		t.Fatal("pairing of a removed key can still log in")
	}
	if _, err := s.MintFrom("vm3", key, tok2.ID); !errors.Is(err, ErrCredentialGone) {
		t.Fatalf("mint from an orphaned pairing: %v", err)
	}
	if toks, _ := s.List(); len(toks) != 1 || toks[0].ID != free.ID {
		t.Fatalf("store after the key went: %+v", toks)
	}

	// Revoke counts the children it removes.
	auth, _ = s.Authorize("alice", key)
	if _, err := s.MintFrom("vm", key, auth.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := s.Revoke("alice"); err != nil || n != 2 {
		t.Fatalf("revoke alice: %d %v", n, err)
	}

	// Revoke by fingerprint takes every pairing and key of an identity,
	// not its grants.
	s.Authorize("bob", key)
	if _, err := s.Mint("bob-pw", key); err != nil {
		t.Fatal(err)
	}
	g, err := s.MintGrant("agent", key)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := s.Revoke(Fingerprint(key)); err != nil || n != 2 {
		t.Fatalf("revoke by fingerprint: %d %v", n, err)
	}
	if ok, _ := s.Exists(g.ID); !ok {
		t.Fatal("revoke by fingerprint removed a grant token")
	}
	if ok, _ := s.Exists(free.ID); !ok {
		t.Fatal("revoke by fingerprint removed another identity's pairing")
	}

	// A grant token cannot mint.
	if _, err := s.MintFrom("x", key, g.ID); err == nil {
		t.Fatal("minted a pairing from a grant token")
	}
}
