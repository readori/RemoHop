package x11disp

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// xauthEntry is one record of an Xauthority file.
type xauthEntry struct {
	family  uint16
	addr    string
	display string
	name    string
	data    []byte
}

// parseXauthority reads every record of an Xauthority file. A truncated
// trailing record is ignored.
func parseXauthority(r io.Reader) []xauthEntry {
	var out []xauthEntry
	str := func() (string, bool) {
		var n uint16
		if binary.Read(r, binary.BigEndian, &n) != nil {
			return "", false
		}
		b := make([]byte, n)
		if _, err := io.ReadFull(r, b); err != nil {
			return "", false
		}
		return string(b), true
	}
	for {
		var e xauthEntry
		if binary.Read(r, binary.BigEndian, &e.family) != nil {
			return out
		}
		var ok1, ok2, ok3, ok4 bool
		e.addr, ok1 = str()
		e.display, ok2 = str()
		e.name, ok3 = str()
		var data string
		data, ok4 = str()
		if !(ok1 && ok2 && ok3 && ok4) {
			return out
		}
		e.data = []byte(data)
		out = append(out, e)
	}
}

// cookie is a candidate MIT-MAGIC-COOKIE-1.
type cookie struct {
	hex string
	// exact is true when standard Xauthority lookup (libXau, as used by
	// ffmpeg and xclip) would find this cookie for us: a wildcard entry or
	// one for this hostname.
	exact bool
}

// cookiesFor picks the MIT-MAGIC-COOKIE-1 cookies in entries that may unlock
// display number n. Entries for this machine's hostname come first; the
// others are kept because containers and display managers often write
// cookies under a different hostname.
func cookiesFor(entries []xauthEntry, n int, hostname string) []cookie {
	const familyLocal, familyWild = 256, 65535
	disp := strconv.Itoa(n)
	type cand struct {
		c    cookie
		rank int
	}
	var cands []cand
	for _, e := range entries {
		if e.name != "MIT-MAGIC-COOKIE-1" || len(e.data) != 16 {
			continue
		}
		if e.display != "" && e.display != disp {
			continue
		}
		rank, exact := 2, false
		switch {
		case e.family == familyWild:
			rank, exact = 0, true
		case e.family == familyLocal && e.addr == hostname:
			rank, exact = 0, true
		case e.display == disp:
			rank = 1
		}
		cands = append(cands, cand{cookie{hex.EncodeToString(e.data), exact}, rank})
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].rank < cands[j].rank })
	seen := map[string]bool{}
	var out []cookie
	for _, c := range cands {
		if !seen[c.c.hex] {
			seen[c.c.hex] = true
			out = append(out, c.c)
		}
	}
	return out
}

// marshalXauthority serialises entries in the Xauthority file format.
func marshalXauthority(es []xauthEntry) []byte {
	var b []byte
	put16 := func(v int) { b = append(b, byte(v>>8), byte(v)) }
	for _, e := range es {
		put16(int(e.family))
		for _, s := range []string{e.addr, e.display, e.name, string(e.data)} {
			put16(len(s))
			b = append(b, s...)
		}
	}
	return b
}

// parseDisplay splits an X display string (":0", ":0.1", "unix:0",
// "host:2") into host, display number and screen.
func parseDisplay(s string) (host string, n, screen int, err error) {
	i := strings.LastIndexByte(s, ':')
	if i < 0 || i == len(s)-1 {
		return "", 0, 0, fmt.Errorf("bad display %q", s)
	}
	host = s[:i]
	rest := s[i+1:]
	if j := strings.LastIndexByte(rest, '.'); j >= 0 {
		if screen, err = strconv.Atoi(rest[j+1:]); err != nil || screen < 0 {
			return "", 0, 0, fmt.Errorf("bad display %q", s)
		}
		rest = rest[:j]
	}
	if n, err = strconv.Atoi(rest); err != nil || n < 0 {
		return "", 0, 0, fmt.Errorf("bad display %q", s)
	}
	return host, n, screen, nil
}
