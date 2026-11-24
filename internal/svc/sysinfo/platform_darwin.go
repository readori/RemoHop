//go:build darwin

package sysinfo

import (
	"os"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// platform fills the macOS fields from sysctls. Each one is independent: a
// sysctl that is missing on some release leaves only its own field zero.
func platform(i *Info) {
	i.DistroName = "macOS"
	if v, err := unix.Sysctl("kern.osproductversion"); err == nil {
		i.DistroVersion = strings.TrimSpace(v)
	}
	if v, err := unix.Sysctl("kern.osrelease"); err == nil {
		i.Kernel = strings.TrimSpace(v)
	}
	if v, err := unix.Sysctl("machdep.cpu.brand_string"); err == nil {
		i.CPUModel = strings.TrimSpace(v)
	}
	if v, ok := sysctlUint("hw.memsize"); ok {
		i.MemTotalBytes = v
	}
	// Free plus speculative pages is what the kernel can hand out without
	// reclaiming anything; it understates what is reclaimable, like "free"
	// rather than "available" in vm_stat terms.
	if free, ok := sysctlUint("vm.page_free_count"); ok {
		spec, _ := sysctlUint("vm.page_speculative_count")
		i.MemAvailBytes = (free + spec) * uint64(os.Getpagesize())
	}
	if raw, err := unix.SysctlRaw("kern.boottime"); err == nil {
		if sec, ok := decodeBootTime(raw); ok {
			i.UptimeSeconds = uptimeSince(sec, time.Now())
		}
	}
	if raw, err := unix.SysctlRaw("vm.loadavg"); err == nil {
		i.LoadAverage = decodeBSDLoadavg(raw)
	}
	i.Display = displayAvailable()
	statRoot(i)
}

func sysctlUint(name string) (uint64, bool) {
	raw, err := unix.SysctlRaw(name)
	if err != nil {
		return 0, false
	}
	return leUint(raw)
}

// displayAvailable reports whether this process's user owns the GUI console.
// macOS gives /dev/console to the user logged in at the screen, so the rule
// is: the capture tool exists and the console belongs to us, a non-root user.
// Root is excluded because root owns the console when nobody is logged in,
// and a root daemon is outside the GUI session anyway. A process of the
// console user started over SSH still reports true although it may be unable
// to capture; that cannot be told apart without Mach bootstrap calls.
func displayAvailable() bool {
	if _, err := os.Stat("/usr/sbin/screencapture"); err != nil {
		return false
	}
	fi, err := os.Stat("/dev/console")
	if err != nil {
		return false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	uid := os.Geteuid()
	return uid != 0 && st.Uid == uint32(uid)
}
