package shell

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEscaper(t *testing.T) {
	cases := []struct {
		in   string
		out  string
		act  escAction
		esc  byte
		name string
	}{
		{"ls\r", "ls\r", escNone, '~', "plain"},
		{"~.", "", escDetach, '~', "detach at start"},
		{"a~.", "a~.", escNone, '~', "not at line start"},
		{"x\r~.rest", "x\r", escDetach, '~', "detach after newline"},
		{"~~x", "~x", escNone, '~', "literal escape"},
		{"~x", "~x", escNone, '~', "unknown escape passes through"},
		{"~?", "", escHelp, '~', "help"},
		{"~.", "~.", escNone, 0, "disabled"},
		{"\n\x1d.", "\n", escDetach, 0x1d, "control escape"},
	}
	for _, c := range cases {
		e := &escaper{esc: c.esc, lineStart: true}
		out, act := e.feed([]byte(c.in))
		if string(out) != c.out || act != c.act {
			t.Errorf("%s: feed(%q) = %q, %v; want %q, %v", c.name, c.in, out, act, c.out, c.act)
		}
	}
	// An escape split across reads.
	e := &escaper{esc: '~', lineStart: true}
	if out, act := e.feed([]byte("~")); len(out) != 0 || act != escNone {
		t.Fatalf("pending escape leaked %q", out)
	}
	if _, act := e.feed([]byte(".")); act != escDetach {
		t.Fatal("split escape not recognised")
	}
}

func TestRingReplay(t *testing.T) {
	r := ring{size: 10}
	r.write([]byte("abc"))
	if string(r.snapshot()) != "abc" {
		t.Fatal(string(r.snapshot()))
	}
	r.write([]byte("defgh\nijklmnop"))
	// Wrapped: the last 10 bytes are "h\nijklmnop"; replay starts after the
	// first newline.
	if got := string(r.snapshot()); got != "ijklmnop" {
		t.Fatalf("snapshot %q", got)
	}
	for range 100 {
		r.write([]byte("0123456789"))
	}
	if len(r.buf) >= 2*r.size+10 {
		t.Fatalf("ring grew to %d", len(r.buf))
	}
}

func TestParseEscape(t *testing.T) {
	for in, want := range map[string]byte{"~": '~', "none": 0, "^]": 0x1d, "^a": 1, "#": '#'} {
		if got, err := parseEscape(in); err != nil || got != want {
			t.Errorf("parseEscape(%q) = %v, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "ab", "^1", "\x01"} {
		if _, err := parseEscape(bad); err == nil {
			t.Errorf("parseEscape(%q) accepted", bad)
		}
	}
}

func TestTailOffset(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "log"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() // Windows cannot delete the temp dir while it is open
	f.WriteString("1\n2\n3\n4\n5\n")
	for n, want := range map[int]int64{1: 8, 2: 6, 5: 0, 9: 0} {
		if got, _ := tailOffset(f, n); got != want {
			t.Errorf("tail %d: offset %d want %d", n, got, want)
		}
	}
	f.WriteString("partial")
	if got, _ := tailOffset(f, 1); got != 10 {
		t.Errorf("unterminated last line: offset %d", got)
	}
}
