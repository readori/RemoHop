package desktop

import (
	"context"
	"fmt"

	"github.com/readori/RemoHop/internal/unattended"
)

// Controls a Windows host cannot offer, and why.
var platformUnsupported = map[string]string{
	ControlCtrlAltDel: "Windows accepts Ctrl+Alt+Del only from the keyboard driver or from a system service (SendSAS); " +
		"remohop runs in the user's session and cannot send it (the RemoHop service with unattended access can: remohop service install --unattended)",
	ControlBlockInput: "not implemented on Windows hosts",
	ControlBlackout:   "not implemented on Windows hosts",
}

func init() {
	// The service with unattended access sends Ctrl+Alt+Del itself
	// (SendSAS): the injector turns the key chord into it.
	adjustControls = func(out map[string]string) {
		if !unattended.Active() {
			return
		}
		if ok, why := unattended.SASAvailable(); ok {
			delete(out, ControlCtrlAltDel)
		} else {
			out[ControlCtrlAltDel] = why
		}
	}
}

var procLockWorkStation = user32.NewProc("LockWorkStation")

// lockScreen locks the interactive session; the service with unattended
// access has its helper in the console session do it.
func lockScreen(ctx context.Context, _ captureSpec) error {
	if unattended.Active() {
		return unattended.Lock(ctx)
	}
	if r, _, err := procLockWorkStation.Call(); r == 0 {
		return fmt.Errorf("LockWorkStation: %v", err)
	}
	return nil
}
