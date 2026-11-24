package inject

// Ctrl+Alt+Del is the secure attention sequence on Windows: the system takes
// it from the keyboard driver only, so a synthesised Delete with Ctrl and
// Alt held does nothing. A host that can raise the sequence another way (the
// Windows service with unattended access, through SendSAS) turns that chord
// into the real thing; see SASSender.

// Held-key identifiers, as the Windows injector records them: "ev" plus an
// evdev code for physical keys, "sym" plus a hex keysym for symbolic ones.
var (
	sasCtrl   = []string{"ev29", "ev97", "symffe3", "symffe4"}
	sasAlt    = []string{"ev56", "ev100", "symffe9", "symffea"}
	sasDelete = map[string]bool{"ev111": true, "symffff": true}
)

// isSASChord reports whether pressing the key id while the keys for which
// held reports true are down completes Ctrl+Alt+Delete.
func isSASChord(id string, held func(id string) bool) bool {
	if !sasDelete[id] {
		return false
	}
	anyHeld := func(ids []string) bool {
		for _, k := range ids {
			if held(k) {
				return true
			}
		}
		return false
	}
	return anyHeld(sasCtrl) && anyHeld(sasAlt)
}
