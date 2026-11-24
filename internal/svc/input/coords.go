package input

import (
	"errors"
	"math"

	"github.com/readori/RemoHop/internal/inject"
	"github.com/readori/RemoHop/internal/svc/screenshot"
)

// hostSpace converts between display pixels and the coordinates the host's
// injector takes. On X11 and Windows those are virtual-screen pixels (geom
// is nil); on macOS they are global points, with one DisplayGeometry per
// display in the screenshot service's order.
type hostSpace struct {
	geom []inject.DisplayGeometry
}

var errAllPoints = errors.New(`display "all" is not available on macOS hosts (each display has its own scale); choose a display from "screenshot --list"`)

func (s hostSpace) geometry(d screenshot.Display) (inject.DisplayGeometry, bool) {
	if s.geom == nil || d.Index < 0 || d.Index >= len(s.geom) {
		return inject.DisplayGeometry{}, false
	}
	return s.geom[d.Index], true
}

// check refuses displays the space cannot address.
func (s hostSpace) check(d screenshot.Display) error {
	if s.geom != nil && d.Index == screenshot.AllDisplays {
		return errAllPoints
	}
	return nil
}

// toHost maps pixel p of display d (as toDisplay returns it) to injector
// coordinates.
func (s hostSpace) toHost(d screenshot.Display, p Point) (x, y float64) {
	if g, ok := s.geometry(d); ok {
		return g.PixelToPoint(p.X, p.Y)
	}
	return float64(d.X + p.X), float64(d.Y + p.Y)
}

// fromHost maps injector coordinates to the virtual-pixel position
// fromVirtual expects for display d.
func (s hostSpace) fromHost(d screenshot.Display, x, y float64) Point {
	if g, ok := s.geometry(d); ok {
		px, py := g.PointToPixel(x, y)
		return Point{d.X + px, d.Y + py}
	}
	return Point{int(math.Floor(x)), int(math.Floor(y))}
}

// macDisplays describes macOS displays the way the screenshot service
// numbers them: index in CGGetActiveDisplayList order (main first, as
// screencapture -D counts them), size in pixels, origin in points.
func macDisplays(geom []inject.DisplayGeometry) []screenshot.Display {
	ds := make([]screenshot.Display, len(geom))
	for i, g := range geom {
		ds[i] = screenshot.Display{Index: i, X: int(math.Round(g.X)), Y: int(math.Round(g.Y)),
			Width: g.PixelWidth, Height: g.PixelHeight, Primary: g.Main}
		if g.Main {
			ds[i].Name = "main" // what screenshot --list calls it
		}
	}
	return ds
}
