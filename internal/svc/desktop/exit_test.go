//go:build linux || darwin

package desktop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A GPU encoder that initialises (its probe runs at 320x240) but delivers
// nothing at the screen's real size (H.264 hardware encoders stop at about
// 4096 pixels wide: a 5120x1440 ultrawide, two monitors on X11, a 5K Mac)
// hands the session to the next encoder instead of ending it.
func TestEncoderFallbackOnRealSize(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "ffmpeg")
	script := "#!/bin/sh\ncase \"$*\" in *-encoders*) printf ' V....D h264_nvenc NVIDIA\\n V....D libx264 x264\\n';; esac\nexit 0\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	enc, err := selectEncoder(ctx, fake, encRequest{codec: codecH264})
	if err != nil || enc.name != "h264_nvenc" {
		t.Fatalf("first choice %v, %v", enc, err)
	}
	h := &hostSession{ctx: ctx, ffmpeg: fake, enc: enc, fps: 60, spec: captureSpec{width: 5120, height: 1440}, opts: Options{Scale: 1}}
	h.bitrate = h.targetBitrate()
	if err := h.onEncoderExit(encExit{gen: 1, frames: 0, err: errors.New("InitializeEncoder failed: invalid param (8)")}); err != nil {
		t.Fatalf("session ended: %v", err)
	}
	if h.enc.name != "libx264" || !strings.Contains(h.encWarn, "h264_nvenc could not encode the screen at 5120x1440") || h.restartAt.IsZero() {
		t.Fatalf("after the failure: encoder %s, warning %q, restart at %v", h.enc.name, h.encWarn, h.restartAt)
	}
	// The last one failing too ends the session, saying why.
	if err := h.onEncoderExit(encExit{gen: 2, frames: 0, err: errors.New("broken")}); err == nil || !strings.Contains(err.Error(), "broken") {
		t.Fatalf("software encoder failing: %v", err)
	}
}

// A GPU chain that delivers nothing moves the session to the default chain
// without condemning it for other sessions until the default chain works;
// one that works and then fails twice is given up for the session.
func TestGPUChainFailures(t *testing.T) {
	failed := 0
	h := &hostSession{ctx: context.Background(), enc: lookupEncoder("libx264"), fps: 30, opts: Options{Scale: 1}}
	h.spec.gpuFailed = func(*encoder) { failed++ }
	if err := h.onEncoderExit(encExit{gen: 1, gpu: true, frames: 0, err: errors.New("hwmap failed")}); err != nil {
		t.Fatal(err)
	}
	if !h.noGPU || failed != 0 {
		t.Fatalf("after a zero-frame GPU chain: noGPU %v, gpuFailed called %d times", h.noGPU, failed)
	}
	h.proc = &encProc{gpu: false}
	h.onDelivering()
	if failed != 1 {
		t.Fatalf("the default chain worked, gpuFailed called %d times", failed)
	}

	h = &hostSession{ctx: context.Background(), enc: lookupEncoder("libx264"), fps: 30, opts: Options{Scale: 1}, delivered: true}
	for i := range 2 {
		if err := h.onEncoderExit(encExit{gen: i + 1, gpu: true, frames: 500, err: errors.New("device removed")}); err != nil {
			t.Fatal(err)
		}
	}
	if !h.noGPU {
		t.Fatal("a GPU chain that failed twice after delivering is still used")
	}
}
