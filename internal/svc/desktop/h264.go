package desktop

import (
	"bufio"
	"errors"
	"fmt"
	"io"
)

// H.264 leaves ffmpeg in an FLV container. FLV suits a pipe for the same
// reason IVF does for AV1: every tag announces its size, so a frame is
// complete the moment it is written (MPEG-TS would only reveal a frame's end
// when the next one starts, a frame of latency). It also hands over exactly
// what the consumers want: the avcC configuration record (the WebCodecs
// "description" and the MP4 sample entry) and frames as length-prefixed NAL
// units, the format both take.
//
// FLV timestamps are 31-bit milliseconds. With -copyts they are the low bits
// of the wall-clock capture time, which flvTime completes from the host's
// clock.

const (
	flvTagVideo  = 9
	flvCodecAVC  = 7
	flvAVCConfig = 0 // AVCPacketType: sequence header (avcC)
	flvAVCNALU   = 1
	// maxFLVSkip bounds a non-video tag (the onMetaData script tag).
	maxFLVSkip = 1 << 20

	nalIDR = 5
	nalSPS = 7
)

var errBadH264 = errors.New("desktop: malformed H.264 stream")

// flvPacket is one video tag: either the configuration record or a frame.
type flvPacket struct {
	config bool
	data   []byte // avcC, or the access unit as length-prefixed NAL units
	ts     uint32 // 31-bit milliseconds
}

type flvReader struct{ r *bufio.Reader }

func newFLVReader(r io.Reader) (*flvReader, error) {
	br := bufio.NewReaderSize(r, 256<<10)
	var b [9]byte
	if _, err := io.ReadFull(br, b[:]); err != nil {
		return nil, fmt.Errorf("flv header: %w", err)
	}
	if string(b[:3]) != "FLV" {
		return nil, errors.New("flv: bad signature")
	}
	off := int(b[5])<<24 | int(b[6])<<16 | int(b[7])<<8 | int(b[8])
	if off < 9 || off > 1024 {
		return nil, errors.New("flv: bad header length")
	}
	// The rest of the header, then PreviousTagSize0.
	if _, err := br.Discard(off - 9 + 4); err != nil {
		return nil, err
	}
	return &flvReader{r: br}, nil
}

// next returns the next AVC video packet, skipping other tags.
func (fr *flvReader) next() (flvPacket, error) {
	for {
		var h [11]byte
		if _, err := io.ReadFull(fr.r, h[:]); err != nil {
			return flvPacket{}, err
		}
		typ := h[0] & 0x1f
		size := int(h[1])<<16 | int(h[2])<<8 | int(h[3])
		ts := uint32(h[4])<<16 | uint32(h[5])<<8 | uint32(h[6]) | uint32(h[7]&0x7f)<<24
		if typ != flvTagVideo {
			if size > maxFLVSkip {
				return flvPacket{}, fmt.Errorf("flv: bad tag size %d", size)
			}
			if _, err := fr.r.Discard(size + 4); err != nil {
				return flvPacket{}, eofIsUnexpected(err)
			}
			continue
		}
		if size < 5 || size > maxTU+5 {
			return flvPacket{}, fmt.Errorf("flv: bad video tag size %d", size)
		}
		body := make([]byte, size)
		if _, err := io.ReadFull(fr.r, body); err != nil {
			return flvPacket{}, eofIsUnexpected(err)
		}
		if _, err := fr.r.Discard(4); err != nil { // PreviousTagSize
			return flvPacket{}, eofIsUnexpected(err)
		}
		if body[0]&0x0f != flvCodecAVC {
			return flvPacket{}, fmt.Errorf("flv: video codec %d, want AVC", body[0]&0x0f)
		}
		// body[2:5] is the composition time offset: zero, there are no
		// B-frames.
		switch body[1] {
		case flvAVCConfig:
			return flvPacket{config: true, data: body[5:], ts: ts}, nil
		case flvAVCNALU:
			return flvPacket{data: body[5:], ts: ts}, nil
		}
		// End-of-sequence and unknown packet types carry no picture.
	}
}

func eofIsUnexpected(err error) error {
	if err == io.EOF {
		return io.ErrUnexpectedEOF
	}
	return err
}

// flvTime completes a 31-bit FLV millisecond timestamp to the Unix time in
// microseconds nearest to nowMS that has those low bits, or returns false if
// that is not close to now (the timestamps are then not wall-clock).
func flvTime(ts uint32, nowMS int64) (int64, bool) {
	const span = int64(1) << 31
	full := nowMS&^(span-1) | int64(ts)
	switch d := full - nowMS; {
	case d > span/2:
		full -= span
	case d < -span/2:
		full += span
	}
	// Up to 50 ms ahead still counts: timestamps rounded to a frame period
	// may lie a fraction of a frame in the future.
	if d := nowMS - full; d < -50 || d > 10_000 {
		return 0, false
	}
	return full * 1000, true
}

// AVCInfo is what the viewer needs from an avcC record.
type AVCInfo struct {
	Profile, Compat, Level byte
	Width, Height          int
	// NALLength is the size in bytes of the length prefix of each NAL
	// unit in a sample.
	NALLength int
}

// Codec is the WebCodecs / RFC 6381 codec string, avc1.PPCCLL.
func (a AVCInfo) Codec() string {
	return fmt.Sprintf("avc1.%02x%02x%02x", a.Profile, a.Compat, a.Level)
}

// h264Levels are H.264's levels (Table A-1) with the largest macroblock
// rate and frame size of each.
var h264Levels = []struct {
	idc       byte
	mbps, mfs int
}{
	{31, 108000, 3600}, {32, 216000, 5120}, {40, 245760, 8192}, {42, 522240, 8704},
	{50, 589824, 22080}, {51, 983040, 36864}, {52, 2073600, 36864},
	{60, 4177920, 139264}, {61, 8355840, 139264}, {62, 16711680, 139264},
}

// h264LevelFor is the lowest level whose limits admit w x h at fps
// (0 when even the highest does not).
func h264LevelFor(w, h, fps int) byte {
	fs := ((w + 15) / 16) * ((h + 15) / 16)
	for _, l := range h264Levels {
		if fs <= l.mfs && fs*fps <= l.mbps {
			return l.idc
		}
	}
	return 0
}

// parseAVCC reads an AVCDecoderConfigurationRecord (ISO 14496-15 5.2.4.1)
// and the picture size from its first SPS.
func parseAVCC(b []byte) (AVCInfo, error) {
	if len(b) < 7 || b[0] != 1 {
		return AVCInfo{}, errBadH264
	}
	info := AVCInfo{Profile: b[1], Compat: b[2], Level: b[3], NALLength: int(b[4]&3) + 1}
	nsps := int(b[5] & 0x1f)
	if nsps == 0 {
		return AVCInfo{}, errBadH264
	}
	if len(b) < 8 {
		return AVCInfo{}, errBadH264
	}
	n := int(b[6])<<8 | int(b[7])
	if n < 4 || 8+n > len(b) {
		return AVCInfo{}, errBadH264
	}
	sps := b[8 : 8+n]
	if sps[0]&0x1f != nalSPS {
		return AVCInfo{}, errBadH264
	}
	w, h, err := parseSPSSize(sps)
	if err != nil {
		return AVCInfo{}, err
	}
	info.Width, info.Height = w, h
	return info, nil
}

// unescapeRBSP removes emulation prevention bytes (00 00 03 -> 00 00).
func unescapeRBSP(b []byte) []byte {
	out := make([]byte, 0, len(b))
	zeros := 0
	for _, c := range b {
		if zeros >= 2 && c == 3 {
			zeros = 0
			continue
		}
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
		out = append(out, c)
	}
	return out
}

// ue and se are Exp-Golomb codes (H.264 9.1).
func (r *bitReader) ue() uint64 {
	lead := 0
	for !r.flag() {
		if r.err != nil {
			return 0
		}
		if lead++; lead > 32 {
			r.err = errBadH264
			return 0
		}
	}
	return r.f(lead) + (1 << lead) - 1
}

func (r *bitReader) se() int64 {
	v := r.ue()
	if v&1 == 1 {
		return int64(v+1) / 2
	}
	return -int64(v / 2)
}

// parseSPSSize decodes seq_parameter_set_data (H.264 7.3.2.1.1) far enough
// to compute the cropped picture size.
func parseSPSSize(nal []byte) (w, h int, err error) {
	r := &bitReader{b: unescapeRBSP(nal[1:])}
	profile := r.f(8)
	r.f(16) // constraint flags, level_idc
	r.ue()  // seq_parameter_set_id
	chroma := uint64(1)
	switch profile {
	case 100, 110, 122, 244, 44, 83, 86, 118, 128, 138, 139, 134, 135:
		chroma = r.ue()
		if chroma == 3 {
			r.f(1) // separate_colour_plane_flag
		}
		r.ue()        // bit_depth_luma_minus8
		r.ue()        // bit_depth_chroma_minus8
		r.f(1)        // qpprime_y_zero_transform_bypass_flag
		if r.flag() { // seq_scaling_matrix_present_flag
			lists := 8
			if chroma == 3 {
				lists = 12
			}
			for i := 0; i < lists; i++ {
				if !r.flag() {
					continue
				}
				size := 16
				if i >= 6 {
					size = 64
				}
				last, next := int64(8), int64(8)
				for j := 0; j < size; j++ {
					if next != 0 {
						next = (last + r.se() + 256) % 256
					}
					if next != 0 {
						last = next
					}
				}
			}
		}
	}
	r.ue()          // log2_max_frame_num_minus4
	switch r.ue() { // pic_order_cnt_type
	case 0:
		r.ue()
	case 1:
		r.f(1)
		r.se()
		r.se()
		n := r.ue()
		if n > 255 {
			return 0, 0, errBadH264
		}
		for i := uint64(0); i < n; i++ {
			r.se()
		}
	}
	r.ue() // max_num_ref_frames
	r.f(1) // gaps_in_frame_num_value_allowed_flag
	mbW := r.ue() + 1
	mapH := r.ue() + 1
	frameMBsOnly := r.f(1)
	if frameMBsOnly == 0 {
		r.f(1) // mb_adaptive_frame_field_flag
	}
	r.f(1) // direct_8x8_inference_flag
	var cl, cr, ct, cb uint64
	if r.flag() { // frame_cropping_flag
		cl, cr, ct, cb = r.ue(), r.ue(), r.ue(), r.ue()
	}
	if r.err != nil || mbW > 1024 || mapH > 1024 {
		return 0, 0, errBadH264
	}
	// Crop units (H.264 table 6-1 and 7.4.2.1.1).
	cropX, cropY := uint64(1), 2-frameMBsOnly
	if chroma == 1 || chroma == 2 {
		cropX = 2
	}
	if chroma == 1 {
		cropY *= 2
	}
	w = int(mbW*16) - int((cl+cr)*cropX)
	h = int(mapH*16*(2-frameMBsOnly)) - int((ct+cb)*cropY)
	if w <= 0 || h <= 0 {
		return 0, 0, errBadH264
	}
	return w, h, nil
}

// avcIsIDR reports whether an access unit of length-prefixed NAL units
// contains an IDR slice, i.e. a point a decoder can start from.
func avcIsIDR(au []byte, nalLength int) (bool, error) {
	idr := false
	for len(au) > 0 {
		if len(au) < nalLength {
			return false, errBadH264
		}
		n := 0
		for _, c := range au[:nalLength] {
			n = n<<8 | int(c)
		}
		au = au[nalLength:]
		if n <= 0 || n > len(au) {
			return false, errBadH264
		}
		if au[0]&0x1f == nalIDR {
			idr = true
		}
		au = au[n:]
	}
	return idr, nil
}
