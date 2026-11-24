package vpn

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/readori/RemoHop/internal/peer"
)

// Device is a virtual network interface seen from user space: Read returns
// one IP packet the kernel routed into the interface, Write injects one.
type Device interface {
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	Close() error
	Name() string
}

// Stats counts what a tunnel end has carried and refused.
type Stats struct {
	Sent, Received    atomic.Int64 // packets to and from the peer
	ViaStream         atomic.Int64 // sent packets that did not fit a datagram
	DroppedOut        atomic.Int64 // local packets not allowed into the tunnel
	DroppedIn         atomic.Int64 // peer packets not allowed out of it
	BytesOut, BytesIn atomic.Int64
}

// link carries packets over a session: datagrams when possible, the
// tunnel's stream otherwise.
type link struct {
	d   *peer.DatagramChannel // nil when the session has no datagrams
	s   peer.Stream
	wmu sync.Mutex
}

// datagrams returns the session's datagram channel for tunnels and the
// largest packet it carries right now, or (nil, 0) if the session has no
// datagrams. The channel is shared by everything on the session that uses
// the VPN tag; a session carries one tunnel at a time (the host enforces
// it), so packets need no further header. quic-go has no getter for the
// size limit: an oversized send is refused with the limit in the error and
// sends nothing.
func datagrams(c *peer.Conn) (*peer.DatagramChannel, int) {
	d, err := c.Datagrams(peer.DatagramVPN)
	if err != nil {
		return nil, 0
	}
	var tooLarge *quic.DatagramTooLargeError
	if err := d.Send(make([]byte, 1<<16)); errors.As(err, &tooLarge) {
		if n := int(tooLarge.MaxDatagramPayloadSize) - peer.DatagramOverhead; n > 0 {
			return d, n
		}
	}
	return nil, 0
}

// send transmits one packet. viaStream reports that it went the slow way.
func (l *link) send(pkt []byte, datagrams bool) (viaStream bool, err error) {
	if datagrams && l.d != nil {
		err := l.d.Send(pkt)
		if err == nil {
			return false, nil
		}
		var tooLarge *quic.DatagramTooLargeError
		if !errors.As(err, &tooLarge) && !errors.Is(err, peer.ErrNoDatagrams) {
			return false, err
		}
	}
	buf := make([]byte, 2+len(pkt))
	binary.BigEndian.PutUint16(buf, uint16(len(pkt)))
	copy(buf[2:], pkt)
	l.wmu.Lock()
	_, err = l.s.Write(buf)
	l.wmu.Unlock()
	return true, err
}

// readStream reads one length-prefixed packet from the tunnel's stream.
func (l *link) readStream(buf []byte) ([]byte, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(l.s, hdr[:]); err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint16(hdr[:]))
	if n == 0 {
		return nil, nil // keep-alive
	}
	if _, err := io.ReadFull(l.s, buf[:n]); err != nil {
		return nil, err
	}
	return buf[:n], nil
}

// pump moves packets between dev and the session until either fails or ctx
// ends, and returns why. out filters packets leaving this machine through
// the tunnel, in those arriving from the peer; packets larger than mtu are
// never accepted from the peer.
func pump(ctx context.Context, dev Device, l *link, mtu, mssLimit int, datagrams bool, out, in policy, st *Stats) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	mss := uint16(mssLimit)
	interval, dead := keepAlive, deadAfter
	var heard atomic.Int64
	heard.Store(time.Now().UnixNano())

	deliver := func(pkt []byte) {
		heard.Store(time.Now().UnixNano())
		if pkt == nil {
			return
		}
		h, ok := parse(pkt)
		if !ok || len(pkt) > mtu || !in.allows(h) {
			st.DroppedIn.Add(1)
			return
		}
		clampMSS(pkt, h, mss)
		if _, err := dev.Write(pkt); err != nil {
			st.DroppedIn.Add(1)
			return
		}
		st.Received.Add(1)
		st.BytesIn.Add(int64(len(pkt)))
	}

	var wg sync.WaitGroup
	// local → peer
	wg.Go(func() {
		buf := make([]byte, 65536)
		for {
			n, err := dev.Read(buf)
			if err != nil {
				cancel(err)
				return
			}
			pkt := buf[:n]
			h, ok := parse(pkt)
			if !ok || n > mtu || !out.allows(h) {
				st.DroppedOut.Add(1)
				continue
			}
			clampMSS(pkt, h, mss)
			slow, err := l.send(pkt, datagrams)
			if err != nil {
				cancel(err)
				return
			}
			st.Sent.Add(1)
			st.BytesOut.Add(int64(n))
			if slow {
				st.ViaStream.Add(1)
			}
		}
	})
	// peer → local, stream
	wg.Go(func() {
		buf := make([]byte, 65536)
		for {
			pkt, err := l.readStream(buf)
			if err != nil {
				if err == io.EOF {
					err = errors.New("the other side closed the tunnel")
				}
				cancel(err)
				return
			}
			deliver(pkt)
		}
	})
	// peer → local, datagrams
	if l.d != nil {
		wg.Go(func() {
			for {
				d, err := l.d.Receive(ctx)
				if err != nil {
					cancel(err)
					return
				}
				deliver(d)
			}
		})
	}
	// keep-alive out, liveness in
	wg.Go(func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			if time.Since(time.Unix(0, heard.Load())) > dead {
				cancel(errors.New("the other end of the tunnel stopped answering"))
				return
			}
			l.wmu.Lock()
			_, err := l.s.Write([]byte{0, 0})
			l.wmu.Unlock()
			if err != nil {
				cancel(err)
				return
			}
		}
	})
	<-ctx.Done()
	// Unblock the readers: the device and the stream are ours to end.
	dev.Close()
	l.s.Reset()
	wg.Wait()
	if err := context.Cause(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return ctx.Err()
}
