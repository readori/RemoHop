package ledger

import (
	"strings"
	"testing"

	"github.com/readori/RemoHop/internal/auth"
)

func ident(t *testing.T) *auth.Identity {
	t.Helper()
	id, err := auth.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestChain(t *testing.T) {
	a, b, c := ident(t), ident(t), ident(t)
	log := []Entry{Create(a, "me@example.com", "laptop")}
	st, err := Verify(log)
	if err != nil {
		t.Fatal(err)
	}
	add := func(signer *auth.Identity, op string, dev *auth.Identity) {
		t.Helper()
		e, err := st.Next(signer, op, dev.Public(), "x")
		if err != nil {
			t.Fatal(err)
		}
		log = append(log, e)
		if st, err = Verify(log); err != nil {
			t.Fatal(err)
		}
	}
	add(a, OpAdd, b)
	add(b, OpAdd, c)
	if !st.IsMember(c.Public()) || len(st.Members) != 3 {
		t.Fatalf("members %d", len(st.Members))
	}
	add(c, OpRemove, a)
	if st.IsMember(a.Public()) {
		t.Fatal("a still a member")
	}
	// A removed device can no longer sign.
	if _, err := st.Next(a, OpAdd, ident(t).Public(), ""); err == nil {
		t.Fatal("removed device signed an entry")
	}
	if !Extends(log[:2], log) || Extends(log, log[:2]) {
		t.Fatal("Extends")
	}
}

func TestForgeries(t *testing.T) {
	a, b, mallory := ident(t), ident(t), ident(t)
	create := Create(a, "me@example.com", "")
	st, _ := Verify([]Entry{create})
	good, _ := st.Next(a, OpAdd, b.Public(), "")

	// Signed by a key that is not a member.
	evil := Entry{Seq: 1, Prev: create.Hash(), Op: OpAdd, Device: mallory.Public(), Time: 1}
	evil.sign(mallory)
	if _, err := Verify([]Entry{create, evil}); err == nil || !strings.Contains(err.Error(), "not a member") {
		t.Fatalf("non-member signer: %v", err)
	}
	// A member's entry altered after signing.
	tampered := good
	tampered.Device = mallory.Public()
	if _, err := Verify([]Entry{create, tampered}); err == nil {
		t.Fatal("tampered entry accepted")
	}
	// Wrong predecessor.
	broken := Entry{Seq: 1, Prev: make([]byte, 32), Op: OpAdd, Device: mallory.Public()}
	broken.sign(a)
	if _, err := Verify([]Entry{create, broken}); err == nil {
		t.Fatal("broken chain accepted")
	}
	// Another account's genesis has another ID.
	other := Create(mallory, "me@example.com", "")
	ost, _ := Verify([]Entry{other})
	if string(ost.ID) == string(st.ID) {
		t.Fatal("IDs collide")
	}
	// A fork: two different entries at the same position.
	fork, _ := st.Next(a, OpAdd, mallory.Public(), "")
	if Extends([]Entry{create, good}, []Entry{create, fork}) {
		t.Fatal("fork seen as extension")
	}
}

func TestNormalize(t *testing.T) {
	if e, err := NormalizeEmail(" Me@Example.COM "); err != nil || e != "me@example.com" {
		t.Fatalf("%q %v", e, err)
	}
	for _, bad := range []string{"", "me", "Me <me@example.com>", "me@localhost", "a b@example.com", "me@example.com\r\nBcc: x@y.z"} {
		if _, err := NormalizeEmail(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if NormalizeFingerprint("3F9A01BC77D2E4109C55") != "3f9a:01bc:77d2:e410:9c55" || NormalizeFingerprint("3f9a:01bc") != "" {
		t.Fatal("fingerprint")
	}
}
