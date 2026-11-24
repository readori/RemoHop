package desktop

import (
	"context"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/peer/peertest"
)

// startPulse runs a private PulseAudio with a null sink as the default
// output and points PULSE_SERVER at it. It skips the test when PulseAudio
// is not installed or cannot run here.
func startPulse(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"pulseaudio", "pactl", "ffmpeg"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed", bin)
		}
	}
	// Unix socket paths are short: do not use t.TempDir().
	dir, err := os.MkdirTemp("", "remohoppa")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "native")
	cmd := exec.Command("pulseaudio", "-n", "--exit-idle-time=-1", "--daemonize=no", "--log-target=stderr",
		"--load=module-null-sink sink_name=remohoptest sink_properties=device.description=remohoptest",
		"--load=module-native-protocol-unix socket="+sock)
	cmd.Env = append(os.Environ(), "XDG_RUNTIME_DIR="+dir, "PULSE_RUNTIME_PATH="+dir, "HOME="+dir)
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start pulseaudio: %v", err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	server := "unix:" + sock
	var lastErr error
	for i := 0; i < 50; i++ {
		c := exec.Command("pactl", "--server", server, "set-default-sink", "remohoptest")
		if lastErr = c.Run(); lastErr == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if lastErr != nil {
		t.Skipf("pulseaudio does not come up here: %v", lastErr)
	}
	t.Setenv("PULSE_SERVER", server)
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv(audioSourceEnv, "")
}

// play sends a tone to the null sink for as long as the test runs.
func playTone(t *testing.T, freq string) {
	t.Helper()
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-nostdin", "-re", "-f", "lavfi",
		"-i", "sine=frequency="+freq+":sample_rate=48000", "-f", "pulse", "-device", "remohoptest", "remohop-test-tone")
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	time.Sleep(300 * time.Millisecond)
}

// The real path on Linux: what a program plays to the default output is
// captured from the sound server's monitor source, encoded to Opus, sent,
// and decodes back to the same tone.
func TestAudioPulseNullSink(t *testing.T) {
	startPulse(t)
	playTone(t, "660")
	client, _ := peertest.Pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	a, err := OpenAudio(ctx, client, AudioOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if h := a.Hello(); !strings.Contains(h.Source, "PulseAudio") || h.Codec != "opus" {
		t.Fatalf("hello %+v", h)
	}
	var packets [][]byte
	var first, last AudioPacket
	for len(packets) < 150 {
		p, err := a.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(packets) == 0 {
			first = p
		}
		packets = append(packets, p.Data)
		last = p
	}
	if span := last.Capture.Sub(first.Capture); span < 2800*time.Millisecond || span > 3200*time.Millisecond {
		t.Errorf("capture times span %v for 3 s of audio", span)
	}
	// Capture times are in the past, but not by much: a few frames of
	// pipeline, not seconds.
	if lag := time.Since(last.Capture); lag < 0 || lag > 500*time.Millisecond {
		t.Errorf("the last packet claims to be captured %v ago", lag)
	}
	pcm := decodePCM(t, oggOpus(packets, 20))
	tone, rms := toneLevel(pcm[9600:], 660)
	if tone < 0.05 || math.Abs(tone/math.Sqrt2-rms) > 0.1*rms {
		t.Fatalf("captured audio is not the 660 Hz tone: tone amplitude %.3f, rms %.3f", tone, rms)
	}
}

// Without a reachable sound server the error says what is missing.
func TestAudioNoSoundServer(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	ff, _ := ffmpegPath()
	if !ffmpegHasEncoder(ff, "libopus") || !ffmpegHasInputDevice(ff, "pulse") {
		t.Skip("ffmpeg lacks libopus or libpulse")
	}
	t.Setenv("PULSE_SERVER", "unix:/nonexistent/remohop-no-pulse")
	client, _ := peertest.Pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := OpenAudio(ctx, client, AudioOptions{})
	if err == nil || !strings.Contains(err.Error(), "PulseAudio") || !strings.Contains(err.Error(), "PipeWire") {
		t.Fatalf("want an error naming PulseAudio and PipeWire, got %v", err)
	}
	t.Log(err)
}
