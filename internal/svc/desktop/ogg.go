package desktop

import (
	"bufio"
	"errors"
	"fmt"
	"io"
)

// oggReader splits an Ogg stream into packets. ffmpeg's ogg muxer is how
// the host gets Opus packets out of an ffmpeg process: it writes one page
// per packet (-page_duration 1) and flushes each, so a packet arrives as
// soon as the encoder has produced it. Pages are not CRC-checked: the
// stream comes from a child process over a pipe, not from the network.
type oggReader struct {
	r       *bufio.Reader
	partial []byte
	queue   [][]byte
}

// maxOggPacket bounds one packet; an Opus packet is at most a few
// kilobytes.
const maxOggPacket = 64 << 10

func newOggReader(r io.Reader) *oggReader { return &oggReader{r: bufio.NewReaderSize(r, 16<<10)} }

var errBadOgg = errors.New("desktop: malformed Ogg stream from ffmpeg")

// next returns the next complete packet.
func (o *oggReader) next() ([]byte, error) {
	for len(o.queue) == 0 {
		if err := o.page(); err != nil {
			return nil, err
		}
	}
	p := o.queue[0]
	o.queue = o.queue[1:]
	return p, nil
}

// page reads one page and queues the packets it completes.
func (o *oggReader) page() error {
	var hdr [27]byte
	if _, err := io.ReadFull(o.r, hdr[:]); err != nil {
		return err
	}
	if string(hdr[:4]) != "OggS" || hdr[4] != 0 {
		return errBadOgg
	}
	continued := hdr[5]&1 != 0
	segs := make([]byte, hdr[26])
	if _, err := io.ReadFull(o.r, segs); err != nil {
		return err
	}
	if !continued && len(o.partial) > 0 {
		o.partial = o.partial[:0] // the stream skipped the rest of a packet
	}
	for _, n := range segs {
		seg := make([]byte, n)
		if _, err := io.ReadFull(o.r, seg); err != nil {
			return err
		}
		o.partial = append(o.partial, seg...)
		if len(o.partial) > maxOggPacket {
			return fmt.Errorf("%w: packet over %d bytes", errBadOgg, maxOggPacket)
		}
		if n < 255 { // a segment shorter than 255 ends the packet
			o.queue = append(o.queue, append([]byte(nil), o.partial...))
			o.partial = o.partial[:0]
		}
	}
	return nil
}
