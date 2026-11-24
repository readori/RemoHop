package desktop

import (
	"bytes"
	"math/rand"
	"testing"
)

// TestParseRealH264 reads real libx264 output in the FLV framing the host
// uses: the avcC record first, the picture size out of its SPS (including
// sizes that need cropping), an IDR at every GOP start and nowhere else.
func TestParseRealH264(t *testing.T) {
	for _, size := range []struct{ w, h int }{{352, 288}, {1366, 768}, {1920, 1080}, {322, 242}} {
		src := "testsrc2=s=" + itoa3(size.w) + "x" + itoa3(size.h) + ":r=30"
		e := lookupEncoder("libx264")
		args := append([]string{"-pix_fmt", "yuv420p"}, e.args(encParams{fps: 30, bitrate: 1_000_000, gop: 10, pixels: size.w * size.h})...)
		data := encodeFrom(t, "flv", src, 25, args...)
		fr, err := newFLVReader(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		var info *AVCInfo
		var keys []int
		n := 0
		for {
			pkt, err := fr.next()
			if err != nil {
				break
			}
			if pkt.config {
				i, err := parseAVCC(pkt.data)
				if err != nil {
					t.Fatalf("avcC: %v", err)
				}
				info = &i
				continue
			}
			if info == nil {
				t.Fatal("frame before the configuration record")
			}
			idr, err := avcIsIDR(pkt.data, info.NALLength)
			if err != nil {
				t.Fatalf("frame %d: %v", n, err)
			}
			if idr {
				keys = append(keys, n)
			}
			n++
		}
		if info == nil || info.Width != size.w || info.Height != size.h {
			t.Fatalf("%dx%d: parsed %+v", size.w, size.h, info)
		}
		if !codecRE.MatchString(info.Codec()) || info.Codec()[:7] != "avc1.64" || info.NALLength != 4 {
			t.Fatalf("codec %s, NAL length %d", info.Codec(), info.NALLength)
		}
		if n != 25 || len(keys) != 3 || keys[0] != 0 || keys[1] != 10 || keys[2] != 20 {
			t.Fatalf("%d frames, IDR at %v, want 25 and [0 10 20]", n, keys)
		}
		t.Logf("%dx%d: %s", info.Width, info.Height, info.Codec())
	}
}

func itoa3(v int) string {
	var b []byte
	for ; v > 0; v /= 10 {
		b = append([]byte{byte('0' + v%10)}, b...)
	}
	return string(b)
}

// Malformed H.264/FLV input must produce errors, not panics.
func TestParseH264Garbage(t *testing.T) {
	e := lookupEncoder("libx264")
	args := append([]string{"-pix_fmt", "yuv420p"}, e.args(encParams{fps: 30, bitrate: 500_000, gop: 10, pixels: 352 * 288})...)
	data := encodeFrom(t, "flv", "testsrc2=s=352x288:r=30", 3, args...)
	fr, _ := newFLVReader(bytes.NewReader(data))
	cfg, err := fr.next()
	if err != nil || !cfg.config {
		t.Fatalf("first packet: %+v %v", cfg.config, err)
	}
	frame, err := fr.next()
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(1))
	mutate := func(src []byte) []byte {
		b := append([]byte(nil), src[:rng.Intn(len(src)+1)]...)
		for j := rng.Intn(4); j > 0 && len(b) > 0; j-- {
			b[rng.Intn(len(b))] ^= byte(1 << rng.Intn(8))
		}
		return b
	}
	for i := 0; i < 20000; i++ {
		parseAVCC(mutate(cfg.data))
		avcIsIDR(mutate(frame.data), 1+rng.Intn(4))
		r, err := newFLVReader(bytes.NewReader(mutate(data[:min(len(data), 600)])))
		for err == nil {
			_, err = r.next()
		}
	}
	for _, bad := range [][]byte{nil, []byte("FLV"), []byte("XXX\x01\x01\x00\x00\x00\x09\x00\x00\x00\x00")} {
		if _, err := newFLVReader(bytes.NewReader(bad)); err == nil {
			t.Errorf("accepted bad flv header %q", bad)
		}
	}
}

func TestFLVTime(t *testing.T) {
	const span = int64(1) << 31
	now := int64(1_795_057_508_694) // a wall clock in ms
	for _, ago := range []int64{0, 3, 250, 9_000} {
		got, ok := flvTime(uint32((now-ago)%span), now)
		if !ok || got != (now-ago)*1000 {
			t.Errorf("%d ms ago: %d %v", ago, got, ok)
		}
	}
	// Across a wrap of the 31 bits.
	wrap := now&^(span-1) + span + 5
	if got, ok := flvTime(uint32((wrap-20)%span), wrap); !ok || got != (wrap-20)*1000 {
		t.Errorf("across the wrap: %d %v", got, ok)
	}
	// Timestamps that are not the wall clock (a device counting from 0).
	if _, ok := flvTime(1234, now); ok {
		t.Error("took a relative timestamp for wall clock")
	}
	if _, ok := flvTime(uint32((now+5000)%span), now); ok {
		t.Error("took a timestamp from the future for wall clock")
	}
}
