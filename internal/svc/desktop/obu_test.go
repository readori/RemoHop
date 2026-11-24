package desktop

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"math/rand"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// encodeIVF produces a real AV1 stream with ffmpeg's synthetic source.
func encodeIVF(t *testing.T, args ...string) []byte {
	return encodeIVFFrom(t, "testsrc2=s=352x288:r=30", 25, args...)
}

// testFFmpeg is the ffmpeg a host would use (REMOHOP_FFMPEG, then PATH), so the
// encoder tests exercise whatever encoder libraries that build links.
func testFFmpeg(t *testing.T) string {
	t.Helper()
	p, err := ffmpegPath()
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	return p
}

func encodeIVFFrom(t *testing.T, src string, frames int, args ...string) []byte {
	return encodeFrom(t, "ivf", src, frames, args...)
}

// encodeFrom encodes frames of an ffmpeg lavfi source into the given
// container on stdout.
func encodeFrom(t *testing.T, format, src string, frames int, args ...string) []byte {
	t.Helper()
	ffmpeg := testFFmpeg(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	full := append([]string{"-hide_banner", "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", src, "-frames:v", strconv.Itoa(frames)}, args...)
	full = append(full, "-f", format, "-")
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, ffmpeg, full...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		if strings.Contains(errb.String(), "Unknown encoder") {
			t.Skipf("encoder unavailable: %s", errb.String())
		}
		t.Fatalf("ffmpeg: %v\n%s", err, errb.String())
	}
	return out.Bytes()
}

func TestParseRealStreams(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		codec string
	}{
		{"svt-8bit", []string{"-pix_fmt", "yuv420p", "-c:v", "libsvtav1", "-preset", "12", "-g", "10", "-svtav1-params", "pred-struct=1:lookahead=0"}, "av01.0.%M.08"},
		{"svt-10bit", []string{"-pix_fmt", "yuv420p10le", "-c:v", "libsvtav1", "-preset", "12", "-g", "10", "-svtav1-params", "pred-struct=1:lookahead=0"}, "av01.0.%M.10"},
		{"aom-realtime", []string{"-pix_fmt", "yuv420p", "-c:v", "libaom-av1", "-usage", "realtime", "-cpu-used", "8", "-lag-in-frames", "0", "-g", "10"}, "av01.0.%M.08"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := encodeIVF(t, tc.args...)
			ir, err := newIVFReader(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			if ir.hdr.Width != 352 || ir.hdr.Height != 288 {
				t.Fatalf("ivf header %+v", ir.hdr)
			}
			var keys []int
			var seq *SequenceInfo
			for i := 0; ; i++ {
				tu, _, err := ir.next()
				if err != nil {
					break
				}
				info, err := classifyTU(tu, false)
				if err != nil {
					t.Fatalf("frame %d: %v", i, err)
				}
				if info.seq != nil {
					seq = info.seq
				}
				if info.key {
					keys = append(keys, i)
					if info.seq == nil {
						t.Errorf("key frame %d without sequence header", i)
					}
				}
			}
			if seq == nil {
				t.Fatal("no sequence header")
			}
			if seq.Width != 352 || seq.Height != 288 {
				t.Fatalf("sequence %dx%d", seq.Width, seq.Height)
			}
			want := strings.Replace(tc.codec, "%M", seq.Codec()[7:10], 1)
			if seq.Codec() != want || seq.Level > 31 {
				t.Fatalf("codec %s, want %s", seq.Codec(), want)
			}
			if len(keys) != 3 || keys[0] != 0 || keys[1] != 10 || keys[2] != 20 {
				t.Fatalf("key frames at %v, want [0 10 20]", keys)
			}
			t.Logf("%s: codec %s keys %v", tc.name, seq.Codec(), keys)
		})
	}
}

func TestCodecString(t *testing.T) {
	cases := []struct {
		s    SequenceInfo
		want string
	}{
		{SequenceInfo{Profile: 0, Level: 8, Tier: 0, BitDepth: 8}, "av01.0.08M.08"},
		{SequenceInfo{Profile: 0, Level: 13, Tier: 1, BitDepth: 10}, "av01.0.13H.10"},
		{SequenceInfo{Profile: 2, Level: 4, BitDepth: 12}, "av01.2.04M.12"},
	}
	for _, c := range cases {
		if got := c.s.Codec(); got != c.want || !codecRE.MatchString(got) {
			t.Errorf("%+v: %s, want %s", c.s, got, c.want)
		}
	}
}

// Malformed input (truncated, corrupted) must produce errors, not panics.
func TestParseGarbage(t *testing.T) {
	data := encodeIVF(t, "-pix_fmt", "yuv420p", "-c:v", "libsvtav1", "-preset", "12")
	ir, err := newIVFReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	tu, _, err := ir.next()
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 20000; i++ {
		b := append([]byte(nil), tu[:rng.Intn(len(tu)+1)]...)
		for j := rng.Intn(4); j > 0 && len(b) > 0; j-- {
			b[rng.Intn(len(b))] ^= byte(1 << rng.Intn(8))
		}
		classifyTU(b, rng.Intn(2) == 0)
		parseSequenceHeaderOBU(b)
	}
	for _, bad := range [][]byte{nil, []byte("DKIF"), append([]byte("DKIF\x00\x00\x20\x00VP90"), make([]byte, 20)...)} {
		if _, err := newIVFReader(bytes.NewReader(bad)); err == nil {
			t.Errorf("accepted bad ivf header %q", bad)
		}
	}
	// A frame header announcing an absurd size is rejected before
	// allocation.
	hdr := append([]byte(nil), data[:32]...)
	hdr = append(hdr, 0xff, 0xff, 0xff, 0x7f, 0, 0, 0, 0, 0, 0, 0, 0)
	ir, _ = newIVFReader(bytes.NewReader(hdr))
	if _, _, err := ir.next(); err == nil {
		t.Error("accepted oversized frame")
	}
}

func TestMicros(t *testing.T) {
	ir := &ivfReader{hdr: ivfHeader{TimeDen: 1000, TimeNum: 1}}
	if got := ir.micros(1795057508694); got != 1795057508694000 {
		t.Fatalf("ms timebase: %d", got)
	}
	ir.hdr = ivfHeader{TimeDen: 60, TimeNum: 1}
	if got := ir.micros(61); got != 1016666 {
		t.Fatalf("1/60 timebase: %d", got)
	}
}

func TestKeymap(t *testing.T) {
	spot := map[string]uint16{
		"Escape": 1, "KeyA": 30, "KeyZ": 44, "Digit0": 11, "Enter": 28, "Space": 57,
		"ShiftLeft": 42, "ShiftRight": 54, "ControlLeft": 29, "ControlRight": 97,
		"AltLeft": 56, "AltRight": 100, "MetaLeft": 125, "MetaRight": 126,
		"F1": 59, "F10": 68, "F11": 87, "F12": 88, "ArrowUp": 103, "ArrowDown": 108,
		"ArrowLeft": 105, "ArrowRight": 106, "Home": 102, "End": 107, "PageUp": 104,
		"PageDown": 109, "Insert": 110, "Delete": 111, "NumpadEnter": 96, "Numpad0": 82,
		"NumpadDivide": 98, "NumpadMultiply": 55, "PrintScreen": 99, "Pause": 119,
		"ContextMenu": 127, "IntlBackslash": 86, "Backquote": 41, "Quote": 40,
	}
	for code, want := range spot {
		if got := domToEvdev[code]; got != want {
			t.Errorf("%s = %d, want %d", code, got, want)
		}
	}
	// Every letter, digit, F-key and numpad digit is present.
	for c := 'A'; c <= 'Z'; c++ {
		if _, ok := domToEvdev["Key"+string(c)]; !ok {
			t.Errorf("missing Key%c", c)
		}
	}
	for i := 0; i <= 9; i++ {
		for _, p := range []string{"Digit", "Numpad"} {
			if _, ok := domToEvdev[p+string(rune('0'+i))]; !ok {
				t.Errorf("missing %s%d", p, i)
			}
		}
	}
	for i := 1; i <= 24; i++ {
		if _, ok := domToEvdev["F"+itoa(i)]; !ok {
			t.Errorf("missing F%d", i)
		}
	}
	// No two codes share an evdev code, except legacy aliases.
	aliases := map[string]bool{"OSLeft": true, "OSRight": true}
	seen := map[uint16]string{}
	for code, ev := range domToEvdev {
		if aliases[code] {
			continue
		}
		if prev, dup := seen[ev]; dup {
			t.Errorf("%s and %s both map to %d", prev, code, ev)
		}
		seen[ev] = code
		if ev == 0 || ev+8 > 255 {
			t.Errorf("%s = %d has no X keycode", code, ev)
		}
	}
}

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return string(rune('0'+i/10)) + string(rune('0'+i%10))
}

func TestNormalize(t *testing.T) {
	o := Options{}
	if err := o.normalize(); err != nil || o.FPS != 60 || o.Scale != 1 {
		t.Fatalf("defaults: %+v %v", o, err)
	}
	for _, bad := range []Options{
		{FPS: -1}, {FPS: 1000}, {Bitrate: 5}, {MaxHeight: 10}, {Scale: 2}, {Scale: -0.5},
		{Display: -2}, {Display: 1000}, {Encoder: "x264; rm -rf /"},
	} {
		if err := bad.normalize(); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func TestWheelAndClamp(t *testing.T) {
	var w wheelAcc
	n := 0
	for i := 0; i < 10; i++ {
		_, sy := w.steps(0, 0.25)
		n += sy
	}
	if n != 2 {
		t.Fatalf("10 quarter notches gave %d clicks", n)
	}
	if clamp01(-1) != 0 || clamp01(2) != 1 || clamp01(0.5) != 0.5 {
		t.Fatal("clamp")
	}
	nan := 0.0
	nan /= nan
	if clamp01(nan) != 0 {
		t.Fatal("NaN not clamped")
	}
}

// TestEncoderTracksSmallChanges encodes a static screen on which a small
// square changes colour with the exact options a session would use for each
// software encoder (as selectEncoder configures it for the installed
// library version), decodes the result, and checks every frame shows the
// change. Low-delay fast presets of some encoder versions skip such updates
// until the next key frame, which on a desktop means a stale screen.
func TestEncoderTracksSmallChanges(t *testing.T) {
	ffmpeg := testFFmpeg(t)
	const w, h, n = 960, 540, 120
	src := fmt.Sprintf("color=gray:s=%dx%d:r=60,"+
		"drawbox=x=0:y=0:w=100:h=100:color=red:t=fill:enable='between(mod(t,1.6),0.4,1.2)',"+
		"drawbox=x=0:y=0:w=100:h=100:color=blue:t=fill:enable='gte(mod(t,1.6),1.2)'", w, h)
	want := make([]byte, n)
	for i := range want {
		switch tt := math.Mod(float64(i)/60, 1.6); {
		case tt >= 1.2:
			want[i] = 'b'
		case tt >= 0.4:
			want[i] = 'r'
		default:
			want[i] = '.'
		}
	}
	for _, name := range []string{"libsvtav1", "libaom-av1", "libx264"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()
			e, err := selectEncoder(ctx, ffmpeg, encRequest{forced: name})
			if err != nil {
				t.Skipf("encoder unavailable: %v", err)
			}
			args := append([]string{"-pix_fmt", "yuv420p"}, e.args(encParams{fps: 60, bitrate: 4_000_000, gop: 120, pixels: w * h})...)
			desc := fmt.Sprintf("%s (encoder library version: %s) with %s", name, orUnknown(e.version), strings.Join(args, " "))
			if name != "libsvtav1" {
				desc = name + " with " + strings.Join(args, " ")
			}
			t.Log(desc)
			// Raw streams (IVF for AV1, Annex-B for H.264) decode from a
			// pipe.
			format := "ivf"
			if e.codec == codecH264 {
				format = "h264"
			}
			ivf := encodeFrom(t, format, src, n, args...)
			cmd := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-i", "-", "-f", "rawvideo", "-pix_fmt", "rgb24", "-")
			cmd.Stdin = bytes.NewReader(ivf)
			raw, err := cmd.Output()
			if err != nil {
				t.Fatal(err)
			}
			got := make([]byte, 0, n)
			for off := 0; off+w*h*3 <= len(raw); off += w * h * 3 {
				i := off + (50*w+50)*3
				r, b := raw[i], raw[i+2]
				switch {
				case r > 128 && b < 128:
					got = append(got, 'r')
				case b > 128 && r < 128:
					got = append(got, 'b')
				default:
					got = append(got, '.')
				}
			}
			bad := 0
			for i := range min(len(got), len(want)) {
				if got[i] != want[i] {
					bad++
				}
			}
			// One frame of slack per transition for rounding of the
			// source's switch times.
			if len(got) != n || bad > 4 {
				t.Fatalf("%s: %d of %d frames wrong\nwant %s\ngot  %s", desc, bad, n, want, got)
			}
		})
	}
}

func TestSVTVersionPlan(t *testing.T) {
	banner := "Svt[info]: -------------------------------------------\n" +
		"Svt[info]: SVT [version]:\tSVT-AV1 Encoder Lib v2.3.0\nSvt[info]: SVT [build]  :\tGCC 15.2.0\t 64 bit\n"
	if v, text, ok := parseSVTVersion(banner); !ok || v != [3]int{2, 3, 0} || text != "2.3.0" {
		t.Fatalf("parse: %v %q %v", v, text, ok)
	}
	if v, _, ok := parseSVTVersion("SVT-AV1 Encoder Lib v3.1.2-14-gdeadbeef"); !ok || v != [3]int{3, 1, 2} {
		t.Fatalf("parse suffixed: %v %v", v, ok)
	}
	if _, _, ok := parseSVTVersion("no banner here"); ok {
		t.Fatal("parsed a version out of nothing")
	}
	// Versions whose real-time presets lose screen updates get the slow
	// preset; so does an unknown version.
	cases := []struct {
		v    [3]int
		slow bool
	}{
		{[3]int{0, 9, 1}, false}, {[3]int{1, 7, 0}, false}, {[3]int{2, 0, 0}, false}, {[3]int{2, 1, 2}, false},
		{[3]int{2, 2, 0}, true}, {[3]int{2, 3, 0}, true}, {[3]int{3, 0, 2}, true},
		{[3]int{3, 1, 0}, false}, {[3]int{3, 1, 2}, false}, {[3]int{4, 0, 0}, false}, {[3]int{4, 3, 0}, false},
	}
	for _, c := range cases {
		if got := svtPlanFor(c.v, true).slow(); got != c.slow {
			t.Errorf("SVT-AV1 %v: slow=%v, want %v", c.v, got, c.slow)
		}
	}
	if !svtPlanFor([3]int{}, false).slow() {
		t.Error("unknown version must use the safe preset")
	}
	p := encParams{fps: 60, bitrate: 8_000_000, gop: 120, pixels: 1920 * 1080}
	preset := func(pl svtPlan) string { return pl.args(p)[3] }
	if preset(svtPlan{}) != "13" || preset(svtPlan{maxPreset: svtSafePreset}) != "7" {
		t.Fatalf("presets %s / %s", preset(svtPlan{}), preset(svtPlan{maxPreset: svtSafePreset}))
	}
	// rtc=1 re-enables the broken shortcut on 3.1-3.x: never pass it.
	if a := strings.Join(svtPlan{}.args(p), " "); strings.Contains(a, "rtc") || !strings.Contains(a, "pred-struct=1") || !strings.Contains(a, "scm=1") {
		t.Fatalf("svt args %q", a)
	}
}
