package desktop

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/readori/RemoHop/internal/nowindow"
)

// Key frames on demand from the running encoder.
//
// ffmpeg cannot be told from outside to code the next frame as a key frame,
// so a key frame used to cost a replacement encoder (a new process, capture
// and encoder set-up): measured with libx264 on Linux, a median 120 ms (p95
// 143 ms) from the request to the key frame at 30 fps, and restarts made
// only for key frames had to be spaced two seconds apart. Instead, the
// encoder runs with
//
//   - the zmq filter at the head of its filter chain, which takes filter
//     commands from a socket and applies them to the next frame;
//   - settb and setpts, which put every timestamp in milliseconds rounded
//     to an even number, or to an odd one once a command switches setpts
//     over;
//   - -force_key_frames with an expression that forces a key frame whenever
//     the parity of the timestamp differs from that of the last forced one.
//
// So switching the parity (one command) makes the next frame a key frame,
// and only that one; timestamps move by at most 2 ms. The encoders turn a
// forced key frame into an IDR frame (libx264, NVENC and Quick Sync with
// their forced-IDR switch; VAAPI, VideoToolbox, libaom and libopenh264 on
// their own). One that does not (SVT-AV1 1.7 with ffmpeg 6.1 ignores forced
// key frames; Media Foundation and AMF may code an I frame that is not an
// IDR) is caught by its key frame not coming once the filter took the
// command (keyForceTimeout), and gets restarts from then on (kfRefused).
// Measured with libx264 on Linux, requests falling anywhere in the frame
// period: a median 19 ms (p95 34 ms) at 30 fps, 11 ms (p95 19 ms) at 60.
//
// The command socket is a Unix socket in a private temporary directory,
// one per encoder. An ffmpeg without the zmq filter (it needs a build with
// libzmq) or whose libzmq cannot bind there (keyForceWorks), or forceKeyEnv
// set to 0 on the host, keeps the restarts.
const forceKeyEnv = "REMOHOP_FORCE_KEYFRAMES"

const (
	// keyForceGap spaces out forced key frames: each is a large frame, and
	// a link that is already behind would only fall further behind.
	keyForceGap = 500 * time.Millisecond
	// keyForceTimeout is how long after the filter took the command (and
	// so a frame was on its way to the encoder) the key frame may take
	// before the encoder is taken not to honour forced key frames.
	keyForceTimeout = 500 * time.Millisecond
	// keyForceStall is how long a forced key frame may take in all (the
	// socket being connected again, say) before a restart gets it.
	keyForceStall = 2 * time.Second
	// keyForceDial is how long a forcer tries to reach its encoder's
	// socket, which appears once ffmpeg has set up its filters.
	keyForceDial = firstFrameTimeout
	// kfFilterName names the setpts instance the commands go to.
	kfFilterName = "setpts@remohop_kf"
)

// kfExpr is the -force_key_frames expression: a frame whose timestamp (in
// the encoder's 1/1000 time base) has another parity than the last forced
// key frame's. prev_forced_t is NaN until the first, which makes the first
// frame a forced key frame; it is one anyway.
const kfExpr = "expr:not(eq(mod(round(t*1000),2),mod(round(prev_forced_t*1000),2)))"

// kfPTS is the setpts expression for a parity, in milliseconds (settb puts
// the frames in that time base first). It has no commas, so it needs no
// escaping in a filter graph or a command.
func kfPTS(odd bool) string {
	if odd {
		return "2*floor(PTS/2)+1"
	}
	return "2*floor(PTS/2)"
}

// forceKeyOff reports whether the host's owner turned forced key frames off.
func forceKeyOff() bool { return os.Getenv(forceKeyEnv) == "0" }

// filterEscape quotes s as a filter option value inside a filter graph
// description: once for the option parser, once for the graph parser.
func filterEscape(s string) string {
	esc := func(s, special string) string {
		var b strings.Builder
		for _, r := range s {
			if strings.ContainsRune(special, r) {
				b.WriteByte('\\')
			}
			b.WriteRune(r)
		}
		return b.String()
	}
	return esc(esc(s, `\':`), `\'[],;`)
}

// keyForcer asks one running encoder for key frames.
type keyForcer struct {
	dir, sock string
	want      chan struct{}
	stop      chan struct{}
	once      sync.Once
	// broken: the forcer cannot reach the encoder (or lost it); key frames
	// then take a restart. connected: it can send a command now.
	broken, connected atomic.Bool
	// asked counts requests. appliedSeq is the count the last command the
	// filter took covers, appliedAt when it took it (Unix nanoseconds).
	asked, appliedSeq, appliedAt atomic.Int64
}

// newKeyForcer prepares a forcer: its socket's directory, private to this
// user.
func newKeyForcer() (*keyForcer, error) {
	dir, err := os.MkdirTemp("", "remohop-kf-")
	if err != nil {
		return nil, err
	}
	sock := filepath.Join(dir, "s")
	// sun_path holds 104 bytes on macOS and BSD, 108 on Linux.
	if len(sock) > 100 {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("socket path %s is too long", sock)
	}
	return &keyForcer{dir: dir, sock: sock, want: make(chan struct{}, 1), stop: make(chan struct{})}, nil
}

// filters is the head of the encoder's filter chain.
func (k *keyForcer) filters() string {
	return "zmq=b=" + filterEscape("ipc://"+k.sock) + ",settb=1/1000," + kfFilterName + "=expr=" + kfPTS(false)
}

// start connects to the encoder in the background and then sends a command
// for every request.
func (k *keyForcer) start() { go k.run() }

// request asks for a key frame and returns the request's number (see
// applied); requests made while one is on its way come to one.
func (k *keyForcer) request() int64 {
	n := k.asked.Add(1)
	select {
	case k.want <- struct{}{}:
	default:
	}
	return n
}

// applied reports when the filter took the command for request n, if it
// has: the next frame then went to the encoder as a forced key frame.
func (k *keyForcer) applied(n int64) (time.Time, bool) {
	if k.appliedSeq.Load() < n {
		return time.Time{}, false
	}
	return time.Unix(0, k.appliedAt.Load()), true
}

// close stops the forcer and removes its socket. Safe to call more than
// once.
func (k *keyForcer) close() {
	k.once.Do(func() {
		close(k.stop)
		os.RemoveAll(k.dir)
	})
}

// maxKFReconnects bounds how often a forcer reconnects to its encoder.
const maxKFReconnects = 8

func (k *keyForcer) run() {
	var z *zmtpConn
	defer func() {
		if z != nil {
			z.c.Close()
		}
	}()
	// ffmpeg sets its filters up anew when the input changes (it may do
	// so right after the first frames), which closes the socket and opens
	// it again with the filters as on the command line: setpts back at
	// even timestamps. The forcer then connects again; the request in
	// flight, if any, is sent again.
	odd, pending := false, false
	var last time.Time
	for tries := 0; ; tries++ {
		if z != nil {
			z.c.Close()
		}
		var err error
		if z, err = k.connect(tries == 0); err != nil {
			if !k.stopped() {
				slog.Debug("desktop: cannot reach the encoder for key frames; restarting it for them instead", "err", err)
				k.broken.Store(true)
			}
			return
		}
		odd = false
		k.connected.Store(true)
		for err == nil {
			if !pending {
				select {
				case <-k.stop:
					return
				case <-k.want:
				}
			}
			if wait := time.Until(last.Add(keyForceGap)); wait > 0 && !pending {
				select {
				case <-k.stop:
					return
				case <-time.After(wait):
				}
			}
			odd, pending, last = !odd, true, time.Now()
			// Every request made so far is covered: those made later
			// left a signal in want.
			seq := k.asked.Load()
			// The filter answers as it takes the next frame, which is
			// then coded as a key frame.
			var reply string
			reply, err = z.request(kfFilterName+" expr "+kfPTS(odd), 5*time.Second)
			if err == nil && !strings.HasPrefix(reply, "0 ") && reply != "0" {
				err = fmt.Errorf("filter replied %q", reply)
				tries = maxKFReconnects // not a connection that went away
			}
			if err == nil {
				pending = false
				k.appliedAt.Store(time.Now().UnixNano())
				k.appliedSeq.Store(seq)
			}
		}
		k.connected.Store(false)
		if k.stopped() {
			return
		}
		if tries >= maxKFReconnects {
			slog.Debug("desktop: lost the encoder's key frame socket; restarting it for key frames instead", "err", err)
			k.broken.Store(true)
			return
		}
		slog.Debug("desktop: the encoder's key frame socket closed; connecting again", "err", err)
	}
}

// connect dials the encoder's socket, waiting for it to appear (first) or
// to come back, and does the ZMTP handshake.
func (k *keyForcer) connect(first bool) (*zmtpConn, error) {
	wait := 5 * time.Second
	if first {
		wait = keyForceDial
	}
	deadline := time.Now().Add(wait)
	for {
		c, err := net.DialTimeout("unix", k.sock, time.Second)
		if err == nil {
			z := &zmtpConn{c: c}
			if err = z.handshake(); err == nil {
				// close unblocks a request waiting for its reply.
				go func() {
					<-k.stop
					c.Close()
				}()
				return z, nil
			}
			c.Close()
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		select {
		case <-k.stop:
			return nil, errors.New("stopped")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func (k *keyForcer) stopped() bool {
	select {
	case <-k.stop:
		return true
	default:
		return false
	}
}

// zmtpConn is the client end of a ZeroMQ REQ socket (ZMTP 3.0 with the
// NULL mechanism), just enough to send the zmq filter its commands.
type zmtpConn struct {
	c net.Conn
}

func (z *zmtpConn) handshake() error {
	z.c.SetDeadline(time.Now().Add(5 * time.Second))
	defer z.c.SetDeadline(time.Time{})
	var g [64]byte
	g[0], g[9], g[10], g[11] = 0xff, 0x7f, 3, 0
	copy(g[12:32], "NULL")
	if _, err := z.c.Write(g[:]); err != nil {
		return err
	}
	var peer [64]byte
	if _, err := io.ReadFull(z.c, peer[:]); err != nil {
		return err
	}
	if peer[0] != 0xff || peer[9] != 0x7f || peer[10] < 3 {
		return errors.New("not a ZMTP 3 peer")
	}
	if mech := string(bytes.TrimRight(peer[12:32], "\x00")); mech != "NULL" {
		return fmt.Errorf("peer wants security mechanism %q", mech)
	}
	ready := []byte("\x05READY\x0bSocket-Type")
	ready = binary.BigEndian.AppendUint32(ready, 3)
	ready = append(ready, "REQ"...)
	if err := z.writeFrame(0x04, ready); err != nil {
		return err
	}
	flags, body, err := z.readFrame()
	if err != nil {
		return err
	}
	if flags&0x04 == 0 || !bytes.HasPrefix(body, []byte("\x05READY")) {
		return fmt.Errorf("peer answered the handshake with %q", body)
	}
	return nil
}

// request sends one message and returns the reply.
func (z *zmtpConn) request(msg string, timeout time.Duration) (string, error) {
	z.c.SetDeadline(time.Now().Add(timeout))
	defer z.c.SetDeadline(time.Time{})
	// A REQ message starts with an empty delimiter frame.
	if err := z.writeFrame(0x01, nil); err != nil {
		return "", err
	}
	if err := z.writeFrame(0x00, []byte(msg)); err != nil {
		return "", err
	}
	var parts [][]byte
	for {
		flags, body, err := z.readFrame()
		if err != nil {
			return "", err
		}
		if flags&0x04 != 0 {
			continue // a command (heartbeat), not part of the reply
		}
		parts = append(parts, body)
		if flags&0x01 == 0 {
			break
		}
	}
	// The reply is the last frame after the empty delimiter.
	return strings.TrimRight(string(parts[len(parts)-1]), "\x00"), nil
}

func (z *zmtpConn) writeFrame(flags byte, body []byte) error {
	var hdr []byte
	if len(body) > 255 {
		hdr = binary.BigEndian.AppendUint64([]byte{flags | 0x02}, uint64(len(body)))
	} else {
		hdr = []byte{flags, byte(len(body))}
	}
	_, err := z.c.Write(append(hdr, body...))
	return err
}

func (z *zmtpConn) readFrame() (flags byte, body []byte, err error) {
	var h [9]byte
	if _, err = io.ReadFull(z.c, h[:2]); err != nil {
		return
	}
	flags = h[0]
	n := uint64(h[1])
	if flags&0x02 != 0 {
		if _, err = io.ReadFull(z.c, h[2:9]); err != nil {
			return
		}
		n = binary.BigEndian.Uint64(h[1:9])
	}
	if n > 1<<16 {
		return 0, nil, fmt.Errorf("ZMTP frame of %d bytes", n)
	}
	body = make([]byte, n)
	_, err = io.ReadFull(z.c, body)
	return
}

// keyForceWorks reports whether ffmpeg can run the filters key frames on
// demand need: the zmq filter (which needs a build with libzmq) bound to a
// Unix socket, settb and setpts, with the -force_key_frames expression. The
// answer is kept like an encoder probe's.
func keyForceWorks(ctx context.Context, ffmpeg string) bool {
	kfProbeMu.Lock()
	defer kfProbeMu.Unlock()
	if p, ok := kfProbeCache[ffmpeg]; ok && (p.err == nil || time.Since(p.at) < failedProbeLife) {
		return p.err == nil
	}
	err := kfProbe(ctx, ffmpeg)
	if ctx.Err() != nil {
		return false
	}
	if err != nil {
		slog.Debug("desktop: ffmpeg cannot force key frames on demand; restarting the encoder for them instead", "err", err)
	}
	kfProbeCache[ffmpeg] = probed{err: err, at: time.Now()}
	return err == nil
}

var (
	kfProbeMu    sync.Mutex
	kfProbeCache = map[string]probed{}
	// kfRefused are encoders (by ffmpeg and name) that did not code a key
	// frame when one was forced: they get restarts instead from then on.
	kfRefused sync.Map
)

func kfProbe(ctx context.Context, ffmpeg string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	k, err := newKeyForcer()
	if err != nil {
		return err
	}
	defer k.close()
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i", "color=c=gray:s=64x64:r=10", "-frames:v", "2",
		"-vf", k.filters(), "-enc_time_base", "1:1000", "-force_key_frames", kfExpr, "-f", "null", "-"}
	var stderr bytes.Buffer
	cmd := nowindow.Set(exec.CommandContext(ctx, ffmpeg, args...))
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return errors.New(lastLine(stderr.String(), err.Error()))
	}
	return nil
}

func kfRefusedKey(ffmpeg, enc string) string { return ffmpeg + "\x00" + enc }
