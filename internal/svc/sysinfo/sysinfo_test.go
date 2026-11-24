package sysinfo

import (
	"context"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/peer/peertest"
)

func TestGetOverPeerSession(t *testing.T) {
	client, _ := peertest.Pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	info, err := Get(ctx, client)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	wantHost, _ := os.Hostname()
	if info.Hostname != wantHost {
		t.Errorf("Hostname = %q, want %q", info.Hostname, wantHost)
	}
	if info.OS != runtime.GOOS || info.Arch != runtime.GOARCH {
		t.Errorf("OS/Arch = %s/%s, want %s/%s", info.OS, info.Arch, runtime.GOOS, runtime.GOARCH)
	}
	if info.CPUCores != runtime.NumCPU() || info.CPUCores < 1 {
		t.Errorf("CPUCores = %d, want %d", info.CPUCores, runtime.NumCPU())
	}
	if info.Cwd == "" {
		t.Error("Cwd is empty")
	}
	if _, err := time.Parse(time.RFC3339, info.LocalTime); err != nil {
		t.Errorf("LocalTime %q is not RFC 3339: %v", info.LocalTime, err)
	}
	if info.RemoHopVersion == "" {
		t.Error("RemoHopVersion is empty")
	}
	if runtime.GOOS == "linux" {
		if info.MemTotalBytes == 0 || info.Kernel == "" {
			t.Errorf("linux host reported no memory (%d) or kernel (%q)", info.MemTotalBytes, info.Kernel)
		}
		if info.RootTotalBytes == 0 {
			t.Error("linux host reported no root filesystem size")
		}
	}
}

func TestCollectNeverFailsOrLeaksNonFinite(t *testing.T) {
	info := collect()
	if info.Hostname == "" || info.OS == "" {
		t.Errorf("collect left basic fields empty: %+v", info)
	}
	if strings.ContainsAny(info.Hostname, "\x00\n") {
		t.Errorf("hostname contains control characters: %q", info.Hostname)
	}
}

func TestSafeReplacesControlAndFormatCharacters(t *testing.T) {
	in := "web\x1b[2J-01‮evil\n"
	got := safe(in)
	if strings.ContainsAny(got, "\x1b\n") || strings.ContainsRune(got, '‮') {
		t.Errorf("safe(%q) = %q still contains control or bidi characters", in, got)
	}
	if got != "web?[2J-01?evil?" {
		t.Errorf("safe(%q) = %q", in, got)
	}
	if safe("plain-host.example") != "plain-host.example" {
		t.Error("safe altered printable text")
	}
}
