package nat_test

import (
	"context"
	"crypto/rand"
	"io"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/nat"
	"github.com/readori/RemoHop/internal/nat/vnet"
)

// relayGoodput moves size bytes through a relay session capped at rate
// bytes/s (echoed back, so 2×size cross the relay) over a path with the
// given one-way latency per hop, and returns the goodput as a fraction of
// the cap.
func relayGoodput(t *testing.T, rate, size int, latency, maxDelay time.Duration) (float64, nat.RelayStats) {
	t.Helper()
	w := newWorld(t)
	w.net.SetLatency(latency)
	if v, err := strconv.ParseFloat(os.Getenv("REMOHOP_RELAY_GOODPUT_LOSS"), 64); err == nil {
		w.net.SetLoss(v)
	}
	client := w.behind("203.0.113.10", "192.168.1.10", vnet.Symmetric)
	host := w.behind("203.0.113.20", "10.0.0.20", vnet.Symmetric)
	serve(t, host)
	nonce := newNonce()
	relay, statsCh := relayWorld(t, w, nonce, nat.RelayLimits{Rate: rate, MaxDelay: maxDelay, Idle: 500 * time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := client.sock.BindRelay(ctx, relay, nonce, nat.RoleClient); err != nil {
		t.Fatal(err)
	}
	if err := host.sock.BindRelay(ctx, relay, nonce, nat.RoleHost); err != nil {
		t.Fatal(err)
	}
	id, _ := auth.NewIdentity()
	tlsConf, _ := id.ClientTLS(testALPN, nil)
	q, err := client.sock.Transport().Dial(ctx, net.UDPAddrFromAddrPort(relay), tlsConf, quicConf())
	if err != nil {
		t.Fatal(err)
	}
	s, err := q.OpenStreamSync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	msg := make([]byte, size)
	rand.Read(msg)
	start := time.Now()
	go func() { s.Write(msg); s.Close() }()
	n, err := io.Copy(io.Discard, s)
	elapsed := time.Since(start)
	if err != nil || n != int64(size) {
		t.Fatalf("echo through the relay: %d bytes, %v", n, err)
	}
	q.CloseWithError(0, "")
	st := <-statsCh
	return float64(2*size) / elapsed.Seconds() / float64(rate), st
}

// TestRelayGoodput: with pacing, a bulk transfer through a capped relay
// gets close to the cap. A policer that drops everything over the rate
// (MaxDelay < 0, how the relay used to work) leaves QUIC far below it.
func TestRelayGoodput(t *testing.T) {
	if testing.Short() {
		t.Skip("measures throughput for several seconds")
	}
	rate := 500_000 // 4 Mbit/s
	size := 1 << 20
	if v, err := strconv.Atoi(os.Getenv("REMOHOP_RELAY_GOODPUT_KBPS")); err == nil && v > 0 {
		rate = v * 125
		size = max(size, rate*2)
	}
	latency := 10 * time.Millisecond
	if v, err := time.ParseDuration(os.Getenv("REMOHOP_RELAY_GOODPUT_LATENCY")); err == nil {
		latency = v
	}
	paced, st := relayGoodput(t, rate, size, latency, 0)
	t.Logf("paced:   %.0f%% of %d kbit/s (relayed %d packets, %d delayed, %d dropped)", paced*100, rate/125, st.Packets, st.Delayed, st.Dropped)
	policed, st2 := relayGoodput(t, rate, size, latency, -1)
	t.Logf("policed: %.0f%% of %d kbit/s (relayed %d packets, %d dropped)", policed*100, rate/125, st2.Packets, st2.Dropped)
	if paced < 0.6 {
		t.Errorf("paced relay goodput %.0f%% of the cap", paced*100)
	}
	if paced > 1.15 {
		t.Errorf("paced relay passed %.0f%% of the cap", paced*100)
	}
}
