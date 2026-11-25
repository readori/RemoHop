package account_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/readori/RemoHop/internal/account"
)

// The ID a device is listed under is the rendezvous server's word. An ID
// the server gives to two member devices names neither: nothing is pinned
// for it (security audit finding 11).
func TestHostKeyRefusesAnIDOfTwoDevices(t *testing.T) {
	s := startServer(t)
	email := "me@example.com"
	h1dir, h2dir, cdir := newDir(t, "box1"), newDir(t, "box2"), newDir(t, "laptop")
	if _, err := s.login(t, h1dir, email, ""); err != nil {
		t.Fatal(err)
	}
	s.join(t, h2dir, h1dir, email)
	s.join(t, cdir, h1dir, email)
	h1, h2 := startHost(t, h1dir, s.addr), startHost(t, h2dir, s.addr)
	if _, _, err := account.Devices(testCtx(t), s.opts(cdir)); err != nil {
		t.Fatal(err)
	}
	if key, ok := account.HostKey(testCtx(t), s.opts(cdir), h1.ID(), false); !ok || !key.Equal(h1.PublicKey()) {
		t.Fatalf("HostKey(box1) = %v %v", key, ok)
	}

	// The cached list (as a lying server would have sent it) gives box2
	// box1's ID too.
	p := filepath.Join(cdir, account.File)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var st map[string]any
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatal(err)
	}
	for _, d := range st["devices"].([]any) {
		if d := d.(map[string]any); d["id"] == h2.ID() {
			d["id"] = h1.ID()
		}
	}
	b, _ = json.Marshal(st)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if key, ok := account.HostKey(testCtx(t), s.opts(cdir), h1.ID(), false); ok {
		t.Fatalf("an ID listed for two devices pinned %v", key)
	}
}
