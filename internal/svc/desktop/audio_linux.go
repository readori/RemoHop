package desktop

import (
	"context"
	"os"
	"strconv"
)

// Linux captures the monitor of the default output of the sound server:
// the stream of everything the machine plays. PulseAudio and PipeWire (with
// its PulseAudio compatibility layer, pipewire-pulse) both offer it, and
// ffmpeg reads it through libpulse. "@DEFAULT_MONITOR@" is resolved by the
// server to the monitor of whatever the default output is at that moment.
func platformAudioSources(ctx context.Context, ffmpeg string) ([]audioSource, string) {
	if !ffmpegHasInputDevice(ffmpeg, "pulse") {
		return nil, "the host's ffmpeg was built without PulseAudio capture (libpulse), which is how the machine's output is captured on Linux. " + audioHelp()
	}
	name, desc := "@DEFAULT_MONITOR@", "monitor of the default PulseAudio/PipeWire output"
	if s := os.Getenv(audioSourceEnv); s != "" {
		name, desc = s, "PulseAudio source "+s
	}
	return []audioSource{{
		desc: desc,
		// 10 ms capture fragments (48 kHz, 16 bit, stereo = 1920 bytes).
		args: []string{"-f", "pulse", "-fragment_size", "1920", "-i", name},
		env:  pulseEnv(),
	}}, ""
}

// pulseEnv is the environment ffmpeg needs to find the sound server. A host
// started from systemd or ssh often lacks XDG_RUNTIME_DIR although the
// user's sound server runs: its socket is at /run/user/UID/pulse/native.
func pulseEnv() []string {
	env := os.Environ()
	if os.Getenv("PULSE_SERVER") != "" || os.Getenv("XDG_RUNTIME_DIR") != "" {
		return env
	}
	dir := "/run/user/" + strconv.Itoa(os.Getuid())
	if _, err := os.Stat(dir + "/pulse/native"); err == nil {
		env = append(env, "XDG_RUNTIME_DIR="+dir, "PULSE_SERVER=unix:"+dir+"/pulse/native")
	}
	return env
}

func audioHelp() string {
	return "Linux audio forwarding needs PulseAudio or PipeWire with pipewire-pulse running in the desktop session that remohop runs in " +
		"(PULSE_SERVER or XDG_RUNTIME_DIR must reach it), and an ffmpeg with libpulse and libopus. " +
		"Set " + audioSourceEnv + " to capture a different PulseAudio source (see `pactl list short sources`)"
}
