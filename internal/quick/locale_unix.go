//go:build !windows && !darwin

package quick

import "os"

// SystemLang is the language of the user's session: "zh", "en" or "" when
// it is neither (the page then follows the browser).
func SystemLang() string {
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANGUAGE", "LANG"} {
		if v := os.Getenv(k); v != "" && v != "C" && v != "POSIX" {
			return normLang(v)
		}
	}
	return ""
}
