package desktop

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/wire"
)

// AudioPacket is one Opus packet of the host's audio.
type AudioPacket struct {
	Seq uint32
	// Capture is when the packet's first sample was captured, on the host's
	// clock, the one video frames use (Session.HostToLocal converts it).
	Capture time.Time
	// Received is when it arrived here, on the local clock.
	Received time.Time
	// Data is one Opus packet (48 kHz, stereo).
	Data []byte
}

// AudioSession is an open audio stream from the host.
type AudioSession struct {
	s     peer.Stream
	hello AudioHello

	pkts chan AudioPacket
	done chan struct{}

	mu      sync.Mutex
	err     error
	dropped uint64
	once    sync.Once
}

// audioQueue is how many packets may wait for the application (about a
// second at 20 ms frames) before the oldest are dropped.
const audioQueue = 50

// maxAudioPacket bounds a packet from a host (a 20 ms Opus packet at the
// highest bitrate is under 1.3 KB).
const maxAudioPacket = 4 << 10

// OpenAudio starts audio forwarding on conn and waits for the host to say
// it captures: a host without a way to capture its output, or with audio
// switched off, answers with an error that says why.
func OpenAudio(ctx context.Context, conn *peer.Conn, o AudioOptions) (*AudioSession, error) {
	if err := o.normalize(); err != nil {
		return nil, err
	}
	s, err := conn.Open(ctx, AudioServiceName, o)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, s.Reset)
	typ, payload, err := wire.ReadFrame(s, nil)
	stop()
	if err != nil {
		s.Reset()
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return nil, fmt.Errorf("desktop: audio: %w", err)
	}
	switch typ {
	case frameError:
		var m errorMsg
		json.Unmarshal(payload, &m)
		s.Reset()
		return nil, fmt.Errorf("desktop: audio: host: %s", sanitize(m.Error, 2000))
	case frameHello:
	default:
		s.Reset()
		return nil, fmt.Errorf("desktop: audio: unexpected frame %q", typ)
	}
	var h AudioHello
	if err := json.Unmarshal(payload, &h); err != nil {
		s.Reset()
		return nil, fmt.Errorf("desktop: audio: bad hello: %w", err)
	}
	if h.Codec != "opus" || h.SampleRate != audioSampleRate || h.Channels != audioChannels {
		s.Reset()
		return nil, fmt.Errorf("desktop: audio: host sent unsupported audio %q %d Hz x%d", sanitize(h.Codec, 16), h.SampleRate, h.Channels)
	}
	h.Source = sanitize(h.Source, 200)
	h.Warning = sanitize(h.Warning, 600)
	a := &AudioSession{s: s, hello: h, pkts: make(chan AudioPacket, audioQueue), done: make(chan struct{})}
	go a.readLoop()
	return a, nil
}

// ProbeAudio checks whether the host can forward its audio, without
// starting a stream that keeps running.
func ProbeAudio(ctx context.Context, conn *peer.Conn) (AudioHello, error) {
	a, err := OpenAudio(ctx, conn, AudioOptions{Probe: true})
	if err != nil {
		return AudioHello{}, err
	}
	h := a.Hello()
	a.Close()
	return h, nil
}

// Hello describes the stream.
func (a *AudioSession) Hello() AudioHello { return a.hello }

// Done is closed when the stream ends; Err says why.
func (a *AudioSession) Done() <-chan struct{} { return a.done }

// Err is why the stream ended (nil while it runs).
func (a *AudioSession) Err() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.err
}

// Dropped is how many packets were discarded because the caller was slow.
func (a *AudioSession) Dropped() uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.dropped
}

// Next returns the next packet.
func (a *AudioSession) Next(ctx context.Context) (AudioPacket, error) {
	select {
	case p := <-a.pkts:
		return p, nil
	case <-ctx.Done():
		return AudioPacket{}, ctx.Err()
	case <-a.done:
		select {
		case p := <-a.pkts:
			return p, nil
		default:
		}
		return AudioPacket{}, a.Err()
	}
}

// Close ends the stream; the host stops capturing.
func (a *AudioSession) Close() error {
	a.finish(errors.New("desktop: audio closed"))
	return a.s.Close()
}

func (a *AudioSession) finish(err error) {
	a.once.Do(func() {
		a.mu.Lock()
		a.err = err
		a.mu.Unlock()
		close(a.done)
	})
}

func (a *AudioSession) readLoop() {
	for {
		typ, payload, err := wire.ReadFrame(a.s, nil)
		if err != nil {
			select {
			case <-a.done:
			default:
				a.finish(fmt.Errorf("desktop: audio stream ended: %w", err))
			}
			return
		}
		switch typ {
		case frameError:
			var m errorMsg
			json.Unmarshal(payload, &m)
			a.finish(fmt.Errorf("desktop: audio: host: %s", sanitize(m.Error, 2000)))
			return
		case frameAudio:
			if len(payload) <= audioHeaderLen || len(payload) > audioHeaderLen+maxAudioPacket {
				continue
			}
			p := AudioPacket{
				Seq:      binary.BigEndian.Uint32(payload),
				Capture:  time.UnixMicro(int64(binary.BigEndian.Uint64(payload[4:]))),
				Received: time.Now(),
				Data:     payload[audioHeaderLen:],
			}
			// Newest wins: a consumer that cannot keep up misses the
			// oldest sound rather than hearing everything late.
			for {
				select {
				case a.pkts <- p:
				default:
					select {
					case <-a.pkts:
						a.mu.Lock()
						a.dropped++
						a.mu.Unlock()
					default:
					}
					continue
				}
				break
			}
		}
	}
}
