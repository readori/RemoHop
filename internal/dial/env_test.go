package dial

import (
	"errors"
	"strings"
	"testing"

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
