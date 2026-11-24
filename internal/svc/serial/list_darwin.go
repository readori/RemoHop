//go:build darwin

package serial

import (
	"path/filepath"
	"sort"
	"strings"
)

// ListPorts enumerates /dev/cu.* (the call-out devices); Bluetooth and debug
// pseudo-ports are listed too, as macOS offers them.
func ListPorts() ([]PortInfo, error) {
	m, _ := filepath.Glob("/dev/cu.*")
	sort.Strings(m)
	var out []PortInfo
	for _, p := range m {
		pi := PortInfo{Path: p, Name: strings.TrimPrefix(p, "/dev/")}
		pi.clean()
		out = append(out, pi)
		if len(out) >= maxPorts {
			break
		}
	}
	return out, nil
}
