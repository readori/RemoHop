//go:build !windows

package quick

// systemAppBrowsers is empty outside Windows: no Chromium-family browser
// is part of macOS or of Linux desktops, and starting one the user did not
// choose as their browser would be presumptuous. The page opens in the
// default browser (or pass --app-browser).
func systemAppBrowsers() []string { return nil }
