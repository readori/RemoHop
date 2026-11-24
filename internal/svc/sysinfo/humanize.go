package sysinfo

import (
	"fmt"
	"strings"
	"unicode"
)

// humanBytes formats n with binary units, e.g. "15.6 GiB".
func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit && exp < len("KMGTPE")-1; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// humanDuration formats seconds as "3d 4h 12m", dropping leading zero units.
func humanDuration(seconds int64) string {
	if seconds < 0 {
		seconds = 0
	}
	d := seconds / 86400
	h := (seconds % 86400) / 3600
	m := (seconds % 3600) / 60
	switch {
	case d > 0:
		return fmt.Sprintf("%dd %dh %dm", d, h, m)
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm", m)
	default:
		return fmt.Sprintf("%ds", seconds)
	}
}

// safe makes a string written by the remote host printable on a terminal.
// Control and format characters (such as ESC or bidi overrides) become '?', so
// a hostile host cannot rewrite the operator's screen through its hostname.
func safe(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return '?'
		}
		return r
	}, s)
}
