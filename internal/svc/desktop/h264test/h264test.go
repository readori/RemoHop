// Package h264test makes synthetic H.264 elementary streams in Annex B
// form for tests of the provided-screen path (internal/svc/desktop's
// Screen and the mobile host mode): a real SPS that parses to the size
// asked for, a PPS, and slice NAL units whose payload is filler. Nothing
// here decodes as a picture; it exercises framing, parameter sets, key
// frame detection and the Hello, which is what the host side looks at.
package h264test

// SPS returns a Constrained Baseline sequence parameter set NAL unit
// (without start code) for a w x h picture (both even), level 3.1.
func SPS(w, h int) []byte {
	mbW, mbH := (w+15)/16, (h+15)/16
	var b bits
	b.u(8, 66)   // profile_idc: Baseline
	b.u(8, 0xc0) // constraint_set0 and set1: Constrained Baseline
	b.u(8, 31)   // level_idc
	b.ue(0)      // seq_parameter_set_id
	b.ue(0)      // log2_max_frame_num_minus4
	b.ue(2)      // pic_order_cnt_type
	b.ue(1)      // max_num_ref_frames
	b.u(1, 0)    // gaps_in_frame_num_value_allowed_flag
	b.ue(uint(mbW - 1))
	b.ue(uint(mbH - 1))
	b.u(1, 1) // frame_mbs_only_flag
	b.u(1, 1) // direct_8x8_inference_flag
	cropR, cropB := (mbW*16-w)/2, (mbH*16-h)/2
	if cropR > 0 || cropB > 0 {
		b.u(1, 1)
		b.ue(0)
		b.ue(uint(cropR))
		b.ue(0)
		b.ue(uint(cropB))
	} else {
		b.u(1, 0)
	}
	b.u(1, 0) // vui_parameters_present_flag
	b.trailing()
	return append([]byte{0x67}, escape(b.out)...)
}

// PPS is a picture parameter set NAL unit (without start code).
func PPS() []byte { return []byte{0x68, 0xce, 0x3c, 0x80} }

// IDR is an IDR slice NAL unit with n bytes of filler payload.
func IDR(n int) []byte { return slice(0x65, n) }

// Delta is a non-IDR slice NAL unit with n bytes of filler payload.
func Delta(n int) []byte { return slice(0x41, n) }

func slice(hdr byte, n int) []byte {
	out := []byte{hdr, 0x88}
	for i := 0; i < n; i++ {
		out = append(out, byte(0x10+i%0xe0))
	}
	return out
}

// AnnexB joins NAL units with four-byte start codes.
func AnnexB(nals ...[]byte) []byte {
	var out []byte
	for _, n := range nals {
		out = append(out, 0, 0, 0, 1)
		out = append(out, n...)
	}
	return out
}

// Config is a codec-config buffer as MediaCodec emits it: SPS and PPS.
func Config(w, h int) []byte { return AnnexB(SPS(w, h), PPS()) }

// bits is an MSB-first bit writer.
type bits struct {
	out  []byte
	nbit int
}

func (b *bits) u(n int, v uint) {
	for i := n - 1; i >= 0; i-- {
		if b.nbit%8 == 0 {
			b.out = append(b.out, 0)
		}
		if v>>uint(i)&1 == 1 {
			b.out[len(b.out)-1] |= 0x80 >> uint(b.nbit%8)
		}
		b.nbit++
	}
}

// ue writes an unsigned Exp-Golomb code.
func (b *bits) ue(v uint) {
	v++
	n := 0
	for x := v; x > 1; x >>= 1 {
		n++
	}
	b.u(n, 0)
	b.u(n+1, v)
}

func (b *bits) trailing() {
	b.u(1, 1)
	for b.nbit%8 != 0 {
		b.u(1, 0)
	}
}

// escape inserts emulation prevention bytes.
func escape(rbsp []byte) []byte {
	var out []byte
	zeros := 0
	for _, c := range rbsp {
		if zeros >= 2 && c <= 3 {
			out = append(out, 3)
			zeros = 0
		}
		out = append(out, c)
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return out
}
