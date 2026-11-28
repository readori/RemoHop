package dial

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/signal"
)

var registerHostExt sync.Once

// An extension built on the host sends a request to its rendezvous server
// over the host's registered connection, as the host's proven key.
func TestHostSignalExt(t *testing.T) {
	registerHostExt.Do(func() {
		signal.RegisterRequest("x-test-host-ext", func(ctx context.Context, s *signal.Server, r signal.ExtRequest) (any, error) {
			return map[string]string{"key": hex.EncodeToString(r.Key)}, nil
		})
	})
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())

	alone := startHost(t, hostd.Config{})
	if _, err := alone.SignalExt(context.Background(), "x-test-host-ext", nil); !errors.Is(err, hostd.ErrNotRegistered) {
		t.Fatalf("a host with no server: %v", err)
	}

	addr := loopbackSignal(t)
	h := startHost(t, hostd.Config{Signal: addr})
	select {
	case <-h.Registered():
	case <-time.After(20 * time.Second):
		t.Fatal("the host did not register")
	}
	if h.SignalServer() != addr {
		t.Fatalf("SignalServer: %q", h.SignalServer())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	raw, err := h.SignalExt(ctx, "x-test-host-ext", map[string]string{"hello": "server"})
	if err != nil {
		t.Fatal(err)
	}
	var reply struct{ Key string }
	if err := json.Unmarshal(raw, &reply); err != nil || reply.Key != hex.EncodeToString(h.PublicKey()) {
		t.Fatalf("reply %s: %v", raw, err)
	}
}
