package mcp

import (
	"reflect"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Text that came from the remote machine ends up in a model's context, so it
// must not be able to smuggle terminal control sequences (ANSI colour codes,
// OSC window-title or hyperlink sequences, cursor movement) or bidirectional
// text overrides into what the model and the person reading the transcript
// see. Invalid UTF-8 is replaced because JSON cannot carry it.
var (
	ansiCSI = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
	ansiOSC = regexp.MustCompile(`\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)?`)
	ansiESC = regexp.MustCompile(`\x1b[@-Z\\-_]`)
)

// clean sanitises s for display. Newlines and tabs survive; carriage returns
// are folded into newlines so progress bars do not hide earlier output.
func clean(s string) string {
	out, _ := sanitize(s, false)
	return out
}

// sanitize removes escape sequences and control characters from s. With
// keepCR, carriage returns are preserved (file contents: CRLF files must
// round-trip). changed reports whether anything was altered.
func sanitize(s string, keepCR bool) (out string, changed bool) {
	if isPlain(s, keepCR) {
		return s, false
	}
	t := s
	if strings.IndexByte(t, 0x1b) >= 0 {
		t = ansiOSC.ReplaceAllString(t, "")
		t = ansiCSI.ReplaceAllString(t, "")
		t = ansiESC.ReplaceAllString(t, "")
	}
	if !keepCR {
		t = strings.ReplaceAll(t, "\r\n", "\n")
	}
	t = strings.ToValidUTF8(t, "�")
	var b strings.Builder
	b.Grow(len(t))
	for _, r := range t {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r == '\r':
			if keepCR {
				b.WriteRune(r)
			} else {
				b.WriteByte('\n')
			}
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f):
		case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
		default:
			b.WriteRune(r)
		}
	}
	return b.String(), true
}

func isPlain(s string, keepCR bool) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
		case r == '\r' && keepCR:
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f):
			return false
		case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
			return false
		}
	}
	return true
}

// cleanStrings sanitises every string reachable from v (a pointer to a
// struct, slice or string), for result structs built from remote data.
func cleanStrings(v any) {
	walk(reflect.ValueOf(v))
}

func walk(v reflect.Value) {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			walk(v.Elem())
		}
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				walk(v.Field(i))
			}
		}
	case reflect.Slice, reflect.Array:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return
		}
		for i := range v.Len() {
			walk(v.Index(i))
		}
	case reflect.String:
		if v.CanSet() {
			v.SetString(clean(v.String()))
		}
	}
}

// truncUTF8 cuts s to at most n bytes without splitting a rune.
func truncUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
