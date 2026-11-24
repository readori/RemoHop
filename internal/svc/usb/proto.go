package usb

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"regexp"
)

// USB/IP protocol constants (Documentation/usb/usbip_protocol.rst).
const (
	Version = 0x0111

	OpReqDevlist uint16 = 0x8005
	OpRepDevlist uint16 = 0x0005
	OpReqImport  uint16 = 0x8003
	OpRepImport  uint16 = 0x0003
)

// Status codes of an OP_REP header.
const (
	StOK      uint32 = 0
	StNA      uint32 = 1 // request not possible
	StDevBusy uint32 = 2
	StDevErr  uint32 = 3
	StNoDev   uint32 = 4
	StError   uint32 = 5
)

const (
	deviceLen     = 312 // struct usbip_usb_device
	busidLen      = 32
	pathLen       = 256
	maxDevlist    = 128
	maxInterfaces = 255
)

// USB device speeds as the kernel numbers them (enum usb_device_speed).
const (
	SpeedUnknown   = 0
	SpeedLow       = 1
	SpeedFull      = 2
	SpeedHigh      = 3
	SpeedWireless  = 4
	SpeedSuper     = 5
	SpeedSuperPlus = 6
)

var busidRE = regexp.MustCompile(`^[0-9]{1,4}-[0-9]{1,3}(\.[0-9]{1,3}){0,6}$`)

// ValidBusID reports whether s is a USB bus id like "1-2" or "3-1.4.2". The
// strict form matters: busids are used to build sysfs paths.
func ValidBusID(s string) bool { return busidRE.MatchString(s) }

// Device is struct usbip_usb_device.
type Device struct {
	Path          string
	BusID         string
	BusNum        uint32
	DevNum        uint32
	Speed         uint32
	VendorID      uint16
	ProductID     uint16
	BCDDevice     uint16
	Class         uint8
	SubClass      uint8
	Protocol      uint8
	ConfigValue   uint8
	NumConfigs    uint8
	NumInterfaces uint8
}

// DevID is the id the vhci driver wants: bus number and device number.
func (d Device) DevID() uint32 { return d.BusNum<<16 | d.DevNum }

// Interface is struct usbip_usb_interface (the 4th byte is padding).
type Interface struct{ Class, SubClass, Protocol uint8 }

// Entry is a device of an OP_REP_DEVLIST with its interfaces.
type Entry struct {
	Device     Device
	Interfaces []Interface
}

func putStr(dst []byte, s string) {
	copy(dst, s) // the rest stays NUL
	if len(s) >= len(dst) {
		dst[len(dst)-1] = 0
	}
}

func getStr(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

// Marshal encodes the device as the 312-byte wire struct.
func (d Device) Marshal() []byte {
	b := make([]byte, deviceLen)
	putStr(b[0:pathLen], d.Path)
	putStr(b[pathLen:pathLen+busidLen], d.BusID)
	o := pathLen + busidLen
	binary.BigEndian.PutUint32(b[o:], d.BusNum)
	binary.BigEndian.PutUint32(b[o+4:], d.DevNum)
	binary.BigEndian.PutUint32(b[o+8:], d.Speed)
	o += 12
	binary.BigEndian.PutUint16(b[o:], d.VendorID)
	binary.BigEndian.PutUint16(b[o+2:], d.ProductID)
	binary.BigEndian.PutUint16(b[o+4:], d.BCDDevice)
	b[o+6], b[o+7], b[o+8] = d.Class, d.SubClass, d.Protocol
	b[o+9], b[o+10], b[o+11] = d.ConfigValue, d.NumConfigs, d.NumInterfaces
	return b
}

// ParseDevice decodes the wire struct.
func ParseDevice(b []byte) (Device, error) {
	if len(b) != deviceLen {
		return Device{}, fmt.Errorf("usbip: device struct is %d bytes, want %d", len(b), deviceLen)
	}
	var d Device
	d.Path = getStr(b[0:pathLen])
	d.BusID = getStr(b[pathLen : pathLen+busidLen])
	o := pathLen + busidLen
	d.BusNum = binary.BigEndian.Uint32(b[o:])
	d.DevNum = binary.BigEndian.Uint32(b[o+4:])
	d.Speed = binary.BigEndian.Uint32(b[o+8:])
	o += 12
	d.VendorID = binary.BigEndian.Uint16(b[o:])
	d.ProductID = binary.BigEndian.Uint16(b[o+2:])
	d.BCDDevice = binary.BigEndian.Uint16(b[o+4:])
	d.Class, d.SubClass, d.Protocol = b[o+6], b[o+7], b[o+8]
	d.ConfigValue, d.NumConfigs, d.NumInterfaces = b[o+9], b[o+10], b[o+11]
	return d, nil
}

// opHeader is the 8-byte header of every OP_ message.
func opHeader(code uint16, status uint32) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint16(b, Version)
	binary.BigEndian.PutUint16(b[2:], code)
	binary.BigEndian.PutUint32(b[4:], status)
	return b
}

// ReadOpHeader reads and checks an OP_ header.
func ReadOpHeader(r io.Reader) (code uint16, status uint32, err error) {
	var b [8]byte
	if _, err = io.ReadFull(r, b[:]); err != nil {
		return
	}
	if v := binary.BigEndian.Uint16(b[:]); v != Version {
		return 0, 0, fmt.Errorf("usbip: unsupported protocol version %#04x", v)
	}
	return binary.BigEndian.Uint16(b[2:]), binary.BigEndian.Uint32(b[4:]), nil
}

// WriteReqDevlist sends OP_REQ_DEVLIST.
func WriteReqDevlist(w io.Writer) error { _, err := w.Write(opHeader(OpReqDevlist, 0)); return err }

// WriteRepDevlist sends OP_REP_DEVLIST.
func WriteRepDevlist(w io.Writer, entries []Entry) error {
	var b bytes.Buffer
	b.Write(opHeader(OpRepDevlist, StOK))
	binary.Write(&b, binary.BigEndian, uint32(len(entries)))
	for _, e := range entries {
		d := e.Device
		d.NumInterfaces = uint8(len(e.Interfaces))
		b.Write(d.Marshal())
		for _, i := range e.Interfaces {
			b.Write([]byte{i.Class, i.SubClass, i.Protocol, 0})
		}
	}
	_, err := w.Write(b.Bytes())
	return err
}

// ReadRepDevlist reads the body of OP_REP_DEVLIST (after its header),
// bounding the device count.
func ReadRepDevlist(r io.Reader) ([]Entry, error) {
	var n uint32
	if err := binary.Read(r, binary.BigEndian, &n); err != nil {
		return nil, err
	}
	if n > maxDevlist {
		return nil, fmt.Errorf("usbip: device list of %d entries refused", n)
	}
	out := make([]Entry, 0, n)
	buf := make([]byte, deviceLen)
	for i := uint32(0); i < n; i++ {
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		d, err := ParseDevice(buf)
		if err != nil {
			return nil, err
		}
		e := Entry{Device: d}
		ib := make([]byte, 4*int(d.NumInterfaces))
		if _, err := io.ReadFull(r, ib); err != nil {
			return nil, err
		}
		for j := 0; j < int(d.NumInterfaces); j++ {
			e.Interfaces = append(e.Interfaces, Interface{ib[4*j], ib[4*j+1], ib[4*j+2]})
		}
		out = append(out, e)
	}
	return out, nil
}

// WriteReqImport sends OP_REQ_IMPORT for busid.
func WriteReqImport(w io.Writer, busid string) error {
	if !ValidBusID(busid) {
		return fmt.Errorf("usbip: bad bus id %q", busid)
	}
	b := make([]byte, 8+busidLen)
	copy(b, opHeader(OpReqImport, 0))
	putStr(b[8:], busid)
	_, err := w.Write(b)
	return err
}

// ReadReqImport reads the body of OP_REQ_IMPORT (after its header) and
// returns the busid, which is validated.
func ReadReqImport(r io.Reader) (string, error) {
	var b [busidLen]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return "", err
	}
	s := getStr(b[:])
	if !ValidBusID(s) {
		return "", errors.New("malformed bus id")
	}
	return s, nil
}

// WriteRepImport sends OP_REP_IMPORT; dev is sent only with StOK.
func WriteRepImport(w io.Writer, status uint32, dev *Device) error {
	b := opHeader(OpRepImport, status)
	if status == StOK && dev != nil {
		b = append(b, dev.Marshal()...)
	}
	_, err := w.Write(b)
	return err
}

// ReadRepImport reads the body of OP_REP_IMPORT for a header with status st.
func ReadRepImport(r io.Reader, st uint32) (*Device, error) {
	if st != StOK {
		return nil, nil
	}
	buf := make([]byte, deviceLen)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	d, err := ParseDevice(buf)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// StatusText describes a non-zero OP_REP status.
func StatusText(st uint32) string {
	switch st {
	case StNA:
		return "request not possible"
	case StDevBusy:
		return "device is busy (already exported)"
	case StDevErr:
		return "device is in an error state"
	case StNoDev:
		return "no such device"
	}
	return fmt.Sprintf("host refused (status %d)", st)
}
