package desktop

import (
	"errors"
	"fmt"
)

// AV1 OBU types (AV1 spec section 6.2.2) that the frame classifier cares
// about.
const (
	obuSequenceHeader   = 1
	obuTemporalDelim    = 2
	obuFrameHeader      = 3
	obuFrame            = 6
	obuRedundantFrameHd = 7
)

var errBadOBU = errors.New("desktop: malformed AV1 OBU")

// obu is one parsed OBU inside a temporal unit.
type obu struct {
	typ     int
	start   int // offset of the OBU header in the temporal unit
	end     int // offset just past the OBU
	payload []byte
}

// splitOBUs parses a temporal unit in the low-overhead bitstream format
// (every OBU carries obu_has_size_field, as IVF/WebCodecs require). The last
// OBU may omit its size field, in which case it extends to the end.
func splitOBUs(tu []byte) ([]obu, error) {
	var out []obu
	for off := 0; off < len(tu); {
		start := off
		h := tu[off]
		if h&0x80 != 0 {
			return nil, errBadOBU // forbidden bit
		}
		typ := int(h>>3) & 0x0f
		ext := h&0x04 != 0
		hasSize := h&0x02 != 0
		off++
		if ext {
			off++
		}
		if off > len(tu) {
			return nil, errBadOBU
		}
		size := len(tu) - off
		if hasSize {
			v, n, err := leb128(tu[off:])
			if err != nil {
				return nil, err
			}
			off += n
			if v > uint64(len(tu)-off) {
				return nil, errBadOBU
			}
			size = int(v)
		}
		out = append(out, obu{typ: typ, start: start, end: off + size, payload: tu[off : off+size]})
		off += size
	}
	return out, nil
}

func leb128(b []byte) (uint64, int, error) {
	var v uint64
	for i := 0; i < 8; i++ {
		if i >= len(b) {
			return 0, 0, errBadOBU
		}
		v |= uint64(b[i]&0x7f) << (7 * i)
		if b[i]&0x80 == 0 {
			return v, i + 1, nil
		}
	}
	return 0, 0, errBadOBU
}

// bitReader reads MSB-first bit fields as the AV1 spec's f(n).
type bitReader struct {
	b   []byte
	pos int // in bits
	err error
}

func (r *bitReader) f(n int) uint64 {
	var v uint64
	for i := 0; i < n; i++ {
		if r.pos >= len(r.b)*8 {
			r.err = errBadOBU
			return 0
		}
		bit := (r.b[r.pos/8] >> (7 - uint(r.pos%8))) & 1
		v = v<<1 | uint64(bit)
		r.pos++
	}
	return v
}

func (r *bitReader) flag() bool { return r.f(1) == 1 }

// uvlc is the spec's variable-length unsigned code.
func (r *bitReader) uvlc() uint64 {
	lead := 0
	for !r.flag() {
		if r.err != nil {
			return 0
		}
		lead++
		if lead >= 32 {
			return (1 << 32) - 1
		}
	}
	return r.f(lead) + (1 << lead) - 1
}

// SequenceInfo is what the viewer needs from an AV1 sequence header.
type SequenceInfo struct {
	Profile  int
	Level    int // seq_level_idx of operating point 0
	Tier     int // 0 main, 1 high
	BitDepth int
	Mono     bool
	// SubX, SubY and ChromaPos are the chroma subsampling (both true for
	// 4:2:0) and chroma_sample_position.
	SubX, SubY bool
	ChromaPos  int
	Width      int // max_frame_width
	Height     int // max_frame_height
	// ReducedStill is reduced_still_picture_header; it changes how frame
	// headers are laid out.
	ReducedStill bool
}

// Codec is the WebCodecs / ISO-BMFF codec string, av01.P.LLT.DD.
func (s SequenceInfo) Codec() string {
	tier := "M"
	if s.Tier == 1 {
		tier = "H"
	}
	return fmt.Sprintf("av01.%d.%02d%s.%02d", s.Profile, s.Level, tier, s.BitDepth)
}

// av1C builds the MP4 AV1CodecConfigurationRecord (AV1-ISOBMFF 2.3.3) for a
// stream with this sequence header, given as a complete OBU.
func (s SequenceInfo) av1C(seqOBU []byte) []byte {
	bit := func(b bool, shift uint) byte {
		if b {
			return 1 << shift
		}
		return 0
	}
	out := []byte{
		0x81, // marker, version 1
		byte(s.Profile)<<5 | byte(s.Level),
		byte(s.Tier)<<7 | bit(s.BitDepth > 8, 6) | bit(s.BitDepth == 12, 5) | bit(s.Mono, 4) |
			bit(s.SubX, 3) | bit(s.SubY, 2) | byte(s.ChromaPos),
		0, // no initial presentation delay
	}
	return append(out, seqOBU...)
}

// parseSequenceHeader decodes sequence_header_obu (AV1 spec 5.5) up to and
// including color_config.
func parseSequenceHeader(p []byte) (SequenceInfo, error) {
	r := &bitReader{b: p}
	var s SequenceInfo
	s.Profile = int(r.f(3))
	r.f(1) // still_picture
	s.ReducedStill = r.flag()
	if s.ReducedStill {
		s.Level = int(r.f(5))
	} else {
		decoderModelInfo := false
		bufferDelayLen := 0
		if r.flag() { // timing_info_present_flag
			r.f(32) // num_units_in_display_tick
			r.f(32) // time_scale
			if r.flag() {
				r.uvlc() // num_ticks_per_picture_minus_1
			}
			decoderModelInfo = r.flag()
			if decoderModelInfo {
				bufferDelayLen = int(r.f(5)) + 1
				r.f(32) // num_units_in_decoding_tick
				r.f(5)  // buffer_removal_time_length_minus_1
				r.f(5)  // frame_presentation_time_length_minus_1
			}
		}
		initialDisplayDelay := r.flag()
		ops := int(r.f(5)) + 1
		for i := 0; i < ops; i++ {
			r.f(12) // operating_point_idc
			lvl := int(r.f(5))
			tier := 0
			if lvl > 7 {
				tier = int(r.f(1))
			}
			if i == 0 {
				s.Level, s.Tier = lvl, tier
			}
			if decoderModelInfo && r.flag() {
				r.f(bufferDelayLen) // decoder_buffer_delay
				r.f(bufferDelayLen) // encoder_buffer_delay
				r.f(1)              // low_delay_mode_flag
			}
			if initialDisplayDelay && r.flag() {
				r.f(4)
			}
		}
	}
	wbits := int(r.f(4)) + 1
	hbits := int(r.f(4)) + 1
	s.Width = int(r.f(wbits)) + 1
	s.Height = int(r.f(hbits)) + 1
	orderHint := false
	if !s.ReducedStill {
		if r.flag() { // frame_id_numbers_present_flag
			r.f(4)
			r.f(3)
		}
	}
	r.f(3) // use_128x128_superblock, enable_filter_intra, enable_intra_edge_filter
	if !s.ReducedStill {
		r.f(4) // interintra, masked compound, warped motion, dual filter
		orderHint = r.flag()
		if orderHint {
			r.f(2) // jnt_comp, ref_frame_mvs
		}
		forceSCT := uint64(2)
		if !r.flag() { // seq_choose_screen_content_tools
			forceSCT = r.f(1)
		}
		if forceSCT > 0 && !r.flag() { // seq_choose_integer_mv
			r.f(1)
		}
		if orderHint {
			r.f(3)
		}
	}
	r.f(3) // superres, cdef, restoration
	// color_config
	high := r.flag()
	s.BitDepth = 8
	switch {
	case s.Profile == 2 && high:
		if r.flag() {
			s.BitDepth = 12
		} else {
			s.BitDepth = 10
		}
	case high:
		s.BitDepth = 10
	}
	if s.Profile != 1 {
		s.Mono = r.flag()
	}
	// The rest of color_config gives the chroma layout the MP4 av1C box
	// records.
	matrixRGB := false
	if r.flag() { // color_description_present_flag
		cp, tc, mc := r.f(8), r.f(8), r.f(8)
		matrixRGB = cp == 1 && tc == 13 && mc == 0 // sRGB in 4:4:4
	}
	switch {
	case s.Mono:
		r.f(1) // color_range
		s.SubX, s.SubY = true, true
	case matrixRGB:
	default:
		r.f(1) // color_range
		switch {
		case s.Profile == 0:
			s.SubX, s.SubY = true, true
		case s.Profile == 2 && s.BitDepth == 12:
			if s.SubX = r.flag(); s.SubX {
				s.SubY = r.flag()
			}
		case s.Profile == 2:
			s.SubX = true
		}
		if s.SubX && s.SubY {
			s.ChromaPos = int(r.f(2))
		}
	}
	if r.err != nil {
		return SequenceInfo{}, fmt.Errorf("desktop: truncated AV1 sequence header")
	}
	if s.Profile > 2 || s.Width <= 0 || s.Height <= 0 {
		return SequenceInfo{}, fmt.Errorf("desktop: unsupported AV1 sequence header (profile %d, %dx%d)", s.Profile, s.Width, s.Height)
	}
	return s, nil
}

// tuInfo classifies one temporal unit.
type tuInfo struct {
	key    bool          // contains a shown key frame
	seq    *SequenceInfo // sequence header carried by this TU, if any
	seqOBU []byte        // the raw sequence header OBU, if any
	tdEnd  int           // offset just past a leading temporal delimiter (0 if none)
}

// classifyTU finds the sequence header and decides whether the temporal unit
// starts with a key frame, by reading the first frame header's
// show_existing_frame and frame_type fields. reducedStill comes from the
// stream's sequence header (the TU's own one takes precedence).
func classifyTU(tu []byte, reducedStill bool) (tuInfo, error) {
	obus, err := splitOBUs(tu)
	if err != nil {
		return tuInfo{}, err
	}
	var info tuInfo
	for i, o := range obus {
		switch o.typ {
		case obuTemporalDelim:
			if i == 0 {
				info.tdEnd = o.end
			}
		case obuSequenceHeader:
			s, err := parseSequenceHeader(o.payload)
			if err != nil {
				return tuInfo{}, err
			}
			info.seq = &s
			info.seqOBU = tu[o.start:o.end]
			reducedStill = s.ReducedStill
		case obuFrameHeader, obuFrame:
			if reducedStill {
				info.key = true
				return info, nil
			}
			r := &bitReader{b: o.payload}
			showExisting := r.flag()
			frameType := r.f(2)
			if r.err != nil {
				return tuInfo{}, errBadOBU
			}
			// KEY_FRAME is 0. A shown-existing key frame is a repeat of
			// an earlier frame and does not reset the decoder.
			info.key = !showExisting && frameType == 0
			return info, nil
		}
	}
	return info, nil
}
