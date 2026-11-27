package dial

import (
	"errors"
	"strings"
	"testing"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/signal"
)

// TestRendezvousFirst: an ID with no rendezvous server configured is
// reported as such before any password prompt, with the command that sets
// one and the address alternative; an address needs no server.
func TestRendezvousFirst(t *testing.T) {
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	t.Setenv("REMOHOP_SIGNAL", "")
	err := rendezvousFirst("123 456 789", Options{})
	if !errors.Is(err, signal.ErrNotConfigured) || !strings.Contains(err.Error(), "remohop config set signal") || !strings.Contains(err.Error(), "HOST:PORT") {
		t.Fatalf("ID without a server: %v", err)
	}
	if err := rendezvousFirst("192.168.1.5:21118", Options{}); err != nil {
		t.Fatalf("address: %v", err)
	}
	if err := rendezvousFirst("123456789", Options{Signal: "rv.example.net:21116"}); err != nil {
		t.Fatalf("ID with --signal: %v", err)
	}
}

// TestWaitingNote: while the host's owner is asked, the client says how it
// appears in their prompt.
func TestWaitingNote(t *testing.T) {
	id, err := auth.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	got := waitingNote("work laptop", id)
	if !strings.Contains(got, "waiting for approval on the host") || !strings.Contains(got, `"work laptop"`) ||
		!strings.Contains(got, auth.Fingerprint(id.Public())) {
		t.Errorf("note %q", got)
	}
}
