package input

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"runtime"
	"time"

	"github.com/readori/RemoHop/internal/inject"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/svc/screenshot"
	"github.com/readori/RemoHop/internal/wire"
)

func init() { peer.Handle(ServiceName, serve) }

// hostSem serialises input requests on the host: two agents typing at once
// would interleave characters, and a combo's modifiers would leak into the
// other request's keys.
var hostSem = make(chan struct{}, 1)

// Pacing of pointer actions, so applications see distinct events: the gap
// between a move and a click, between the clicks of a double click (well
// under any double-click time), and between wheel notches.
const (
	settleGap = 15 * time.Millisecond
	clickGap  = 40 * time.Millisecond
	wheelGap  = 8 * time.Millisecond
)

// backend is what a host action needs: an injector and the monitors, in
// the screenshot service's numbering.
type backend struct {
	inj      *inject.Injector
	displays []screenshot.Display
	space    hostSpace
	warning  string
}

func serve(ctx context.Context, c *peer.Conn, s peer.Stream, raw json.RawMessage) error {
	fail := func(err error) error { return wire.WriteJSON(s, reply{Error: err.Error()}) }
	var req request
	if err := peer.DecodeReq(raw, &req); err != nil {
		return fail(errors.New("input: bad request"))
	}
	p, err := req.validate()
	if err != nil {
		return fail(err)
	}
	if inject.ViewOnly() {
		return fail(fmt.Errorf("input is disabled on this host (%s is set)", inject.ViewOnlyEnv))
	}
	// The client never writes after the header; its reset (or the end of
	// the session) ends the read and aborts a long action.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		io.Copy(io.Discard, s)
		cancel()
	}()
	select {
	case hostSem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	res, err := func() (*Result, error) {
		defer func() { <-hostSem }()
		b, err := openBackend(ctx)
		if err != nil {
			return nil, err
		}
		// Close releases every key and button still held, whatever
		// happened, before the next request may start.
		defer b.inj.Close()
		return run(ctx, b, p)
	}()
	if err != nil {
		return fail(err)
	}
	return wire.WriteJSON(s, reply{Result: res})
}

// sleep waits d unless ctx ends first.
func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func run(ctx context.Context, b *backend, p *plan) (*Result, error) {
	req := p.req
	d, err := screenshot.SelectDisplay(b.displays, req.Display)
	if err != nil {
		return nil, fmt.Errorf("input: %w", err)
	}
	if err := b.space.check(d); err != nil {
		return nil, fmt.Errorf("input: %w", err)
	}
	res := &Result{Action: req.Op, Display: d, ScaleFrom: req.ScaleFrom, Warning: b.warning}
	inj := b.inj
	if req.At != nil {
		at, _, err := toDisplay(d, req.ScaleFrom, *req.At)
		if err != nil {
			return nil, err
		}
		hx, hy := b.space.toHost(d, at)
		v := Point{int(math.Floor(hx)), int(math.Floor(hy))}
		res.At, res.Virtual = &at, &v
		if err := inj.MoveToF(hx, hy); err != nil {
			return nil, err
		}
		if runtime.GOOS == "darwin" {
			awaitPointer(ctx, inj, hx, hy)
		}
	}
	switch req.Op {
	case opClick:
		res.Button, res.Clicks = req.Button, req.Clicks
		if err := sleep(ctx, settleGap); err != nil {
			return nil, err
		}
		for i := range req.Clicks {
			if i > 0 {
				if err := sleep(ctx, clickGap); err != nil {
					return nil, err
				}
			}
			if err := inj.Button(p.button, true); err != nil {
				return nil, err
			}
			if err := inj.Button(p.button, false); err != nil {
				return nil, err
			}
		}
	case opScroll:
		res.DX, res.DY = req.DX, req.DY
		if req.At != nil {
			if err := sleep(ctx, settleGap); err != nil {
				return nil, err
			}
		}
		// One notch at a time: applications coalesce or drop bursts.
		dx, dy := req.DX, req.DY
		for dx != 0 || dy != 0 {
			sx, sy := sign(dx), sign(dy)
			if err := inj.Scroll(sx, sy); err != nil {
				return nil, err
			}
			dx, dy = dx-sx, dy-sy
			if err := sleep(ctx, wheelGap); err != nil {
				return nil, err
			}
		}
	case opType:
		for i, r := range p.text {
			if err := inj.TypeRune(r); err != nil {
				return nil, fmt.Errorf("input: typed %d of %d characters: %w", i, len(p.text), err)
			}
			res.Chars++
			if err := sleep(ctx, p.delay); err != nil {
				return nil, err
			}
		}
	case opKey:
		for i, c := range p.combos {
			if i > 0 {
				if err := sleep(ctx, p.delay); err != nil {
					return nil, err
				}
			}
			if err := pressCombo(inj, c); err != nil {
				return nil, fmt.Errorf("input: %s: %w", c.name, err)
			}
			res.Keys = append(res.Keys, c.name)
		}
	}
	x, y, err := inj.PointerF()
	if err != nil {
		return nil, err
	}
	res.PointerVirtual = Point{int(math.Floor(x)), int(math.Floor(y))}
	res.Pointer, res.PointerScaled, res.PointerOnDisplay = fromVirtual(d, req.ScaleFrom, b.space.fromHost(d, x, y))
	return res, nil
}

func sign(v int) int {
	switch {
	case v > 0:
		return 1
	case v < 0:
		return -1
	}
	return 0
}

// pressCombo presses c's keys in order and releases them in reverse, even
// when a press fails. A final key the layout only has on its Shift level
// ("plus" on a US keyboard) gets Shift added unless the combo has it.
func pressCombo(inj *inject.Injector, c combo) (err error) {
	syms := c.syms
	last := syms[len(syms)-1]
	hasShift := false
	for _, s := range syms {
		hasShift = hasShift || s == inject.KeysymShiftL
	}
	if !hasShift && inj.NeedsShift(last) {
		syms = append(append(syms[:len(syms)-1:len(syms)-1], inject.KeysymShiftL), last)
	}
	var held []inject.Keysym
	defer func() {
		for i := len(held) - 1; i >= 0; i-- {
			if e := inj.KeySym(held[i], false); e != nil && err == nil {
				err = e
			}
		}
	}()
	for _, s := range syms {
		if err := inj.KeySym(s, true); err != nil {
			return err
		}
		held = append(held, s)
	}
	return nil
}

// awaitPointer waits briefly until the pointer reaches (x, y). macOS posts
// the move as an event that is applied after CGEventPost returns, so the
// position read right after it (and a click that follows) can be stale.
func awaitPointer(ctx context.Context, inj *inject.Injector, x, y float64) {
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if px, py, err := inj.PointerF(); err != nil || math.Abs(px-x) <= 1.5 && math.Abs(py-y) <= 1.5 {
			return
		}
		if sleep(ctx, 5*time.Millisecond) != nil {
			return
		}
	}
}
