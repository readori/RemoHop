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

// GatewayRunning reports whether the gateway runs with an open listener
// (no I/O: it cannot see a socket iOS took away; EnsureGateway can).
func GatewayRunning() bool { return mobile.GatewayRunning() }

// EnsureGateway returns the URL of a gateway that has just answered a
// request: it starts one, or reopens a paused or dead listener (same port
// and token when possible). Call it on every return to the foreground and
// when the page cannot reach the gateway; load the URL only if it changed.
func EnsureGateway() (string, error) { return mobile.EnsureGateway() }

// PauseGateway closes the listener but keeps the gateway and its sessions
// (iOS: call it when the app goes to the background).
func PauseGateway() { mobile.PauseGateway() }

// BreakGatewayListenerForTest breaks the listener as iOS does to a
// suspended app (tests of EnsureGateway only).
func BreakGatewayListenerForTest(stall bool) { mobile.BreakGatewayListenerForTest(stall) }

// CheckSessions re-establishes host sessions that stopped answering (call
// it when the network changes).
func CheckSessions() { mobile.CheckSessions() }

// SessionsJSON lists the host sessions: [{"key","target","state",...}].
func SessionsJSON() (string, error) { return mobile.SessionsJSON() }

// DisconnectAll ends every host session (the notification's Disconnect).
func DisconnectAll() { mobile.DisconnectAll() }

// DropSessionsForTest drops every host connection as an outage would; the
// sessions reconnect by themselves (tests only).
func DropSessionsForTest() { mobile.DropSessionsForTest() }

// CheckRendezvous connects to a rendezvous server ("" = the configured one)
// and reports the outcome as JSON {"ok","code","error"|"server","key"}.
func CheckRendezvous(addr string) (string, error) { return mobile.CheckRendezvous(addr) }

// GetSetting reads "signal", "signal-pin", "relay" or "name".
func GetSetting(key string) (string, error) { return mobile.GetSetting(key) }

// SetSetting validates and stores one of those settings ("" removes it).
func SetSetting(key, value string) error { return mobile.SetSetting(key, value) }

// SetSettingJSON is SetSetting with a JSON outcome carrying a code.
func SetSettingJSON(key, value string) (string, error) { return mobile.SetSettingJSON(key, value) }

// PendingConnections counts connection attempts that have not finished.
func PendingConnections() int { return mobile.PendingConnections() }

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

// SavedPassword is the password saved for a host that does not pair.
func SavedPassword(target string) (string, error) { return mobile.SavedPassword(target) }

// ForgetSavedPassword deletes the password saved for a host.
func ForgetSavedPassword(target string) error { return mobile.ForgetSavedPassword(target) }

// Forget discards what is saved for a host.
func Forget(ref string) (int, error) { return mobile.Forget(ref) }

// --- host mode: this phone can be viewed and controlled (internal/mobile/host.go) ---

// HostListener receives host-mode events: the whole state as JSON when it
// changes, and each request that starts waiting for the owner.
type HostListener interface {
	HostChanged(state string)
	HostRequest(request string)
}

// ScreenSource is the native screen capture and H.264 encoder.
type ScreenSource interface {
	StartScreen(maxSize, fps int, bitrate int64) error
	StopScreen()
	RequestKeyframe()
	SetBitrate(bitrate int64)
}

// InputInjector delivers a controller's input (coordinates are 0..1
// fractions of the shared picture).
type InputInjector interface {
	Unavailable() string
	Move(x, y float64) error
	Button(button int, down bool, x, y float64) error
	Wheel(dx, dy float64) error
	Key(code string, down bool) error
	TypeText(text string) error
	ReleaseAll()
}

// SetHostListener sets (nil clears) the receiver of host events.
func SetHostListener(l HostListener) { mobile.SetHostListener(l) }

// SetScreenSource sets (nil clears) the native screen capture.
func SetScreenSource(s ScreenSource) { mobile.SetScreenSource(s) }

// SetInputInjector sets (nil clears) the native input injector.
func SetInputInjector(i InputInjector) { mobile.SetInputInjector(i) }

// PushScreenFrame hands the core one encoder output buffer (H.264 Annex B).
func PushScreenFrame(data []byte, captureMicros int64) error {
	return mobile.PushScreenFrame(data, captureMicros)
}

// SetScreenInfo reports the screen's size in pixels and rotation in degrees.
func SetScreenInfo(width, height, rotation int) { mobile.SetScreenInfo(width, height, rotation) }

// StartHost starts host mode (every login asks; temporary password only).
func StartHost() error { return mobile.StartHost() }

// StopHost stops host mode and ends every session.
func StopHost() { mobile.StopHost() }

// HostRunning reports whether host mode runs.
func HostRunning() bool { return mobile.HostRunning() }

// HostStateJSON returns host mode's state as JSON.
func HostStateJSON() (string, error) { return mobile.HostStateJSON() }

// DecideHostRequest answers request n (control: may also control the phone).
func DecideHostRequest(n int, allow, control bool) error {
	return mobile.DecideHostRequest(n, allow, control)
}

// SetHostSessionControl gives a connected session control or takes it away.
func SetHostSessionControl(id int, control bool) error {
	return mobile.SetHostSessionControl(id, control)
}

// DisconnectHostSession ends a connected session; 0 ends them all.
func DisconnectHostSession(id int) error { return mobile.DisconnectHostSession(id) }

// RegenerateHostPassword replaces the temporary password.
func RegenerateHostPassword() error { return mobile.RegenerateHostPassword() }
