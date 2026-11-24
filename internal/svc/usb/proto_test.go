package usb

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// Golden bytes built independently (Python struct.pack of the layout in
// Documentation/usb/usbip_protocol.rst), not by this package's encoder.

func goldenDevice() (Device, string) {
	d := Device{Path: "/sys/devices/pci0000:00/0000:00:14.0/usb1/1-1", BusID: "1-1", BusNum: 1, DevNum: 2, Speed: 3,
		VendorID: 0x0bda, ProductID: 0x8153, BCDDevice: 0x3000, Class: 0, SubClass: 0, Protocol: 0, ConfigValue: 1, NumConfigs: 1, NumInterfaces: 1}
	path := hex.EncodeToString([]byte(d.Path)) + strings.Repeat("00", 256-len(d.Path))
	busid := hex.EncodeToString([]byte("1-1")) + strings.Repeat("00", 32-3)
	rest := "00000001" + "00000002" + "00000003" + "0bda" + "8153" + "3000" + "00" + "00" + "00" + "01" + "01" + "01"
	return d, path + busid + rest
}

func TestGoldenOpHeaders(t *testing.T) {
	var b bytes.Buffer
	WriteReqDevlist(&b)
	if got := hex.EncodeToString(b.Bytes()); got != "0111800500000000" {
		t.Fatalf("REQ_DEVLIST %s", got)
	}
	b.Reset()
	if err := WriteReqImport(&b, "1-1.2"); err != nil {
		t.Fatal(err)
	}
	want := "0111" + "8003" + "00000000" + hex.EncodeToString([]byte("1-1.2")) + strings.Repeat("00", 32-5)
	if got := hex.EncodeToString(b.Bytes()); got != want {
		t.Fatalf("REQ_IMPORT\n got %s\nwant %s", got, want)
	}
	code, st, err := ReadOpHeader(bytes.NewReader(b.Bytes()[:8]))
	if err != nil || code != OpReqImport || st != 0 {
		t.Fatal(code, st, err)
	}
	busid, err := ReadReqImport(bytes.NewReader(b.Bytes()[8:]))
	if err != nil || busid != "1-1.2" {
		t.Fatal(busid, err)
	}
}

func TestGoldenDeviceAndImportReply(t *testing.T) {
	d, gold := goldenDevice()
	if got := hex.EncodeToString(d.Marshal()); got != gold {
		t.Fatalf("device struct\n got %s\nwant %s", got, gold)
	}
	if len(d.Marshal()) != 312 {
		t.Fatal("size")
	}
	back, err := ParseDevice(d.Marshal())
	if err != nil || back != d {
		t.Fatalf("round trip %+v %v", back, err)
	}
	var b bytes.Buffer
	WriteRepImport(&b, StOK, &d)
	if got := hex.EncodeToString(b.Bytes()); got != "01110003"+"00000000"+gold {
		t.Fatalf("REP_IMPORT %s", got)
	}
	if d.DevID() != 1<<16|2 {
		t.Fatal("devid")
	}
	b.Reset()
	WriteRepImport(&b, StNA, nil)
	if hex.EncodeToString(b.Bytes()) != "0111000300000001" {
		t.Fatalf("refusal %x", b.Bytes())
	}
}

func TestGoldenDevlist(t *testing.T) {
	d, gold := goldenDevice()
	var b bytes.Buffer
	WriteRepDevlist(&b, []Entry{{Device: d, Interfaces: []Interface{{0xff, 0x01, 0x02}}}})
	want := "01110005" + "00000000" + "00000001" + gold + "ff010200"
	if got := hex.EncodeToString(b.Bytes()); got != want {
		t.Fatalf("REP_DEVLIST\n got %s\nwant %s", got, want)
	}
	r := bytes.NewReader(b.Bytes())
	code, st, _ := ReadOpHeader(r)
	if code != OpRepDevlist || st != 0 {
		t.Fatal(code, st)
	}
	es, err := ReadRepDevlist(r)
	if err != nil || len(es) != 1 || es[0].Device != d || !reflect.DeepEqual(es[0].Interfaces, []Interface{{0xff, 1, 2}}) {
		t.Fatalf("%+v %v", es, err)
	}
}

func TestProtocolRejectsHostile(t *testing.T) {
	// A huge announced count is refused before any allocation.
	var b bytes.Buffer
	binary.Write(&b, binary.BigEndian, uint32(1<<30))
	if _, err := ReadRepDevlist(&b); err == nil {
		t.Fatal("huge devlist accepted")
	}
	// Wrong version.
	if _, _, err := ReadOpHeader(bytes.NewReader([]byte{0x01, 0x10, 0x80, 0x03, 0, 0, 0, 0})); err == nil {
		t.Fatal("bad version accepted")
	}
	// Truncated device struct.
	if _, err := ReadRepDevlist(bytes.NewReader([]byte{0, 0, 0, 1, 1, 2, 3})); err == nil {
		t.Fatal("truncated accepted")
	}
	// Malformed busids: traversal, empty, too long, no NUL padding, junk.
	for _, id := range []string{"../../x", "", "1-2/../..", "1-2\n", "a-b", "1-2.", strings.Repeat("1", 40), "usb1", "1-2;rm"} {
		if ValidBusID(id) {
			t.Errorf("busid %q accepted", id)
		}
		raw := make([]byte, 32)
		copy(raw, id)
		if _, err := ReadReqImport(bytes.NewReader(raw)); err == nil {
			t.Errorf("import busid %q accepted", id)
		}
		if WriteReqImport(&bytes.Buffer{}, id) == nil {
			t.Errorf("wrote import for %q", id)
		}
	}
	for _, id := range []string{"1-1", "3-1.4.2", "12-13.14"} {
		if !ValidBusID(id) {
			t.Errorf("busid %q rejected", id)
		}
	}
	// A busid filling all 32 bytes (no terminator) must not validate.
	if _, err := ReadReqImport(bytes.NewReader(bytes.Repeat([]byte("1"), 32))); err == nil {
		t.Fatal("unterminated busid accepted")
	}
}

func TestParseVhciStatus(t *testing.T) {
	// The second controller of a kernel with 8 ports per hub: the kernel
	// prints global port numbers (16 + n), which attach/detach take.
	text := `hub port sta spd dev      sockfd local_busid
hs  0016 004 000 00000000 000000 0-0
hs  0017 006 003 00010002 000005 3-1
ss  0024 004 000 00000000 000000 0-0
ss  0025 006 005 00020003 000007 4-1
garbage line
`
	ps := parseVhciStatus(1, text)
	if len(ps) != 4 {
		t.Fatalf("%d ports", len(ps))
	}
	if p := ps[1]; p.Status != vhciUsed || p.DevID != 0x00010002 || p.LocalBusID != "3-1" || p.Index != 17 || p.Port != 17 || p.Hub != "hs" || p.Controller != 1 {
		t.Fatalf("%+v", p)
	}
	if f := freePorts(ps, SpeedHigh); len(f) != 1 || f[0].Port != 16 {
		t.Fatalf("hs free %+v", f)
	}
	if f := freePorts(ps, SpeedSuper); len(f) != 1 || f[0].Port != 24 {
		t.Fatalf("ss free %+v", f)
	}
	old := parseVhciStatus(0, "prt sta spd dev sockfd local_busid\n000 004 000 00000000 000000 0-0\n")
	if len(old) != 1 || old[0].Hub != "hs" {
		t.Fatalf("legacy %+v", old)
	}
}

// Two vhci controllers as the kernel lays them out: both status files and
// the attach/detach attributes live on vhci_hcd.0, with global port numbers.
func TestVhciTwoControllers(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("sysfs paths: USB/IP is Linux only")
	}
	root := t.TempDir()
	plat := filepath.Join(root, "sys", "devices", "platform")
	os.MkdirAll(filepath.Join(plat, "vhci_hcd.0"), 0o755)
	os.MkdirAll(filepath.Join(plat, "vhci_hcd.1"), 0o755)
	status := func(base int, used bool) string {
		var b strings.Builder
		b.WriteString("hub port sta spd dev      sockfd local_busid\n")
		for i := 0; i < 2; i++ {
			if used {
				fmt.Fprintf(&b, "hs  %04d 006 003 00010002 000005 3-%d\n", base+i, i+1)
			} else {
				fmt.Fprintf(&b, "hs  %04d 004 000 00000000 000000 0-0\n", base+i)
			}
		}
		for i := 0; i < 2; i++ {
			fmt.Fprintf(&b, "ss  %04d 004 000 00000000 000000 0-0\n", base+2+i)
		}
		return b.String()
	}
	os.WriteFile(filepath.Join(plat, "vhci_hcd.0", "status"), []byte(status(0, true)), 0o644)
	os.WriteFile(filepath.Join(plat, "vhci_hcd.0", "status.1"), []byte(status(4, false)), 0o644)
	var writes []string
	sys := system{root: root, write: func(p, v string) error {
		writes = append(writes, strings.TrimPrefix(p, root)+"="+v)
		return nil
	}}
	ports, err := sys.vhciPorts()
	if err != nil || len(ports) != 8 {
		t.Fatalf("ports %v %+v", err, ports)
	}
	port, err := sys.vhciAttach(Device{BusNum: 1, DevNum: 2, Speed: SpeedHigh}, 9)
	if err != nil || port != 4 {
		t.Fatalf("attach: port %d, %v", port, err)
	}
	want := "/sys/devices/platform/vhci_hcd.0/attach=4 9 65538 3"
	if len(writes) != 1 || writes[0] != want {
		t.Fatalf("writes %q, want %q", writes, want)
	}
}
