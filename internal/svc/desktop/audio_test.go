package desktop

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/peer/peertest"
)

// syntheticAudio makes the host capture a 440 Hz sine from ffmpeg's lavfi
// instead of a sound server, paced at real time like a capture device.
func syntheticAudio(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	ff, _ := ffmpegPath()
	if !ffmpegHasEncoder(ff, "libopus") {
		t.Skip("ffmpeg has no libopus")
	}
	old := setAudioSources(func(context.Context, string) ([]audioSource, string) {
		return []audioSource{{desc: "synthetic sine", args: []string{"-re", "-readrate_initial_burst", "0", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000"}}}, ""
	})
	t.Cleanup(func() { setAudioSources(old) })
}

// oggOpus wraps raw Opus packets (20 ms each) in an Ogg stream, so that
// ffmpeg can decode what the host sent.
func oggOpus(packets [][]byte, frameMS int) []byte {
	var out bytes.Buffer
	crcTable := func() [256]uint32 {
		var t [256]uint32
		for i := range t {
			r := uint32(i) << 24
			for j := 0; j < 8; j++ {
				if r&0x80000000 != 0 {
					r = r<<1 ^ 0x04c11db7
				} else {
					r <<= 1
				}
			}
			t[i] = r
		}
		return t
	}()
	var seq uint32
	page := func(typ byte, granule int64, pkt []byte) {
		var segs []byte
		for n := len(pkt); ; n -= 255 {
			if n >= 255 {
				segs = append(segs, 255)
				continue
			}
			segs = append(segs, byte(n))
			break
		}
		h := make([]byte, 27)
		copy(h, "OggS")
		h[5] = typ
		binary.LittleEndian.PutUint64(h[6:], uint64(granule))
		binary.LittleEndian.PutUint32(h[14:], 0x1234)
		binary.LittleEndian.PutUint32(h[18:], seq)
		seq++
		h[26] = byte(len(segs))
		b := append(append(h, segs...), pkt...)
		var crc uint32
		for _, c := range b {
			crc = crc<<8 ^ crcTable[byte(crc>>24)^c]
		}
		binary.LittleEndian.PutUint32(b[22:], crc)
		out.Write(b)
	}
	head := []byte("OpusHead\x01\x02\x00\x00\x80\xbb\x00\x00\x00\x00\x00")
	page(2, 0, head)
	page(0, 0, append([]byte("OpusTags\x00\x00\x00\x00"), 0, 0, 0, 0))
	for i, p := range packets {
		page(0, int64(i+1)*int64(frameMS)*48, p)
	}
	return out.Bytes()
}

// decodePCM decodes an Ogg Opus file to mono float samples at 48 kHz.
func decodePCM(t *testing.T, ogg []byte) []float32 {
	t.Helper()
	dir := t.TempDir()
	in := filepath.Join(dir, "a.opus")
	if err := os.WriteFile(in, ogg, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-i", in, "-ac", "1", "-ar", "48000", "-f", "f32le", "-").Output()
	if err != nil {
		t.Fatalf("ffmpeg could not decode the Opus packets the host sent: %v", err)
	}
	s := make([]float32, len(out)/4)
	for i := range s {
		s[i] = math.Float32frombits(binary.LittleEndian.Uint32(out[4*i:]))
	}
	return s
}

// toneLevel is the amplitude of the freq component in s (Goertzel) and the
// overall RMS.
func toneLevel(s []float32, freq float64) (tone, rms float64) {
	w := 2 * math.Pi * freq / 48000
	var re, im, sq float64
	for i, v := range s {
		re += float64(v) * math.Cos(w*float64(i))
		im += float64(v) * math.Sin(w*float64(i))
		sq += float64(v) * float64(v)
	}
	n := float64(len(s))
	return 2 * math.Hypot(re, im) / n, math.Sqrt(sq / n)
}

func TestAudioClock(t *testing.T) {
	c := audioClock{frame: 20 * time.Millisecond}
	t0 := time.Unix(1000, 0)
	// Packets arrive every 20 ms with up to 15 ms of delay jitter.
	var last time.Time
	for i := 0; i < 500; i++ {
		jitter := time.Duration((i*7)%16) * time.Millisecond
		got := c.stamp(t0.Add(time.Duration(i)*20*time.Millisecond + jitter))
		if i > 0 && got.Sub(last) != 20*time.Millisecond && i > 40 {
			// After the minimum has settled, packets are exactly a frame
			// apart: jitter does not reach the timestamps.
			if d := got.Sub(last); d < 19*time.Millisecond || d > 21*time.Millisecond {
				t.Fatalf("packet %d: %v after the previous", i, d)
			}
		}
		last = got
	}
	// A long stall restarts the estimate instead of dragging captured
	// times behind real time.
	at := t0.Add(10 * time.Second)
	first := c.stamp(at)
	second := c.stamp(at.Add(20 * time.Millisecond))
	if d := at.Sub(first); d < 0 || d > 100*time.Millisecond || second.Sub(first) != 20*time.Millisecond {
		t.Fatalf("after a stall the clock reads %v behind real time, %v between packets", d, second.Sub(first))
	}
}

func TestAudioOptions(t *testing.T) {
	o := AudioOptions{}
	if err := o.normalize(); err != nil || o.Bitrate != defaultAudioBPS || o.FrameMS != 20 {
		t.Fatalf("defaults: %+v %v", o, err)
	}
	for _, bad := range []AudioOptions{{Bitrate: 100}, {Bitrate: 10_000_000}, {FrameMS: 7}, {FrameMS: -20}} {
		if err := bad.normalize(); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func TestOggReader(t *testing.T) {
	// A packet spanning pages (255-byte segments) and two packets on one
	// page.
	big := bytes.Repeat([]byte{7}, 600)
	small := []byte{1, 2, 3}
	stream := oggOpus([][]byte{big, small, small}, 20)
	r := newOggReader(bytes.NewReader(stream))
	var got [][]byte
	for {
		p, err := r.next()
		if err != nil {
			break
		}
		got = append(got, p)
	}
	if len(got) != 5 || !bytes.Equal(got[2], big) || !bytes.Equal(got[3], small) {
		t.Fatalf("got %d packets: %q", len(got), got)
	}
	if _, err := newOggReader(strings.NewReader("not ogg at all, definitely not ogg")).next(); err == nil {
		t.Fatal("garbage accepted")
	}
}

func TestAudioDeviceParsers(t *testing.T) {
	avf := `[AVFoundation indev @ 0x7f8] AVFoundation video devices:
[AVFoundation indev @ 0x7f8] [0] FaceTime HD Camera
[AVFoundation indev @ 0x7f8] [1] Capture screen 0
[AVFoundation indev @ 0x7f8] AVFoundation audio devices:
[AVFoundation indev @ 0x7f8] [0] MacBook Pro Microphone
[AVFoundation indev @ 0x7f8] [1] BlackHole 2ch
`
	devs := parseAVFoundationAudio(avf)
	if len(devs) != 2 || devs[1].index != 1 || devs[1].name != "BlackHole 2ch" || isLoopbackName(devs[0].name) || !isLoopbackName(devs[1].name) {
		t.Fatalf("avfoundation: %+v", devs)
	}
	oldFmt := `[dshow @ 0000] DirectShow video devices (some may be both video and audio devices)
[dshow @ 0000]  "Integrated Camera"
[dshow @ 0000]     Alternative name "@device_pnp_x"
[dshow @ 0000] DirectShow audio devices
[dshow @ 0000]  "Microphone (Realtek Audio)"
[dshow @ 0000]     Alternative name "@device_cm_y"
[dshow @ 0000]  "virtual-audio-capturer"
[dshow @ 0000]     Alternative name "@device_sw_z"
`
	newFmt := `[dshow @ 0000] "Integrated Camera" (video)
[dshow @ 0000]   Alternative name "@device_pnp_x"
[dshow @ 0000] "Stereo Mix (Realtek Audio)" (audio)
[dshow @ 0000]   Alternative name "@device_cm_y"
[dshow @ 0000] "Microphone (Realtek Audio)" (audio)
[dshow @ 0000] "Elgato" (video, audio)
`
	if n := parseDshowAudio(oldFmt); len(n) != 2 || n[1] != "virtual-audio-capturer" {
		t.Fatalf("dshow old format: %q", n)
	}
	if n := parseDshowAudio(newFmt); len(n) != 2 || n[0] != "Stereo Mix (Realtek Audio)" {
		t.Fatalf("dshow new format: %q", n)
	}
	devices := ` D  lavfi           Libavfilter virtual input device
  E opengl          OpenGL output
 DE pulse           Pulse audio output
 D  x11grab         X11 screen capture, using XCB
`
	if !listsInputDevice(devices, "pulse") || !listsInputDevice(devices, "x11grab") || listsInputDevice(devices, "opengl") || listsInputDevice(devices, "wasapi") {
		t.Fatal("device list parsing")
	}
}

// The host captures, encodes Opus and the client receives packets that an
// independent decoder turns back into the tone, with sane capture times.
func TestAudioSession(t *testing.T) {
	syntheticAudio(t)
	client, _ := peertest.Pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	a, err := OpenAudio(ctx, client, AudioOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if h := a.Hello(); h.Codec != "opus" || h.SampleRate != 48000 || h.Channels != 2 || h.FrameMS != 20 || h.Source != "synthetic sine" {
		t.Fatalf("hello %+v", h)
	}
	var packets [][]byte
	var first AudioPacket
	var last AudioPacket
	for len(packets) < 150 { // 3 s
		p, err := a.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(packets) == 0 {
			first = p
		} else if p.Seq != last.Seq+1 {
			t.Fatalf("sequence jumped from %d to %d", last.Seq, p.Seq)
		}
		if lag := p.Received.Sub(p.Capture); lag < 0 || lag > time.Second {
			t.Fatalf("packet %d: captured %v before it arrived", p.Seq, lag)
		}
		packets = append(packets, p.Data)
		last = p
	}
	// 150 packets of 20 ms span about 3 s of capture time. ffmpeg's -re
	// starts with a half-second burst, which a real capture device does not
	// have and which makes the span shorter here.
	if span := last.Capture.Sub(first.Capture); span < 2300*time.Millisecond || span > 3200*time.Millisecond {
		t.Fatalf("capture times span %v for 3 s of audio", span)
	}
	pcm := decodePCM(t, oggOpus(packets, 20))
	if len(pcm) < 48000*2 {
		t.Fatalf("decoded only %d samples", len(pcm))
	}
	// Skip the first 200 ms (start-up), then the tone must dominate.
	tone, rms := toneLevel(pcm[9600:], 440)
	if tone < 0.1 || tone < 0.9*rms*math.Sqrt2 {
		t.Fatalf("decoded audio is not the 440 Hz tone: tone amplitude %.3f, rms %.3f", tone, rms)
	}
	// Closing the stream stops the capture process.
	a.Close()
}

func TestAudioProbeAndErrors(t *testing.T) {
	syntheticAudio(t)
	client, _ := peertest.Pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h, err := ProbeAudio(ctx, client)
	if err != nil || h.Source != "synthetic sine" {
		t.Fatalf("probe: %+v %v", h, err)
	}
	// A host owner can switch audio off.
	t.Setenv(hostAudioEnv, "off")
	if _, err := OpenAudio(ctx, client, AudioOptions{}); err == nil || !strings.Contains(err.Error(), "switched off") {
		t.Fatalf("audio off: %v", err)
	}
	t.Setenv(hostAudioEnv, "")
	// No capture source: the error says why and what to do.
	setAudioSources(func(context.Context, string) ([]audioSource, string) {
		return nil, "no loopback device (try BlackHole)"
	})
	if _, err := OpenAudio(ctx, client, AudioOptions{}); err == nil || !strings.Contains(err.Error(), "BlackHole") {
		t.Fatalf("no source: %v", err)
	}
	// A source that fails to start reports ffmpeg's reason.
	setAudioSources(func(context.Context, string) ([]audioSource, string) {
		return []audioSource{{desc: "broken", args: []string{"-f", "lavfi", "-i", "nosuchfilter"}}}, ""
	})
	_, err = OpenAudio(ctx, client, AudioOptions{})
	if err == nil || !strings.Contains(err.Error(), "broken") {
		t.Fatalf("broken source: %v", err)
	}
	if _, err := OpenAudio(ctx, client, AudioOptions{Bitrate: 5}); err == nil {
		t.Fatal("bad bitrate accepted")
	}
}
