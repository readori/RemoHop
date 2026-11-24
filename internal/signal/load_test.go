package signal

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"math/rand/v2"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/nat"
)

// TestLoad measures a running `remohop signal` process from the outside. It is
// a tool, not part of the suite: set REMOHOP_SIGNAL_LOAD=HOST:PORT (the
// server, started with --relay for the relay phase) and REMOHOP_SIGNAL_PID
// (its process ID, for memory and CPU from /proc). It uses addresses all
// over 127.0.0.0/8 so that each simulated machine is its own source, as on
// the internet, and the per-source limits do not throttle the test.
//
//	REMOHOP_SIGNAL_HOSTS (default 1000)   hosts that register and stay online
//	REMOHOP_SIGNAL_RATE  (default 200)    connect+presence requests per second
//	REMOHOP_SIGNAL_SECS  (default 20)     length of the request phase
//	REMOHOP_SIGNAL_RELAYS (default 10)    relayed sessions run at the cap
func TestLoad(t *testing.T) {
	if dir := os.Getenv("REMOHOP_SIGNAL_PREPARE"); dir != "" {
		prepareLoad(t, dir)
		return
	}
	server := os.Getenv("REMOHOP_SIGNAL_LOAD")
	if server == "" {
		t.Skip("set REMOHOP_SIGNAL_LOAD=HOST:PORT and REMOHOP_SIGNAL_PID to measure a running remohop signal")
	}
	pid, _ := strconv.Atoi(os.Getenv("REMOHOP_SIGNAL_PID"))
	env := func(k string, def int) int {
		if v, err := strconv.Atoi(os.Getenv(k)); err == nil {
			return v
		}
		return def
	}
	hosts, rate, secs, relays := env("REMOHOP_SIGNAL_HOSTS", 1000), env("REMOHOP_SIGNAL_RATE", 200), env("REMOHOP_SIGNAL_SECS", 20), env("REMOHOP_SIGNAL_RELAYS", 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	report := func(phase string) (rssKB int, cpuMS float64) {
		rssKB, cpuMS = procStats(t, pid)
		t.Logf("%-34s RSS %6.1f MB   CPU so far %7.0f ms", phase, float64(rssKB)/1024, cpuMS)
		return
	}
	rss0, _ := report("idle")

	addr := func(net8, i int) string { return fmt.Sprintf("127.%d.%d.%d:0", net8, i/250%250, i%250+1) }
	dial := func(local string) (*nat.Socket, *Client, error) {
		sock, err := nat.Listen(local)
		if err != nil {
			return nil, nil, err
		}
		dctx, dcancel := context.WithTimeout(ctx, 10*time.Second)
		defer dcancel()
		c, err := Dial(dctx, sock, server, Options{})
		if err != nil {
			sock.Close()
			return nil, nil, err
		}
		return sock, c, nil
	}

	// Phase 1: hosts register and stay online, answering offers and relays.
	ids := make([]string, hosts)
	var hostSocks sync.Map
	var wg sync.WaitGroup
	sem := make(chan struct{}, 64)
	var failures atomic.Int64
	start := time.Now()
	for i := 0; i < hosts; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			sock, c, err := dial(addr(1, i))
			if err != nil {
				failures.Add(1)
				return
			}
			id := loadIdentity(i)
			var reg *Registration
			for try := 0; ; try++ {
				rctx, rcancel := context.WithTimeout(ctx, 10*time.Second)
				reg, err = c.Register(rctx, id)
				rcancel()
				// New IDs have a global budget; wait for it as a real
				// host's reconnect loop would.
				if e, ok := err.(*Error); ok && e.Code == CodeRateLimited && try < 100 {
					time.Sleep(e.RetryAfter + time.Duration(rand.IntN(50))*time.Millisecond)
					continue
				}
				break
			}
			if err != nil {
				t.Logf("host %d: %v", i, err)
				failures.Add(1)
				return
			}
			ids[i] = reg.ID
			hostSocks.Store(reg.ID, sock)
			c.OnRelay = func(nonce []byte, relay netip.AddrPort) error {
				bctx, bcancel := context.WithTimeout(ctx, 5*time.Second)
				defer bcancel()
				return sock.BindRelay(bctx, relay, nonce, nat.RoleHost)
			}
			go c.ServeOffers(ctx, func(Offer) Answer { return Answer{} })
		}(i)
	}
	wg.Wait()
	if failures.Load() > 0 {
		t.Fatalf("%d of %d hosts failed to register", failures.Load(), hosts)
	}
	t.Logf("%d hosts registered in %s", hosts, time.Since(start).Round(time.Millisecond))
	time.Sleep(2 * time.Second)
	rss1, cpu1 := report(fmt.Sprintf("%d hosts online", hosts))
	t.Logf("=> %.1f KB per online host", float64(rss1-rss0)/float64(hosts))
	time.Sleep(10 * time.Second)
	rssIdle, cpuIdle := report(fmt.Sprintf("%d hosts online, 10 s later", hosts))
	t.Logf("=> keeping %d hosts online costs %.2f%% of one CPU (keep-alives), RSS %+.1f MB", hosts, (cpuIdle-cpu1)/10000*100, float64(rssIdle-rss1)/1024)
	rss1, cpu1 = rssIdle, cpuIdle

	// Phase 2: requests from 200 other sources, half connects (a full
	// introduction: offer to the host and back), half presence queries of
	// ten IDs each.
	clients := 200
	type cl struct{ c *Client }
	cls := make([]cl, clients)
	for i := range cls {
		_, c, err := dial(addr(2, i))
		if err != nil {
			t.Fatal(err)
		}
		cls[i] = cl{c}
	}
	var done, limited, errs atomic.Int64
	pctx, pcancel := context.WithTimeout(ctx, time.Duration(secs)*time.Second)
	tick := time.NewTicker(time.Second / time.Duration(rate))
	peak := rss1
	n := 0
	for pctx.Err() == nil {
		<-tick.C
		c := cls[n%clients].c
		connect := n%2 == 0
		n++
		go func() {
			rctx, rc := context.WithTimeout(ctx, 10*time.Second)
			defer rc()
			var err error
			if connect {
				_, err = c.Connect(rctx, ids[rand.IntN(len(ids))], nil, nat.TypeUnknown)
			} else {
				q := make([]string, 10)
				for j := range q {
					q[j] = ids[rand.IntN(len(ids))]
				}
				_, err = c.Presence(rctx, q)
			}
			switch {
			case err == nil:
				done.Add(1)
			case CodeOf(err) == CodeRateLimited:
				limited.Add(1)
			default:
				errs.Add(1)
			}
		}()
		if n%rate == 0 {
			if r, _ := procStats(t, pid); r > peak {
				peak = r
			}
		}
	}
	pcancel()
	tick.Stop()
	time.Sleep(2 * time.Second)
	rss2, cpu2 := report("after the request phase")
	if rss2 > peak {
		peak = rss2
	}
	total := done.Load() + limited.Load() + errs.Load()
	t.Logf("=> %d requests in %ds (%d ok, %d rate limited, %d errors), %.0f ms CPU per 1000 requests, peak RSS %.1f MB",
		total, secs, done.Load(), limited.Load(), errs.Load(), (cpu2-cpu1)/float64(total)*1000, float64(peak)/1024)

	// Optional: churn. Waves of short-lived clients (connect, one request,
	// hang up) must leave the server's memory where it was.
	for round := 1; round <= env("REMOHOP_SIGNAL_CHURN", 0); round++ {
		var cw sync.WaitGroup
		for i := 0; i < 1000; i++ {
			cw.Add(1)
			sem <- struct{}{}
			go func(i int) {
				defer cw.Done()
				defer func() { <-sem }()
				sock, c, err := dial(addr(10+round%100, i))
				if err != nil {
					return
				}
				qctx, qc := context.WithTimeout(ctx, 5*time.Second)
				c.Presence(qctx, []string{ids[i%len(ids)]})
				qc()
				c.Close()
				sock.Close()
			}(i)
		}
		cw.Wait()
		time.Sleep(3 * time.Second)
		report(fmt.Sprintf("churn round %d (1000 clients came and went)", round))
	}

	// Phase 3: relayed sessions, each pushing traffic at more than the
	// per-session cap in both directions.
	if relays == 0 {
		return
	}
	cpu3start := cpu2
	var relayed atomic.Int64
	rctx, rcancel := context.WithTimeout(ctx, 15*time.Second)
	defer rcancel()
	for i := 0; i < relays; i++ {
		sock, c, err := dial(addr(3, i))
		if err != nil {
			t.Fatal(err)
		}
		in, err := c.Connect(rctx, ids[i], nil, nat.TypeUnknown)
		if err != nil {
			t.Fatal(err)
		}
		relay, err := c.Relay(rctx, in.Nonce)
		if err != nil {
			t.Fatalf("relay: %v", err)
		}
		if err := sock.BindRelay(rctx, relay, in.Nonce, nat.RoleClient); err != nil {
			t.Fatal(err)
		}
		hs, _ := hostSocks.Load(ids[i])
		for _, s := range []*nat.Socket{sock, hs.(*nat.Socket)} {
			go func(s *nat.Socket) {
				pkt := make([]byte, 1200)
				pkt[0] = 0x40 // looks like QUIC: passed through, not parsed
				to := net.UDPAddrFromAddrPort(relay)
				// 300 KB/s per direction: above the 250 KB/s session cap.
				t := time.NewTicker(4 * time.Millisecond)
				defer t.Stop()
				for rctx.Err() == nil {
					<-t.C
					if _, err := s.Transport().WriteTo(pkt, to); err == nil {
						relayed.Add(1)
					}
				}
			}(s)
		}
	}
	<-rctx.Done()
	time.Sleep(time.Second)
	rss3, cpu3 := report(fmt.Sprintf("after %d relayed sessions, 15 s", relays))
	t.Logf("=> %d relay sessions at the cap: %.1f%% of one CPU in total (%.2f%% each), RSS %+.1f MB",
		relays, (cpu3-cpu3start)/15000*100, (cpu3-cpu3start)/15000*100/float64(relays), float64(rss3-rss2)/1024)
}

// procStats reads a process's resident memory (KB) and CPU time used (ms).
func procStats(t *testing.T, pid int) (rssKB int, cpuMS float64) {
	if pid <= 0 {
		return 0, 0
	}
	status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(status), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			rssKB, _ = strconv.Atoi(strings.Fields(line)[1])
		}
	}
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		t.Fatal(err)
	}
	f := strings.Fields(string(stat)[strings.LastIndexByte(string(stat), ')')+2:])
	ut, _ := strconv.Atoi(f[11])
	st, _ := strconv.Atoi(f[12])
	return rssKB, float64(ut+st) * 10 // clock ticks of 10 ms
}

// loadIdentity is host i's identity, the same on every run, so that a
// server prepared with prepareLoad already knows it: the hosts re-register,
// as they do after a server restart, instead of queueing for new IDs.
func loadIdentity(i int) *auth.Identity {
	seed := sha256.Sum256([]byte("remohop load test host " + strconv.Itoa(i)))
	der, _ := x509.MarshalPKCS8PrivateKey(ed25519.NewKeyFromSeed(seed[:]))
	id, err := auth.ParseIdentity(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	if err != nil {
		panic(err)
	}
	return id
}

// prepareLoad writes an ID journal holding the load test's hosts into a
// server data directory (REMOHOP_SIGNAL_PREPARE=DIR, before the server
// starts there).
func prepareLoad(t *testing.T, dir string) {
	hosts := 1000
	if v, err := strconv.Atoi(os.Getenv("REMOHOP_SIGNAL_HOSTS")); err == nil {
		hosts = v
	}
	var b strings.Builder
	now := time.Now().Unix()
	for i := 0; i < hosts; i++ {
		fmt.Fprintf(&b, "%s %d %d %d\n", auth.EncodeKey(loadIdentity(i).Public()), 100000000+i, now, now)
	}
	if err := os.WriteFile(filepath.Join(dir, idsFile), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %d hosts to %s", hosts, filepath.Join(dir, idsFile))
}
