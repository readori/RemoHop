//go:build !windows

package desktop

import "context"

// runElevated is only needed on Windows (unattended access elsewhere is a
// per-user service).
func runElevated(context.Context, string, []string) error {
	return Errorf("unsupported", "not needed on this system")
}
