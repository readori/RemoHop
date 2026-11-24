package clip

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/wire"
)

// Regression: a peer whose stamps are far ahead of ours (its clock is hours
// off) must not make its stale change beat a newer change of ours. Ordering
// is purely Lamport, so the item the client sends after seeing the host's
// stamp is ordered after it, whatever the wall clocks say.
func TestSyncOrderingIgnoresWallClockSkew(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	s, err := r.client.Open(ctx, Service, request{Op: opSync, Push: true, Pull: true, Images: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var hello message
	if err := wire.ReadJSON(s, &hello); err != nil {
		t.Fatal(err)
	}

	// A peer whose counter is far ahead (as a skewed wall-clock stamp
	// would be), but within the accepted range.
	ahead := int64(1) << 50
	c := Text("from-peer")
	if err := writeItem(s, message{}, c, ahead); err != nil {
		t.Fatal(err)
	}
	eventually(t, "host applies", func() bool { return boardText(r.remote) == "from-peer" })

	// The host's user copies something: its stamp must exceed everything seen.
	r.remote.Write(ctx, Text("from-host"))
	var m message
	if err := wire.ReadJSON(s, &m); err != nil || m.Item == nil {
		t.Fatalf("expected the host's item: %v %+v", err, m)
	}
	if _, inv, err := readItem(s, m.Item); err != nil || inv != nil {
		t.Fatal(err, inv)
	}
	if m.Item.TS <= ahead {
		t.Fatalf("host stamp %d not above the peer's %d", m.Item.TS, ahead)
	}

	// The peer's later copy, stamped after what it saw, must win on the host.
	if err := writeItem(s, message{}, Text("later-from-peer"), m.Item.TS+1); err != nil {
		t.Fatal(err)
	}
	eventually(t, "later change wins", func() bool { return boardText(r.remote) == "later-from-peer" })
}

func TestApplyBoundsHostileStamp(t *testing.T) {
	b := NewMemoryBoard()
	e := &engine{board: b, role: 0, recv: true, images: true}
	e.apply(context.Background(), Text("x"), math.MaxInt64)
	if e.clock != maxLamport {
		t.Fatalf("clock = %d, want clamp to %d", e.clock, int64(maxLamport))
	}
	e.apply(context.Background(), Text("y"), math.MinInt64)
	if e.clock != maxLamport {
		t.Fatalf("negative stamp moved the clock: %d", e.clock)
	}
	e.clock++
	if e.clock < 0 {
		t.Fatal("clock overflowed")
	}
}

func TestHistoryCapsArePerPeer(t *testing.T) {
	h := openTestHistory(t)
	h.MaxEntries = 5
	mustAdd(t, h, Entry{Dir: DirPush, Peer: "quiet", Time: time.Unix(1, 0)}, Text("keep me"))
	for i := 0; i < 50; i++ {
		mustAdd(t, h, Entry{Dir: DirPull, Peer: "flooder", Time: time.Unix(int64(10+i), 0)}, Text(strings.Repeat("f", i+1)))
	}
	if es, _ := h.List(Filter{Peer: "quiet"}); len(es) != 1 {
		t.Fatalf("a flooding peer evicted another peer's history: %d", len(es))
	}
	if es, _ := h.List(Filter{Peer: "flooder"}); len(es) != 5 || es[4].Size != 50 {
		t.Fatalf("flooder kept %d entries", len(es))
	}

	// Bytes are per peer too.
	h = openTestHistory(t)
	h.MaxBytes = 1000
	mustAdd(t, h, Entry{Dir: DirPush, Peer: "quiet", Time: time.Unix(1, 0)}, Text(strings.Repeat("q", 900)))
	for i := 0; i < 10; i++ {
		mustAdd(t, h, Entry{Dir: DirPull, Peer: "flooder", Time: time.Unix(int64(10+i), 0)}, Text(strings.Repeat(string(rune('a'+i)), 400)))
	}
	if es, _ := h.List(Filter{Peer: "quiet"}); len(es) != 1 {
		t.Fatal("byte flood evicted the other peer")
	}
	if es, _ := h.List(Filter{Peer: "flooder"}); len(es) != 2 {
		t.Fatalf("flooder kept %d entries", len(es))
	}

	// The overall ceiling still holds.
	h = openTestHistory(t)
	h.MaxEntries = 2
	for i := 0; i < 40; i++ {
		mustAdd(t, h, Entry{Dir: DirPush, Peer: string(rune('A' + i)), Time: time.Unix(int64(i), 0)}, Text("x"))
	}
	if es, _ := h.List(Filter{}); len(es) != 2*totalFactor {
		t.Fatalf("ceiling kept %d entries, want %d", len(es), 2*totalFactor)
	}
}

func TestMarkdownMetadataSanitised(t *testing.T) {
	e := Entry{Time: time.Unix(0, 0), Dir: DirPull, Via: "sync", Kind: KindText, Size: 3, Hash: "abc",
		Peer: "evil\n# injected\x1b]52;c;AAAA\x07", Text: "payload\x1b[31m kept verbatim"}
	md := RenderMarkdown([]Entry{e})
	if strings.Contains(md, "\x07") || strings.Contains(md, "\n# injected") {
		t.Fatalf("metadata not sanitised:\n%q", md)
	}
	if !strings.Contains(md, "payload\x1b[31m kept verbatim") {
		t.Fatal("clipboard text must stay verbatim")
	}
}

func TestBackendNameFromHostSanitised(t *testing.T) {
	r := newRig(t)
	SetHostBoard(namedBoard{r.remote, "evil\x1b]0;pwn\x07\nname"})
	info, err := RemoteInfo(context.Background(), r.client)
	if err != nil || strings.ContainsAny(info.Backend, "\x1b\x07\n") {
		t.Fatalf("backend = %q, %v", info.Backend, err)
	}
}

type namedBoard struct {
	*MemoryBoard
	name string
}

func (n namedBoard) Name() string { return n.name }

func TestCallOptionsPeerAndNoHistory(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if err := SetWith(ctx, r.client, Text("aliased"), CallOptions{Peer: "my-alias"}); err != nil {
		t.Fatal(err)
	}
	if _, err := GetWith(ctx, r.client, CallOptions{Peer: "my-alias"}); err != nil {
		t.Fatal(err)
	}
	SetWith(ctx, r.client, Text("secret"), CallOptions{NoHistory: true})
	GetWith(ctx, r.client, CallOptions{NoHistory: true})
	h, _ := OpenHistory("")
	es, _ := h.List(Filter{})
	if len(es) != 2 || es[0].Peer != "my-alias" || es[1].Peer != "my-alias" {
		t.Fatalf("history = %+v", es)
	}
}
