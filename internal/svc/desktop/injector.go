//go:build linux || windows || darwin

package desktop

import (
	"errors"
	"fmt"

	"github.com/readori/RemoHop/internal/inject"
)

// hostInjector turns viewer events (coordinates as fractions of the
// captured area, DOM key codes, fractional wheel notches) into input on the
// host through the shared injector.
type hostInjector struct {
	inj   *inject.Injector
	spec  captureSpec
	wheel wheelAcc
	// geom, on macOS, maps the captured display's pixels to the global
	// points the injector takes; geomFor finds it for a capture area.
	geom    *inject.DisplayGeometry
	geomFor func(captureSpec) *inject.DisplayGeometry
}

// domButtonToX maps DOM MouseEvent.button numbers to inject buttons.
func domButtonToX(b int) (inject.Button, bool) {
	switch b {
	case 0:
		return inject.Left, true
	case 1:
		return inject.Middle, true
	case 2:
		return inject.Right, true
	case 3:
		return inject.Back, true
	case 4:
		return inject.Forward, true
	}
	return 0, false
}

func (h *hostInjector) moveTo(nx, ny float64) error {
	if g := h.geom; g != nil {
		x, y := g.PixelToPoint(int(clamp01(nx)*float64(g.PixelWidth-1)+0.5), int(clamp01(ny)*float64(g.PixelHeight-1)+0.5))
		return h.inj.MoveToF(x, y)
	}
	if h.geomFor != nil || h.spec.width == 0 {
		return errors.New("the captured display's geometry is unknown; pointer input is unavailable")
	}
	px := h.spec.x + int(clamp01(nx)*float64(h.spec.width-1)+0.5)
	py := h.spec.y + int(clamp01(ny)*float64(h.spec.height-1)+0.5)
	return h.inj.MoveTo(px, py)
}

func (h *hostInjector) inject(ev InputEvent) error {
	switch ev.Kind {
	case InputMove:
		return h.moveTo(ev.X, ev.Y)
	case InputButton:
		b, ok := domButtonToX(ev.Button)
		if !ok {
			return fmt.Errorf("unknown mouse button %d", ev.Button)
		}
		if err := h.moveTo(ev.X, ev.Y); err != nil {
			return err
		}
		return h.inj.Button(b, ev.Down)
	case InputWheel:
		sx, sy := h.wheel.steps(ev.DX, ev.DY)
		return h.inj.Scroll(sx, sy)
	case InputKey:
		code, ok := domToEvdev[ev.Code]
		if !ok {
			return fmt.Errorf("unknown key code %q", ev.Code)
		}
		return h.inj.KeyEvdev(code, ev.Down)
	case InputReleaseAll:
		return h.inj.ReleaseAll()
	}
	return fmt.Errorf("unknown input event %q", ev.Kind)
}

func (h *hostInjector) close() { h.inj.Close() }

func (h *hostInjector) retarget(spec captureSpec) {
	h.spec = spec
	if h.geomFor != nil {
		h.geom = h.geomFor(spec)
	}
}

func (h *hostInjector) typeText(text string) error {
	// Typing must not combine with modifiers the viewer still holds.
	if err := h.inj.ReleaseAll(); err != nil {
		return err
	}
	var skipped int
	for _, r := range text {
		if _, ok := inject.RuneKeysym(r); !ok {
			skipped++
			continue
		}
		if err := h.inj.TypeRune(r); err != nil {
			return err
		}
	}
	if skipped > 0 {
		return fmt.Errorf("%d character(s) cannot be typed (control characters) and were skipped", skipped)
	}
	return nil
}
