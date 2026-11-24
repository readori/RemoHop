package usb

import (
	"context"
	"errors"
	"fmt"
	"net"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/wire"
)

// HostError is a refusal that came from the host, with the host's reason.
type HostError struct{ Msg string }

func (e *HostError) Error() string { return "host: " + e.Msg }

// List asks the host for its USB devices. note, when non-empty, says why the
// host cannot export yet (a missing module). Strings are bounded but must be
// sanitised before printing.
func List(ctx context.Context, c *peer.Conn) (devs []DeviceInfo, note string, err error) {
	s, err := c.Open(ctx, Service, request{Op: "list"})
	if err != nil {
		return nil, "", err
	}
	defer s.Close()
	stop := context.AfterFunc(ctx, s.Reset)
	defer stop()
	var rep listReply
	if err := wire.ReadJSON(s, &rep); err != nil {
		return nil, "", err
	}
	if !rep.OK {
		return nil, "", &HostError{clip(rep.Error)}
	}
	if len(rep.Devices) > maxListed {
		rep.Devices = rep.Devices[:maxListed]
	}
	for i := range rep.Devices {
		rep.Devices[i].clean()
	}
	return rep.Devices, clip(rep.Note), nil
}

// DevList asks the host, with the standard OP_REQ_DEVLIST, for the devices
// its owner allowed to export.
func DevList(ctx context.Context, c *peer.Conn) ([]Entry, error) {
	s, err := c.Open(ctx, Service, request{Op: "usbip"})
	if err != nil {
		return nil, err
	}
	defer s.Close()
	stop := context.AfterFunc(ctx, s.Reset)
	defer stop()
	if err := WriteReqDevlist(s); err != nil {
		return nil, err
	}
	code, st, err := ReadOpHeader(s)
	if err != nil {
		return nil, err
	}
	if code != OpRepDevlist || st != StOK {
		return nil, fmt.Errorf("usbip: bad devlist reply %#04x/%d", code, st)
	}
	return ReadRepDevlist(s)
}

// ClientCheck says why this machine cannot import devices, or nil.
func clientCheck(h hooks) error {
	switch {
	case !h.supported:
		return fmt.Errorf("attaching USB devices is supported on Linux only (this machine runs %s)", runtime.GOOS)
	case !h.isRoot():
		return errors.New("attaching a USB device needs root on this machine: run with sudo")
	}
	if len(h.sys.vhciControllers()) == 0 {
		return errors.New("kernel module vhci-hcd is not loaded on this machine (it also needs usbip-core): run `sudo modprobe vhci-hcd`")
	}
	return nil
}

// Attached is a remote device imported through vhci_hcd.
type Attached struct {
	BusID  string
	Device Device
	// Port is the kernel's vhci port number (what `remohop usb detach N`
	// takes).
	Port int

	sys  system
	s    peer.Stream
	conn net.Conn
	done chan struct{}
	once sync.Once
}

// Attach imports the host's device busid: it asks the host to export it,
// hands the kernel a loopback socket and relays it over the session.
func Attach(ctx context.Context, c *peer.Conn, busid string) (*Attached, error) {
	if !ValidBusID(busid) {
		return nil, fmt.Errorf("bad bus id %q (like 1-2 or 3-1.4)", busid)
	}
	h := getHooks()
	if err := clientCheck(h); err != nil {
		return nil, err
	}
	s, err := c.Open(ctx, Service, request{Op: "usbip"})
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*Attached, error) { s.Reset(); return nil, err }
	stop := context.AfterFunc(ctx, s.Reset)
	defer stop()
	if err := WriteReqImport(s, busid); err != nil {
		return fail(err)
	}
	code, st, err := ReadOpHeader(s)
	if err != nil {
		return fail(err)
	}
	if code != OpRepImport {
		return fail(fmt.Errorf("usbip: unexpected reply %#04x", code))
	}
	if st != StOK {
		var er errReply
		s.SetReadDeadline(time.Now().Add(3 * time.Second))
		msg := StatusText(st)
		if wire.ReadJSONLimit(s, &er, 4096) == nil && er.Error != "" {
			msg = clip(er.Error)
		}
		s.Close()
		return nil, &HostError{msg}
	}
	dev, err := ReadRepImport(s, st)
	if err != nil {
		return fail(err)
	}
	if dev.BusID != busid || dev.Speed > SpeedSuperPlus {
		return fail(errors.New("usbip: host answered with a different device"))
	}
	kernelEnd, relayEnd, err := loopbackPair()
	if err != nil {
		return fail(err)
	}
	kfile, err := kernelEnd.File()
	kernelEnd.Close()
	if err != nil {
		relayEnd.Close()
		return fail(err)
	}
	port, err := h.sys.vhciAttach(*dev, int(kfile.Fd()))
	kfile.Close()
	if err != nil {
		relayEnd.Close()
		return fail(err)
	}
	a := &Attached{BusID: busid, Device: *dev, Port: port, sys: h.sys, s: s, conn: relayEnd, done: make(chan struct{})}
	go func() { relay(relayEnd, s); a.Close() }()
	return a, nil
}

// vhciAttach finds a free port for dev and attaches the socket fd to it. It
// returns the kernel's port number.
func (s system) vhciAttach(dev Device, fd int) (port int, err error) {
	ports, err := s.vhciPorts()
	if err != nil {
		return 0, err
	}
	cands := freePorts(ports, dev.Speed)
	if len(cands) == 0 {
		return 0, errors.New("no free vhci port for this device speed (detach something with `remohop usb detach`)")
	}
	var last error
	for _, p := range cands {
		val := fmt.Sprintf("%d %d %d %d", p.Port, fd, dev.DevID(), dev.Speed)
		if last = s.put(s.vhciFile("attach"), val); last == nil {
			return p.Port, nil
		}
	}
	return 0, fmt.Errorf("vhci attach: %w", last)
}

// Done is closed when the device is detached or the session ends.
func (a *Attached) Done() <-chan struct{} { return a.done }

// Close detaches the device and ends the relay.
func (a *Attached) Close() error {
	a.once.Do(func() {
		a.sys.put(a.sys.vhciFile("detach"), strconv.Itoa(a.Port))
		a.conn.Close()
		a.s.Close()
		close(a.done)
	})
	return nil
}

// AttachedPort is a device imported on this machine.
type AttachedPort struct {
	Port       int    `json:"port"`
	LocalBusID string `json:"local_busid"`
	DevID      uint32 `json:"devid"`
	Speed      int    `json:"speed"`
}

// ListAttached lists the vhci ports in use on this machine.
func ListAttached() ([]AttachedPort, error) {
	h := getHooks()
	if !h.supported {
		return nil, errors.New("USB attach is supported on Linux only")
	}
	ports, err := h.sys.vhciPorts()
	if errors.Is(err, ErrNoVHCI) {
		return nil, nil // nothing can be attached without the module
	}
	if err != nil {
		return nil, err
	}
	var out []AttachedPort
	for _, p := range ports {
		if p.Status == vhciUsed {
			out = append(out, AttachedPort{p.Index, p.LocalBusID, p.DevID, p.Speed})
		}
	}
	return out, nil
}

// DetachPort detaches vhci port index n (as shown by `remohop usb`).
func DetachPort(n int) error {
	h := getHooks()
	if !h.supported {
		return errors.New("USB attach is supported on Linux only")
	}
	if !h.isRoot() {
		return errors.New("detaching a USB device needs root: run with sudo")
	}
	if n < 0 || n > maxVhciPort {
		return fmt.Errorf("bad port %d", n)
	}
	return h.sys.put(h.sys.vhciFile("detach"), strconv.Itoa(n))
}
