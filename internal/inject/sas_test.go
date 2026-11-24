package inject

import "testing"

func TestIsSASChord(t *testing.T) {
	heldOf := func(ids ...string) func(string) bool {
		m := map[string]bool{}
		for _, id := range ids {
			m[id] = true
		}
		return func(id string) bool { return m[id] }
	}
	cases := []struct {
		press string
		held  []string
		want  bool
	}{
		{"ev111", []string{"ev29", "ev56"}, true},         // left Ctrl + left Alt + Delete
		{"ev111", []string{"ev97", "ev100"}, true},        // right ones
		{"symffff", []string{"symffe3", "symffe9"}, true}, // keysyms (remohop input key ctrl+alt+delete)
		{"symffff", []string{"ev29", "symffea"}, true},    // mixed
		{"ev111", []string{"ev29"}, false},                // Ctrl+Delete
		{"ev111", []string{"ev56"}, false},                // Alt+Delete
		{"ev111", nil, false},
		{"ev14", []string{"ev29", "ev56"}, false}, // Ctrl+Alt+Backspace
		{"ev83", []string{"ev29", "ev56"}, false}, // keypad Delete is not taken
	}
	for _, c := range cases {
		if got := isSASChord(c.press, heldOf(c.held...)); got != c.want {
			t.Errorf("isSASChord(%q, %v) = %v, want %v", c.press, c.held, got, c.want)
		}
	}
}
