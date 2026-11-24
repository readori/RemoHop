package quick

import "golang.org/x/sys/windows"

// SystemLang is the language of the user's Windows interface: "zh", "en"
// or "".
func SystemLang() string {
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetUserDefaultUILanguage")
	if proc.Find() != nil {
		return ""
	}
	id, _, _ := proc.Call()
	// The low ten bits of a LANGID are the primary language.
	switch id & 0x3ff {
	case 0x04: // LANG_CHINESE
		return "zh"
	case 0x09: // LANG_ENGLISH
		return "en"
	}
	return ""
}
