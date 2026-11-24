package sysinfo

import (
	"encoding/binary"
	"testing"
	"time"
)

func TestLEUint(t *testing.T) {
	if v, ok := leUint([]byte{0x00, 0x40, 0x00, 0x00}); !ok || v != 16384 {
		t.Errorf("4-byte = %d, %v", v, ok)
	}
	if v, ok := leUint(binary.LittleEndian.AppendUint64(nil, 17179869184)); !ok || v != 17179869184 {
		t.Errorf("8-byte = %d, %v", v, ok)
	}
	for _, raw := range [][]byte{nil, {1}, {1, 2, 3}, make([]byte, 5), make([]byte, 16)} {
		if v, ok := leUint(raw); ok || v != 0 {
			t.Errorf("leUint(len %d) = %d, %v; want 0, false", len(raw), v, ok)
		}
	}
}

// loadavgBuf builds a 64-bit `struct loadavg` as the kernel lays it out.
func loadavgBuf(l1, l5, l15 uint32, scale uint64) []byte {
	b := make([]byte, 24)
	binary.LittleEndian.PutUint32(b[0:], l1)
	binary.LittleEndian.PutUint32(b[4:], l5)
	binary.LittleEndian.PutUint32(b[8:], l15)
	binary.LittleEndian.PutUint64(b[16:], scale)
	return b
}

func TestDecodeBSDLoadavg(t *testing.T) {
	short := make([]byte, 16) // 32-bit long: scale at offset 12
	binary.LittleEndian.PutUint32(short[0:], 4096)
	binary.LittleEndian.PutUint32(short[12:], 2048)

	tests := []struct {
		name string
		in   []byte
		want [3]float64
	}{
		{name: "64-bit layout", in: loadavgBuf(3072, 2048, 1024, 2048), want: [3]float64{1.5, 1, 0.5}},
		{name: "idle", in: loadavgBuf(0, 0, 0, 2048)},
		{name: "32-bit layout", in: short, want: [3]float64{2, 0, 0}},
		{name: "zero scale", in: loadavgBuf(100, 100, 100, 0)},
		{name: "truncated", in: make([]byte, 12)},
		{name: "empty", in: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := decodeBSDLoadavg(tc.in); got != tc.want {
				t.Errorf("decodeBSDLoadavg = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDecodeBootTimeAndUptime(t *testing.T) {
	tv := make([]byte, 16) // struct timeval: int64 sec, int32 usec, padding
	binary.LittleEndian.PutUint64(tv, 1700000000)
	binary.LittleEndian.PutUint32(tv[8:], 123456)

	sec, ok := decodeBootTime(tv)
	if !ok || sec != 1700000000 {
		t.Fatalf("decodeBootTime = %d, %v", sec, ok)
	}
	for _, raw := range [][]byte{nil, make([]byte, 7), make([]byte, 16)} {
		if _, ok := decodeBootTime(raw); ok {
			t.Errorf("decodeBootTime(len %d, zero) reported ok", len(raw))
		}
	}
	neg := binary.LittleEndian.AppendUint64(nil, ^uint64(0))
	if _, ok := decodeBootTime(neg); ok {
		t.Error("negative boot time reported ok")
	}

	now := time.Unix(1700000000+3*86400+7, 0)
	if got := uptimeSince(sec, now); got != 3*86400+7 {
		t.Errorf("uptimeSince = %d", got)
	}
	if got := uptimeSince(now.Unix()+100, now); got != 0 {
		t.Errorf("boot in the future: uptimeSince = %d, want 0", got)
	}
	if got := uptimeSince(0, now); got != 0 {
		t.Errorf("unset boot time: uptimeSince = %d, want 0", got)
	}
}

func TestWindowsRelease(t *testing.T) {
	tests := []struct {
		major, minor, build uint32
		server              bool
		want                string
	}{
		{10, 0, 19045, false, "10"},
		{10, 0, 22631, false, "11"},
		{10, 0, 26100, false, "11"},
		{10, 0, 14393, true, "Server 2016"},
		{10, 0, 17763, true, "Server 2019"},
		{10, 0, 20348, true, "Server 2022"},
		{10, 0, 26100, true, "Server 2025"},
		{6, 1, 7601, false, "6.1"},
		{6, 3, 9600, true, "Server 6.3"},
		{0, 0, 0, false, "0.0"},
	}
	for _, tc := range tests {
		if got := windowsRelease(tc.major, tc.minor, tc.build, tc.server); got != tc.want {
			t.Errorf("windowsRelease(%d,%d,%d,%v) = %q, want %q", tc.major, tc.minor, tc.build, tc.server, got, tc.want)
		}
	}
}
