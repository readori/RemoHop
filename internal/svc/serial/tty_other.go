//go:build !linux && !darwin && !windows

package serial

import "fmt"

func openDevice(path string, cfg Config) (Port, error) {
	return nil, fmt.Errorf("%w (host)", ErrUnsupported)
}
