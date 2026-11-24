//go:build darwin

package sysinfo

import "testing"

func TestCollectDarwin(t *testing.T) {
	i := collect()
	if i.Kernel == "" {
		t.Error("Kernel is empty")
	}
	if i.MemTotalBytes == 0 {
		t.Error("MemTotalBytes is 0")
	}
	if i.UptimeSeconds <= 0 {
		t.Errorf("UptimeSeconds = %d, want > 0", i.UptimeSeconds)
	}
	if i.CPUCores < 1 {
		t.Errorf("CPUCores = %d", i.CPUCores)
	}
	if i.DistroName != "macOS" {
		t.Errorf("DistroName = %q, want macOS", i.DistroName)
	}
	if i.MemAvailBytes > i.MemTotalBytes {
		t.Errorf("MemAvailBytes %d exceeds MemTotalBytes %d", i.MemAvailBytes, i.MemTotalBytes)
	}
	t.Logf("%+v", *i)
}
