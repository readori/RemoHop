//go:build !linux && !darwin && !windows

package desktop

import (
	"context"
	"fmt"
	"runtime"
)

func platformAudioSources(context.Context, string) ([]audioSource, string) {
	return nil, fmt.Sprintf("audio forwarding is not supported on %s hosts", runtime.GOOS)
}

func audioHelp() string { return "" }
