package inject

import "math"

// DisplayGeometry describes a display on hosts whose input coordinates are
// not screen pixels. On macOS the injector works in global display points
// (origin at the main display's top-left; displays left of or above it have
// negative coordinates), while screenshots are in pixels, two per point on
// a Retina display. Each display has its own scale.
type DisplayGeometry struct {
	// X, Y, Width, Height are the display's bounds in global points.
	X, Y, Width, Height float64
	// PixelWidth, PixelHeight are its size in pixels, as a screenshot of
	// it is.
	PixelWidth, PixelHeight int
	// Main marks the main display (menu bar; origin of the global space).
	Main bool
}

// PixelToPoint maps pixel (px, py) of the display, counted from its
// top-left, to the global point at the centre of that pixel, so that on a
// 2x display pixel 1 lands on the right half of point 0, exactly where a
// screenshot shows it.
func (g DisplayGeometry) PixelToPoint(px, py int) (x, y float64) {
	if g.PixelWidth <= 0 || g.PixelHeight <= 0 {
		return g.X + float64(px), g.Y + float64(py)
	}
	x = g.X + (float64(px)+0.5)*g.Width/float64(g.PixelWidth)
	y = g.Y + (float64(py)+0.5)*g.Height/float64(g.PixelHeight)
	return x, y
}

// PointToPixel maps a global point to the pixel of this display it falls
// in. The result lies outside 0..PixelWidth-1 (or -Height) when the point
// is on another display.
func (g DisplayGeometry) PointToPixel(x, y float64) (px, py int) {
	if g.Width <= 0 || g.Height <= 0 || g.PixelWidth <= 0 || g.PixelHeight <= 0 {
		return int(math.Floor(x - g.X)), int(math.Floor(y - g.Y))
	}
	px = int(math.Floor((x - g.X) * float64(g.PixelWidth) / g.Width))
	py = int(math.Floor((y - g.Y) * float64(g.PixelHeight) / g.Height))
	return px, py
}
