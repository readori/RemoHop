package serial

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func mk(t *testing.T, p string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestListSysfsFakeTree(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("sysfs only exists on Linux (production use of listSysfs); fake tree relies on POSIX symlinks and paths")
	}
	root := t.TempDir()
	class := filepath.Join(root, "sys/class/tty")
	dev := filepath.Join(root, "dev")
	usb := filepath.Join(root, "sys/devices/pci0/usb1/1-2")
	mk(t, filepath.Join(usb, "idVendor"), "1a86\n")
	mk(t, filepath.Join(usb, "idProduct"), "7523\n")
	mk(t, filepath.Join(usb, "manufacturer"), "QinHeng\n")
	mk(t, filepath.Join(usb, "product"), "USB Serial\x1b[2J\n")
	mk(t, filepath.Join(usb, "serial"), "A1B2")
	iface := filepath.Join(usb, "1-2:1.0")
	os.MkdirAll(filepath.Join(iface, "ttyUSB0"), 0o755)
	drv := filepath.Join(root, "sys/bus/usb/drivers/ch341-uart")
	os.MkdirAll(drv, 0o755)

	add := func(name string, withDevice bool) {
		mk(t, filepath.Join(dev, name), "")
		os.MkdirAll(filepath.Join(class, name), 0o755)
		if withDevice {
			if err := os.Symlink(iface, filepath.Join(class, name, "device")); err != nil {
				t.Fatal(err)
			}
		}
	}
	add("ttyUSB0", true)
	os.Symlink(drv, filepath.Join(iface, "driver"))
	add("ttyUSB10", true)
	add("ttyUSB2", true)
	// phantom 16550 slot: device link but type 0
	add("ttyS0", true)
	mk(t, filepath.Join(class, "ttyS0", "type"), "0\n")
	// real on-board UART
	add("ttyS1", true)
	mk(t, filepath.Join(class, "ttyS1", "type"), "4\n")
	// console, no device link; vt; a port whose /dev node is gone
	add("tty0", false)
	add("ttyUSB5", true)
	os.Remove(filepath.Join(dev, "ttyUSB5"))
	os.MkdirAll(filepath.Join(dev, "serial/by-id"), 0o755)
	os.Symlink("../../ttyUSB0", filepath.Join(dev, "serial/by-id/usb-QinHeng_USB_Serial-if00-port0"))

	ports, err := listSysfs(class, dev)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range ports {
		names = append(names, p.Name)
	}
	if want := []string{"ttyS1", "ttyUSB0", "ttyUSB2", "ttyUSB10"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("names %v, want %v", names, want)
	}
	var u PortInfo
	for _, p := range ports {
		if p.Name == "ttyUSB0" {
			u = p
		}
	}
	if u.VID != "1a86" || u.PID != "7523" || u.Serial != "A1B2" || u.Driver != "ch341-uart" ||
		u.Path != filepath.Join(dev, "ttyUSB0") || len(u.ByID) != 1 {
		t.Fatalf("ttyUSB0 = %+v", u)
	}
	if u.Description != "QinHeng USB Serial\x1b[2J" {
		// raw text is kept (bounded); printing sanitises it
		t.Fatalf("description %q", u.Description)
	}
}

func TestConfigParse(t *testing.T) {
	c := DefaultConfig()
	if err := c.ParseFormat("7e2"); err != nil || c.Data != 7 || c.Parity != 'E' || c.Stop != 2 {
		t.Fatalf("%+v %v", c, err)
	}
	for _, bad := range []string{"", "9N1", "8X1", "8N3", "8N", "88N11"} {
		c := DefaultConfig()
		if c.ParseFormat(bad) == nil {
			t.Errorf("format %q accepted", bad)
		}
	}
	for _, b := range []int{0, 49, 4_000_001, -1} {
		c := DefaultConfig()
		c.Baud = b
		if c.Validate() == nil {
			t.Errorf("baud %d accepted", b)
		}
	}
}

func TestNatLess(t *testing.T) {
	if !natLess("ttyUSB2", "ttyUSB10") || natLess("ttyUSB10", "ttyUSB2") || !natLess("ttyACM0", "ttyUSB0") {
		t.Fatal("ordering")
	}
}
