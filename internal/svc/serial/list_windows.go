//go:build windows

package serial

import (
	"sort"

	"golang.org/x/sys/windows/registry"
)

// ListPorts reads HKLM\HARDWARE\DEVICEMAP\SERIALCOMM. UNVERIFIED (compile-only).
func ListPorts() ([]PortInfo, error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `HARDWARE\DEVICEMAP\SERIALCOMM`, registry.QUERY_VALUE)
	if err != nil {
		if err == registry.ErrNotExist {
			return nil, nil
		}
		return nil, err
	}
	defer k.Close()
	names, err := k.ReadValueNames(0)
	if err != nil {
		return nil, err
	}
	var out []PortInfo
	for _, n := range names {
		com, _, err := k.GetStringValue(n)
		if err != nil || com == "" {
			continue
		}
		p := PortInfo{Path: com, Name: com, Driver: n}
		p.clean()
		out = append(out, p)
		if len(out) >= maxPorts {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return natLess(out[i].Name, out[j].Name) })
	return out, nil
}
