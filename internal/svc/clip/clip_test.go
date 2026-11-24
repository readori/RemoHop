package clip

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/peer/peertest"
	"github.com/readori/RemoHop/internal/wire"
)

// rig is a client/host pair with an in-memory clipboard on each end.
type rig struct {
	client        *peer.Conn
	local, remote *MemoryBoard
	hist          *History
}

func newRig(t *testing.T) *rig {
	t.Helper()
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	r := &rig{local: NewMemoryBoard(), remote: NewMemoryBoard()}
	SetHostBoard(r.remote)
	t.Cleanup(func() { SetHostBoard(nil) })
	r.client, _ = peertest.Pair(t)
	var err error
	if r.hist, err = OpenHistory(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	return r
}

func testPNG(t testing.TB, w, h int, noisy bool) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	rng := rand.New(rand.NewSource(1))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if noisy {
				img.Set(x, y, color.NRGBA{uint8(rng.Intn(256)), uint8(rng.Intn(256)), uint8(rng.Intn(256)), 255})
			} else {
				img.Set(x, y, color.NRGBA{uint8(x), uint8(y), 7, 255})
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func boardText(b Board) string {
	c, err := b.Read(context.Background())
	if err != nil || c.Kind != KindText {
		return ""
	}
	return c.String()
}

func boardHash(b Board) string {
	c, err := b.Read(context.Background())
	if err != nil {
		return ""
	}
	return c.Hash()
}

func TestGetSetRoundTrip(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()

	if _, err := Get(ctx, r.client); !errors.Is(err, ErrEmpty) {
		t.Fatalf("Get on empty clipboard: %v, want ErrEmpty", err)
	}
	info, err := RemoteInfo(ctx, r.client)
	if err != nil || info.Backend != "memory" {
		t.Fatalf("RemoteInfo = %+v, %v", info, err)
	}

	text := "héllo\nwörld 你好 ```"
	if err := Set(ctx, r.client, Text(text)); err != nil {
		t.Fatal(err)
	}
	if got := boardText(r.remote); got != text {
		t.Fatalf("host clipboard = %q", got)
	}
	c, err := Get(ctx, r.client)
	if err != nil || c.Kind != KindText || c.String() != text || c.MIME != MIMEText {
		t.Fatalf("Get = %+v, %v", c, err)
	}

	// Larger than one frame chunk, so chunking is exercised.
	big := testPNG(t, 900, 900, true)
	if len(big) < 2*chunkSize {
		t.Fatalf("test image too small for chunking: %d", len(big))
	}
	img, err := PNG(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := Set(ctx, r.client, img); err != nil {
		t.Fatal(err)
	}
	c, err = Get(ctx, r.client)
	if err != nil || c.Kind != KindImage || !bytes.Equal(c.Data, big) || c.MIME != MIMEImage {
		t.Fatalf("image round trip failed: kind=%v len=%d err=%v", c.Kind, len(c.Data), err)
	}
}

func TestSetRejectsBadContent(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if err := Set(ctx, r.client, Content{Kind: KindImage, Data: []byte("not a png")}); err == nil {
		t.Error("Set accepted a non-PNG image")
	}
	if err := Set(ctx, r.client, Content{Kind: KindText, Data: []byte{0xff, 0xfe}}); err == nil {
		t.Error("Set accepted invalid UTF-8")
	}
	if err := Set(ctx, r.client, Text("")); !errors.Is(err, ErrEmpty) {
		t.Errorf("empty text: %v", err)
	}
	if err := Set(ctx, r.client, Text(strings.Repeat("a", MaxSize+1))); !errors.Is(err, ErrTooLarge) {
		t.Errorf("oversize text: %v, want ErrTooLarge", err)
	}
	// Exactly at the limit is fine.
	if err := Set(ctx, r.client, Text(strings.Repeat("a", MaxSize))); err != nil {
		t.Errorf("text at the limit: %v", err)
	}
}

// A malicious client must not make the host allocate or accept more than the
// limit, nor an item whose hash or format lies.
func TestHostRejectsHostileItems(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()

	open := func() peer.Stream {
		s, err := r.client.Open(ctx, Service, request{Op: opSet})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		return s
	}

	// Announces more than MaxSize: the host drops the stream.
	s := open()
	wire.WriteJSON(s, message{Item: &itemMeta{Kind: KindText, Size: MaxSize + 1, Hash: "x"}})
	var m message
	if err := wire.ReadJSON(s, &m); err == nil && m.Error == "" {
		t.Errorf("oversize announcement accepted: %+v", m)
	}

	// Lying hash.
	s = open()
	data := []byte("payload")
	wire.WriteJSON(s, message{Item: &itemMeta{Kind: KindText, Size: len(data), Hash: "deadbeef"}})
	wire.WriteFrame(s, frameData, data)
	m = message{}
	if err := wire.ReadJSON(s, &m); err != nil || !strings.Contains(m.Error, "hash") {
		t.Errorf("bad hash: %+v, %v", m, err)
	}

	// Frame longer than announced.
	s = open()
	wire.WriteJSON(s, message{Item: &itemMeta{Kind: KindText, Size: 3, Hash: "x"}})
	wire.WriteFrame(s, frameData, []byte("too long"))
	if err := wire.ReadJSON(s, &message{}); err == nil {
		t.Error("overlong frame was accepted")
	}

	// Unknown kind, and PNG kind with non-PNG data.
	s = open()
	bad := Content{Kind: KindImage, Data: []byte("GIF89a....")}
	wire.WriteJSON(s, message{Item: &itemMeta{Kind: KindImage, Size: len(bad.Data), Hash: bad.Hash()}})
	wire.WriteFrame(s, frameData, bad.Data)
	m = message{}
	if err := wire.ReadJSON(s, &m); err != nil || m.Error == "" {
		t.Errorf("non-PNG image: %+v, %v", m, err)
	}
	if _, err := r.remote.Read(ctx); !errors.Is(err, ErrEmpty) {
		t.Errorf("host clipboard was modified by rejected items: %v", err)
	}

	// Unknown operation.
	s, err := r.client.Open(ctx, Service, request{Op: "bogus"})
	if err != nil {
		t.Fatal(err)
	}
	m = message{}
	if err := wire.ReadJSON(s, &m); err != nil || m.Error == "" {
		t.Errorf("unknown op: %+v, %v", m, err)
	}
}

// startSync runs Sync in the background and returns once it is established.
type syncRun struct {
	cancel context.CancelFunc
	done   chan error
	pushes atomic.Int32
	pulls  atomic.Int32
	refuse atomic.Int32
}

func startSync(t *testing.T, r *rig, opts SyncOptions) *syncRun {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s := &syncRun{cancel: cancel, done: make(chan error, 1)}
	ready := make(chan SyncInfo, 1)
	opts.Local = r.local
	opts.History = r.hist
	opts.OnReady = func(i SyncInfo) { ready <- i }
	opts.OnTransfer = func(tr Transfer) {
		switch {
		case tr.Err != nil:
			s.refuse.Add(1)
		case tr.Dir == DirPush:
			s.pushes.Add(1)
		default:
			s.pulls.Add(1)
		}
	}
	go func() { s.done <- Sync(ctx, r.client, opts) }()
	select {
	case i := <-ready:
		if i.LocalBackend != "memory" || i.RemoteBackend != "memory" {
			t.Errorf("SyncInfo = %+v", i)
		}
	case err := <-s.done:
		t.Fatalf("Sync ended early: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("Sync did not become ready")
	}
	t.Cleanup(func() { s.stop(t) })
	return s
}

func (s *syncRun) stop(t *testing.T) {
	t.Helper()
	s.cancel()
	select {
	case err := <-s.done:
		if err != nil {
			t.Errorf("Sync returned %v after cancel", err)
		}
		s.done <- nil
	case <-time.After(5 * time.Second):
		t.Error("Sync did not stop after cancel")
	}
}

// settle waits long enough for any echo to have bounced if it were going to.
func settle() { time.Sleep(300 * time.Millisecond) }

func TestSyncBothDirectionsNoEcho(t *testing.T) {
	r := newRig(t)
	s := startSync(t, r, SyncOptions{Peer: "test-host"})
	ctx := context.Background()

	// Pre-existing content is not transferred, only changes.
	r.local.Write(ctx, Text("one"))
	eventually(t, "push", func() bool { return boardText(r.remote) == "one" })
	r.remote.Write(ctx, Text("two"))
	eventually(t, "pull", func() bool { return boardText(r.local) == "two" })

	img, _ := PNG(testPNG(t, 40, 30, false))
	r.local.Write(ctx, img)
	eventually(t, "image push", func() bool { return boardHash(r.remote) == img.Hash() })
	img2, _ := PNG(testPNG(t, 30, 40, false))
	r.remote.Write(ctx, img2)
	eventually(t, "image pull", func() bool { return boardHash(r.local) == img2.Hash() })

	settle()
	// Exactly one transfer per change: nothing bounced back.
	if p, q := s.pushes.Load(), s.pulls.Load(); p != 2 || q != 2 {
		t.Fatalf("transfers: %d pushes, %d pulls, want 2 and 2", p, q)
	}
	// Re-copying identical content is not re-sent either.
	r.local.Write(ctx, img2)
	r.remote.Write(ctx, img2)
	settle()
	if p, q := s.pushes.Load(), s.pulls.Load(); p != 2 || q != 2 {
		t.Fatalf("duplicate content transferred: %d pushes, %d pulls", p, q)
	}

	s.stop(t)
	entries, err := r.hist.List(Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 4 {
		t.Fatalf("history has %d entries, want 4: %+v", len(entries), entries)
	}
	want := []struct {
		dir  Direction
		kind Kind
	}{{DirPush, KindText}, {DirPull, KindText}, {DirPush, KindImage}, {DirPull, KindImage}}
	for i, w := range want {
		e := entries[i]
		if e.Dir != w.dir || e.Kind != w.kind || e.Via != "sync" || e.Peer != "test-host" {
			t.Errorf("entry %d = %+v, want %v %v via sync", i, e, w.dir, w.kind)
		}
	}
}

func TestSyncConvergesOnSimultaneousChange(t *testing.T) {
	r := newRig(t)
	s := startSync(t, r, SyncOptions{})
	ctx := context.Background()

	for round := 0; round < 25; round++ {
		a := Text("local-" + string(rune('a'+round)))
		b := Text("remote-" + string(rune('a'+round)))
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); <-start; r.local.Write(ctx, a) }()
		go func() { defer wg.Done(); <-start; r.remote.Write(ctx, b) }()
		close(start)
		wg.Wait()
		eventually(t, "convergence", func() bool {
			l, rm := boardText(r.local), boardText(r.remote)
			return l == rm && (l == a.String() || l == b.String())
		})
		settle2 := boardText(r.local)
		time.Sleep(60 * time.Millisecond)
		if boardText(r.local) != settle2 || boardText(r.remote) != settle2 {
			t.Fatalf("round %d: clipboards kept changing after convergence", round)
		}
	}
	// A later, non-concurrent change still wins afterwards.
	r.remote.Write(ctx, Text("final"))
	eventually(t, "final pull", func() bool { return boardText(r.local) == "final" })
	if got := s.pushes.Load() + s.pulls.Load(); got > 51 { // two sends per round plus the final one
		t.Errorf("too many transfers (%d): looks like ping-pong", got)
	}
}

func TestSyncDirectionAndImageFlags(t *testing.T) {
	ctx := context.Background()

	t.Run("push-only", func(t *testing.T) {
		r := newRig(t)
		s := startSync(t, r, SyncOptions{PushOnly: true})
		r.remote.Write(ctx, Text("from host"))
		r.local.Write(ctx, Text("from client"))
		eventually(t, "push", func() bool { return boardText(r.remote) == "from client" })
		settle()
		if got := boardText(r.local); got != "from client" {
			t.Errorf("push-only client applied a remote change: %q", got)
		}
		if s.pulls.Load() != 0 {
			t.Errorf("push-only pulled %d items", s.pulls.Load())
		}
	})
	t.Run("pull-only", func(t *testing.T) {
		r := newRig(t)
		s := startSync(t, r, SyncOptions{PullOnly: true})
		r.local.Write(ctx, Text("from client"))
		r.remote.Write(ctx, Text("from host"))
		eventually(t, "pull", func() bool { return boardText(r.local) == "from host" })
		settle()
		if got := boardText(r.remote); got != "from host" {
			t.Errorf("pull-only client changed the host clipboard: %q", got)
		}
		if s.pushes.Load() != 0 {
			t.Errorf("pull-only pushed %d items", s.pushes.Load())
		}
	})
	t.Run("no-images", func(t *testing.T) {
		r := newRig(t)
		s := startSync(t, r, SyncOptions{NoImages: true})
		img, _ := PNG(testPNG(t, 10, 10, false))
		r.local.Write(ctx, img)
		r.remote.Write(ctx, img)
		r.local.Write(ctx, Text("text still flows"))
		eventually(t, "text push", func() bool { return boardText(r.remote) == "text still flows" })
		settle()
		if s.pushes.Load() != 1 || s.pulls.Load() != 0 {
			t.Errorf("transfers with --no-images: %d pushes, %d pulls", s.pushes.Load(), s.pulls.Load())
		}
	})
	t.Run("exclusive", func(t *testing.T) {
		r := newRig(t)
		if err := Sync(ctx, r.client, SyncOptions{Local: r.local, PushOnly: true, PullOnly: true}); err == nil {
			t.Error("both PushOnly and PullOnly accepted")
		}
	})
}

// injectBoard lets a test feed arbitrary "clipboard changes" to a sync.
type injectBoard struct {
	*MemoryBoard
	inj chan Content
}

func (b *injectBoard) Watch(ctx context.Context) <-chan Content { return b.inj }

func TestSyncSurvivesRefusedItem(t *testing.T) {
	r := newRig(t)
	ib := &injectBoard{MemoryBoard: r.local, inj: make(chan Content, 4)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{})
	var refused atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- Sync(ctx, r.client, SyncOptions{Local: ib, NoHistory: true,
			OnReady: func(SyncInfo) { close(ready) },
			OnTransfer: func(tr Transfer) {
				if tr.Err != nil {
					refused.Add(1)
				}
			}})
	}()
	<-ready
	// Over the limit: reported, not sent. The next change still flows.
	ib.inj <- Content{Kind: KindText, MIME: MIMEText, Data: make([]byte, MaxSize+1)}
	r.local.Write(ctx, Text("after")) // a real change: the board holds what the event says
	ib.inj <- Text("after")
	eventually(t, "push after refusal", func() bool { return boardText(r.remote) == "after" })
	if refused.Load() != 1 {
		t.Errorf("refused = %d, want 1", refused.Load())
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("Sync: %v", err)
	}
}

func TestSyncEndsWhenSessionEnds(t *testing.T) {
	r := newRig(t)
	done := make(chan error, 1)
	ready := make(chan struct{})
	go func() {
		done <- Sync(context.Background(), r.client, SyncOptions{Local: r.local, NoHistory: true, OnReady: func(SyncInfo) { close(ready) }})
	}()
	<-ready
	r.client.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Sync kept running after the session closed")
	}
}

func TestGetSetRecordHistory(t *testing.T) {
	r := newRig(t) // REMOHOP_CONFIG_DIR points at a temp dir: the default store
	ctx := context.Background()
	Set(ctx, r.client, Text("secret"))
	Get(ctx, r.client)
	h, err := OpenHistory("")
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := h.List(Filter{})
	if len(entries) != 2 || entries[0].Dir != DirPush || entries[0].Via != "set" ||
		entries[1].Dir != DirPull || entries[1].Via != "get" || entries[0].Peer != "test-host" {
		t.Fatalf("history = %+v", entries)
	}
}

func TestHelperTimeout(t *testing.T) {
	old := helperTimeout
	helperTimeout = 200 * time.Millisecond
	defer func() { helperTimeout = old }()
	start := time.Now()
	_, _, err := helper{}.output(context.Background(), nil, "sleep", "10")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("output err = %v", err)
	}
	if err := (helper{}).own(context.Background(), []byte("x"), "sleep", "10"); err == nil {
		t.Fatal("own did not time out")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("helpers took %s to give up", time.Since(start))
	}
}

func TestImageFromBytesNormalisesToPNG(t *testing.T) {
	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	jpeg.Encode(&buf, img, nil)
	c, err := ImageFromBytes(buf.Bytes())
	if err != nil || c.Kind != KindImage || !bytes.HasPrefix(c.Data, pngMagic) {
		t.Fatalf("JPEG -> %v, %v", c.Kind, err)
	}
	p := testPNG(t, 5, 5, false)
	if c, err := ImageFromBytes(p); err != nil || !bytes.Equal(c.Data, p) {
		t.Fatalf("PNG changed: %v", err)
	}
	if _, err := ImageFromBytes([]byte("hello")); err == nil {
		t.Fatal("garbage accepted as an image")
	}
}

func TestMemoryBoardWatch(t *testing.T) {
	m := NewMemoryBoard()
	ctx, cancel := context.WithCancel(context.Background())
	ch := m.Watch(ctx)
	m.Write(ctx, Text("a"))
	if c := <-ch; c.String() != "a" {
		t.Fatalf("got %q", c.String())
	}
	// A slow subscriber never blocks writers and always sees the newest.
	for i := 0; i < 100; i++ {
		m.Write(ctx, Text(strings.Repeat("x", i+1)))
	}
	var last Content
	for len(ch) > 0 {
		last = <-ch
	}
	if len(last.Data) != 100 {
		t.Fatalf("newest item lost: %d", len(last.Data))
	}
	cancel()
	for range ch {
	}
}

// Deterministic regression for stale watch events: the local user copies X,
// but before the sync loop sees that event a remote item Y is applied over it.
// The late event for X must be dropped (the clipboard no longer holds X);
// sending it would stamp it newer than Y and overwrite the peer, leaving the
// two clipboards different. Event delivery is controlled by the fake board, so
// no timing is involved.
func TestSyncDropsStaleWatchEvent(t *testing.T) {
	r := newRig(t)
	ib := &injectBoard{MemoryBoard: r.local, inj: make(chan Content, 4)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{})
	var pushes atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- Sync(ctx, r.client, SyncOptions{Local: ib, NoHistory: true,
			OnReady: func(SyncInfo) { close(ready) },
			OnTransfer: func(tr Transfer) {
				if tr.Err == nil && tr.Dir == DirPush {
					pushes.Add(1)
				}
			}})
	}()
	<-ready

	x := Text("X: local copy, event still in flight")
	r.local.Write(ctx, x) // no event delivered yet
	r.remote.Write(ctx, Text("Y: remote copy"))
	eventually(t, "remote item applied over X", func() bool { return boardText(r.local) == "Y: remote copy" })

	ib.inj <- x // the late event for the overwritten content
	settle()
	if got := boardText(r.remote); got != "Y: remote copy" {
		t.Fatalf("stale event resurrected old content on the host: %q", got)
	}
	if got := boardText(r.local); got != "Y: remote copy" {
		t.Fatalf("local clipboard changed: %q", got)
	}
	if pushes.Load() != 0 {
		t.Fatalf("stale event was sent (%d pushes)", pushes.Load())
	}

	// Fresh changes still flow afterwards.
	z := Text("Z: new local copy")
	r.local.Write(ctx, z)
	ib.inj <- z
	eventually(t, "fresh change pushed", func() bool { return boardText(r.remote) == z.String() })
	cancel()
	<-done
}
