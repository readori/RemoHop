//go:build !linux && !windows && !darwin

package desktop

import (
	"context"
	"errors"
)

var platformUnsupported = map[string]string{
	ControlType:       "not supported on this host's operating system",
	ControlLock:       "not supported on this host's operating system",
	ControlCtrlAltDel: "not supported on this host's operating system",
	ControlBlockInput: "not supported on this host's operating system",
	ControlBlackout:   "not supported on this host's operating system",
}

func lockScreen(context.Context, captureSpec) error {
	return errors.New("not supported on this host's operating system")
}
