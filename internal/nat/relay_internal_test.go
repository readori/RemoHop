package nat

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/nat/vnet"
)

// Strangers' packets do not keep a relay session alive, and a replayed bind
// cannot move a peer that is still talking.
func TestRelayStrangersAndReplayedBinds(t *testing.T) {
	n := vnet.New()
	pc := n.Host("198.51.100.9").MustListen(0)
	relay := pc.LocalAddr()
	nonce := make([]byte, NonceSize)
	nonce[0] = 7
	key := relayKey(nonce)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const idle = 500 * time.Millisecond
	done := make(chan struct{})
	go func() { ServeRelay(ctx, pc, nonce, RelayLimits{Rate: 1 << 20, Idle: idle}); close(done) }()

	client := n.Host("192.0.2.10").MustListen(0)
	host := n.Host("192.0.2.20").MustListen(0)
	spy := n.Host("192.0.2.66").MustListen(0)
	buf := make([]byte, 2048)
	read := func(c net.PacketConn, within time.Duration) (int, bool) {
		c.SetReadDeadline(time.Now().Add(within))
		k, _, err := c.ReadFrom(buf)
		return k, err == nil
	}
	for _, b := range []struct {
		c    net.PacketConn
		role Role
	}{{client, RoleClient}, {host, RoleHost}} {
		b.c.WriteTo(relayBind(key, b.role), relay)
		if k, ok := read(b.c, time.Second); !ok || k != relayAckLen {
			t.Fatalf("bind as %d not acknowledged", b.role)
		}
	}
	data := make([]byte, 100)
	data[0] = 0x40
	client.WriteTo(data, relay)
	if k, ok := read(host, time.Second); !ok || k != len(data) {
		t.Fatal("nothing relayed")
	}

	// Someone who saw the client's bind replays it from elsewhere while the
	// client is active: ignored.
	spy.WriteTo(relayBind(key, RoleClient), relay)
	if _, ok := read(spy, 300*time.Millisecond); ok {
		t.Fatal("a replayed bind was acknowledged while its peer was active")
	}
	host.WriteTo(data, relay)
	if _, ok := read(client, time.Second); !ok {
		t.Fatal("the replayed bind moved the client away")
	}

	// The peers go quiet; a stranger keeps sending. The session ends after
	// its idle time anyway.
	start := time.Now()
	stop := time.After(3 * time.Second)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-done:
			if took := time.Since(start); took > idle+700*time.Millisecond {
				t.Fatalf("session ended %s after the peers went quiet", took)
			}
			return
		case <-tick.C:
			spy.WriteTo(data, relay)
		case <-stop:
			t.Fatal("a stranger's packets kept the session alive")
		}
	}
}
