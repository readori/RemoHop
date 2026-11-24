package desktop

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// macOS has no API to record what the machine plays, so audio forwarding
// needs a loopback device (BlackHole, Loopback, Soundflower, ...) that the
// user has made part of the system output (an Audio MIDI Setup
// "Multi-Output Device" containing both the speakers and the loopback
// device). ffmpeg then records the loopback device through AVFoundation.
// A microphone is never picked automatically: only devices that look like
// loopbacks are, or the one named in REMOHOP_DESKTOP_AUDIO_SOURCE.
func platformAudioSources(ctx context.Context, ffmpeg string) ([]audioSource, string) {
	out := ffmpegOutput(ffmpeg, "-hide_banner", "-f", "avfoundation", "-list_devices", "true", "-i", "")
	devs := parseAVFoundationAudio(out)
	if len(devs) == 0 {
		return nil, "ffmpeg lists no audio devices through AVFoundation. " + audioHelp()
	}
	var picked []listedDevice
	if want := os.Getenv(audioSourceEnv); want != "" {
		for _, d := range devs {
			if strings.EqualFold(d.name, want) || strconv.Itoa(d.index) == want {
				picked = append(picked, d)
			}
		}
		if len(picked) == 0 {
			return nil, fmt.Sprintf("%s=%q matches none of the audio devices: %s", audioSourceEnv, want, deviceNames(devs))
		}
	} else {
		for _, d := range devs {
			if isLoopbackName(d.name) {
				picked = append(picked, d)
			}
		}
		if len(picked) == 0 {
			return nil, "this Mac has no loopback audio device (found: " + deviceNames(devs) + "). " + audioHelp()
		}
	}
	var srcs []audioSource
	for _, d := range picked {
		srcs = append(srcs, audioSource{
			desc: "AVFoundation loopback device " + d.name,
			args: []string{"-f", "avfoundation", "-i", ":" + strconv.Itoa(d.index)},
			warning: "macOS: the sound is captured from " + d.name + "; you hear it on the Mac only while the system output is a " +
				"Multi-Output Device that includes both the speakers and " + d.name,
		})
	}
	return srcs, ""
}

func deviceNames(devs []listedDevice) string {
	var n []string
	for _, d := range devs {
		n = append(n, d.name)
	}
	return strings.Join(n, ", ")
}

func audioHelp() string {
	return "macOS cannot record the system output by itself: install a loopback audio driver such as BlackHole " +
		"(brew install blackhole-2ch), create a Multi-Output Device in Audio MIDI Setup that contains your speakers and " +
		"BlackHole, select it as the sound output, and give remohop the Microphone permission (macOS treats loopback devices as microphones). " +
		"Set " + audioSourceEnv + " to a device name or index to use a different device"
}
