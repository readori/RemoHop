//go:build windows

package sysinfo

import (
	"fmt"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// x/sys/windows does not wrap GlobalMemoryStatusEx, so it is called through a
// lazy proc. NewLazySystemDLL only ever loads from the system directory.
var procGlobalMemoryStatusEx = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")

// memoryStatusEx mirrors MEMORYSTATUSEX (64 bytes).
type memoryStatusEx struct {
	length               uint32
	memoryLoad           uint32
	totalPhys            uint64
	availPhys            uint64
	totalPageFile        uint64
	availPageFile        uint64
	totalVirtual         uint64
	availVirtual         uint64
	availExtendedVirtual uint64
}

// verNTWorkstation is OSVERSIONINFOEX.wProductType for client editions;
// every other value is a server or domain controller.
const verNTWorkstation = 1

func platform(i *Info) {
	v := windows.RtlGetVersion()
	i.DistroName = "Windows"
	i.DistroVersion = windowsRelease(v.MajorVersion, v.MinorVersion, v.BuildNumber, v.ProductType != verNTWorkstation)
	i.Kernel = fmt.Sprintf("%d.%d.%d", v.MajorVersion, v.MinorVersion, v.BuildNumber)

	i.CPUModel = cpuModel()

	if procGlobalMemoryStatusEx.Find() == nil {
		m := memoryStatusEx{length: uint32(unsafe.Sizeof(memoryStatusEx{}))}
		if r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&m))); r != 0 {
			i.MemTotalBytes = m.totalPhys
			i.MemAvailBytes = m.availPhys
		}
	}

	i.UptimeSeconds = int64(windows.DurationSinceBoot().Seconds())

	// Commands on a Windows host run under the command interpreter, not $SHELL
	// (which only exists inside POSIX layers such as Git Bash).
	if comspec := os.Getenv("ComSpec"); comspec != "" {
		i.Shell = comspec
	}

	if root, err := windows.UTF16PtrFromString(systemDrive() + `\`); err == nil {
		var avail, total, free uint64
		if windows.GetDiskFreeSpaceEx(root, &avail, &total, &free) == nil {
			i.RootTotalBytes = total
			i.RootFreeBytes = avail // what this user may write, as on Unix
		}
	}

	// Session 0 is the non-interactive services session; any other session
	// has a window station a desktop can be captured from.
	var session uint32
	if windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &session) == nil {
		i.Display = session != 0
	}
}

func cpuModel() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `HARDWARE\DESCRIPTION\System\CentralProcessor\0`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	s, _, err := k.GetStringValue("ProcessorNameString")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(s)
}

// systemDrive is the volume Windows is installed on, the closest thing to "/".
func systemDrive() string {
	if d := os.Getenv("SystemDrive"); len(d) == 2 && d[1] == ':' {
		return d
	}
	return "C:"
}
