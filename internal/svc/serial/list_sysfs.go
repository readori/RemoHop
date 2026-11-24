package serial

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ttyPrefixes are the kernel names of real serial ports.
var ttyPrefixes = []string{"ttyUSB", "ttyACM", "ttyS", "ttyAMA", "ttyTHS", "ttyO", "ttyUL", "ttyPS", "ttymxc", "ttySAC", "ttyHS", "ttyXRUSB"}

func readTrim(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// listSysfs enumerates the serial ports under a sysfs tty class directory
// (normally /sys/class/tty) whose device nodes exist under devDir. Ports
// without hardware (the 32 phantom ttyS nodes of a PC) are left out: they
// have a device link but a port type of 0.
func listSysfs(classDir, devDir string) ([]PortInfo, error) {
	ents, err := os.ReadDir(classDir)
	if err != nil {
		return nil, err
	}
	byID := byIDNames(devDir)
	var out []PortInfo
	for _, e := range ents {
		name := e.Name()
		if !hasPrefix(name) {
			continue
		}
		dir := filepath.Join(classDir, name)
		dev := filepath.Join(dir, "device")
		if _, err := os.Lstat(dev); err != nil {
			continue
		}
		if strings.HasPrefix(name, "ttyS") {
			if t := readTrim(filepath.Join(dir, "type")); t == "" || t == "0" {
				continue
			}
		}
		node := filepath.Join(devDir, name)
		if st, err := os.Stat(node); err != nil || st.IsDir() {
			continue
		}
		p := PortInfo{Path: node, Name: name, ByID: byID[name]}
		if drv, err := os.Readlink(filepath.Join(dev, "driver")); err == nil {
			p.Driver = filepath.Base(drv)
		}
		usbInfo(&p, dev)
		p.clean()
		out = append(out, p)
		if len(out) >= maxPorts {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return natLess(out[i].Name, out[j].Name) })
	return out, nil
}

func hasPrefix(name string) bool {
	for _, p := range ttyPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// usbInfo fills VID/PID/description from the USB device above a tty's device
// link, if there is one.
func usbInfo(p *PortInfo, dev string) {
	d, err := filepath.EvalSymlinks(dev)
	if err != nil {
		return
	}
	for i := 0; i < 6 && d != "/" && d != "."; i++ {
		if _, err := os.Stat(filepath.Join(d, "idVendor")); err == nil {
			p.VID = readTrim(filepath.Join(d, "idVendor"))
			p.PID = readTrim(filepath.Join(d, "idProduct"))
			p.Serial = readTrim(filepath.Join(d, "serial"))
			p.Description = strings.TrimSpace(readTrim(filepath.Join(d, "manufacturer")) + " " + readTrim(filepath.Join(d, "product")))
			return
		}
		d = filepath.Dir(d)
	}
}

// byIDNames maps tty names to their /dev/serial/by-id aliases.
func byIDNames(devDir string) map[string][]string {
	dir := filepath.Join(devDir, "serial", "by-id")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	m := map[string][]string{}
	for _, e := range ents {
		l, err := os.Readlink(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		m[filepath.Base(l)] = append(m[filepath.Base(l)], filepath.Join(dir, e.Name()))
	}
	return m
}

// natLess orders ttyUSB2 before ttyUSB10.
func natLess(a, b string) bool {
	i := len(a)
	for i > 0 && a[i-1] >= '0' && a[i-1] <= '9' {
		i--
	}
	j := len(b)
	for j > 0 && b[j-1] >= '0' && b[j-1] <= '9' {
		j--
	}
	if a[:i] != b[:j] {
		return a < b
	}
	na, nb := a[i:], b[j:]
	if len(na) != len(nb) {
		return len(na) < len(nb)
	}
	return na < nb
}
