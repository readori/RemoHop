package main

import (
	"fmt"
	"image"
	"image/draw"
	"path/filepath"
)

// msixAsset is one image of an MSIX package's visual assets (see
// packaging/windows/msix). The manifest names each logo without a
// qualifier (Assets\Square44x44Logo.png); makepri indexes the qualified
// files below, and Windows picks the one that fits the display.
type msixAsset struct {
	name   string  // file name below Assets\
	w, h   int     // canvas size in pixels
	icon   int     // size of the icon drawn in the middle
	margin float64 // the icon's own margin (0: the tile fills its square)
}

// msixAssets lists the images: the app-list and taskbar icon
// (Square44x44Logo, plated scales and unplated target sizes), the Start
// tiles (small, medium, wide, large) and the Store logo, at 100% and 200%
// scale. Tiles show the icon at about half their height on a transparent
// background (BackgroundColor="transparent" in the manifest), as Windows'
// own apps do.
func msixAssets() []msixAsset {
	var out []msixAsset
	for _, s := range []struct {
		scale int
	}{{100}, {200}} {
		k := s.scale / 100
		q := fmt.Sprintf(".scale-%d.png", s.scale)
		out = append(out,
			msixAsset{"Square44x44Logo" + q, 44 * k, 44 * k, 44 * k, 0},
			msixAsset{"Square71x71Logo" + q, 71 * k, 71 * k, 44 * k, 0},
			msixAsset{"Square150x150Logo" + q, 150 * k, 150 * k, 80 * k, 0},
			msixAsset{"Wide310x150Logo" + q, 310 * k, 150 * k, 80 * k, 0},
			msixAsset{"Square310x310Logo" + q, 310 * k, 310 * k, 160 * k, 0},
			msixAsset{"StoreLogo" + q, 50 * k, 50 * k, 50 * k, 0},
		)
	}
	for _, n := range []int{16, 24, 32, 48, 256} {
		for _, form := range []string{"", "_altform-unplated"} {
			out = append(out, msixAsset{fmt.Sprintf("Square44x44Logo.targetsize-%d%s.png", n, form), n, n, n, 0})
		}
	}
	return out
}

// msixImage draws one asset: the icon centred on a transparent canvas.
func msixImage(a msixAsset) *image.NRGBA {
	canvas := image.NewNRGBA(image.Rect(0, 0, a.w, a.h))
	icon := render(a.icon, a.margin)
	at := image.Pt((a.w-a.icon)/2, (a.h-a.icon)/2)
	draw.Draw(canvas, icon.Bounds().Add(at), icon, image.Point{}, draw.Over)
	return canvas
}

// writeMsixAssets writes every asset into dir.
func writeMsixAssets(dir string, write func(path string, data []byte) error) error {
	for _, a := range msixAssets() {
		if err := write(filepath.Join(dir, a.name), pngBytes(msixImage(a))); err != nil {
			return err
		}
	}
	return nil
}
