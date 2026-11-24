//go:build windows

package vpn

import (
	"crypto/sha256"
	"fmt"
	"os"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/readori/RemoHop/internal/config"
)

// The Wintun API is called through the DLL's exports directly rather than
// through golang.zx2c4.com/wintun. That module loads wintun.dll by name
// from the executable's directory or System32 only; remohop has to load the
// one verified copy it keeps in a private directory (see wintun_locate.go),
// by full path. The dozen calls used are small enough to bind here, with
// golang.org/x/sys/windows, which remohop already depends on.
type wintunAPI struct {
	createAdapter, openAdapter, closeAdapter, getAdapterLUID uintptr
	startSession, endSession, getReadWaitEvent               uintptr
	receivePacket, releaseReceivePacket                      uintptr
	allocateSendPacket, sendPacket                           uintptr
}

var wintun struct {
	once sync.Once
	api  *wintunAPI
	err  error
}

// loadWintun locates, verifies and loads wintun.dll once per process.
func loadWintun() (*wintunAPI, error) {
	wintun.once.Do(func() {
		private, err := config.SubDir("wintun")
		if err == nil {
			err = config.MakePrivate(private)
		}
		if err != nil {
			wintun.err = err
			return
		}
		path, err := locateWintun(wintunSearchDirs(), private, runtime.GOARCH)
		if err != nil {
			wintun.err = err
			return
		}
		// Resolve the DLL's own imports from its directory and System32
		// only, never from the current directory or PATH.
		mod, err := windows.LoadLibraryEx(path, 0, windows.LOAD_LIBRARY_SEARCH_DLL_LOAD_DIR|windows.LOAD_LIBRARY_SEARCH_SYSTEM32)
		if err != nil {
			wintun.err = fmt.Errorf("loading %s: %w", path, err)
			return
		}
		api := &wintunAPI{}
		for name, p := range map[string]*uintptr{
			"WintunCreateAdapter": &api.createAdapter, "WintunOpenAdapter": &api.openAdapter,
			"WintunCloseAdapter": &api.closeAdapter, "WintunGetAdapterLUID": &api.getAdapterLUID,
			"WintunStartSession": &api.startSession, "WintunEndSession": &api.endSession,
			"WintunGetReadWaitEvent": &api.getReadWaitEvent, "WintunReceivePacket": &api.receivePacket,
			"WintunReleaseReceivePacket": &api.releaseReceivePacket,
			"WintunAllocateSendPacket":   &api.allocateSendPacket, "WintunSendPacket": &api.sendPacket,
		} {
			if *p, err = windows.GetProcAddress(mod, name); err != nil {
				wintun.err = fmt.Errorf("%s is not a usable wintun.dll: %s: %w", path, name, err)
				return
			}
		}
		wintun.api = api
	})
	return wintun.api, wintun.err
}

// ptr turns an address returned by the DLL into a pointer without the
// uintptr-to-pointer conversion vet rightly distrusts in general: these
// addresses point into Wintun's ring buffer, which Go's collector never
// moves or frees.
func ptr(r uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&r)) }

// adapterGUID derives a stable GUID for a tunnel, so that running the
// same tunnel again finds (and replaces) a leftover adapter instead of
// adding to them, and Windows' network list does not grow a new entry
// every time.
func adapterGUID(tunnel string) windows.GUID {
	dir, _ := config.Dir()
	sum := sha256.Sum256([]byte("remohop wintun adapter\x00" + dir + "\x00" + tunnel))
	var g windows.GUID
	g.Data1 = uint32(sum[0])<<24 | uint32(sum[1])<<16 | uint32(sum[2])<<8 | uint32(sum[3])
	g.Data2 = uint16(sum[4])<<8 | uint16(sum[5])
	g.Data3 = (uint16(sum[6])<<8|uint16(sum[7]))&0x0fff | 0x5000 // name-based
	copy(g.Data4[:], sum[8:16])
	g.Data4[0] = g.Data4[0]&0x3f | 0x80
	return g
}

// adapterName is the interface name Windows shows: RemoHop plus a short
// tag that tells concurrent tunnels apart.
func adapterName(g windows.GUID) string {
	return fmt.Sprintf("%s-%04x", ifPrefix, g.Data1>>16)
}

// adapterInstanceID is the device instance Wintun creates for a GUID, as
// pnputil names it.
func adapterInstanceID(g windows.GUID) string { return `SWD\Wintun\` + g.String() }

// adapters maps the names of this process's adapters to their interface
// indexes, which netsh is given instead of names.
var adapters sync.Map

type wintunDev struct {
	api      *wintunAPI
	adapter  uintptr
	session  uintptr
	readWait windows.Handle
	quit     windows.Handle
	name     string
	index    uint32
	instance string

	mu     sync.RWMutex
	closed bool
}

func openWintun(tunnel string) (Device, error) {
	api, err := loadWintun()
	if err != nil {
		return nil, err
	}
	g := adapterGUID(tunnel)
	name := adapterName(g)
	wname, _ := windows.UTF16PtrFromString(name)
	wtype, _ := windows.UTF16PtrFromString(ifPrefix)

	// An adapter of this name is a leftover of a run that was killed
	// (a live tunnel to the same peer is refused earlier). Remove it.
	if h, _, _ := syscall.SyscallN(api.openAdapter, uintptr(unsafe.Pointer(wname))); h != 0 {
		syscall.SyscallN(api.closeAdapter, h)
		run("pnputil", "/remove-device", adapterInstanceID(g))
	}
	adapter, _, errno := syscall.SyscallN(api.createAdapter, uintptr(unsafe.Pointer(wname)), uintptr(unsafe.Pointer(wtype)), uintptr(unsafe.Pointer(&g)))
	if adapter == 0 {
		return nil, fmt.Errorf("creating the Wintun adapter %s: %w", name, errno)
	}
	d := &wintunDev{api: api, adapter: adapter, name: name, instance: adapterInstanceID(g)}
	fail := func(err error) (Device, error) {
		d.Close()
		return nil, err
	}
	var luid uint64
	syscall.SyscallN(api.getAdapterLUID, adapter, uintptr(unsafe.Pointer(&luid)))
	row := windows.MibIfRow2{InterfaceLuid: luid}
	if err := windows.GetIfEntry2Ex(windows.MibIfEntryNormalWithoutStatistics, &row); err != nil {
		return fail(fmt.Errorf("finding the Wintun adapter's interface: %w", err))
	}
	d.index = row.InterfaceIndex
	adapters.Store(name, d.index)
	// A 4 MiB ring: room for a few thousand packets.
	session, _, errno := syscall.SyscallN(api.startSession, adapter, 0x400000)
	if session == 0 {
		return fail(fmt.Errorf("starting the Wintun session: %w", errno))
	}
	d.session = session
	ev, _, _ := syscall.SyscallN(api.getReadWaitEvent, session)
	d.readWait = windows.Handle(ev)
	if d.quit, err = windows.CreateEvent(nil, 1, 0, nil); err != nil {
		return fail(err)
	}
	return d, nil
}

func (d *wintunDev) Name() string { return d.name }

// undoSteps: Wintun removes the adapter when it is closed, and Windows
// when the process that created it ends; the recorded step removes it in
// any case that leaves it behind.
func (d *wintunDev) undoSteps() []undoStep {
	return []undoStep{{Cmd: []string{"pnputil", "/remove-device", d.instance}}}
}

func (d *wintunDev) Read(p []byte) (int, error) {
	for {
		d.mu.RLock()
		if d.closed {
			d.mu.RUnlock()
			return 0, os.ErrClosed
		}
		var size uint32
		pkt, _, errno := syscall.SyscallN(d.api.receivePacket, d.session, uintptr(unsafe.Pointer(&size)))
		if pkt != 0 {
			n := copy(p, unsafe.Slice((*byte)(ptr(pkt)), size))
			syscall.SyscallN(d.api.releaseReceivePacket, d.session, pkt)
			d.mu.RUnlock()
			return n, nil
		}
		d.mu.RUnlock()
		if errno != windows.ERROR_NO_MORE_ITEMS {
			return 0, fmt.Errorf("reading from the Wintun adapter: %w", errno)
		}
		if _, err := windows.WaitForMultipleObjects([]windows.Handle{d.readWait, d.quit}, false, windows.INFINITE); err != nil {
			return 0, err
		}
	}
}

func (d *wintunDev) Write(p []byte) (int, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.closed {
		return 0, os.ErrClosed
	}
	buf, _, errno := syscall.SyscallN(d.api.allocateSendPacket, d.session, uintptr(len(p)))
	if buf == 0 {
		if errno == windows.ERROR_BUFFER_OVERFLOW {
			return len(p), nil // ring full: dropped, as a busy NIC would
		}
		return 0, fmt.Errorf("writing to the Wintun adapter: %w", errno)
	}
	copy(unsafe.Slice((*byte)(ptr(buf)), len(p)), p)
	syscall.SyscallN(d.api.sendPacket, d.session, buf)
	return len(p), nil
}

// Close ends the session and removes the adapter, and with it its
// addresses, routes and DNS settings.
func (d *wintunDev) Close() error {
	if d.quit != 0 {
		windows.SetEvent(d.quit)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	d.closed = true
	if d.session != 0 {
		syscall.SyscallN(d.api.endSession, d.session)
	}
	if d.adapter != 0 {
		syscall.SyscallN(d.api.closeAdapter, d.adapter)
		adapters.Delete(d.name)
	}
	if d.quit != 0 {
		windows.CloseHandle(d.quit)
	}
	return nil
}
