package web

import (
	"context"
	"encoding/binary"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/svc/desktop"
)

// The host's audio on the desktop WebSocket (see the protocol in
// desktop.go): the page asks with audio{on}; the gateway opens the host's
// audio service (desktop.OpenAudio, which the host allows with the screen
// feature and refuses, saying why, when it cannot capture or its owner
// switched audio off) and answers
//
//	{"type":"audio","on":true,"audio":desktop.AudioHello}, or
//	{"type":"audio","on":false,"error":"..."}
//
// then sends each Opus packet as a binary message: 'A', sequence (4),
// capture time on the gateway clock (8, microseconds), arrival time (8),
// the packet. {"type":"audio_end","error":"..."} says the stream ended by
// itself.

const frameAudioTag = 'A'

const audioHeaderLen = 21

// deskAudioMsg answers an audio request.
type deskAudioMsg struct {
	Type  string              `json:"type"` // "audio" or "audio_end"
	On    bool                `json:"on,omitempty"`
	Audio *desktop.AudioHello `json:"audio,omitempty"`
	Error string              `json:"error,omitempty"`
}

// deskAudio forwards the host's audio to the page while the page wants it.
// Requests are applied in order by one goroutine (run), the latest
// winning, since opening waits on the host.
type deskAudio struct {
	ctx  context.Context
	conn *peer.Conn
	sess *desktop.Session
	c    *wsConn

	want atomic.Bool
	kick chan struct{}

	mu  sync.Mutex
	cur *desktop.AudioSession
}

func newDeskAudio(ctx context.Context, conn *peer.Conn, sess *desktop.Session, c *wsConn) *deskAudio {
	a := &deskAudio{ctx: ctx, conn: conn, sess: sess, c: c, kick: make(chan struct{}, 1)}
	go a.run()
	return a
}

// request asks for audio on or off.
func (a *deskAudio) request(on bool) {
	a.want.Store(on)
	select {
	case a.kick <- struct{}{}:
	default:
	}
}

func (a *deskAudio) run() {
	defer a.set(false)
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-a.kick:
			a.set(a.want.Load())
		}
	}
}

func (a *deskAudio) set(on bool) {
	a.mu.Lock()
	old := a.cur
	a.cur = nil
	a.mu.Unlock()
	if old != nil {
		old.Close()
	}
	if !on || a.ctx.Err() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
	defer cancel()
	as, err := desktop.OpenAudio(ctx, a.conn, desktop.AudioOptions{})
	if err != nil {
		if a.ctx.Err() == nil {
			a.c.sendJSON(deskAudioMsg{Type: "audio", Error: audioError(err)})
		}
		return
	}
	a.mu.Lock()
	a.cur = as
	a.mu.Unlock()
	h := as.Hello()
	a.c.sendJSON(deskAudioMsg{Type: "audio", On: true, Audio: &h})
	go a.pump(as)
}

func (a *deskAudio) pump(as *desktop.AudioSession) {
	for {
		p, err := as.Next(a.ctx)
		if err != nil {
			a.mu.Lock()
			current := a.cur == as
			if current {
				a.cur = nil
			}
			a.mu.Unlock()
			if current && a.ctx.Err() == nil {
				a.c.sendJSON(deskAudioMsg{Type: "audio_end", Error: audioError(err)})
			}
			return
		}
		msg := make([]byte, audioHeaderLen+len(p.Data))
		msg[0] = frameAudioTag
		binary.BigEndian.PutUint32(msg[1:], p.Seq)
		binary.BigEndian.PutUint64(msg[5:], uint64(a.sess.HostToLocal(p.Capture).UnixMicro()))
		binary.BigEndian.PutUint64(msg[13:], uint64(p.Received.UnixMicro()))
		copy(msg[audioHeaderLen:], p.Data)
		if a.c.send(msg) != nil {
			return
		}
	}
}

// audioError is what the page shows: the host's own reason where it gave
// one.
func audioError(err error) string {
	m := classify(err).Message
	for _, p := range []string{"desktop: audio: host: ", "desktop: audio: "} {
		m = strings.TrimPrefix(m, p)
	}
	return m
}
