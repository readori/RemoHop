//go:build linux

package sysinfo

import (
	"io"
	"io/fs"
	"os"
	"strings"
)

// readSmall reads a kernel or system file. The limit keeps a surprising file
// from exhausting memory; the largest we read, /proc/cpuinfo on a big machine,
// is well under it.
func readSmall(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 4<<20))
	return string(b), err
}

func platform(i *Info) {
	if data, err := readSmall("/etc/os-release"); err == nil {
		i.DistroName, i.DistroVersion = parseOSRelease(data)
	} else if data, err := readSmall("/usr/lib/os-release"); err == nil {
		i.DistroName, i.DistroVersion = parseOSRelease(data)
	}
	if data, err := readSmall("/proc/sys/kernel/osrelease"); err == nil {
		i.Kernel = strings.TrimSpace(data)
	}
	if data, err := readSmall("/proc/meminfo"); err == nil {
		i.MemTotalBytes, i.MemAvailBytes = parseMeminfo(data)
	}
	if data, err := readSmall("/proc/cpuinfo"); err == nil {
		i.CPUModel = parseCPUModel(data)
	}
	if data, err := readSmall("/proc/uptime"); err == nil {
		i.UptimeSeconds = parseUptime(data)
	}
	if data, err := readSmall("/proc/loadavg"); err == nil {
		i.LoadAverage = parseLoadavg(data)
	}
	i.Display = displayAvailable()
	statRoot(i)
}

// displayAvailable reports whether a graphical session is probably reachable
// from this process: a display variable is set, or an X server has its socket
// in /tmp/.X11-unix. It cannot tell whether that server is answering.
func displayAvailable() bool {
	if os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != "" {
		return true
	}
	entries, err := os.ReadDir("/tmp/.X11-unix")
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.Type()&fs.ModeSocket != 0 {
			return true
		}
	}
	return false
}
