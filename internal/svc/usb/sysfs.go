package usb

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const maxStr = 256

// system is the kernel's sysfs, rooted at root ("" for the real one) so that
// tests can run against a fake tree. Writes go through write so a test can
// both record them and play the part of the kernel.
type system struct {
	root  string
	write func(path, val string) error
}

// realSystem talks to the running kernel.
var realSystem = system{write: writeSysfs}

func (s system) p(parts ...string) string {
	return filepath.Join(append([]string{s.root, "/sys"}, parts...)...)
}

func (s system) put(path, val string) error {
	if s.write != nil {
		return s.write(path, val)
	}
	return writeSysfs(path, val)
}

// writeSysfs writes val with a single write(2), as sysfs attributes require.
func writeSysfs(path, val string) error {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_, err = f.WriteString(val)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func readAttr(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func clip(s string) string {
	if len(s) > maxStr {
		s = s[:maxStr]
	}
	return strings.ToValidUTF8(s, "?")
}

func hexU(s string) uint64 {
	v, _ := strconv.ParseUint(strings.TrimSpace(s), 16, 32)
	return v
}

func decU(s string) uint64 {
	v, _ := strconv.ParseUint(strings.TrimSpace(s), 10, 32)
	return v
}

var classNames = map[uint8]string{
	0x01: "audio", 0x02: "cdc-comm", 0x03: "hid", 0x05: "physical", 0x06: "image", 0x07: "printer",
	0x08: "mass-storage", 0x09: "hub", 0x0a: "cdc-data", 0x0b: "smartcard", 0x0d: "security",
	0x0e: "video", 0x0f: "health", 0x10: "av", 0x11: "billboard", 0xdc: "diagnostic",
	0xe0: "wireless", 0xef: "misc", 0xfe: "app-specific", 0xff: "vendor",
}

// ClassName names a USB class code.
func ClassName(c uint8) string {
	if n, ok := classNames[c]; ok {
		return n
	}
	return fmt.Sprintf("%02x", c)
}

// DeviceInfo is what `usb list` shows about a host device. Strings are
// peer-supplied on the client: print them through cli.Sanitize.
type DeviceInfo struct {
	BusID        string   `json:"busid"`
	VID          string   `json:"vid"`
	PID          string   `json:"pid"`
	Manufacturer string   `json:"manufacturer,omitempty"`
	Product      string   `json:"product,omitempty"`
	Serial       string   `json:"serial,omitempty"`
	Class        string   `json:"class"`                // device class, or the interfaces' classes if the device says "per interface"
	Interfaces   []string `json:"interfaces,omitempty"` // interface class names
	Speed        string   `json:"speed,omitempty"`      // Mbit/s as the kernel writes it ("480", "5000")
	Driver       string   `json:"driver,omitempty"`     // current kernel driver of the device
	Allowed      bool     `json:"allowed"`              // the host owner allowed exporting it
	Exported     bool     `json:"exported"`             // bound to usbip-host
	InUse        bool     `json:"in_use"`               // a client is using it right now
}

func (d *DeviceInfo) clean() {
	d.BusID, d.VID, d.PID = clip(d.BusID), clip(d.VID), clip(d.PID)
	d.Manufacturer, d.Product, d.Serial = clip(d.Manufacturer), clip(d.Product), clip(d.Serial)
	d.Class, d.Speed, d.Driver = clip(d.Class), clip(d.Speed), clip(d.Driver)
	if len(d.Interfaces) > 16 {
		d.Interfaces = d.Interfaces[:16]
	}
	for i := range d.Interfaces {
		d.Interfaces[i] = clip(d.Interfaces[i])
	}
}

// busids lists the USB devices (not interfaces, not root hubs) in sysfs.
func (s system) busids() ([]string, error) {
	ents, err := os.ReadDir(s.p("bus/usb/devices"))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if ValidBusID(e.Name()) {
			out = append(out, e.Name())
		}
	}
	sort.Slice(out, func(i, j int) bool { return busidLess(out[i], out[j]) })
	return out, nil
}

func busidLess(a, b string) bool {
	pa, pb := strings.FieldsFunc(a, func(r rune) bool { return r == '-' || r == '.' }), strings.FieldsFunc(b, func(r rune) bool { return r == '-' || r == '.' })
	for i := 0; i < len(pa) && i < len(pb); i++ {
		x, _ := strconv.Atoi(pa[i])
		y, _ := strconv.Atoi(pb[i])
		if x != y {
			return x < y
		}
	}
	return len(pa) < len(pb)
}

func (s system) devDir(busid string) string { return s.p("bus/usb/devices", busid) }

// driverOf is the kernel driver bound to the device itself ("usb" for an
// ordinary device, "usbip-host" while exported), or "".
func (s system) driverOf(busid string) string {
	l, err := os.Readlink(filepath.Join(s.devDir(busid), "driver"))
	if err != nil {
		return ""
	}
	return filepath.Base(l)
}

// interfaces reads the interface descriptors of a device.
func (s system) interfaces(busid string) []Interface {
	dir := s.devDir(busid)
	ents, _ := os.ReadDir(dir)
	var out []Interface
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), busid+":") {
			continue
		}
		d := filepath.Join(dir, e.Name())
		if _, err := os.Stat(filepath.Join(d, "bInterfaceClass")); err != nil {
			continue
		}
		out = append(out, Interface{uint8(hexU(readAttr(filepath.Join(d, "bInterfaceClass")))),
			uint8(hexU(readAttr(filepath.Join(d, "bInterfaceSubClass")))), uint8(hexU(readAttr(filepath.Join(d, "bInterfaceProtocol"))))})
		if len(out) >= maxInterfaces {
			break
		}
	}
	return out
}

func speedCode(mbps string) uint32 {
	switch strings.TrimSpace(mbps) {
	case "1.5":
		return SpeedLow
	case "12":
		return SpeedFull
	case "480":
		return SpeedHigh
	case "53.3", "106.7", "480.0":
		return SpeedWireless
	case "5000":
		return SpeedSuper
	case "10000", "20000":
		return SpeedSuperPlus
	}
	return SpeedUnknown
}

// ErrNoDevice means the busid is not a device of this machine.
var ErrNoDevice = errors.New("no such USB device")

// device reads the usbip view of one device.
func (s system) device(busid string) (Device, []Interface, error) {
	if !ValidBusID(busid) {
		return Device{}, nil, ErrNoDevice
	}
	dir := s.devDir(busid)
	if _, err := os.Stat(filepath.Join(dir, "idVendor")); err != nil {
		return Device{}, nil, ErrNoDevice
	}
	path := dir
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		path = r
	}
	if s.root != "" { // keep the wire path realistic in tests
		path = strings.TrimPrefix(path, s.root)
	}
	rd := func(n string) string { return readAttr(filepath.Join(dir, n)) }
	d := Device{
		Path: path, BusID: busid,
		BusNum: uint32(decU(rd("busnum"))), DevNum: uint32(decU(rd("devnum"))),
		Speed:    speedCode(rd("speed")),
		VendorID: uint16(hexU(rd("idVendor"))), ProductID: uint16(hexU(rd("idProduct"))), BCDDevice: uint16(hexU(rd("bcdDevice"))),
		Class: uint8(hexU(rd("bDeviceClass"))), SubClass: uint8(hexU(rd("bDeviceSubClass"))), Protocol: uint8(hexU(rd("bDeviceProtocol"))),
		ConfigValue: uint8(decU(rd("bConfigurationValue"))), NumConfigs: uint8(decU(rd("bNumConfigurations"))),
	}
	ifs := s.interfaces(busid)
	d.NumInterfaces = uint8(len(ifs))
	return d, ifs, nil
}

// info builds the listing row of a device.
func (s system) info(busid string, allowed bool) (DeviceInfo, bool) {
	d, ifs, err := s.device(busid)
	if err != nil {
		return DeviceInfo{}, false
	}
	dir := s.devDir(busid)
	rd := func(n string) string { return readAttr(filepath.Join(dir, n)) }
	di := DeviceInfo{
		BusID: busid, VID: fmt.Sprintf("%04x", d.VendorID), PID: fmt.Sprintf("%04x", d.ProductID),
		Manufacturer: rd("manufacturer"), Product: rd("product"), Serial: rd("serial"),
		Speed: rd("speed"), Driver: s.driverOf(busid), Allowed: allowed,
	}
	seen := map[string]bool{}
	for _, i := range ifs {
		if n := ClassName(i.Class); !seen[n] {
			seen[n] = true
			di.Interfaces = append(di.Interfaces, n)
		}
	}
	di.Class = ClassName(d.Class)
	if d.Class == 0 {
		di.Class = "per-interface"
	}
	if di.Driver == "usbip-host" {
		di.Exported = true
		di.InUse = readAttr(filepath.Join(dir, "usbip_status")) == sdevUsed
	}
	di.clean()
	return di, true
}

// carriesRisk names the classes whose export would cut the host off from
// something it is probably using (its keyboard, its disk) or is a hub.
func carriesRisk(d Device, ifs []Interface) string {
	classes := []uint8{d.Class}
	for _, i := range ifs {
		classes = append(classes, i.Class)
	}
	for _, c := range classes {
		switch c {
		case 0x03:
			return "keyboard/mouse (HID)"
		case 0x08:
			return "storage (mass-storage)"
		case 0x09:
			return "hub"
		}
	}
	return ""
}

// ---- vhci (client side) ----

// VhciPort is one virtual USB port of vhci_hcd.
//
// The kernel numbers ports across all controllers (port = controller *
// ports-per-controller + root-hub port, the high-speed hub's ports before
// the super-speed hub's), prints that number in the status files and takes
// it in attach and detach, which only exist on vhci_hcd.0.
type VhciPort struct {
	Controller int    `json:"controller"`
	Hub        string `json:"hub"`    // "hs" or "ss"
	Port       int    `json:"-"`      // the kernel's port number (global over controllers)
	Index      int    `json:"port"`   // what `usb detach N` takes: the same number
	Status     int    `json:"status"` // 4 free, 6 in use
	Speed      int    `json:"speed"`
	DevID      uint32 `json:"devid"`
	LocalBusID string `json:"local_busid"`
}

const (
	vhciFree = 4 // VDEV_ST_NULL
	vhciUsed = 6 // VDEV_ST_USED
)

// ErrNoVHCI means vhci_hcd is not loaded.
var ErrNoVHCI = errors.New("vhci_hcd is not loaded")

func (s system) vhciControllers() []int {
	var out []int
	for n := 0; n < 16; n++ {
		if _, err := os.Stat(s.p("devices/platform", "vhci_hcd."+strconv.Itoa(n))); err == nil {
			out = append(out, n)
		}
	}
	return out
}

func (s system) vhciPorts() ([]VhciPort, error) {
	ctrls := s.vhciControllers()
	if len(ctrls) == 0 {
		return nil, ErrNoVHCI
	}
	var out []VhciPort
	for _, c := range ctrls {
		name := "status"
		if c > 0 {
			name += "." + strconv.Itoa(c)
		}
		// Every controller's status file is an attribute of vhci_hcd.0.
		b, err := os.ReadFile(s.vhciFile(name))
		if err != nil && c > 0 {
			b, err = os.ReadFile(s.p("devices/platform", "vhci_hcd."+strconv.Itoa(c), name))
		}
		if err != nil {
			return nil, err
		}
		out = append(out, parseVhciStatus(c, string(b))...)
	}
	return out, nil
}

// parseVhciStatus parses the vhci_hcd status file:
//
//	hub port sta spd dev      sockfd local_busid
//	hs  0000 004 000 00000000 000000 0-0
//
// Kernels before 4.x have no hub column; those lines have six fields.
func parseVhciStatus(ctrl int, text string) []VhciPort {
	var out []VhciPort
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || f[0] == "hub" || f[0] == "prt" {
			continue
		}
		hub := "hs"
		if len(f) == 7 {
			hub, f = f[0], f[1:]
		}
		if len(f) != 6 {
			continue
		}
		port, e1 := strconv.Atoi(f[0])
		sta, e2 := strconv.Atoi(f[1])
		spd, e3 := strconv.Atoi(f[2])
		if e1 != nil || e2 != nil || e3 != nil || port < 0 || port > maxVhciPort {
			continue
		}
		p := VhciPort{Controller: ctrl, Hub: hub, Port: port, Index: port, Status: sta, Speed: spd,
			DevID: uint32(hexU(f[3])), LocalBusID: clip(f[5])}
		out = append(out, p)
	}
	return out
}

// freePorts lists the idle ports that can carry a device of speed.
func freePorts(ports []VhciPort, speed uint32) []VhciPort {
	want := "hs"
	if speed >= SpeedSuper {
		want = "ss"
	}
	var out []VhciPort
	for _, p := range ports {
		if p.Status == vhciFree && p.Hub == want {
			out = append(out, p)
		}
	}
	return out
}

// maxVhciPort bounds the kernel's port numbers: at most 128 controllers
// (USB_VHCI_NR_HCS) of 2 x 15 ports (USB_VHCI_HC_PORTS).
const maxVhciPort = 128*30 - 1

// vhciFile is an attribute of vhci_hcd.0, which carries attach, detach and
// the status files of every controller.
func (s system) vhciFile(name string) string {
	return s.p("devices/platform", "vhci_hcd.0", name)
}
