//go:build !unix

package xfer

import (
	"testing"
	"time"
)

// waitUnlocked: partial files are not locked here; give the host a moment
// to close its copy.
func waitUnlocked(*testing.T, string) { time.Sleep(100 * time.Millisecond) }
