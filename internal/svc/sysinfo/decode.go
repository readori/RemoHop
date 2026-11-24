package sysinfo

import (
	"encoding/binary"
	"fmt"
	"math"
	"time"
)

// The decoders below turn raw kernel answers (macOS sysctl buffers, Windows
// version numbers) into values. They are pure and untagged so they are tested
// on every OS, including the ones the code that calls them cannot run on.

// leUint decodes a little-endian unsigned integer of 4 or 8 bytes, the two
// widths numeric sysctls come in. macOS has changed the width of some values
// between releases, so the caller must not assume one.
func leUint(raw []byte) (uint64, bool) {
	switch len(raw) {
	case 4:
		return uint64(binary.LittleEndian.Uint32(raw)), true
	case 8:
		return binary.LittleEndian.Uint64(raw), true
	}
	return 0, false
}

// decodeBSDLoadavg decodes the vm.loadavg sysctl, C `struct loadavg`:
//
//	fixpt_t ldavg[3];  // three uint32 fixed-point values
//	long    fscale;    // the scale they are multiplied by
//
// On 64-bit kernels fscale is 8 bytes at offset 16 (after 4 bytes of padding);
// with a 32-bit long it sits at offset 12.
func decodeBSDLoadavg(raw []byte) (loads [3]float64) {
	var scale uint64
	switch {
	case len(raw) >= 24:
		scale = binary.LittleEndian.Uint64(raw[16:24])
	case len(raw) >= 16:
		scale = uint64(binary.LittleEndian.Uint32(raw[12:16]))
	default:
		return loads
	}
	if scale == 0 {
		return loads
	}
	for i := range loads {
		v := float64(binary.LittleEndian.Uint32(raw[4*i:])) / float64(scale)
		if !math.IsInf(v, 0) && !math.IsNaN(v) {
			loads[i] = v
		}
	}
	return loads
}

// decodeBootTime returns the seconds field of the kern.boottime sysctl, a C
// `struct timeval` whose tv_sec is a 64-bit little-endian integer on every
// 64-bit macOS.
func decodeBootTime(raw []byte) (sec int64, ok bool) {
	if len(raw) < 8 {
		return 0, false
	}
	sec = int64(binary.LittleEndian.Uint64(raw[:8]))
	return sec, sec > 0
}

// uptimeSince returns whole seconds between a boot time (Unix seconds) and
// now, or 0 when the boot time is unset or in the future (a clock that was
// stepped backwards).
func uptimeSince(bootSec int64, now time.Time) int64 {
	if bootSec <= 0 {
		return 0
	}
	if up := now.Unix() - bootSec; up > 0 {
		return up
	}
	return 0
}

// windowsRelease names a Windows release from RtlGetVersion's numbers.
// Windows 11 and the Server releases still report version 10.0, so only the
// build number tells them apart.
func windowsRelease(major, minor, build uint32, server bool) string {
	if major != 10 {
		if server {
			return fmt.Sprintf("Server %d.%d", major, minor)
		}
		return fmt.Sprintf("%d.%d", major, minor)
	}
	if !server {
		if build >= 22000 {
			return "11"
		}
		return "10"
	}
	switch {
	case build >= 26100:
		return "Server 2025"
	case build >= 20348:
		return "Server 2022"
	case build >= 17763:
		return "Server 2019"
	default:
		return "Server 2016"
	}
}
