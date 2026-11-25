package desktop

import (
	"context"
	"errors"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type shellExecuteInfo struct {
	Size       uint32
	Mask       uint32
	Wnd        uintptr
	Verb       *uint16
	File       *uint16
	Parameters *uint16
	Directory  *uint16
	Show       int32
	InstApp    uintptr
	IDList     uintptr
	Class      *uint16
	KeyClass   uintptr
	HotKey     uint32
	Icon       uintptr
	Process    windows.Handle
}

var procShellExecuteExW = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")

// quoteArg quotes one argument for the Windows command line.
func quoteArg(s string) string { return syscall.EscapeArg(s) }

// runElevated starts exe with args as administrator: Windows shows the UAC
// prompt. It waits for the program to end.
func runElevated(ctx context.Context, exe string, args []string) error {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = quoteArg(a)
	}
	info := shellExecuteInfo{Mask: 0x40 /* SEE_MASK_NOCLOSEPROCESS */ | 0x400, /* SEE_MASK_FLAG_NO_UI */
		Verb: windows.StringToUTF16Ptr("runas"), File: windows.StringToUTF16Ptr(exe),
		Parameters: windows.StringToUTF16Ptr(strings.Join(q, " ")), Show: 0 /* SW_HIDE */}
	info.Size = uint32(unsafe.Sizeof(info))
	if r, _, err := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&info))); r == 0 {
		if errors.Is(err, windows.ERROR_CANCELLED) {
			return Errorf("elevation_cancelled", "the administrator prompt was cancelled")
		}
		return err
	}
	defer windows.CloseHandle(info.Process)
	for {
		ev, err := windows.WaitForSingleObject(info.Process, 500)
		if err != nil {
			return err
		}
		if ev == windows.WAIT_OBJECT_0 {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}
