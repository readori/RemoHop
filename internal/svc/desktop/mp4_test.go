package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// testStream is a real encoded stream as a session would deliver it.
type testStream struct {
	hello  Hello
	frames []*Frame
}

// makeStream encodes n frames at 30 fps with a key frame every gop frames
// and turns them into a Hello and Frames, timestamps starting at start.
func makeStream(t *testing.T, codec string, w, h, n, gop int, start time.Duration) testStream {
	t.Helper()
	src := "testsrc2=s=" + strconv.Itoa(w) + "x" + strconv.Itoa(h) + ":r=30"
	p := encParams{fps: 30, bitrate: 800_000, gop: gop, pixels: w * h}
	st := testStream{hello: Hello{Width: w, Height: h, FPS: 30}}
	pts := func(i int) time.Duration { return start + time.Duration(i)*time.Second/30 }
	if codec == codecH264 {
		args := append([]string{"-pix_fmt", "yuv420p"}, lookupEncoder("libx264").args(p)...)
		fr, err := newFLVReader(bytes.NewReader(encodeFrom(t, "flv", src, n, args...)))
		if err != nil {
			t.Fatal(err)
		}
		var info AVCInfo
		for {
			pkt, err := fr.next()
			if err != nil {
				break
			}
			if pkt.config {
				if info, err = parseAVCC(pkt.data); err != nil {
					t.Fatal(err)
				}
				st.hello.Codec, st.hello.CodecConfig = info.Codec(), pkt.data
				continue
			}
			key, _ := avcIsIDR(pkt.data, info.NALLength)
			st.frames = append(st.frames, &Frame{Seq: uint32(len(st.frames) + 1), Key: key, PTS: pts(len(st.frames)), Data: pkt.data})
		}
		return st
	}
	args := []string{"-pix_fmt", "yuv420p", "-c:v", "libaom-av1", "-usage", "realtime", "-cpu-used", "8", "-lag-in-frames", "0", "-g", strconv.Itoa(gop)}
	ir, err := newIVFReader(bytes.NewReader(encodeFrom(t, "ivf", src, n, args...)))
	if err != nil {
		t.Fatal(err)
	}
	for {
		tu, _, err := ir.next()
		if err != nil {
			break
		}
		info, err := classifyTU(tu, false)
		if err != nil {
			t.Fatal(err)
		}
		if info.seq != nil && st.hello.Codec == "" {
			st.hello.Codec, st.hello.CodecConfig = info.seq.Codec(), info.seq.av1C(info.seqOBU)
		}
		st.frames = append(st.frames, &Frame{Seq: uint32(len(st.frames) + 1), Key: info.key, PTS: pts(len(st.frames)), Data: tu})
	}
	return st
}

type probeResult struct {
	Streams []struct {
		CodecName    string `json:"codec_name"`
		Width        int    `json:"width"`
		Height       int    `json:"height"`
		NbReadFrames string `json:"nb_read_frames"`
		StartTime    string `json:"start_time"`
	} `json:"streams"`
	Format struct {
		Duration   string `json:"duration"`
		FormatName string `json:"format_name"`
	} `json:"format"`
}

// ffprobe decodes the whole file (so nb_read_frames counts frames that
// really decode) and reports what a player would see.
func ffprobe(t *testing.T, path string) (codec string, w, h, frames int, start, duration float64) {
	t.Helper()
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-count_frames", "-show_streams", "-show_format", "-of", "json", path).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", path, err)
	}
	var r probeResult
	if err := json.Unmarshal(out, &r); err != nil || len(r.Streams) != 1 {
		t.Fatalf("ffprobe output: %v %s", err, out)
	}
	if !strings.Contains(r.Format.FormatName, "mp4") {
		t.Fatalf("format %q", r.Format.FormatName)
	}
	s := r.Streams[0]
	frames, _ = strconv.Atoi(s.NbReadFrames)
	start, _ = strconv.ParseFloat(s.StartTime, 64)
	duration, _ = strconv.ParseFloat(r.Format.Duration, 64)
	return s.CodecName, s.Width, s.Height, frames, start, duration
}

// decodeErrors runs the file through ffmpeg's decoder and returns what it
// complained about.
func decodeErrors(t *testing.T, path string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-v", "error", "-xerror", "-i", path, "-f", "null", "-").CombinedOutput()
	if err != nil {
		return err.Error() + ": " + string(out)
	}
	return string(out)
}

func TestRecorder(t *testing.T) {
	for _, tc := range []struct{ codec, name string }{{codecAV1, "av1"}, {codecH264, "h264"}} {
		t.Run(tc.codec, func(t *testing.T) {
			// Timestamps as in a session: far from zero.
			st := makeStream(t, tc.codec, 320, 240, 45, 15, 37*time.Second)
			path := filepath.Join(t.TempDir(), "rec.mp4")
			rec, err := NewRecorder(path, false)
			if err != nil {
				t.Fatal(err)
			}
			if err := rec.SetStream(st.hello); err != nil {
				t.Fatal(err)
			}
			// Recording switched on mid-GOP: the 5 delta frames before
			// the next key frame are left out.
			for _, f := range st.frames[10:] {
				if err := rec.WriteFrame(f); err != nil {
					t.Fatal(err)
				}
			}
			// Complete after every frame: what is on disk before Close
			// already plays, short of the last frame.
			if _, _, _, frames, _, _ := ffprobe(t, path); frames != 29 {
				t.Errorf("before Close: %d frames on disk, want 29", frames)
			}
			if err := rec.Close(); err != nil {
				t.Fatal(err)
			}
			codec, w, h, frames, start, dur := ffprobe(t, path)
			if codec != tc.name || w != 320 || h != 240 {
				t.Fatalf("recorded %s %dx%d", codec, w, h)
			}
			if frames != 30 || rec.Frames() != 30 {
				t.Fatalf("%d frames decode (recorder says %d), want 30", frames, rec.Frames())
			}
			if start != 0 || dur < 0.98 || dur > 1.02 {
				t.Fatalf("starts at %v s, lasts %v s; want 0 and 1.0", start, dur)
			}
			if msg := decodeErrors(t, path); msg != "" {
				t.Fatalf("decoder: %s", msg)
			}
			if runtime.GOOS != "windows" {
				if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
					t.Fatalf("mode %v, want 0600", fi.Mode().Perm())
				}
			}
			if got := rec.Files(); len(got) != 1 || got[0] != path {
				t.Fatalf("files %v", got)
			}
		})
	}
}

// A stream that changes shape mid-recording continues in a second file; an
// encoder restart with the same parameters stays in the first.
func TestRecorderStreamChange(t *testing.T) {
	a := makeStream(t, codecH264, 320, 240, 20, 10, time.Second)
	a2 := makeStream(t, codecH264, 320, 240, 20, 10, 2*time.Second)
	b := makeStream(t, codecAV1, 160, 120, 20, 10, 3*time.Second)
	dir := t.TempDir()
	path := filepath.Join(dir, "session.mp4")
	rec, err := NewRecorder(path, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range []testStream{a, a2, b} {
		if err := rec.SetStream(st.hello); err != nil {
			t.Fatal(err)
		}
		for _, f := range st.frames {
			if err := rec.WriteFrame(f); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}
	second := filepath.Join(dir, "session-2.mp4")
	if got := rec.Files(); len(got) != 2 || got[0] != path || got[1] != second {
		t.Fatalf("files %v", got)
	}
	codec, w, h, frames, _, dur := ffprobe(t, path)
	if codec != "h264" || w != 320 || h != 240 || frames != 40 {
		t.Fatalf("first file: %s %dx%d, %d frames", codec, w, h, frames)
	}
	// Two 20-frame runs one second apart: the gap is kept.
	if dur < 1.6 || dur > 1.75 {
		t.Fatalf("first file lasts %v s, want ~1.67", dur)
	}
	codec, w, h, frames, _, _ = ffprobe(t, second)
	if codec != "av1" || w != 160 || h != 120 || frames != 20 {
		t.Fatalf("second file: %s %dx%d, %d frames", codec, w, h, frames)
	}
	for _, p := range []string{path, second} {
		if msg := decodeErrors(t, p); msg != "" {
			t.Fatalf("%s: decoder: %s", p, msg)
		}
	}
}

func TestRecorderFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.mp4")
	if err := os.WriteFile(path, []byte("precious"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRecorder(path, false); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("overwrote without --force: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "precious" {
		t.Fatal("existing file was touched")
	}
	if _, err := NewRecorder(filepath.Join(dir, "x.mkv"), false); err == nil {
		t.Fatal("accepted a non-mp4 name")
	}
	rec, err := NewRecorder(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
			t.Fatalf("forced file has mode %v", fi.Mode().Perm())
		}
	}
	// Nothing recorded (no key frame ever came): no empty file is left.
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("empty recording left behind: %v", err)
	}
	if len(rec.Files()) != 0 {
		t.Fatalf("files %v", rec.Files())
	}
}
