package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"path/filepath"
	"testing"
)

func TestGrantTokens(t *testing.T) {
	store := OpenTokenStore(filepath.Join(t.TempDir(), TokensFile))
	keyA, _, _ := ed25519.GenerateKey(rand.Reader)
	keyB, _, _ := ed25519.GenerateKey(rand.Reader)

	g, err := store.MintGrant("agent", nil)
	if err != nil {
		t.Fatal(err)
	}
	if g.Grant != "agent" || len(g.ClientKey) != 0 || len(g.Secret) != TokenSize {
		t.Fatalf("grant token: %+v", g)
	}
	// An ordinary pairing and an authorisation of the same key leave the
	// grant token alone, and the grant token is no authorised key.
	if _, err := store.Mint("laptop", keyA); err != nil {
		t.Fatal(err)
	}
	if err := store.Bind(g.ID, keyA); err != nil {
		t.Fatalf("first bind: %v", err)
	}
	if _, err := store.Mint("laptop", keyA); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.AuthorizedKey(keyA); ok {
		t.Fatal("a grant token counts as an authorised key")
	}
	if tok, ok, _ := store.Lookup(g.ID); !ok || !ed25519.PublicKey(tok.ClientKey).Equal(keyA) {
		t.Fatalf("grant token after re-pairing: %+v %v", tok, ok)
	}
	if err := store.Bind(g.ID, keyA); err != nil {
		t.Fatalf("binding again to the same key: %v", err)
	}
	if err := store.Bind(g.ID, keyB); err == nil {
		t.Fatal("bound to a second key")
	}
	if err := store.Bind("nope", keyB); err == nil {
		t.Fatal("bound a missing token")
	}
	// Reissuing replaces the grant's token; revoking removes it only.
	g2, err := store.MintGrant("agent", keyB)
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := store.Exists(g.ID); ok {
		t.Fatal("the reissued grant's old token survived")
	}
	if n, err := store.RevokeGrant("agent"); err != nil || n != 1 {
		t.Fatalf("RevokeGrant: %d %v", n, err)
	}
	if ok, _ := store.Exists(g2.ID); ok {
		t.Fatal("revoked grant token still there")
	}
	toks, _ := store.List()
	if len(toks) != 1 || toks[0].Name != "laptop" {
		t.Fatalf("left: %+v", toks)
	}
}
