package desktop

import (
	"slices"
	"testing"
)

// The all-monitors capture covers the bounding box of every monitor, also
// with monitors left of or above the primary (negative coordinates), and
// the pointer is mapped onto that box.
func TestAllMonitorsSpec(t *testing.T) {
	ds := []DisplayInfo{
		{X: 0, Y: 0, Width: 1920, Height: 1080},       // primary
		{X: -1280, Y: 56, Width: 1280, Height: 1024},  // left of it
		{X: 1920, Y: -300, Width: 1080, Height: 1920}, // right, taller, higher
	}
	spec, err := allMonitorsSpec(ds, 30)
	if err != nil {
		t.Fatal(err)
	}
	if spec.x != -1280 || spec.y != -300 || spec.width != 4280 || spec.height != 1920 || !spec.all {
		t.Fatalf("area %d,%d %dx%d all %v", spec.x, spec.y, spec.width, spec.height, spec.all)
	}
	if !slices.Contains(spec.input, "gdigrab") || !slices.Contains(spec.input, "-1280") || !slices.Contains(spec.input, "4280x1920") {
		t.Fatalf("ffmpeg input %v", spec.input)
	}
	if _, err := allMonitorsSpec(nil, 30); err == nil {
		t.Fatal("no monitors accepted")
	}
}
