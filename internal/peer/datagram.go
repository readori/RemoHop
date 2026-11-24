package peer

import (
	"context"
	"errors"
	"sync"
)

// A QUIC connection has a single datagram queue, so services cannot each
// read from it: one would steal the other's packets. Datagrams demultiplexes
// that queue by a one-byte tag chosen by the service.

// Tags in use. A service picks one constant and uses it on both ends.
const (
	DatagramForwardUDP byte = 0x01
	DatagramVPN        byte = 0x02
	DatagramMesh       byte = 0x03 // internal/mesh
)

// ErrNoDatagrams is returned when the session cannot carry datagrams (it does
// not run directly on QUIC, or the peer did not enable them). Callers fall
// back to a stream.
var ErrNoDatagrams = errors.New("peer: session does not support datagrams")

// datagramQueue bounds what a slow reader can make us hold. Datagrams are
// unreliable by contract, so the overflow is dropped.
const datagramQueue = 512

// DatagramChannel is one service's share of the session's datagrams.
type DatagramChannel struct {
	c   *Conn
	tag byte
	in  chan []byte
}

type datagramMux struct {
	mu       sync.Mutex
	started  bool
	channels map[byte]*DatagramChannel
}

// Datagrams returns the channel for tag, creating it on first use. Every call
// with the same tag returns the same channel.
func (c *Conn) Datagrams(tag byte) (*DatagramChannel, error) {
	q := QUICConn(c)
	if q == nil || !q.ConnectionState().SupportsDatagrams.Remote {
		return nil, ErrNoDatagrams
	}
	m := &c.dgrams
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.channels == nil {
		m.channels = map[byte]*DatagramChannel{}
	}
	ch := m.channels[tag]
	if ch == nil {
		ch = &DatagramChannel{c: c, tag: tag, in: make(chan []byte, datagramQueue)}
		m.channels[tag] = ch
	}
	if !m.started {
		m.started = true
		go c.datagramLoop()
	}
	return ch, nil
}

func (c *Conn) datagramLoop() {
	q := QUICConn(c)
	ctx := c.Context()
	for {
		p, err := q.ReceiveDatagram(ctx)
		if err != nil {
			return
		}
		if len(p) == 0 {
			continue
		}
		c.dgrams.mu.Lock()
		ch := c.dgrams.channels[p[0]]
		c.dgrams.mu.Unlock()
		if ch == nil {
			continue // nobody asked for this tag
		}
		select {
		case ch.in <- p[1:]:
		default: // reader is behind: drop, as a network would
		}
	}
}

// Send transmits one datagram. It fails with *quic.DatagramTooLargeError when
// p does not fit the path, which is the caller's cue to use a stream instead.
func (d *DatagramChannel) Send(p []byte) error {
	q := QUICConn(d.c)
	if q == nil {
		return ErrNoDatagrams
	}
	buf := make([]byte, 1+len(p))
	buf[0] = d.tag
	copy(buf[1:], p)
	return q.SendDatagram(buf)
}

// Receive returns the next datagram for this channel. The slice is the
// caller's to keep.
func (d *DatagramChannel) Receive(ctx context.Context) ([]byte, error) {
	select {
	case p := <-d.in:
		return p, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-d.c.Done():
		return nil, d.c.Context().Err()
	}
}

// Overhead is the number of bytes Send adds to a payload.
const DatagramOverhead = 1
