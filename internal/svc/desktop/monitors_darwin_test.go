package desktop

import (
	"strings"
	"testing"
)

// macOS says plainly that it has no all-monitors view.
func TestAllMonitorsRefusedOnMac(t *testing.T) {
	if _, err := discoverCapture(DisplayAll, 30); err == nil || !strings.Contains(err.Error(), "one display at a time") {
		t.Fatalf("discoverCapture(DisplayAll): %v", err)
	}
	if platformUnsupported[ControlAllMonitors] == "" {
		t.Fatal("the probe does not report the missing all-monitors view")
	}
}
