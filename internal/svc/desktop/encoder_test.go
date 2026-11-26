package desktop

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeFFmpeg writes a stand-in ffmpeg that lists the given encoders, lets a
// test encode succeed for those in works, and prints SVT-AV1's banner for
// svtVersion ("" prints none).
func fakeFFmpeg(t *testing.T, listed, works []string, svtVersion string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}
	var list strings.Builder
	for _, e := range listed {
		list.WriteString(" V....D " + e + " some encoder\n")
	}
	script := "#!/bin/sh\n" +
		"case \"$*\" in *-encoders*) cat <<'LIST'\nEncoders:\n ------\n" + list.String() + "LIST\nexit 0;; esac\n" +
		"enc=; prev=\nfor a in \"$@\"; do [ \"$prev\" = -c:v ] && enc=$a; prev=$a; done\n" +
		"case \" " + strings.Join(works, " ") + " \" in *\" $enc \"*) ;; *) echo \"$enc: cannot initialise\" >&2; exit 1;; esac\n"
	if svtVersion != "" {
		script += "[ \"$enc\" = libsvtav1 ] && printf 'Svt[info]: SVT [version]:\\tSVT-AV1 Encoder Lib v" + svtVersion + "\\n' >&2\n"
	}
	script += "exit 0\n"
	path := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSelectEncoderBySVTVersion(t *testing.T) {
	all := []string{"av1_nvenc", "libsvtav1", "libaom-av1"}
	sw := []string{"libsvtav1", "libaom-av1"}
	p := encParams{fps: 60, bitrate: 8_000_000, gop: 120, pixels: 1920 * 1080}
	cases := []struct {
		name          string
		listed, works []string
		svt, forced   string
		want, preset  string
		warn          string // substring of the warning, "" for none
	}{
		{"hardware first", all, all, "2.3.0", "", "av1_nvenc", "", ""},
		{"svt 1.7 fast", all, sw, "1.7.0", "", "libsvtav1", "13", ""},
		{"svt 2.3 demoted below libaom", all, sw, "2.3.0", "", "libaom-av1", "", "using libaom-av1 instead"},
		{"svt 3.0 demoted below libaom", all, sw, "3.0.2", "", "libaom-av1", "", "SVT-AV1 3.0.2"},
		{"svt 2.3 alone runs slow preset", []string{"libsvtav1"}, sw, "2.3.0", "", "libsvtav1", "7", "slow preset 7"},
		{"svt 2.3 forced runs slow preset", all, sw, "2.3.0", "libsvtav1", "libsvtav1", "7", "slow preset 7"},
		{"svt 2.3, libaom broken", all, []string{"libsvtav1"}, "2.3.0", "", "libsvtav1", "7", "slow preset 7"},
		{"svt 3.1 fast", all, sw, "3.1.2", "", "libsvtav1", "13", ""},
		{"svt 4.3 fast", all, sw, "4.3.0", "", "libsvtav1", "13", ""},
		{"svt unknown version demoted", all, sw, "", "", "libaom-av1", "", "SVT-AV1 (unknown version)"},
		{"svt 2.3 never probed when x264 works", append([]string{"libx264"}, sw...), append([]string{"libx264"}, sw...), "2.3.0", "", "libx264", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, err := selectEncoder(context.Background(), fakeFFmpeg(t, c.listed, c.works, c.svt), encRequest{forced: c.forced})
			if err != nil {
				t.Fatal(err)
			}
			if e.name != c.want {
				t.Fatalf("selected %s, want %s", e.name, c.want)
			}
			if (c.warn == "") != (e.warning == "") || !strings.Contains(e.warning, c.warn) {
				t.Errorf("warning %q, want it to contain %q", e.warning, c.warn)
			}
			if c.preset != "" {
				if args := e.args(p); args[3] != c.preset {
					t.Errorf("preset %s, want %s (%v)", args[3], c.preset, args)
				}
			}
			if c.want == "libsvtav1" && e.version != c.svt {
				t.Errorf("version %q, want %q", e.version, c.svt)
			}
		})
	}
	if _, err := selectEncoder(context.Background(), fakeFFmpeg(t, all, nil, ""), encRequest{}); err == nil || !strings.Contains(err.Error(), "no working AV1 or H.264 encoder") {
		t.Fatalf("nothing works: %v", err)
	}
}

// TestSelectCodec covers the choice between AV1 and H.264: hardware AV1
// first, then H.264 (hardware, then libx264), software AV1 only for a
// viewer that decodes only AV1 or a forced AV1, never a codec the viewer
// cannot decode.
func TestSelectCodec(t *testing.T) {
	everything := []string{"av1_nvenc", "libsvtav1", "libaom-av1", "h264_nvenc", "h264_vaapi", "libx264", "libopenh264"}
	sw := []string{"libsvtav1", "libaom-av1", "libx264", "libopenh264"}
	both, av1, h264 := []string{"av1", "h264"}, []string{"av1"}, []string{"h264"}
	cases := []struct {
		name   string
		works  []string
		svt    string
		req    encRequest
		want   string
		warn   string // substring of the warning, "" for none
		errHas string
	}{
		{"gpu av1 wins", everything, "1.7.0", encRequest{decode: both}, "av1_nvenc", "", ""},
		{"gpu av1 wins over gpu h264 for an unknown viewer", everything, "1.7.0", encRequest{}, "av1_nvenc", "", ""},
		{"viewer without av1 gets gpu h264", everything, "1.7.0", encRequest{decode: h264}, "h264_nvenc", "", ""},
		{"x264 before fast svt", sw, "1.7.0", encRequest{decode: both}, "libx264", "", ""},
		{"gpu h264 before fast svt", append([]string{"h264_vaapi"}, sw...), "4.3.0", encRequest{decode: both}, "h264_vaapi", "", ""},
		{"affected svt: x264, no svt warning", sw, "2.3.0", encRequest{decode: both}, "libx264", "", ""},
		{"affected svt, unknown viewer: x264", sw, "2.3.0", encRequest{}, "libx264", "", ""},
		{"av1-only viewer, fast svt", sw, "4.3.0", encRequest{decode: av1}, "libsvtav1", "", ""},
		{"av1-only viewer, affected svt: libaom", sw, "2.3.0", encRequest{decode: av1}, "libaom-av1", "SVT-AV1 2.3.0", ""},
		{"forced av1, fast svt", sw, "1.7.0", encRequest{codec: "av1", decode: both}, "libsvtav1", "", ""},
		{"forced av1, affected svt: libaom", sw, "2.3.0", encRequest{codec: "av1", decode: both}, "libaom-av1", "using libaom-av1 instead", ""},
		{"forced av1, affected svt, no libaom: slow svt", []string{"libsvtav1", "libx264"}, "2.3.0", encRequest{codec: "av1"}, "libsvtav1", "slow preset 7", ""},
		{"forced h264 on a gpu av1 host", everything, "1.7.0", encRequest{codec: "h264", decode: both}, "h264_nvenc", "", ""},
		{"no h264 at all: fast svt", []string{"libsvtav1", "libaom-av1"}, "1.7.0", encRequest{decode: both}, "libsvtav1", "", ""},
		{"no h264, affected svt: libaom with warning", []string{"libsvtav1", "libaom-av1"}, "2.3.0", encRequest{decode: both}, "libaom-av1", "SVT-AV1 2.3.0", ""},
		{"openh264 only as last resort", []string{"libaom-av1", "libopenh264"}, "", encRequest{decode: both}, "libaom-av1", "", ""},
		{"openh264 when it is all there is", []string{"libopenh264"}, "", encRequest{decode: both}, "libopenh264", "", ""},
		{"viewer decodes h264 only, host has av1 only", []string{"libaom-av1"}, "", encRequest{decode: h264}, "", "", "no working H.264 encoder"},
		{"forced codec the viewer cannot decode", sw, "1.7.0", encRequest{codec: "av1", decode: h264}, "", "", "cannot decode"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, err := selectEncoder(context.Background(), fakeFFmpeg(t, everything, c.works, c.svt), c.req)
			if c.errHas != "" {
				if err == nil || !strings.Contains(err.Error(), c.errHas) {
					t.Fatalf("error %v, want %q", err, c.errHas)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if e.name != c.want {
				t.Fatalf("selected %s, want %s", e.name, c.want)
			}
			if (c.warn == "") != (e.warning == "") || !strings.Contains(e.warning, c.warn) {
				t.Errorf("warning %q, want it to contain %q", e.warning, c.warn)
			}
		})
	}
}

// Above 60 fps each further frame counts half.
func TestAutoBitrateHighFrameRate(t *testing.T) {
	px := 1920 * 1080
	if got, want := autoBitrate(px, 60, true), int64(float64(px)*60*0.12); got != want {
		t.Fatalf("1080p60 fast: %d, want %d", got, want)
	}
	if got, want := autoBitrate(px, 120, true), int64(float64(px)*90*0.12); got != want {
		t.Fatalf("1080p120 fast: %d, want %d", got, want)
	}
	if a, b := autoBitrate(px, 120, false), autoBitrate(px, 144, false); b <= a {
		t.Fatalf("144 fps (%d) not above 120 fps (%d)", b, a)
	}
	if got := autoBitrate(3840*2160, 144, true); got != maxAutoBitrate {
		t.Fatalf("4K144 fast: %d, want the cap", got)
	}
}
