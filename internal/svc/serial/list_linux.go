//go:build linux

package serial

// ListPorts enumerates the serial ports of this machine.
func ListPorts() ([]PortInfo, error) { return listSysfs("/sys/class/tty", "/dev") }
