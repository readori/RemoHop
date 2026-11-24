package quick

import (
	"context"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32                = windows.NewLazySystemDLL("user32.dll")
	procMessageBoxW       = user32.NewProc("MessageBoxW")
	procMessageBoxTimeout = user32.NewProc("MessageBoxTimeoutW")
	procFindWindowExW     = user32.NewProc("FindWindowExW")
	procGetWindowThreadID = user32.NewProc("GetWindowThreadProcessId")
	procPostMessageW      = user32.NewProc("PostMessageW")
)

const (
	mbYesNo         = 0x00000004
	mbIconError     = 0x00000010
	mbIconWarning   = 0x00000030
	mbDefButton2    = 0x00000100
	mbSetForeground = 0x00010000
	mbTopmost       = 0x00040000
	idYes           = 6
	idNo            = 7
	wmCommand       = 0x0111
)

// nativeAsk shows a topmost Yes/No message box (No is the default button)
// and reports the answer. It has no answer when the box cannot be shown,
// when it times out, or when ctx ends first; the box is then taken down.
//
// The box belongs to this process and is answered on this machine's
// desktop. A client waiting for approval has no session yet and so no way
// to send input here.
func nativeAsk(ctx context.Context, lang string, r Request, timeout time.Duration) (allow, ok bool) {
	text, err1 := windows.UTF16PtrFromString(askText(lang, r) + "\n\n" + msg(lang, "ask_question"))
	title, err2 := windows.UTF16PtrFromString(msg(lang, "ask_title"))
	if err1 != nil || err2 != nil || procMessageBoxW.Find() != nil {
		return false, false
	}
	const flags = mbYesNo | mbIconWarning | mbDefButton2 | mbSetForeground | mbTopmost
	type result struct {
		ret uintptr
	}
	tid := make(chan uint32, 1)
	done := make(chan result, 1)
	go func() {
		// A message box is owned by the thread that shows it.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		tid <- windows.GetCurrentThreadId()
		var ret uintptr
		if procMessageBoxTimeout.Find() == nil {
			ret, _, _ = procMessageBoxTimeout.Call(0, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)),
				flags, 0, uintptr(timeout.Milliseconds()))
		} else {
			ret, _, _ = procMessageBoxW.Call(0, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)), flags)
		}
		done <- result{ret}
	}()
	thread := <-tid
	select {
	case res := <-done:
		switch res.ret {
		case idYes:
			return true, true
		case idNo:
			return false, true
		}
		return false, false // timed out, or could not be shown
	case <-ctx.Done():
		// Answered in the page, or the client went away: press "No" on
		// the box so it leaves the screen. Its answer is discarded.
		for i := 0; i < 20; i++ {
			dismissBoxes(thread)
			select {
			case <-done:
				return false, false
			case <-time.After(100 * time.Millisecond):
			}
		}
		return false, false
	}
}

// dismissBoxes presses "No" on every dialog owned by the given thread.
func dismissBoxes(thread uint32) {
	if procFindWindowExW.Find() != nil || procGetWindowThreadID.Find() != nil || procPostMessageW.Find() != nil {
		return
	}
	class, _ := windows.UTF16PtrFromString("#32770") // the dialog window class
	var prev uintptr
	for i := 0; i < 1000; i++ {
		hwnd, _, _ := procFindWindowExW.Call(0, prev, uintptr(unsafe.Pointer(class)), 0)
		if hwnd == 0 {
			return
		}
		if owner, _, _ := procGetWindowThreadID.Call(hwnd, 0); uint32(owner) == thread {
			procPostMessageW.Call(hwnd, wmCommand, idNo, 0)
		}
		prev = hwnd
	}
}

// nativeError tells the user that the program cannot go on. A program
// built for the Windows GUI subsystem has no console to print to.
func nativeError(lang, text string) {
	t, err1 := windows.UTF16PtrFromString(text)
	title, err2 := windows.UTF16PtrFromString(msg(lang, "error_title"))
	if err1 != nil || err2 != nil || procMessageBoxW.Find() != nil {
		return
	}
	procMessageBoxW.Call(0, uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(title)), mbIconError|mbSetForeground|mbTopmost)
}
