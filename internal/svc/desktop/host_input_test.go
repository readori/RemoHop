package desktop

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/wire"
)

// pipeStream is a peer.Stream over net.Pipe.
type pipeStream struct {
	net.Conn
	ctx    context.Context
	cancel context.CancelFunc
}

func newPipeStream(c net.Conn) *pipeStream {
	ctx, cancel := context.WithCancel(context.Background())
	return &pipeStream{Conn: c, ctx: ctx, cancel: cancel}
}

func (p *pipeStream) Close() error             { p.cancel(); return p.Conn.Close() }
func (p *pipeStream) CloseWrite() error        { return nil }
func (p *pipeStream) Reset()                   { p.Close() }
func (p *pipeStream) Context() context.Context { return p.ctx }

// fakeInjector records what reaches it.
type fakeInjector struct {
	mu     sync.Mutex
	events []InputEvent
	closed bool
	typed  int
}

func (f *fakeInjector) inject(ev InputEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		panic("inject after close")
	}
	f.events = append(f.events, ev)
	return nil
}

func (f *fakeInjector) typeText(text string, stop func() bool) error {
	for range text {
		if stop() {
			return nil
		}
		time.Sleep(time.Millisecond)
		f.mu.Lock()
		f.typed++
		f.mu.Unlock()
	}
	return nil
}

func (f *fakeInjector) retarget(captureSpec) {}

func (f *fakeInjector) close() {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
}

func (f *fakeInjector) snapshot() ([]InputEvent, bool, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]InputEvent(nil), f.events...), f.closed, f.typed
}

// inputSession is a host session with only its control side running
// (readControl), on a fake injector: no capture, no encoder.
func inputSession(t *testing.T) (*hostSession, *fakeInjector, net.Conn) {
	t.Helper()
	hostEnd, clientEnd := net.Pipe()
	inj := &fakeInjector{}
	h := &hostSession{
		s: newPipeStream(hostEnd), inj: inj,
		ctl: make(chan Control, 16), out: make(chan []byte, 64), wdone: make(chan struct{}),
		inputChanged: make(chan struct{}, 1),
	}
	h.ctx, h.cancel = context.WithCancel(context.Background())
	go h.readControl()
	t.Cleanup(func() { h.cancel(); clientEnd.Close(); hostEnd.Close() })
	return h, inj, clientEnd
}

// roundTrip sends c and then a ping, and waits for the pong: by then the
// host has handled c.
func roundTrip(t *testing.T, h *hostSession, client net.Conn, c Control) {
	t.Helper()
	if err := wire.WriteJSON(client, c); err != nil {
		t.Fatal(err)
	}
	if err := wire.WriteJSON(client, Control{Type: ctlPing, T: 1}); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case msg := <-h.out:
			if msg[4] == framePong {
				return
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no pong")
		}
	}
}

// Switching input off at the host releases what the client holds and
// drops everything the client sends afterwards, on the stream that is
// already open; switching it back on is left to run (it needs the capture
// area) and announced to it.
func TestSetHostInput(t *testing.T) {
	h, inj, client := inputSession(t)
	roundTrip(t, h, client, Control{Type: InputKey, Code: "ShiftLeft", Down: true})
	if ev, _, _ := inj.snapshot(); len(ev) != 1 {
		t.Fatalf("input before the switch: %+v", ev)
	}

	h.setHostInput(false)
	if _, closed, _ := inj.snapshot(); !closed {
		t.Fatal("the injector (and what it holds down) was not released")
	}
	select {
	case <-h.inputChanged:
	default:
		t.Fatal("run was not told input changed")
	}
	h.injMu.Lock()
	inErr, have := h.inErr, h.inj != nil
	h.injMu.Unlock()
	if have || inErr != hostInputOff {
		t.Fatalf("after the switch: injector %v, reason %q", have, inErr)
	}
	for _, c := range []Control{
		{Type: InputMove, X: 0.5, Y: 0.5},
		{Type: InputKey, Code: "KeyA", Down: true},
		{Type: InputButton, Button: 0, Down: true},
		{Type: InputWheel, DY: 1},
		{Type: InputReleaseAll},
	} {
		roundTrip(t, h, client, c) // inject after close would panic
	}
	if ev, _, _ := inj.snapshot(); len(ev) != 1 {
		t.Fatalf("input reached the host after the switch: %+v", ev[1:])
	}
	if err := h.hostControl(Control{Type: ctlType, Text: "x"}); err == nil {
		t.Fatal("typing allowed after the switch")
	}
	// Repeating the same state changes nothing.
	h.setHostInput(false)
	select {
	case <-h.inputChanged:
		t.Fatal("a repeated switch was announced again")
	default:
	}
	h.setHostInput(true)
	select {
	case <-h.inputChanged:
	default:
		t.Fatal("switching back on was not announced")
	}
}

// Text being typed stops between characters when input is switched off,
// instead of holding the switch up until it is all typed.
func TestSetHostInputStopsTyping(t *testing.T) {
	h, inj, _ := inputSession(t)
	text := make([]byte, maxTypeText)
	for i := range text {
		text[i] = 'a'
	}
	done := make(chan error, 1)
	go func() { done <- h.hostControl(Control{Type: ctlType, Text: string(text)}) }()
	time.Sleep(50 * time.Millisecond)
	start := time.Now()
	h.setHostInput(false)
	if d := time.Since(start); d > time.Second {
		t.Fatalf("switching input off waited %v for typing to finish", d)
	}
	if err := <-done; err == nil {
		t.Fatal("typing that was cut short reported success")
	}
	if _, _, typed := inj.snapshot(); typed == 0 || typed >= len(text) {
		t.Fatalf("typed %d of %d characters", typed, len(text))
	}
}

// Relative input (a pointer locked by the viewer) is input like any other:
// it reaches the host while the session may control it, and nothing of it
// does once the host's owner switches input off, or in a session without
// input (view-only).
func TestRelativeInputGated(t *testing.T) {
	h, inj, client := inputSession(t)
	roundTrip(t, h, client, Control{Type: InputMoveBy, DX: 3, DY: -2})
	roundTrip(t, h, client, Control{Type: InputButton, Button: 0, Down: true, Rel: true})
	ev, _, _ := inj.snapshot()
	if len(ev) != 2 || ev[0].Kind != InputMoveBy || ev[0].DX != 3 || ev[0].DY != -2 || !ev[1].Rel {
		t.Fatalf("relative input on a session with input: %+v", ev)
	}
	h.setHostInput(false)
	roundTrip(t, h, client, Control{Type: InputMoveBy, DX: 3})
	roundTrip(t, h, client, Control{Type: InputButton, Button: 0, Down: false, Rel: true})
	if ev, _, _ := inj.snapshot(); len(ev) != 2 {
		t.Fatalf("relative input reached the host after input was switched off: %+v", ev[2:])
	}

	viewOnly, _, client2 := inputSession(t)
	viewOnly.injMu.Lock()
	viewOnly.inj, viewOnly.inErr = nil, "view-only session"
	viewOnly.injMu.Unlock()
	roundTrip(t, viewOnly, client2, Control{Type: InputMoveBy, DX: 5}) // would panic on a nil injector
}

// Fractional relative movement adds up to whole pixels, and one huge
// event is bounded.
func TestMoveAcc(t *testing.T) {
	var m moveAcc
	sum := 0
	for range 10 {
		x, _ := m.steps(0.25, 0)
		sum += x
	}
	if sum != 2 {
		t.Errorf("ten moves of 0.25 gave %d pixels", sum)
	}
	if x, y := m.steps(1e9, -1e9); x != maxMoveBy || y != -maxMoveBy {
		t.Errorf("a huge move gave %d,%d", x, y)
	}
	nan := 0.0
	nan /= nan
	if x, y := m.steps(nan, 1); x != 0 || y != 0 {
		t.Errorf("NaN gave %d,%d", x, y)
	}
}
