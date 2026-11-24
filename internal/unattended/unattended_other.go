//go:build !windows

package unattended

import (
	"context"
	"errors"
	"io"
)

var errWindowsOnly = errors.New("unattended access is only available for the Windows service")

// ErrNotActive is returned when the process is not a service with
// unattended access.
var ErrNotActive = errWindowsOnly

// Active reports whether this process is the Windows service with unattended
// access enabled; never on this system.
func Active() bool { return false }

// Enable is only available on Windows.
func Enable(io.Writer) (func(), error) { return nil, errWindowsOnly }

// RunHelper is only available on Windows.
func RunHelper(context.Context, io.Reader, io.Writer) error { return errWindowsOnly }

// SASAvailable reports that Ctrl+Alt+Del cannot be raised here.
func SASAvailable() (bool, string) { return false, errWindowsOnly.Error() }
