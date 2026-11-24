// Package remohopcore is what `gomobile bind` turns into the Android AAR and
// the iOS XCFramework. It re-exports internal/mobile one for one: gomobile
// cannot bind a package under internal/, because the wrapper it generates
// lives outside this repository's import tree. Keep the two in step; the
// documentation is in internal/mobile.
//
// Java:  com.remohop.app.core.remohopcore.Remohopcore.startGateway()
// Swift: RemohopcoreStartGateway(&error)
package remohopcore

import "github.com/readori/RemoHop/internal/mobile"

// Init sets the app's private configuration directory and its temporary
// directory (may be empty). Call it first.
func Init(configDir, tempDir string) error { return mobile.Init(configDir, tempDir) }

// SetAppVersion sets the version shown by the page.
func SetAppVersion(v string) { mobile.SetAppVersion(v) }

// SetDeviceName sets the label used when pairing (the phone's model).
func SetDeviceName(name string) { mobile.SetDeviceName(name) }

// SetLocalAddresses reports this device's IP addresses (comma or space
// separated) for LAN candidates; Android 11+ hides them from Go.
func SetLocalAddresses(list string) { mobile.SetLocalAddresses(list) }

// StartGateway starts the loopback gateway and returns its URL, which
// contains the access token: load it in the WebView, never log it.
func StartGateway() (string, error) { return mobile.StartGateway() }

// StopGateway stops the gateway and every host session.
func StopGateway() { mobile.StopGateway() }

// GatewayRunning reports whether the gateway serves.
func GatewayRunning() bool { return mobile.GatewayRunning() }

// GetSetting reads "signal", "signal-pin", "relay" or "name".
func GetSetting(key string) (string, error) { return mobile.GetSetting(key) }

// SetSetting validates and stores one of those settings ("" removes it).
func SetSetting(key, value string) error { return mobile.SetSetting(key, value) }

// SettingsJSON returns the managed settings and the identity fingerprint.
func SettingsJSON() (string, error) { return mobile.SettingsJSON() }

// Identity returns this device's identity fingerprint.
func Identity() (string, error) { return mobile.Identity() }

// PublicKeyLine returns the line `remohop authorize` takes on a host.
func PublicKeyLine() (string, error) { return mobile.PublicKeyLine() }

// DevicesJSON lists saved hosts, optionally with presence.
func DevicesJSON(checkOnline bool) (string, error) { return mobile.DevicesJSON(checkOnline) }

// PairWithPassword pairs with a host; the outcome is JSON either way.
func PairWithPassword(target, password, alias string) (string, error) {
	return mobile.PairWithPassword(target, password, alias)
}

// CancelPair stops a pairing in progress.
func CancelPair() { mobile.CancelPair() }

// SetAlias names a saved host ("" removes the name).
func SetAlias(target, alias string) error { return mobile.SetAlias(target, alias) }

// Forget discards what is saved for a host.
func Forget(ref string) (int, error) { return mobile.Forget(ref) }
