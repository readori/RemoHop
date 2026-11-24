package inject

import (
	"fmt"
	"sync/atomic"
	"unsafe"
)

// Sys carries out the Win32 calls behind the Windows injector. By default
// they are made in this process (LocalSys). A Windows service, which runs in
// session 0 without the user's desktop, installs one that has them made by a
// helper process on the interactive console session's input desktop
// (internal/unattended); every Injector then acts there.
type Sys interface {
	// SendInput sends count INPUT structures of size bytes each, laid
	// out back to back in events.
	SendInput(events []byte, count, size int) error
	// Metric is GetSystemMetrics(index).
	Metric(index int) int
	// CursorPos is GetCursorPos.
	CursorPos() (x, y int, err error)
	// VkKeyScan is VkKeyScanW(ch) for the active keyboard layout.
	VkKeyScan(ch uint16) int16
	// MapVirtualKey is MapVirtualKeyW(code, mapType).
	MapVirtualKey(code, mapType uint32) uint32
}

// SASSender is implemented by a Sys that can raise the secure attention
// sequence. The injector then turns Ctrl+Alt+Delete into the real sequence
// instead of keystrokes, which Windows ignores.
type SASSender interface {
	SendSAS() error
}

type sysBox struct{ s Sys }

var curSys atomic.Pointer[sysBox]

// UseSys makes every Injector of this process use s; nil restores LocalSys.
func UseSys(s Sys) {
	if s == nil {
		curSys.Store(nil)
		return
	}
	curSys.Store(&sysBox{s})
}

func currentSys() Sys {
	if b := curSys.Load(); b != nil {
		return b.s
	}
	return localSys{}
}

// LocalSys makes the calls in this process, on the calling thread's desktop.
func LocalSys() Sys { return localSys{} }

// InputSize is the size of one INPUT structure, for Sys.SendInput.
const InputSize = int(unsafe.Sizeof(winInput{}))

type localSys struct{}

func (localSys) SendInput(events []byte, count, size int) error {
	if count <= 0 {
		return nil
	}
	if size != InputSize || len(events) != count*size {
		return fmt.Errorf("SendInput: %d bytes for %d events of %d bytes (want %d each)", len(events), count, size, InputSize)
	}
	n, _, err := procSendInput.Call(uintptr(count), uintptr(unsafe.Pointer(&events[0])), uintptr(size))
	if int(n) != count {
		return fmt.Errorf("SendInput: %v (input blocked by UIPI or a secure desktop?)", err)
	}
	return nil
}

func (localSys) Metric(index int) int {
	r, _, _ := procGetSystemMetrics.Call(uintptr(index))
	return int(int32(r))
}

func (localSys) CursorPos() (int, int, error) {
	var p struct{ x, y int32 }
	if r, _, err := procGetCursorPos.Call(uintptr(unsafe.Pointer(&p))); r == 0 {
		return 0, 0, fmt.Errorf("GetCursorPos: %v", err)
	}
	return int(p.x), int(p.y), nil
}

func (localSys) VkKeyScan(ch uint16) int16 {
	r, _, _ := procVkKeyScanW.Call(uintptr(ch))
	return int16(r)
}

func (localSys) MapVirtualKey(code, mapType uint32) uint32 {
	r, _, _ := procMapVirtualKeyW.Call(uintptr(code), uintptr(mapType))
	return uint32(r)
}
