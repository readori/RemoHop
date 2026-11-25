package desktop

import (
	"strings"
	"testing"
)

// Every way frames reach an encoder converts them with one matrix and range
// and labels the stream with the same: swscale at full range BT.709 (or
// limited, for an encoder that cannot label full range or a pixel rate
// above fullRangeRate), NVENC's own
// conversion of RGB textures as BT.601, Quick Sync's and AMF's GPU chains as
// limited-range BT.709, and the capture hub's NV12 frames as what the hub
// made of them.
func TestArgsColor(t *testing.T) {
	gpu := func(e *encoder, w, h int) (string, bool) {
		if w > 0 {
			return "", false
		}
		if c, ok := map[string]string{"h264_nvenc": "", "h264_amf": "",
			"h264_qsv": "hwmap=derive_device=qsv,format=qsv,vpp_qsv=format=nv12:out_color_matrix=bt709:out_range=tv"}[e.name]; ok {
			return c, true
		}
		return "", false
	}
	cases := []struct {
		enc       string
		spec      captureSpec
		scale     float64
		wantVF    []string // substrings of -vf, in order ("" for no -vf)
		wantColor []string
	}{
		{"libx264", captureSpec{}, 1, []string{"scale=", "out_color_matrix=bt709:out_range=pc", "format=yuv420p"},
			[]string{"-colorspace", "bt709", "-color_primaries", "bt709", "-color_trc", "bt709", "-color_range", "pc"}},
		{"libsvtav1", captureSpec{}, 0.5, []string{"out_color_matrix=bt709:out_range=pc"},
			[]string{"-colorspace", "bt709", "-color_primaries", "bt709", "-color_trc", "bt709", "-color_range", "pc"}},
		{"h264_mf", captureSpec{}, 1, []string{"out_color_matrix=bt709:out_range=tv", "format=nv12"},
			[]string{"-colorspace", "bt709", "-color_primaries", "bt709", "-color_trc", "bt709", "-color_range", "tv"}},
		{"h264_nvenc", captureSpec{filter: "hwdownload,format=bgra", gpu: gpu}, 1, nil,
			[]string{"-colorspace", "bt470bg", "-color_primaries", "bt709", "-color_trc", "bt709", "-color_range", "tv"}},
		{"h264_amf", captureSpec{filter: "hwdownload,format=bgra", gpu: gpu}, 1, nil,
			[]string{"-colorspace", "bt709", "-color_primaries", "bt709", "-color_trc", "bt709", "-color_range", "tv"}},
		{"h264_qsv", captureSpec{filter: "hwdownload,format=bgra", gpu: gpu}, 1, []string{"vpp_qsv=format=nv12:out_color_matrix=bt709:out_range=tv"},
			[]string{"-colorspace", "bt709", "-color_primaries", "bt709", "-color_trc", "bt709", "-color_range", "tv"}},
		// Scaled: the GPU chain is not used, swscale converts.
		{"h264_nvenc", captureSpec{filter: "hwdownload,format=bgra", gpu: gpu}, 0.5, []string{"hwdownload,format=bgra", "out_range=pc", "format=yuv420p"},
			[]string{"-colorspace", "bt709", "-color_primaries", "bt709", "-color_trc", "bt709", "-color_range", "pc"}},
		{"libx264", captureSpec{filter: yuvSource(hubColor)}, 1, []string{"setparams=range=tv:colorspace=bt709", "out_range=pc", "format=yuv420p"},
			[]string{"-color_range", "pc"}},
		// Above fullRangeRate (here 1080p at 60 fps): limited range.
		{"libx264@60", captureSpec{}, 1, []string{"out_color_matrix=bt709:out_range=tv", "format=yuv420p"},
			[]string{"-colorspace", "bt709", "-color_primaries", "bt709", "-color_trc", "bt709", "-color_range", "tv"}},
	}
	for _, c := range cases {
		name, fps := c.enc, 30
		if n, ok := strings.CutSuffix(name, "@60"); ok {
			name, fps = n, 60
		}
		h := &hostSession{enc: lookupEncoder(name), opts: Options{Scale: c.scale}, fps: fps, bitrate: 4_000_000}
		h.spec = captureSpec{width: 1920, height: 1080}
		args := strings.Join(h.argsFor(c.spec), " ")
		vf := ""
		if _, rest, ok := strings.Cut(args, " -vf "); ok {
			vf, _, _ = strings.Cut(rest, " ")
		}
		if c.wantVF == nil && vf != "" {
			t.Errorf("%s: -vf %q, want none", c.enc, vf)
		}
		at := 0
		for _, w := range c.wantVF {
			i := strings.Index(vf[at:], w)
			if i < 0 {
				t.Errorf("%s: -vf %q lacks %q (in order)", c.enc, vf, w)
				break
			}
			at += i + len(w)
		}
		if want := strings.Join(c.wantColor, " "); !strings.Contains(args, want) {
			t.Errorf("%s (scale %g): args %q lack %q", c.enc, c.scale, args, want)
		}
	}
}

// A scaled picture keeps Quick Sync's GPU chain (its video processor
// scales); NVENC's and AMF's chains cannot scale and say so.
func TestScaleGPUChain(t *testing.T) {
	qsv := "hwmap=derive_device=qsv,format=qsv,vpp_qsv=format=nv12:out_color_matrix=bt709:out_range=tv"
	if c, ok := scaleGPUChain(qsv, 1920, 1080); !ok || c != "hwmap=derive_device=qsv,format=qsv,vpp_qsv=w=1920:h=1080:format=nv12:out_color_matrix=bt709:out_range=tv" {
		t.Errorf("qsv scaled: %q %v", c, ok)
	}
	if c, ok := scaleGPUChain(qsv, 0, 0); !ok || c != qsv {
		t.Errorf("qsv native: %q %v", c, ok)
	}
	if _, ok := scaleGPUChain("", 1280, 720); ok {
		t.Error("NVENC's chain claims to scale")
	}
}
