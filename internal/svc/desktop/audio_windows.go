package desktop

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// Windows candidates, in order: WASAPI loopback, when this ffmpeg build has
// a wasapi capture device (FFmpeg 8 and newer might; the option names below
// follow its documentation and are not verified on a real Windows machine),
// then a DirectShow loopback device: "virtual-audio-capturer" from the
// screen-capture-recorder project, or the sound card's "Stereo Mix" when its
// driver offers one and the user enabled it. Each candidate is test-run
// before use, so a wrong guess falls through to the next one.
func platformAudioSources(ctx context.Context, ffmpeg string) ([]audioSource, string) {
	var srcs []audioSource
	if want := os.Getenv(audioSourceEnv); want != "" {
		return []audioSource{{desc: "DirectShow device " + want, args: []string{"-f", "dshow", "-i", "audio=" + want}}}, ""
	}
	if ffmpegHasInputDevice(ffmpeg, "wasapi") {
		srcs = append(srcs, audioSource{
			desc: "WASAPI loopback of the default output",
			args: []string{"-f", "wasapi", "-loopback", "1", "-i", "default"},
		})
	}
	var names []string
	if ffmpegHasInputDevice(ffmpeg, "dshow") {
		names = parseDshowAudio(ffmpegOutput(ffmpeg, "-hide_banner", "-list_devices", "true", "-f", "dshow", "-i", "dummy"))
		for _, n := range names {
			if isLoopbackName(n) {
				srcs = append(srcs, audioSource{desc: "DirectShow loopback device " + n, args: []string{"-f", "dshow", "-i", "audio=" + n}})
			}
		}
	}
	if len(srcs) == 0 {
		found := "none"
		if len(names) > 0 {
			found = strings.Join(names, ", ")
		}
		return nil, fmt.Sprintf("this machine has no audio loopback device ffmpeg can record (DirectShow audio devices: %s). %s", found, audioHelp())
	}
	return srcs, ""
}

func audioHelp() string {
	return "Windows audio forwarding needs either an ffmpeg with WASAPI loopback capture, or a DirectShow loopback device: " +
		"install the free virtual-audio-capturer driver (screen-capture-recorder) or enable \"Stereo Mix\" " +
		"(Sound settings > Recording > show disabled devices) if the sound card offers it. " +
		"Set " + audioSourceEnv + " to a DirectShow audio device name to use another one"
}
