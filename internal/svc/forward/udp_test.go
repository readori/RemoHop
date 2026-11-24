package forward

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/peer/peertest"
	"github.com/readori/RemoHop/internal/wire"
)

// udpServer answers every packet with reply(payload, source).
func udpServer(t *testing.T, reply func(p []byte, src net.Addr) []byte) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 65536)
		for {
			n, src, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			pc.WriteTo(reply(append([]byte(nil), buf[:n]...), src), src)
		}
	}()
	return pc.LocalAddr().String()
}

func udpEcho(t *testing.T) string {
	return udpServer(t, func(p []byte, _ net.Addr) []byte { return p })
}

func udpSpec(t *testing.T, target string) Spec {
	t.Helper()
	_, p, _ := net.SplitHostPort(target)
	sp, err := ParseSpec("0:" + p)
	if err != nil {
		t.Fatal(err)
	}
	return sp
}

// exchange sends p from c to addr and returns the reply.
func exchange(c net.PacketConn, addr net.Addr, p []byte) ([]byte, error) {
	if _, err := c.WriteTo(p, addr); err != nil {
		return nil, err
	}
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 65536)
	n, _, err := c.ReadFrom(buf)
	return buf[:n], err
}

func udpClient(t *testing.T) net.PacketConn {
	t.Helper()
	c, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func testUDPIntegrity(t *testing.T, wantDatagrams bool) {
	client, _ := peertest.Pair(t)
	f, err := ListenUDP(context.Background(), client, udpSpec(t, udpEcho(t)))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if f.Datagrams() != wantDatagrams {
		t.Fatalf("Datagrams() = %v", f.Datagrams())
	}
	c := udpClient(t)
	// Sizes below and above what fits a QUIC datagram, up to a large
	// fragmented UDP packet: the big ones must take the stream path.
	var up uint64
	sizes := []int{1, 100, 1000, 1150, 1300, 1500, 9000, 60000}
	if runtime.GOOS == "darwin" {
		// macOS caps one datagram at net.inet.udp.maxdgram (9216 by
		// default), so the test's own client cannot send more.
		sizes = sizes[:len(sizes)-1]
	}
	for _, size := range sizes {
		p := make([]byte, size)
		rand.Read(p)
		got, err := exchange(c, f.Addr(), p)
		if err != nil || !bytes.Equal(got, p) {
			t.Fatalf("size %d: got %d bytes, err %v", size, len(got), err)
		}
		up += uint64(size)
	}
	// An empty packet is a packet too.
	if got, err := exchange(c, f.Addr(), nil); err != nil || len(got) != 0 {
		t.Fatalf("empty packet: %d bytes, err %v", len(got), err)
	}
	st := f.Stats()
	n := uint64(len(sizes) + 1)
	if st.Flows != 1 || st.PacketsUp != n || st.PacketsDown != n || st.BytesUp != up || st.BytesDown != up || st.Dropped != 0 {
		t.Fatalf("stats %+v", st)
	}
}

func TestUDPIntegrityDatagrams(t *testing.T) { testUDPIntegrity(t, true) }

func TestUDPIntegrityStreamOnly(t *testing.T) {
	udpDatagrams.Store(false)
	defer udpDatagrams.Store(true)
	testUDPIntegrity(t, false)
}

func TestUDPManyFlows(t *testing.T) {
	client, _ := peertest.Pair(t)
	// The server reports the source it saw, so we can check that every local
	// source got its own socket on the host.
	target := udpServer(t, func(p []byte, src net.Addr) []byte {
		return append([]byte(src.String()+"|"), p...)
	})
	f, err := ListenUDP(context.Background(), client, udpSpec(t, target))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	const flows, rounds = 60, 5
	var wg sync.WaitGroup
	var mu sync.Mutex
	sources := map[string]int{}
	errs := make(chan error, flows)
	for i := range flows {
		c := udpClient(t)
		wg.Add(1)
		go func() {
			defer wg.Done()
			seen := ""
			for r := range rounds {
				msg := fmt.Sprintf("flow-%d-round-%d", i, r)
				got, err := exchange(c, f.Addr(), []byte(msg))
				src, body, ok := bytes.Cut(got, []byte("|"))
				if err != nil || !ok || string(body) != msg {
					errs <- fmt.Errorf("flow %d round %d: %q %v", i, r, got, err)
					return
				}
				if seen != "" && seen != string(src) {
					errs <- fmt.Errorf("flow %d changed host socket: %s then %s", i, seen, src)
					return
				}
				seen = string(src)
			}
			mu.Lock()
			sources[seen]++
			mu.Unlock()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if len(sources) != flows {
		t.Fatalf("%d distinct host sockets for %d flows", len(sources), flows)
	}
	if st := f.Stats(); st.Flows != flows || st.PacketsUp != flows*rounds || st.PacketsDown != flows*rounds {
		t.Fatalf("stats %+v", st)
	}
}

func TestUDPIdleExpiry(t *testing.T) {
	udpIdle.Store(int64(300 * time.Millisecond))
	defer udpIdle.Store(int64(60 * time.Second))
	client, _ := peertest.Pair(t)
	target := udpServer(t, func(_ []byte, src net.Addr) []byte { return []byte(src.String()) })
	f, err := ListenUDP(context.Background(), client, udpSpec(t, target))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	c := udpClient(t)
	first, err := exchange(c, f.Addr(), []byte("a"))
	if err != nil {
		t.Fatal(err)
	}
	// Traffic keeps the flow (and the host's socket) alive past the timeout.
	for range 6 {
		time.Sleep(100 * time.Millisecond)
		again, err := exchange(c, f.Addr(), []byte("a"))
		if err != nil || string(again) != string(first) {
			t.Fatalf("active flow changed: %q -> %q (%v)", first, again, err)
		}
	}
	// Silence expires it on both ends.
	deadline := time.Now().Add(5 * time.Second)
	for f.Stats().Flows != 0 {
		if time.Now().After(deadline) {
			t.Fatal("idle flow was never forgotten")
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(2 * udpIdleTimeout())
	// The same source can simply start again; the host uses a fresh socket.
	later, err := exchange(c, f.Addr(), []byte("a"))
	if err != nil {
		t.Fatal(err)
	}
	if string(later) == string(first) {
		t.Fatalf("host socket %s survived the idle timeout", first)
	}
}

func TestUDPFlowTableBounded(t *testing.T) {
	client, _ := peertest.Pair(t)
	f, err := ListenUDP(context.Background(), client, udpSpec(t, udpEcho(t)))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	const extra = 8
	for i := range maxUDPFlows + extra {
		c, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		c.WriteTo([]byte("x"), f.Addr())
		// Pace the burst so the kernel's socket buffer does not drop packets
		// before the forwarder sees them.
		for w := 0; w < 500; w++ {
			if st := f.Stats(); st.Flows+int(st.Dropped) > i {
				break
			}
			time.Sleep(time.Millisecond)
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		st := f.Stats()
		if st.Flows == maxUDPFlows && st.Dropped >= extra {
			break
		}
		if st.Flows > maxUDPFlows {
			t.Fatalf("flow table grew to %d", st.Flows)
		}
		if time.Now().After(deadline) {
			t.Fatalf("stats %+v", st)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The host must refuse frames larger than a UDP packet and bad targets.
func TestUDPHostRejectsBadInput(t *testing.T) {
	client, _ := peertest.Pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	s, err := client.Open(ctx, svcUDP, udpReq{Addr: udpEcho(t)})
	if err != nil {
		t.Fatal(err)
	}
	var rep udpReply
	if err := wire.ReadJSON(s, &rep); err != nil || !rep.OK || rep.Datagrams {
		t.Fatalf("reply %+v %v", rep, err)
	}
	big := make([]byte, 4+maxUDPPacket+1)
	binary.BigEndian.PutUint32(big, 1)
	if err := wire.WriteFrame(s, udpFrame, big); err != nil {
		t.Fatal(err)
	}
	s.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadAll(s); err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			t.Fatal("host kept the stream open after an oversized frame")
		}
	}
	s.Close()

	for _, addr := range []string{"", "no-port", "127.0.0.1:notaport"} {
		if _, err := ListenUDP(ctx, client, Spec{BindHost: "127.0.0.1", Host: addr, Port: 1}); addr != "" && err == nil {
			t.Errorf("target host %q accepted", addr)
		}
		s, err := client.Open(ctx, svcUDP, udpReq{Addr: addr})
		if err != nil {
			t.Fatal(err)
		}
		var rep udpReply
		if err := wire.ReadJSON(s, &rep); err != nil || rep.OK {
			t.Errorf("addr %q: reply %+v %v", addr, rep, err)
		}
		s.Close()
	}
}

func TestUDPCloseAndSessionDeath(t *testing.T) {
	client, host := peertest.Pair(t)
	echo := udpEcho(t)
	f, err := ListenUDP(context.Background(), client, udpSpec(t, echo))
	if err != nil {
		t.Fatal(err)
	}
	c := udpClient(t)
	if _, err := exchange(c, f.Addr(), []byte("hi")); err != nil {
		t.Fatal(err)
	}
	addr := f.Addr().String()
	f.Close()
	f.Close()
	if f.Err() != nil {
		t.Fatalf("Err after Close = %v", f.Err())
	}
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		t.Fatalf("port still bound after Close: %v", err)
	}
	pc.Close()

	ctx, cancel := context.WithCancel(context.Background())
	f2, err := ListenUDP(ctx, client, udpSpec(t, echo))
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-f2.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("not done after cancel")
	}
	if !errors.Is(f2.Err(), context.Canceled) {
		t.Fatalf("Err = %v", f2.Err())
	}

	f3, err := ListenUDP(context.Background(), client, udpSpec(t, echo))
	if err != nil {
		t.Fatal(err)
	}
	host.Close()
	select {
	case <-f3.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("forward survived session death")
	}
	if !errors.Is(f3.Err(), ErrSessionClosed) {
		t.Fatalf("Err = %v", f3.Err())
	}
}

// A packet the host cannot send to the target (larger than any IPv4 UDP
// datagram, so EMSGSIZE on every OS) is dropped; the flow and the forward
// keep working.
func TestUDPOversizedSendDropsPacketOnly(t *testing.T) {
	client, _ := peertest.Pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s, err := client.Open(ctx, svcUDP, udpReq{Addr: udpEcho(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var rep udpReply
	if err := wire.ReadJSON(s, &rep); err != nil || !rep.OK {
		t.Fatalf("reply %+v %v", rep, err)
	}
	frame := func(p []byte) []byte {
		b := make([]byte, 4+len(p))
		binary.BigEndian.PutUint32(b, 7)
		copy(b[4:], p)
		return b
	}
	if err := wire.WriteFrame(s, udpFrame, frame(make([]byte, 65520))); err != nil {
		t.Fatal(err)
	}
	if err := wire.WriteFrame(s, udpFrame, frame([]byte("still here"))); err != nil {
		t.Fatal(err)
	}
	s.SetReadDeadline(time.Now().Add(10 * time.Second))
	typ, p, err := wire.ReadFrame(s, nil)
	if err != nil || typ != udpFrame || len(p) < 4 || binary.BigEndian.Uint32(p) != 7 || string(p[4:]) != "still here" {
		t.Fatalf("after an oversized packet: %c %q %v", typ, p, err)
	}
}
