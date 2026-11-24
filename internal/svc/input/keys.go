package input

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/readori/RemoHop/internal/inject"
)

// keyDef is one named key: its canonical name (used in results), aliases
// accepted in combos, and the keysym it presses.
type keyDef struct {
	name    string
	aliases []string
	sym     inject.Keysym
	mod     bool
}

// keyDefs are the key names a combo may use besides single characters.
// Names follow X keysym spelling (what xdotool users type) with the common
// alternatives as aliases; matching ignores case, "_" and "-".
var keyDefs = []keyDef{
	{"ctrl", []string{"control", "ctl", "control_l"}, inject.KeysymControlL, true},
	{"shift", []string{"shift_l"}, inject.KeysymShiftL, true},
	{"alt", []string{"alt_l", "option"}, inject.KeysymAltL, true},
	{"super", []string{"win", "windows", "meta", "cmd", "command", "super_l"}, inject.KeysymSuperL, true},
	{"altgr", []string{"iso_level3_shift"}, inject.KeysymLevel3, true},
	{"Return", []string{"enter", "ret"}, 0xff0d, false},
	{"KP_Enter", []string{"numpadenter"}, 0xff8d, false},
	{"Tab", nil, 0xff09, false},
	{"Escape", []string{"esc"}, 0xff1b, false},
	{"BackSpace", []string{"bksp"}, 0xff08, false},
	{"Delete", []string{"del"}, 0xffff, false},
	{"Insert", []string{"ins"}, 0xff63, false},
	{"Home", nil, 0xff50, false},
	{"End", nil, 0xff57, false},
	{"Page_Up", []string{"pgup", "prior"}, 0xff55, false},
	{"Page_Down", []string{"pgdn", "next"}, 0xff56, false},
	{"Left", []string{"arrowleft"}, 0xff51, false},
	{"Up", []string{"arrowup"}, 0xff52, false},
	{"Right", []string{"arrowright"}, 0xff53, false},
	{"Down", []string{"arrowdown"}, 0xff54, false},
	{"space", nil, 0x20, false},
	{"Menu", []string{"contextmenu", "apps"}, 0xff67, false},
	{"Print", []string{"printscreen", "prtsc"}, 0xff61, false},
	{"Pause", []string{"break"}, 0xff13, false},
	{"Caps_Lock", nil, 0xffe5, false},
	{"Num_Lock", nil, 0xff7f, false},
	{"Scroll_Lock", nil, 0xff14, false},
	{"minus", nil, '-', false},
	{"equal", nil, '=', false},
	{"plus", nil, '+', false},
	{"comma", nil, ',', false},
	{"period", []string{"dot"}, '.', false},
	{"slash", nil, '/', false},
	{"backslash", nil, '\\', false},
	{"semicolon", nil, ';', false},
	{"apostrophe", []string{"quote"}, '\'', false},
	{"grave", []string{"backtick"}, '`', false},
	{"bracketleft", nil, '[', false},
	{"bracketright", nil, ']', false},
	{"XF86AudioMute", []string{"mute", "audiomute"}, 0x1008ff12, false},
	{"XF86AudioLowerVolume", []string{"volumedown"}, 0x1008ff11, false},
	{"XF86AudioRaiseVolume", []string{"volumeup"}, 0x1008ff13, false},
	{"XF86AudioPlay", []string{"play", "playpause"}, 0x1008ff14, false},
	{"XF86AudioNext", []string{"medianext"}, 0x1008ff17, false},
	{"XF86AudioPrev", []string{"mediaprev"}, 0x1008ff16, false},
}

var keyIndex = func() map[string]keyDef {
	m := map[string]keyDef{}
	add := func(n string, d keyDef) {
		k := foldName(n)
		if _, dup := m[k]; dup {
			panic("input: duplicate key name " + n)
		}
		m[k] = d
	}
	for _, d := range keyDefs {
		add(d.name, d)
		for _, a := range d.aliases {
			add(a, d)
		}
	}
	for i := 1; i <= 24; i++ {
		add("F"+strconv.Itoa(i), keyDef{name: "F" + strconv.Itoa(i), sym: 0xffbe + inject.Keysym(i-1)})
	}
	return m
}()

func foldName(s string) string {
	return strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(s))
}

// maxComboKeys bounds the keys of one combo (four modifiers and a key).
const maxComboKeys = 5

// KeyNames is the list of valid key names, for usage errors and help.
func KeyNames() string {
	var b strings.Builder
	b.WriteString("modifiers: ")
	first := true
	for _, d := range keyDefs {
		if d.mod {
			if !first {
				b.WriteString(", ")
			}
			b.WriteString(d.name)
			first = false
		}
	}
	b.WriteString("; keys: ")
	first = true
	for _, d := range keyDefs {
		if !d.mod {
			if !first {
				b.WriteString(", ")
			}
			b.WriteString(d.name)
			first = false
		}
	}
	b.WriteString(", F1-F24, or any single character (a-z, 0-9, punctuation, é, ...)")
	return b.String()
}

// combo is a parsed key combination: keys in press order (modifiers first),
// and its canonical spelling.
type combo struct {
	syms []inject.Keysym
	name string
}

// parseCombo parses "ctrl+shift+t", "Return", "alt+F4", "ctrl++". Every
// part but the last must be a modifier; a single character is that
// character's key (letters are taken in lower case: "ctrl+T" is ctrl+t, add
// shift explicitly). Unknown names are an error listing the valid ones.
func parseCombo(s string) (combo, error) {
	if s == "" {
		return combo{}, fmt.Errorf("empty key combo")
	}
	var parts []string
	switch {
	case s == "+":
		parts = []string{"+"}
	case strings.HasSuffix(s, "++"):
		parts = append(strings.Split(s[:len(s)-2], "+"), "+")
	default:
		parts = strings.Split(s, "+")
	}
	if len(parts) > maxComboKeys {
		return combo{}, fmt.Errorf("key combo %q has more than %d keys", s, maxComboKeys)
	}
	var c combo
	var names []string
	seen := map[inject.Keysym]bool{}
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			return combo{}, fmt.Errorf("bad key combo %q: empty key name (write \"plus\" for the + key)", s)
		}
		sym, name, mod, err := lookupKey(p)
		if err != nil {
			return combo{}, err
		}
		last := i == len(parts)-1
		if !mod && !last {
			return combo{}, fmt.Errorf("bad key combo %q: %q is not a modifier; only the last key may be (modifiers: ctrl, shift, alt, super, altgr)", s, p)
		}
		if seen[sym] {
			return combo{}, fmt.Errorf("bad key combo %q: %s appears twice", s, name)
		}
		seen[sym] = true
		c.syms = append(c.syms, sym)
		names = append(names, name)
	}
	c.name = strings.Join(names, "+")
	return c, nil
}

func lookupKey(p string) (inject.Keysym, string, bool, error) {
	if utf8.RuneCountInString(p) == 1 {
		r, _ := utf8.DecodeRuneInString(p)
		if r >= 'A' && r <= 'Z' {
			r += 'a' - 'A'
		}
		sym, ok := inject.RuneKeysym(r)
		if !ok || r == '\n' || r == '\t' || r == '\r' {
			return 0, "", false, fmt.Errorf("unknown key %q; valid names are %s", p, KeyNames())
		}
		return sym, string(r), false, nil
	}
	d, ok := keyIndex[foldName(p)]
	if !ok {
		return 0, "", false, fmt.Errorf("unknown key %q; valid names are %s", p, KeyNames())
	}
	return d.sym, d.name, d.mod, nil
}

// ValidateCombo checks a key combo without sending it and returns its
// canonical spelling (for example "Control+T" gives "ctrl+t").
func ValidateCombo(s string) (string, error) {
	c, err := parseCombo(s)
	return c.name, err
}
