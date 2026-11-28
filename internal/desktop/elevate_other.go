//go:build !windows

package desktop

import "context"

// runElevated is only needed on Windows (unattended access elsewhere is a
// per-user service). It is a variable so that tests can stand in for the
// elevated helper.
var runElevated = func(context.Context, string, []string) error {
	return Errorf("unsupported", "not needed on this system")
}
