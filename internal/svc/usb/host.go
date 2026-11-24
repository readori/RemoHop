package usb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/perm"
	"github.com/readori/RemoHop/internal/wire"
)

// Service is the wire service name.
const Service = "usb"

func init() {
	hk = hooks{sys: realSystem, isRoot: func() bool { return os.Geteuid() == 0 }, supported: runtime.GOOS == "linux"}
	// A feature of its own: exporting a device takes it away from the host
	// (it can be a keyboard or a disk), so the owner decides separately.
	perm.Register(Service, perm.USB)
	peer.Handle(Service, hostServe)
}

// hooks are the host and client side's view of the machine; tests replace
// them to run against a fake sysfs.
type hooks struct {
	sys       system
	isRoot    func() bool
	supported bool
}

var (
	hookMu sync.RWMutex
	hk     hooks
)

func getHooks() hooks {
	hookMu.RLock()
	defer hookMu.RUnlock()
	return hk
}

type request struct {
	Op string `json:"op"`
}

type listReply struct {
	OK      bool         `json:"ok"`
	Error   string       `json:"error,omitempty"`
	Note    string       `json:"note,omitempty"`
	Devices []DeviceInfo `json:"devices,omitempty"`
}

type errReply struct {
	Error string `json:"error"`
}

const maxListed = 256

// hostCheck says why this machine cannot export devices, or nil.
func hostCheck(h hooks) error {
	switch {
	case !h.supported:
		return fmt.Errorf("USB export is supported on Linux hosts only (this host runs %s)", runtime.GOOS)
	case !h.isRoot():
		return errors.New("exporting USB devices needs root: restart `remohop host` as root")
	}
	if _, err := os.Stat(h.sys.p("bus/usb")); err != nil {
		return errors.New("this kernel has no USB support (/sys/bus/usb is missing)")
	}
	if _, err := os.Stat(h.sys.p("bus/usb/drivers/usbip-host")); err != nil {
		return errors.New("kernel module usbip-host is not loaded on the host (it also needs usbip-core): on the host run `sudo modprobe usbip-host`")
	}
	return nil
}

func hostServe(ctx context.Context, c *peer.Conn, s peer.Stream, raw json.RawMessage) error {
	var req request
	if err := peer.DecodeReq(raw, &req); err != nil {
		wire.WriteJSON(s, listReply{Error: "bad request"})
		return err
	}
	h := getHooks()
	switch req.Op {
	case "list":
		return hostList(s, h)
	case "usbip":
		return hostUSBIP(ctx, s, h)
	}
	wire.WriteJSON(s, listReply{Error: "unknown op"})
	return errors.New("usb: unknown op")
}

func hostList(s peer.Stream, h hooks) error {
	if !h.supported {
		err := fmt.Errorf("USB is supported on Linux hosts only (this host runs %s)", runtime.GOOS)
		wire.WriteJSON(s, listReply{Error: err.Error()})
		return err
	}
	ids, err := h.sys.busids()
	if errors.Is(err, fs.ErrNotExist) {
		// No USB bus at all (a VM or container without USB passthrough):
		// that is an empty list, not a failure.
		return wire.WriteJSON(s, listReply{OK: true, Note: "this host has no USB bus (/sys/bus/usb is absent)"})
	}
	if err != nil {
		wire.WriteJSON(s, listReply{Error: "cannot list USB devices: " + err.Error()})
		return err
	}
	rep := listReply{OK: true}
	if cerr := hostCheck(h); cerr != nil {
		rep.Note = "cannot export devices yet: " + cerr.Error()
	}
	for _, id := range ids {
		di, ok := h.sys.info(id, false)
		if !ok {
			continue
		}
		di.Allowed = IsAllowed(id, di.VID, di.PID)
		rep.Devices = append(rep.Devices, di)
		if len(rep.Devices) >= maxListed {
			break
		}
	}
	return wire.WriteJSON(s, rep)
}

// hostUSBIP serves the standard USB/IP protocol on the stream.
func hostUSBIP(ctx context.Context, s peer.Stream, h hooks) error {
	s.SetReadDeadline(time.Now().Add(30 * time.Second))
	code, _, err := ReadOpHeader(s)
	if err != nil {
		return err
	}
	switch code {
	case OpReqDevlist:
		s.SetReadDeadline(time.Time{})
		var entries []Entry
		if h.supported {
			ids, _ := h.sys.busids()
			for _, id := range ids {
				d, ifs, err := h.sys.device(id)
				if err != nil || carriesRisk(d, ifs) == "hub" {
					continue
				}
				if !IsAllowed(id, fmt.Sprintf("%04x", d.VendorID), fmt.Sprintf("%04x", d.ProductID)) {
					continue
				}
				entries = append(entries, Entry{Device: d, Interfaces: ifs})
				if len(entries) >= maxDevlist {
					break
				}
			}
		}
		return WriteRepDevlist(s, entries)
	case OpReqImport:
		busid, err := ReadReqImport(s)
		s.SetReadDeadline(time.Time{})
		if err != nil {
			refuse(s, StNA, "malformed import request")
			return err
		}
		return hostImport(ctx, s, h, busid)
	}
	refuse(s, StNA, "unsupported request")
	return fmt.Errorf("usb: unsupported op %#04x", code)
}

// refuse sends a non-OK OP_REP_IMPORT with the explaining JSON extension.
func refuse(s peer.Stream, status uint32, why string) {
	WriteRepImport(s, status, nil)
	wire.WriteJSON(s, errReply{Error: why})
}

var (
	exportMu sync.Mutex
	exported = map[string]bool{}
)

func claim(busid string) bool {
	exportMu.Lock()
	defer exportMu.Unlock()
	if exported[busid] {
		return false
	}
	exported[busid] = true
	return true
}

func unclaim(busid string) { exportMu.Lock(); delete(exported, busid); exportMu.Unlock() }

func hostImport(ctx context.Context, s peer.Stream, h hooks, busid string) error {
	if err := hostCheck(h); err != nil {
		refuse(s, StNA, err.Error())
		return err
	}
	d, ifs, err := h.sys.device(busid)
	if err != nil {
		refuse(s, StNoDev, fmt.Sprintf("no USB device %s on the host", busid))
		return err
	}
	if what := carriesRisk(d, ifs); what == "hub" {
		refuse(s, StNA, "hubs cannot be exported")
		return errors.New("usb: hub export refused")
	}
	vid, pid := fmt.Sprintf("%04x", d.VendorID), fmt.Sprintf("%04x", d.ProductID)
	if !IsAllowed(busid, vid, pid) {
		msg := fmt.Sprintf("device %s (%s:%s) is not allowed for export: on the host run `remohop usb allow %s`", busid, vid, pid, busid)
		refuse(s, StNA, msg)
		return errors.New("usb: device not allowed")
	}
	if !claim(busid) {
		refuse(s, StDevBusy, "device is already exported to another session")
		return errors.New("usb: busy")
	}
	defer unclaim(busid)

	kernelEnd, relayEnd, err := loopbackPair()
	if err != nil {
		refuse(s, StError, "cannot create local socket: "+err.Error())
		return err
	}
	defer relayEnd.Close()
	kfile, err := kernelEnd.File()
	kernelEnd.Close()
	if err != nil {
		refuse(s, StError, "cannot pass socket to the kernel: "+err.Error())
		return err
	}
	restore, err := h.sys.export(busid, kfile)
	// The kernel holds its own reference to the socket now.
	kfile.Close()
	if err != nil {
		refuse(s, StError, err.Error())
		return err
	}
	// Whatever happens next, give the device back.
	defer func() {
		relayEnd.Close()
		if rerr := restore(); rerr != nil {
			slog.Warn("usb: restoring device driver", "busid", busid, "err", rerr)
		}
	}()
	if err := WriteRepImport(s, StOK, &d); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { relayEnd.Close(); s.Reset() })
	defer stop()
	relay(relayEnd, s)
	return nil
}

// relay copies between the kernel's loopback socket and the stream until
// either direction ends, then closes both.
func relay(c net.Conn, s peer.Stream) {
	var wg sync.WaitGroup
	wg.Add(2)
	end := func() { c.Close(); s.Close() }
	go func() { defer wg.Done(); defer end(); io.Copy(c, s) }()
	go func() { defer wg.Done(); defer end(); io.Copy(s, c) }()
	wg.Wait()
}

// loopbackPair makes a connected TCP pair on 127.0.0.1: one end for the
// kernel driver, one for the relay. A real TCP connection is what the usbip
// tools hand the kernel, so it is what we hand it.
func loopbackPair() (kernel, relayEnd *net.TCPConn, err error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, err
	}
	defer ln.Close()
	dialled, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		return nil, nil, err
	}
	// Accept until the connection is ours: another local process could have
	// connected to the ephemeral port first.
	ln.(*net.TCPListener).SetDeadline(time.Now().Add(5 * time.Second))
	for {
		acc, err := ln.Accept()
		if err != nil {
			dialled.Close()
			return nil, nil, err
		}
		if acc.RemoteAddr().String() == dialled.LocalAddr().String() {
			return dialled.(*net.TCPConn), acc.(*net.TCPConn), nil
		}
		acc.Close()
	}
}

// ---- export / restore (sysfs ABI of usbip-host) ----

const usbipHost = "usbip-host"

// usbip_status values of a device on usbip-host (enum usbip_device_status
// in drivers/usb/usbip/usbip_common.h): available for export, in use by a
// connected client, or broken.
const (
	sdevAvailable = "1"
	sdevUsed      = "2"
	sdevError     = "3"
)

// export binds busid to usbip-host and gives the kernel the socket. It
// returns the function that undoes it: unbind from usbip-host, forget the
// busid, and let the original driver have it back (giveBack).
func (s system) export(busid string, sock *os.File) (restore func() error, err error) {
	drv := s.p("bus/usb/drivers", usbipHost)
	orig := s.driverOf(busid)
	restore = func() error {
		// Give the stub time to see the closed socket and settle.
		for i := 0; i < 60 && readAttr(filepath.Join(s.devDir(busid), "usbip_status")) == sdevUsed; i++ {
			time.Sleep(50 * time.Millisecond)
		}
		return s.giveBack(busid)
	}
	if orig != usbipHost {
		if err := s.put(filepath.Join(drv, "match_busid"), "add "+busid); err != nil {
			return nil, fmt.Errorf("usbip-host match_busid: %w", err)
		}
		if orig != "" {
			if err := s.put(s.p("bus/usb/drivers", orig, "unbind"), busid); err != nil {
				restore()
				return nil, fmt.Errorf("unbind %s from %s: %w", busid, orig, err)
			}
		}
		if err := s.put(filepath.Join(drv, "bind"), busid); err != nil {
			restore()
			return nil, fmt.Errorf("bind %s to usbip-host: %w", busid, err)
		}
	}
	if got := s.driverOf(busid); got != usbipHost {
		restore()
		return nil, fmt.Errorf("device %s did not bind to usbip-host (driver is %q)", busid, got)
	}
	var st string
	for i := 0; i < 40; i++ {
		if st = readAttr(filepath.Join(s.devDir(busid), "usbip_status")); st == sdevAvailable {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if st != sdevAvailable {
		restore()
		if st == sdevError {
			return nil, fmt.Errorf("device %s is in an error state on usbip-host (its last reset failed); unplug and replug it", busid)
		}
		return nil, fmt.Errorf("device %s is not available for export (usbip_status %q)", busid, st)
	}
	if err := s.put(filepath.Join(s.devDir(busid), "usbip_sockfd"), fmt.Sprintf("%d\n", sock.Fd())); err != nil {
		restore()
		return nil, fmt.Errorf("give socket to usbip-host: %w", err)
	}
	return restore, nil
}

// Release returns a device stuck on usbip-host (after a crash) to its normal
// driver. It is the `remohop usb release` command.
func Release(busid string) error {
	h := getHooks()
	if !ValidBusID(busid) {
		return fmt.Errorf("bad bus id %q", busid)
	}
	if !h.supported {
		return errors.New("USB is supported on Linux only")
	}
	if !h.isRoot() {
		return errors.New("releasing a USB device needs root")
	}
	if h.sys.driverOf(busid) != usbipHost {
		return fmt.Errorf("%s is not bound to usbip-host", busid)
	}
	return h.sys.giveBack(busid)
}

// giveBack returns busid from usbip-host to its normal driver, the way
// `usbip unbind` does: unbind it from usbip-host, drop it from match_busid,
// and have usbip-host rebind it. "rebind" first marks the busid as not
// usbip-host's, so the re-probe cannot hand it straight back to usbip-host,
// and then removes it from the table. (A bare "del" after unbind leaves the
// busid in the table, still claimed: the kernel only marks bound entries.)
// Kernels without "rebind" get a plain re-probe instead.
func (s system) giveBack(busid string) error {
	drv := s.p("bus/usb/drivers", usbipHost)
	var errs []error
	if s.driverOf(busid) == usbipHost {
		errs = append(errs, s.put(filepath.Join(drv, "unbind"), busid))
	}
	errs = append(errs, s.put(filepath.Join(drv, "match_busid"), "del "+busid))
	if err := s.put(filepath.Join(drv, "rebind"), busid); err != nil {
		errs = append(errs, s.put(s.p("bus/usb/drivers_probe"), busid))
	}
	return errors.Join(errs...)
}

// ExportedBusIDs lists the devices currently bound to usbip-host.
func ExportedBusIDs() []string {
	h := getHooks()
	ids, _ := h.sys.busids()
	var out []string
	for _, id := range ids {
		if h.sys.driverOf(id) == usbipHost {
			out = append(out, id)
		}
	}
	return out
}
