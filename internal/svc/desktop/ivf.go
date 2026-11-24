package desktop

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// IVF is the container ffmpeg writes to its stdout: a 32-byte file header
// followed by frames, each a 12-byte header (little-endian payload size and
// 64-bit pts) and one AV1 temporal unit. It needs no seeking and no index,
// so it streams through a pipe with no muxing delay.

// maxTU bounds a single temporal unit from the encoder. A 4K intra frame at
// generous quality is a few MB; anything far beyond that is a broken stream.
const maxTU = 12 << 20

type ivfHeader struct {
	FourCC  string
	Width   int
	Height  int
	TimeDen uint32 // timebase denominator
	TimeNum uint32 // timebase numerator
}

type ivfReader struct {
	r   *bufio.Reader
	hdr ivfHeader
}

func newIVFReader(r io.Reader) (*ivfReader, error) {
	br := bufio.NewReaderSize(r, 256<<10)
	var b [32]byte
	if _, err := io.ReadFull(br, b[:]); err != nil {
		return nil, fmt.Errorf("ivf header: %w", err)
	}
	if string(b[0:4]) != "DKIF" {
		return nil, errors.New("ivf: bad signature")
	}
	hl := binary.LittleEndian.Uint16(b[6:8])
	if hl < 32 || hl > 1024 {
		return nil, errors.New("ivf: bad header length")
	}
	if _, err := br.Discard(int(hl) - 32); err != nil {
		return nil, err
	}
	h := ivfHeader{
		FourCC:  string(b[8:12]),
		Width:   int(binary.LittleEndian.Uint16(b[12:14])),
		Height:  int(binary.LittleEndian.Uint16(b[14:16])),
		TimeDen: binary.LittleEndian.Uint32(b[16:20]),
		TimeNum: binary.LittleEndian.Uint32(b[20:24]),
	}
	if h.FourCC != "AV01" {
		return nil, fmt.Errorf("ivf: codec %q, want AV01", h.FourCC)
	}
	if h.TimeDen == 0 || h.TimeNum == 0 {
		return nil, errors.New("ivf: zero timebase")
	}
	return &ivfReader{r: br, hdr: h}, nil
}

// next returns the next temporal unit (freshly allocated) and its pts in
// timebase units.
func (ir *ivfReader) next() ([]byte, int64, error) {
	var b [12]byte
	if _, err := io.ReadFull(ir.r, b[:]); err != nil {
		return nil, 0, err
	}
	n := binary.LittleEndian.Uint32(b[0:4])
	if n == 0 || n > maxTU {
		return nil, 0, fmt.Errorf("ivf: bad frame size %d", n)
	}
	pts := int64(binary.LittleEndian.Uint64(b[4:12]))
	buf := make([]byte, n)
	if _, err := io.ReadFull(ir.r, buf); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return nil, 0, err
	}
	return buf, pts, nil
}

// micros converts a pts in the stream timebase to microseconds.
func (ir *ivfReader) micros(pts int64) int64 {
	num, den := int64(ir.hdr.TimeNum), int64(ir.hdr.TimeDen)
	// Split to avoid overflow for wallclock-based pts in fine timebases.
	return pts/den*num*1e6 + pts%den*num*1e6/den
}
