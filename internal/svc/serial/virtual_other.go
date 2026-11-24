//go:build !linux && !darwin

package serial

import (
	"context"
	"fmt"

	"github.com/readori/RemoHop/internal/peer"
)

// AttachOptions configure Attach.
type AttachOptions struct {
	OpenOptions
	LocalPath string
}

// Virtual is a local virtual serial port; not available on this platform.
type Virtual struct {
	Remote      *Remote
	Path, Slave string
}

// Attach always fails here: virtual serial ports need a Unix pty.
func Attach(ctx context.Context, c *peer.Conn, port string, cfg Config, o AttachOptions) (*Virtual, error) {
	return nil, fmt.Errorf("%w: creating a virtual serial port needs Linux or macOS on the controlling side", ErrUnsupported)
}

func (v *Virtual) Done() <-chan struct{} { return nil }
func (v *Virtual) Err() error            { return ErrUnsupported }
func (v *Virtual) Close() error          { return nil }
