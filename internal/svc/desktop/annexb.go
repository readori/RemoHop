package desktop

import "bytes"

// H.264 in Annex B form, as hardware encoders outside ffmpeg deliver it
// (Android's MediaCodec, Apple's VideoToolbox through a small shim): NAL
// units separated by start codes, with the parameter sets (SPS, PPS) in
// band, in a codec-config buffer of their own or in front of key frames.
// The desktop protocol carries what ffmpeg's FLV gives: an avcC record in
// the Hello and access units as length-prefixed NAL units. The helpers here
// convert.

const (
	nalPPS = 8
	nalAUD = 9
	// maxParamSet bounds an SPS or PPS (real ones are tens of bytes).
	maxParamSet = 1 << 10
)

// splitAnnexB returns the NAL units of b, without their start codes
// (00 00 01 or 00 00 00 01). Bytes before the first start code are
// ignored; empty units are skipped.
func splitAnnexB(b []byte) [][]byte {
	var out [][]byte
	start := -1
	i := 0
	for i+2 < len(b) {
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
			if start >= 0 {
				out = appendNAL(out, b[start:i])
			}
			i += 3
			start = i
			continue
		}
		i++
	}
	if start >= 0 && start < len(b) {
		out = appendNAL(out, b[start:])
	}
	return out
}

// appendNAL appends n with the trailing zero bytes trimmed (they belong to
// the next unit's four-byte start code, or are trailing_zero_8bits).
func appendNAL(out [][]byte, n []byte) [][]byte {
	n = bytes.TrimRight(n, "\x00")
	if len(n) == 0 {
		return out
	}
	return append(out, n)
}

// buildAVCC makes an AVCDecoderConfigurationRecord (ISO 14496-15 5.2.4.1)
// with four-byte NAL lengths from one SPS and one PPS.
func buildAVCC(sps, pps []byte) []byte {
	b := make([]byte, 0, 11+len(sps)+len(pps))
	b = append(b, 1, sps[1], sps[2], sps[3], 0xff, 0xe1, byte(len(sps)>>8), byte(len(sps)))
	b = append(b, sps...)
	b = append(b, 1, byte(len(pps)>>8), byte(len(pps)))
	return append(b, pps...)
}

// lengthPrefixed joins nals into one sample of four-byte length-prefixed
// NAL units, the form the Hello's avcC announces.
func lengthPrefixed(nals [][]byte) []byte {
	n := 0
	for _, u := range nals {
		n += 4 + len(u)
	}
	out := make([]byte, 0, n)
	for _, u := range nals {
		l := len(u)
		out = append(out, byte(l>>24), byte(l>>16), byte(l>>8), byte(l))
		out = append(out, u...)
	}
	return out
}

// annexBUnit is one encoder output buffer taken apart: its parameter sets,
// if any, and the rest of its NAL units as a sample.
type annexBUnit struct {
	sps, pps []byte
	sample   []byte // length-prefixed, nil when the buffer held only parameter sets
	key      bool   // the sample holds an IDR slice
}

// parseAnnexB takes apart one encoder output buffer. Access unit
// delimiters are dropped (the framing marks access units already), and so
// are the parameter sets, which travel in the avcC record.
func parseAnnexB(b []byte) (annexBUnit, error) {
	var u annexBUnit
	var rest [][]byte
	for _, n := range splitAnnexB(b) {
		switch n[0] & 0x1f {
		case nalSPS:
			if len(n) < 4 || len(n) > maxParamSet {
				return u, errBadH264
			}
			u.sps = n
		case nalPPS:
			if len(n) > maxParamSet {
				return u, errBadH264
			}
			u.pps = n
		case nalAUD:
		default:
			if n[0]&0x1f == nalIDR {
				u.key = true
			}
			rest = append(rest, n)
		}
	}
	if len(rest) > 0 {
		u.sample = lengthPrefixed(rest)
	}
	return u, nil
}
