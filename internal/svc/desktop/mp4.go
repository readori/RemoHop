package desktop

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Recorder saves a session's video to MP4 on the client exactly as it
// arrives: the AV1 or H.264 frames are remuxed, never re-encoded.
//
// The file is a fragmented MP4: an ftyp and a moov that describe the one
// video track (sample entry av01 with av1C, or avc1 with avcC) and declare
// no samples, then one moof+mdat fragment per frame carrying its decode
// time, duration, size and sync flag. Nothing has to be patched when the
// recording ends, so a file is complete after every frame: stopping,
// exiting and being killed all leave a playable recording that at worst
// lacks its last frame.
//
// A recording starts at the first key frame. Timestamps are the frames'
// presentation times (the host's capture clock), so the recording keeps the
// session's real pace, including pauses where frames were dropped. When the
// stream changes shape mid-recording (another resolution or codec: a
// quality preset, another monitor), an MP4 track cannot follow, so the
// recorder finishes the file and continues in a new one next to it, named
// NAME-2.mp4, NAME-3.mp4, ...; an encoder restart with unchanged parameters
// stays in the same file.
type Recorder struct {
	mu    sync.Mutex
	base  string // path of the first file
	force bool

	f        *os.File
	files    []string
	stream   *recStream // nil until the first Hello
	wroteHdr bool
	waitKey  bool
	havePTS  bool
	firstPTS time.Duration
	pending  *recSample
	lastDur  time.Duration
	seq      uint32
	frames   int
	err      error
	closed   bool
}

type recStream struct {
	h264          bool
	width, height int
	config        []byte
	fps           int
}

func (a *recStream) same(b *recStream) bool {
	return a.h264 == b.h264 && a.width == b.width && a.height == b.height && bytes.Equal(a.config, b.config)
}

type recSample struct {
	pts  time.Duration
	key  bool
	data []byte
}

// mp4Timescale is the track's time unit: microseconds, the unit of Frame.PTS.
const mp4Timescale = 1_000_000

// NewRecorder creates the recording file, readable by its owner only. It
// refuses to replace an existing file unless force is set.
func NewRecorder(path string, force bool) (*Recorder, error) {
	if !strings.EqualFold(filepath.Ext(path), ".mp4") {
		return nil, fmt.Errorf("recording %q: the file name must end in .mp4", path)
	}
	r := &Recorder{base: path, force: force, waitKey: true}
	if err := r.open(path); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Recorder) open(path string) error {
	if r.force {
		// Replace rather than truncate in place: never write through a
		// link someone planted, and never keep another file's mode.
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("recording: %w", err)
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("recording: %s already exists (use --force to replace it)", path)
		}
		return fmt.Errorf("recording: %w", err)
	}
	r.f = f
	r.files = append(r.files, path)
	r.wroteHdr, r.waitKey, r.havePTS, r.pending, r.seq = false, true, false, nil, 0
	return nil
}

// Files lists the files written so far, in order.
func (r *Recorder) Files() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.files...)
}

// Frames is the number of frames recorded so far.
func (r *Recorder) Frames() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.frames
}

// SetStream tells the recorder the parameters of the frames that follow;
// call it with the session's Hello, and again with every new one.
func (r *Recorder) SetStream(h Hello) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errors.New("recording: closed")
	}
	if r.err != nil {
		return r.err
	}
	if len(h.CodecConfig) == 0 || h.Width <= 0 || h.Height <= 0 || h.Width > 65535 || h.Height > 65535 {
		return r.fail(errors.New("recording: the stream has no codec configuration to record with"))
	}
	next := &recStream{h264: h.IsH264(), width: h.Width, height: h.Height, config: bytes.Clone(h.CodecConfig), fps: h.FPS}
	switch {
	case r.stream == nil || !r.wroteHdr:
		r.stream = next
	case r.stream.same(next):
		// An encoder restart: same track, resume at its key frame.
		r.stream.fps = next.fps
	default:
		if err := r.finishFile(); err != nil {
			return r.fail(err)
		}
		ext := filepath.Ext(r.base)
		if err := r.open(fmt.Sprintf("%s-%d%s", strings.TrimSuffix(r.base, ext), len(r.files)+1, ext)); err != nil {
			return r.fail(err)
		}
		r.stream = next
	}
	r.waitKey = true
	return nil
}

// WriteFrame records one frame. Frames before the first key frame (and
// after a stream change, up to its key frame) are skipped.
func (r *Recorder) WriteFrame(f *Frame) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errors.New("recording: closed")
	}
	if r.err != nil {
		return r.err
	}
	if r.stream == nil {
		return nil
	}
	if r.waitKey && !f.Key {
		return nil
	}
	data := f.Data
	if !r.stream.h264 {
		var err error
		if data, err = stripTemporalDelimiters(f.Data); err != nil {
			return nil // not a frame a player could use; leave it out
		}
	}
	if len(data) == 0 {
		return nil
	}
	if !r.wroteHdr {
		if _, err := r.f.Write(mp4Header(r.stream)); err != nil {
			return r.fail(err)
		}
		r.wroteHdr = true
	}
	if !r.havePTS {
		r.firstPTS, r.havePTS = f.PTS, true
	}
	if r.pending != nil {
		d := f.PTS - r.pending.pts
		if d <= 0 {
			d = time.Microsecond // timestamps must advance
		}
		if err := r.writeSample(r.pending, d); err != nil {
			return r.fail(err)
		}
		r.lastDur = d
	}
	r.waitKey = false
	r.pending = &recSample{pts: f.PTS, key: f.Key, data: bytes.Clone(data)}
	return nil
}

func (r *Recorder) fail(err error) error {
	if r.err == nil {
		if !strings.HasPrefix(err.Error(), "recording:") {
			err = fmt.Errorf("recording: %w", err)
		}
		r.err = err
	}
	return r.err
}

// finishFile writes the frame still waiting for its duration and closes the
// current file.
func (r *Recorder) finishFile() error {
	var err error
	if r.pending != nil {
		d := r.lastDur
		if d <= 0 && r.stream != nil && r.stream.fps > 0 {
			d = time.Second / time.Duration(r.stream.fps)
		}
		if d <= 0 {
			d = time.Second / 30
		}
		err = r.writeSample(r.pending, d)
		r.pending = nil
	}
	if cerr := r.f.Close(); err == nil {
		err = cerr
	}
	return err
}

// Close finishes the recording. A recording that never saw a key frame
// leaves an empty file, which is removed.
func (r *Recorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return r.err
	}
	r.closed = true
	err := r.finishFile()
	if !r.wroteHdr {
		last := r.files[len(r.files)-1]
		os.Remove(last)
		r.files = r.files[:len(r.files)-1]
	}
	if r.err != nil {
		return r.err
	}
	if err != nil {
		r.err = fmt.Errorf("recording: %w", err)
	}
	return r.err
}

// writeSample appends one moof+mdat fragment with a single write.
func (r *Recorder) writeSample(s *recSample, dur time.Duration) error {
	r.seq++
	flags := uint32(0x01010000) // depends on others, not a sync sample
	if s.key {
		flags = 0x02000000 // depends on nothing
	}
	var trun bytes.Buffer
	// duration, size and flags per sample, and the data offset.
	writeU32(&trun, 0x000701, 1, 0 /* data offset, patched below */, uint32(dur.Microseconds()), uint32(len(s.data)), flags)
	var tfdt bytes.Buffer
	writeU32(&tfdt, 0x01000000) // version 1: 64-bit time
	binary.Write(&tfdt, binary.BigEndian, uint64((s.pts - r.firstPTS).Microseconds()))
	moof := box("moof",
		box("mfhd", u32s(0, r.seq)),
		box("traf",
			box("tfhd", u32s(0x020000 /* offsets are relative to the moof */, 1)),
			box("tfdt", tfdt.Bytes()),
			box("trun", trun.Bytes()),
		),
	)
	// The sample starts right after the mdat header that follows the moof;
	// the offset field is the third word of the trun payload, and the trun
	// is the last box of the moof.
	binary.BigEndian.PutUint32(moof[len(moof)-trun.Len()+8:], uint32(len(moof)+8))
	out := make([]byte, 0, len(moof)+8+len(s.data))
	out = append(out, moof...)
	out = binary.BigEndian.AppendUint32(out, uint32(8+len(s.data)))
	out = append(out, "mdat"...)
	out = append(out, s.data...)
	if _, err := r.f.Write(out); err != nil {
		return err
	}
	r.frames++
	return nil
}

func box(typ string, parts ...[]byte) []byte {
	n := 8
	for _, p := range parts {
		n += len(p)
	}
	out := make([]byte, 0, n)
	out = binary.BigEndian.AppendUint32(out, uint32(n))
	out = append(out, typ...)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func u32s(v ...uint32) []byte {
	out := make([]byte, 0, 4*len(v))
	for _, x := range v {
		out = binary.BigEndian.AppendUint32(out, x)
	}
	return out
}

func writeU32(b *bytes.Buffer, v ...uint32) { b.Write(u32s(v...)) }

// mp4Header is the ftyp and moov of a recording of stream s.
func mp4Header(s *recStream) []byte {
	entryType, configType, brand := "av01", "av1C", "av01"
	if s.h264 {
		entryType, configType, brand = "avc1", "avcC", "avc1"
	}
	ftyp := box("ftyp", []byte("iso5"), u32s(512), []byte("iso5iso6mp41"+brand))

	matrix := u32s(0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000)
	var mvhd bytes.Buffer
	writeU32(&mvhd, 0, 0, 0, mp4Timescale, 0 /* duration: in the fragments */, 0x00010000 /* rate 1.0 */)
	mvhd.Write([]byte{1, 0, 0, 0}) // volume 1.0, reserved
	writeU32(&mvhd, 0, 0)
	mvhd.Write(matrix)
	writeU32(&mvhd, 0, 0, 0, 0, 0, 0, 2 /* next track id */)

	var tkhd bytes.Buffer
	writeU32(&tkhd, 0x000003 /* enabled, in movie */, 0, 0, 1 /* track id */, 0, 0 /* duration */, 0, 0, 0, 0)
	tkhd.Write(matrix)
	writeU32(&tkhd, uint32(s.width)<<16, uint32(s.height)<<16)

	var mdhd bytes.Buffer
	writeU32(&mdhd, 0, 0, 0, mp4Timescale, 0, 0x55c40000 /* language "und" */)

	var hdlr bytes.Buffer
	writeU32(&hdlr, 0, 0)
	hdlr.WriteString("vide")
	writeU32(&hdlr, 0, 0, 0)
	hdlr.WriteString("RemoHop\x00")

	var entry bytes.Buffer
	entry.Write([]byte{0, 0, 0, 0, 0, 0, 0, 1}) // reserved, data reference index
	entry.Write(make([]byte, 16))
	entry.Write([]byte{byte(s.width >> 8), byte(s.width), byte(s.height >> 8), byte(s.height)})
	writeU32(&entry, 0x00480000, 0x00480000, 0) // 72 dpi, reserved
	entry.Write([]byte{0, 1})                   // one frame per sample
	entry.Write(make([]byte, 32))               // compressor name
	entry.Write([]byte{0, 0x18, 0xff, 0xff})    // depth 24, pre_defined
	entry.Write(box(configType, s.config))

	stbl := box("stbl",
		box("stsd", u32s(0, 1), box(entryType, entry.Bytes())),
		box("stts", u32s(0, 0)),
		box("stsc", u32s(0, 0)),
		box("stsz", u32s(0, 0, 0)),
		box("stco", u32s(0, 0)),
	)
	minf := box("minf",
		box("vmhd", u32s(1, 0, 0)),
		box("dinf", box("dref", u32s(0, 1), box("url ", u32s(1)))),
		stbl,
	)
	moov := box("moov",
		box("mvhd", mvhd.Bytes()),
		box("trak",
			box("tkhd", tkhd.Bytes()),
			box("mdia", box("mdhd", mdhd.Bytes()), box("hdlr", hdlr.Bytes()), minf),
		),
		box("mvex", box("trex", u32s(0, 1, 1, 0, 0, 0))),
	)
	return append(ftyp, moov...)
}

// stripTemporalDelimiters returns an AV1 temporal unit as an MP4 sample:
// the same OBUs without temporal delimiters, which AV1-in-ISOBMFF leaves
// out.
func stripTemporalDelimiters(tu []byte) ([]byte, error) {
	obus, err := splitOBUs(tu)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(tu))
	for _, o := range obus {
		if o.typ != obuTemporalDelim {
			out = append(out, tu[o.start:o.end]...)
		}
	}
	return out, nil
}
