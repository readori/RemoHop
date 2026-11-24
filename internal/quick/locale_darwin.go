package quick

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// SystemLang is the user's first preferred language: "zh", "en" or "".
// An app started from Finder has no LANG, so the preference is read from
// the user defaults.
func SystemLang() string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// Prints a plist-style list: ( "zh-Hans-CN", "en-US" )
	if out, err := exec.CommandContext(ctx, "/usr/bin/defaults", "read", "-g", "AppleLanguages").Output(); err == nil {
		for _, f := range strings.FieldsFunc(string(out), func(r rune) bool {
			return r == '(' || r == ')' || r == ',' || r == '"' || r == ' ' || r == '\n' || r == '\t'
		}) {
			return normLang(f)
		}
	}
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := os.Getenv(k); v != "" && v != "C" {
			return normLang(v)
		}
	}
	return ""
}
