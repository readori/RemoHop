package input

import (
	"strings"
	"testing"

	"github.com/readori/RemoHop/internal/inject"
)

func TestParseCombo(t *testing.T) {
	for _, tc := range []struct {
		in   string
		name string
		syms []inject.Keysym
	}{
		{"Return", "Return", []inject.Keysym{0xff0d}},
		{"enter", "Return", []inject.Keysym{0xff0d}},
		{"ctrl+c", "ctrl+c", []inject.Keysym{0xffe3, 'c'}},
		{"Control+C", "ctrl+c", []inject.Keysym{0xffe3, 'c'}},
		{"ctrl+shift+t", "ctrl+shift+t", []inject.Keysym{0xffe3, 0xffe1, 't'}},
		{"alt+F4", "alt+F4", []inject.Keysym{0xffe9, 0xffc1}},
		{"super+l", "super+l", []inject.Keysym{0xffeb, 'l'}},
		{"win+L", "super+l", []inject.Keysym{0xffeb, 'l'}},
		{"F1", "F1", []inject.Keysym{0xffbe}},
		{"f24", "F24", []inject.Keysym{0xffd5}},
		{"page_up", "Page_Up", []inject.Keysym{0xff55}},
		{"PageDown", "Page_Down", []inject.Keysym{0xff56}},
		{"page-down", "Page_Down", []inject.Keysym{0xff56}},
		{"ArrowLeft", "Left", []inject.Keysym{0xff51}},
		{"esc", "Escape", []inject.Keysym{0xff1b}},
		{"backspace", "BackSpace", []inject.Keysym{0xff08}},
		{"ctrl++", "ctrl++", []inject.Keysym{0xffe3, '+'}},
		{"+", "+", []inject.Keysym{'+'}},
		{"ctrl+plus", "ctrl+plus", []inject.Keysym{0xffe3, '+'}},
		{"ctrl+-", "ctrl+-", []inject.Keysym{0xffe3, '-'}},
		{"ctrl + alt + Delete", "ctrl+alt+Delete", []inject.Keysym{0xffe3, 0xffe9, 0xffff}},
		{"shift", "shift", []inject.Keysym{0xffe1}},
		{"ctrl+shift", "ctrl+shift", []inject.Keysym{0xffe3, 0xffe1}},
		{"é", "é", []inject.Keysym{0xe9}},
		{"ctrl+ж", "ctrl+ж", []inject.Keysym{0xffe3, 0x01000436}},
		{"space", "space", []inject.Keysym{0x20}},
		{"mute", "XF86AudioMute", []inject.Keysym{0x1008ff12}},
	} {
		c, err := parseCombo(tc.in)
		if err != nil {
			t.Errorf("%q: %v", tc.in, err)
			continue
		}
		if c.name != tc.name || len(c.syms) != len(tc.syms) {
			t.Errorf("%q: got %q %#x, want %q %#x", tc.in, c.name, c.syms, tc.name, tc.syms)
			continue
		}
		for i := range c.syms {
			if c.syms[i] != tc.syms[i] {
				t.Errorf("%q: syms %#x, want %#x", tc.in, c.syms, tc.syms)
			}
		}
	}
	for _, tc := range []struct{ in, want string }{
		{"", "empty"},
		{"Foo", `unknown key "Foo"`},
		{"ctrl+Foo", `unknown key "Foo"`},
		{"F25", `unknown key "F25"`},
		{"a+b", `"a" is not a modifier`},
		{"Return+ctrl", `"Return" is not a modifier`},
		{"ctrl+ctrl+a", "appears twice"},
		{"ctrl+", "empty key name"},
		{"+a", "empty key name"},
		{"ctrl+shift+alt+super+altgr+a", "more than 5 keys"},
		{"\x1b", "unknown key"},
		{"\t", "empty key name"},
	} {
		_, err := parseCombo(tc.in)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: error %v, want %q", tc.in, err, tc.want)
		}
	}
	// The error lists the valid names.
	_, err := parseCombo("nope")
	for _, want := range []string{"ctrl", "super", "Return", "Page_Down", "F1-F24", "single character"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("unknown-key error lacks %q: %v", want, err)
		}
	}
	if n, err := ValidateCombo("Control+T"); err != nil || n != "ctrl+t" {
		t.Errorf("ValidateCombo: %q %v", n, err)
	}
}

func TestRuneKeysym(t *testing.T) {
	for r, want := range map[rune]inject.Keysym{
		'a': 'a', '~': '~', 'é': 0xe9, 'ÿ': 0xff, '\n': 0xff0d, '\t': 0xff09,
		'€': 0x010020ac, '世': 0x01004e16, '😀': 0x0101f600,
	} {
		got, ok := inject.RuneKeysym(r)
		if !ok || got != want {
			t.Errorf("%q: %#x %v, want %#x", r, got, ok, want)
		}
		if back, ok := inject.KeysymRune(got); r != '\r' && (!ok || back != r) {
			t.Errorf("%q round trip: %q %v", r, back, ok)
		}
	}
	for _, r := range []rune{0, 0x1b, 0x7f, 0x85, 0xfffe, 0x1ffff, -1, 0x110000} {
		if _, ok := inject.RuneKeysym(r); ok {
			t.Errorf("%U accepted", r)
		}
	}
}
