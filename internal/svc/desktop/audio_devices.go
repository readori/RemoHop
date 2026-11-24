package desktop

import (
	"context"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Device listing helpers for the audio sources, kept free of build tags so
// that the parsers are tested on every platform.

var (
	devMu    sync.Mutex
	devCache = map[string]string{}
)

// ffmpegOutput runs ffmpeg with args and returns what it printed (device
// listings go to stderr and make ffmpeg exit with an error, which is
// expected). Results are remembered for the life of the process.
func ffmpegOutput(ffmpeg string, args ...string) string {
	k := ffmpeg + "\x00" + strings.Join(args, "\x00")
	devMu.Lock()
	defer devMu.Unlock()
	if v, ok := devCache[k]; ok {
		return v
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, ffmpeg, args...).CombinedOutput()
	devCache[k] = string(out)
	return string(out)
}

// ffmpegHasInputDevice reports whether ffmpeg was built with the named
// capture device (as `ffmpeg -devices` lists it).
func ffmpegHasInputDevice(ffmpeg, name string) bool {
	return listsInputDevice(ffmpegOutput(ffmpeg, "-hide_banner", "-devices"), name)
}

// listsInputDevice parses the output of `ffmpeg -devices`: lines such as
// " D  pulse   Pulse audio output" or " DE alsa   ALSA audio output", where
// the first column holds D when the device can be read from.
func listsInputDevice(out, name string) bool {
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) < 2 || !strings.Contains(f[0], "D") || strings.ContainsAny(f[0], "-:") {
			continue
		}
		for _, n := range strings.Split(f[1], ",") {
			if n == name {
				return true
			}
		}
	}
	return false
}

// listedDevice is one entry of a device listing: its capture index (where
// the device is addressed by index) and name.
type listedDevice struct {
	index int
	name  string
}

var avfDeviceRE = regexp.MustCompile(`\[(\d+)\]\s+(.+?)\s*$`)

// parseAVFoundationAudio extracts the audio devices from the output of
// `ffmpeg -f avfoundation -list_devices true -i ""`.
func parseAVFoundationAudio(out string) []listedDevice {
	var devs []listedDevice
	inAudio := false
	for _, l := range strings.Split(out, "\n") {
		switch {
		case strings.Contains(l, "AVFoundation audio devices"):
			inAudio = true
		case strings.Contains(l, "AVFoundation video devices"):
			inAudio = false
		case inAudio:
			// "[AVFoundation indev @ 0x...] [1] BlackHole 2ch": the device
			// index is the second bracket pair.
			if i := strings.Index(l, "] ["); i >= 0 {
				if m := avfDeviceRE.FindStringSubmatch(l[i+2:]); m != nil {
					idx, _ := strconv.Atoi(m[1])
					devs = append(devs, listedDevice{idx, m[2]})
				}
			}
		}
	}
	return devs
}

var dshowQuotedRE = regexp.MustCompile(`"([^"]+)"(?:\s*\((audio|video|none)[^)]*\))?\s*$`)

// parseDshowAudio extracts the audio devices from the output of
// `ffmpeg -list_devices true -f dshow -i dummy`, in both of ffmpeg's
// formats: the older one with "DirectShow audio devices" section headings
// and the newer one with a "(audio)" suffix on each device.
func parseDshowAudio(out string) []string {
	var names []string
	section := ""
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimRight(l, "\r")
		switch {
		case strings.Contains(l, "DirectShow audio devices"):
			section = "audio"
			continue
		case strings.Contains(l, "DirectShow video devices"):
			section = "video"
			continue
		case strings.Contains(l, "Alternative name"):
			continue
		}
		m := dshowQuotedRE.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		kind := m[2]
		if kind == "" {
			kind = section
		}
		if kind == "audio" || (kind == "none" && section == "audio") {
			names = append(names, m[1])
		}
	}
	return names
}

// loopbackWords mark capture devices that carry what the machine plays,
// not a microphone.
var loopbackWords = []string{"blackhole", "loopback", "soundflower", "ishowu", "virtual-audio-capturer",
	"stereo mix", "stereomix", "what u hear", "what you hear", "wave out mix", "vb-cable", "cable output"}

// isLoopbackName reports whether a capture device name looks like a loopback.
func isLoopbackName(name string) bool {
	n := strings.ToLower(name)
	for _, w := range loopbackWords {
		if strings.Contains(n, w) {
			return true
		}
	}
	return false
}
