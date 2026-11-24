package desktop

import (
	"testing"

	"github.com/readori/RemoHop/internal/inject"
)

// Every key the viewer can send reaches a macOS host, except keys a Mac
// keyboard has no equivalent for.
func TestKeymapReachesMac(t *testing.T) {
	noMacKey := map[string]bool{
		"F21": true, "F22": true, "F23": true, "F24": true, "Power": true, "Lang5": true,
		"Lang3": true, "Lang4": true, "Convert": true, "KanaMode": true, "NonConvert": true,
		"NumpadParenLeft": true, "NumpadParenRight": true, "Sleep": true, "WakeUp": true, "Eject": true,
		"BrowserStop": true, "Again": true, "Props": true, "Undo": true, "Select": true, "Copy": true,
		"Open": true, "Paste": true, "Find": true, "Cut": true, "LaunchApp1": true, "LaunchApp2": true,
		"LaunchMail": true, "BrowserFavorites": true, "BrowserBack": true, "BrowserForward": true,
		"BrowserHome": true, "BrowserRefresh": true, "BrowserSearch": true, "MediaTrackNext": true,
		"MediaPlayPause": true, "MediaTrackPrevious": true, "MediaStop": true,
	}
	for code, ev := range domToEvdev {
		if _, ok := inject.MacKeyForEvdev(ev); ok == noMacKey[code] {
			t.Errorf("%s (evdev %d): mapped to a Mac key = %v", code, ev, ok)
		}
	}
}
