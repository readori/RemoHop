package desktop

import (
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/readori/RemoHop/internal/unattended"
)

var (
	procOpenInputDesktop  = user32.NewProc("OpenInputDesktop")
	procCloseDesktop      = user32.NewProc("CloseDesktop")
	procGetUserObjectInfo = user32.NewProc("GetUserObjectInformationW")
)

// secureReason is what the viewer is told while Windows shows a secure
// desktop.
const secureReason = "Windows is showing a secure screen (a UAC prompt, the lock or sign-in screen, or Ctrl+Alt+Del), " +
	"which only the RemoHop service with unattended access can capture; the picture comes back when it closes"

// platformSecureDesktop says why the screen cannot be captured right now: the input
// desktop is not the user's ("Default") but the secure one (Winlogon) of a
// UAC prompt, the lock or sign-in screen or Ctrl+Alt+Del. Desktop
// Duplication then loses its output (DXGI_ERROR_ACCESS_LOST), and only
// LocalSystem may duplicate or BitBlt the secure desktop. A process outside
// it may not even open it, which says the same. The unattended service's
// helper follows the input desktop and captures it, so for it this is "".
func platformSecureDesktop() string {
	if unattended.Active() {
		return ""
	}
	var session uint32
	if windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &session) == nil && session == 0 {
		// A service without a desktop of its own: there is no input
		// desktop to open, secure or not.
		return ""
	}
	h, _, _ := procOpenInputDesktop.Call(0, 0, 0x0001 /* DESKTOP_READOBJECTS */)
	if h == 0 {
		return secureReason
	}
	defer procCloseDesktop.Call(h)
	var buf [64]uint16
	var n uint32
	if r, _, _ := procGetUserObjectInfo.Call(h, 2 /* UOI_NAME */, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)*2), uintptr(unsafe.Pointer(&n))); r == 0 {
		return ""
	}
	if name := windows.UTF16ToString(buf[:]); name != "" && !strings.EqualFold(name, "Default") {
		return secureReason
	}
	return ""
}

// secureDesktop is platformSecureDesktop; tests replace it.
var secureDesktop = platformSecureDesktop
