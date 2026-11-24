package auth

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// CleanText makes a string that came from another machine safe to print on
// a terminal, write to a log or store: it removes everything that is not
// plain visible text or a space (control characters including ESC and the
// C1 range, Unicode format characters such as bidirectional overrides and
// zero-width joiners, line and paragraph separators, invalid UTF-8) and
// cuts the result to at most max bytes without splitting a character.
//
// Peers and the rendezvous server are not trusted, and a terminal obeys
// escape sequences from whoever manages to get them printed, so every
// remote-supplied string goes through here at the point it is received.
func CleanText(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsSpace(r):
			// Including tabs and every kind of line break: the result
			// is one line.
			return ' '
		case r == utf8.RuneError,
			unicode.IsControl(r),
			unicode.Is(unicode.Cf, r),
			unicode.Is(unicode.Zl, r),
			unicode.Is(unicode.Zp, r),
			unicode.Is(unicode.Co, r),
			unicode.Is(unicode.Cs, r):
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if len(s) > max {
		cut := max
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = strings.TrimSpace(s[:cut])
	}
	return s
}
