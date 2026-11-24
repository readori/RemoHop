//go:build !linux && !darwin && !windows

package serial

// ListPorts is not implemented on this platform.
func ListPorts() ([]PortInfo, error) { return nil, ErrUnsupported }
