//go:build windows

package vpn

// IfIndex is the interface index of a tunnel adapter, as netsh takes it.
func IfIndex(dev string) (string, error) { return interfaceIndex(dev) }
