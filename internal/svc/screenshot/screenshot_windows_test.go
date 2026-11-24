package screenshot

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/peer/peertest"
	"github.com/readori/RemoHop/internal/wintest"
)

func near(c color.Color, r, g, b int) bool {
	cr, cg, cb, _ := c.RGBA()
	d := func(a uint32, want int) bool {
		v := int(a >> 8)
		return v-want < 40 && want-v < 40
	}
	return d(cr, r) && d(cg, g) && d(cb, b)
}

// TestWinScreenshotE2E takes real screenshots of this Windows desktop
// through the screenshot service, with a solid green window over the whole
// primary monitor so the content can be checked, not just the size.
func TestWinScreenshotE2E(t *testing.T) {
	if os.Getenv("REMOHOP_DESKTOP_E2E") != "1" {
		t.Skip("set REMOHOP_DESKTOP_E2E=1 to capture this Windows desktop")
	}
	t.Logf("desktop: %s", wintest.Describe())
	w := wintest.Color(t, "00FF00")
	wintest.SetCursorPos(1, 1)
	client, _ := peertest.Pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	ds, err := Displays(ctx, client)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range ds {
		t.Logf("display %+v", d)
	}
	sw, sh := wintest.ScreenSize()
	if len(ds) == 0 || !ds[0].Primary || ds[0].Width != sw || ds[0].Height != sh {
		t.Fatalf("displays %+v, GetSystemMetrics says %dx%d", ds, sw, sh)
	}

	img, err := Capture(ctx, client, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if img.Width != sw || img.Height != sh || img.Format != PNG {
		t.Fatalf("screenshot %dx%d %s, want %dx%d png", img.Width, img.Height, img.Format, sw, sh)
	}
	pic, err := png.Decode(bytes.NewReader(img.Data))
	if err != nil {
		t.Fatal(err)
	}
	cx, cy := w.Center()
	if c := pic.At(cx, cy); !near(c, 0, 255, 0) {
		t.Fatalf("pixel %d,%d of the screenshot is %v, want the window's green", cx, cy, c)
	}
	if c := pic.At(sw-3, sh-3); !near(c, 0, 255, 0) {
		t.Errorf("corner pixel is %v, want green (the window covers the monitor)", c)
	}

	// JPEG, downscaled, and a region.
	j, err := Capture(ctx, client, Options{Format: JPEG, MaxWidth: 320})
	if err != nil {
		t.Fatal(err)
	}
	jp, err := jpeg.Decode(bytes.NewReader(j.Data))
	if err != nil || jp.Bounds().Dx() != 320 {
		t.Fatalf("jpeg: %v %v", err, jp.Bounds())
	}
	if c := jp.At(160, jp.Bounds().Dy()/2); !near(c, 0, 255, 0) {
		t.Errorf("downscaled jpeg centre is %v", c)
	}
	reg, err := Capture(ctx, client, Options{Region: image.Rect(10, 20, 110, 70)})
	if err != nil {
		t.Fatal(err)
	}
	if reg.Width != 100 || reg.Height != 50 {
		t.Errorf("region screenshot %dx%d, want 100x50", reg.Width, reg.Height)
	}
	all, err := Capture(ctx, client, Options{Display: AllDisplays})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("all displays: %dx%d", all.Width, all.Height)
}
