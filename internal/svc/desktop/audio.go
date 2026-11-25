package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/readori/RemoHop/internal/nowindow"

	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/perm"
	"github.com/readori/RemoHop/internal/wire"
)

// Audio forwarding (host -> viewer).
//
// Audio travels on its own stream, the "desktop.audio" service, opened next
// to the video stream of the same session, so a lost or late video frame
// never delays a sound and vice versa. The host captures what the machine
// plays (platform specific, see audio_*.go), has ffmpeg encode it to Opus
// (libopus, low-delay mode, 20 ms frames by default), and sends one wire
// frame per Opus packet:
//
//	host -> client: wire frames
//	  'H' JSON AudioHello  first
//	  'A' audio            audioHeaderLen-byte header + one Opus packet
//	  'E' JSON {"error"}   fatal error; the host closes the stream after it
//	client -> host: nothing; closing the stream stops the capture.
//
// A packet carries the host-clock time its first sample was captured, on the
// same clock as the video frames' capture times, which is what lets a viewer
// play sound and picture in sync. The stream is reliable, so the host bounds
// latency itself: packets that sat in its queue longer than audioMaxAge are
// dropped instead of delivered late.
//
// Security: listening to a machine is as sensitive as looking at it, so the
// service belongs to the "screen" feature (a host that switched screen
// access off serves no audio), a client must ask for it per session, and
// the host owner can switch it off alone with REMOHOP_DESKTOP_AUDIO=off. The
// client cannot choose the capture source: only the machine's output mix
// (never a microphone) is ever captured, unless the host owner points
// REMOHOP_DESKTOP_AUDIO_SOURCE at something else.

// AudioServiceName is the peer service for audio forwarding.
const AudioServiceName = "desktop.audio"

func init() {
	peer.Handle(AudioServiceName, serveAudio)
	perm.Register(AudioServiceName, perm.Screen)
}

const frameAudio = 'A'

// Audio packet header (big-endian):
//
//	[0:4]   sequence number, per stream, starting at 1
//	[4:12]  capture time of the first sample, Unix microseconds, host clock
const audioHeaderLen = 12

const (
	audioSampleRate = 48000
	audioChannels   = 2
	defaultAudioBPS = 96_000
	minAudioBPS     = 16_000
	maxAudioBPS     = 510_000
	defaultFrameMS  = 20
	// audioMaxAge is how old a packet may be when the host gets to send
	// it; older ones are dropped.
	audioMaxAge = 400 * time.Millisecond
	// audioFirstPacket is how long a capture may take to deliver its first
	// packet.
	audioFirstPacket = 5 * time.Second
	// hostAudioEnv set to off, 0 or false makes the host refuse audio.
	hostAudioEnv = "REMOHOP_DESKTOP_AUDIO"
	// audioSourceEnv overrides what the host captures (a PulseAudio source
	// name on Linux, a dshow or AVFoundation device name elsewhere).
	audioSourceEnv = "REMOHOP_DESKTOP_AUDIO_SOURCE"
)

// AudioOptions is the client's request on the audio stream.
type AudioOptions struct {
	// Bitrate of the Opus stream in bits per second (default 96000).
	Bitrate int `json:"bitrate,omitempty"`
	// FrameMS is the Opus frame length: 10 or 20 (default); smaller is
	// lower latency and costs bitrate.
	FrameMS int `json:"frame_ms,omitempty"`
	// Probe only checks that audio can be captured, then ends the stream
	// after the AudioHello.
	Probe bool `json:"probe,omitempty"`
}

func (o *AudioOptions) normalize() error {
	if o.Bitrate == 0 {
		o.Bitrate = defaultAudioBPS
	}
	if o.Bitrate < minAudioBPS || o.Bitrate > maxAudioBPS {
		return fmt.Errorf("audio bitrate %d out of range %d..%d", o.Bitrate, minAudioBPS, maxAudioBPS)
	}
	switch o.FrameMS {
	case 0:
		o.FrameMS = defaultFrameMS
	case 10, 20:
	default:
		return fmt.Errorf("audio frame length %d ms is not 10 or 20", o.FrameMS)
	}
	return nil
}

// AudioHello describes the audio stream that follows.
type AudioHello struct {
	// Codec is "opus"; packets are plain Opus packets (RFC 6716), one per
	// frame, decodable by WebCodecs AudioDecoder without a description.
	Codec      string `json:"codec"`
	SampleRate int    `json:"sample_rate"`
	Channels   int    `json:"channels"`
	FrameMS    int    `json:"frame_ms"`
	Bitrate    int    `json:"bitrate"`
	// Source says what is captured, for display.
	Source  string `json:"source"`
	Warning string `json:"warning,omitempty"`
}

// audioSource is one way to capture the machine's output with ffmpeg.
type audioSource struct {
	desc string
	// args are the ffmpeg input arguments, ending with -i NAME.
	args []string
	env  []string
	// warning is shown to the user when this source is used.
	warning string
}

type audioSourcesFn = func(context.Context, string) ([]audioSource, string)

// audioSourcesHook finds the capture candidates, best first, or says why
// there are none. Tests substitute a synthetic source with
// setAudioSources; the mutex keeps that safe while a host goroutine from
// an earlier session may still be reading it.
var (
	audioSourcesMu   sync.RWMutex
	audioSourcesHook audioSourcesFn = platformAudioSources
)

func audioSourcesFunc(ctx context.Context, ffmpeg string) ([]audioSource, string) {
	audioSourcesMu.RLock()
	f := audioSourcesHook
	audioSourcesMu.RUnlock()
	return f(ctx, ffmpeg)
}

// setAudioSources replaces the source finder and returns the previous one.
func setAudioSources(f audioSourcesFn) audioSourcesFn {
	audioSourcesMu.Lock()
	defer audioSourcesMu.Unlock()
	old := audioSourcesHook
	audioSourcesHook = f
	return old
}

func audioDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(hostAudioEnv))) {
	case "off", "0", "false", "no":
		return true
	}
	return false
}

// audioPacket is one encoded packet read from ffmpeg.
type audioPacket struct {
	data   []byte
	readAt time.Time
}

// audioCapture is a running audio capture: ffmpeg encoding to Opus.
type audioCapture struct {
	cmd    *exec.Cmd
	stderr *tailBuffer
	src    audioSource
	pkts   chan audioPacket
	done   chan struct{}
	mu     sync.Mutex
	err    error
	once   sync.Once
}

func (c *audioCapture) stop() {
	c.once.Do(func() {
		killGroup(c.cmd)
	})
}

// failure is why the capture ended.
func (c *audioCapture) failure() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	return errors.New("audio capture ended")
}

func audioArgs(src audioSource, o AudioOptions) []string {
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-fflags", "nobuffer", "-flags", "low_delay"}
	args = append(args, src.args...)
	args = append(args, "-vn", "-ac", strconv.Itoa(audioChannels), "-ar", strconv.Itoa(audioSampleRate),
		"-c:a", "libopus", "-application", "lowdelay", "-frame_duration", strconv.Itoa(o.FrameMS),
		"-b:a", strconv.Itoa(o.Bitrate), "-vbr", "off",
		// One Ogg page per packet, flushed at once.
		"-f", "ogg", "-page_duration", "1", "-flush_packets", "1", "pipe:1")
	return args
}

// startAudioCapture runs the first candidate source that delivers a packet.
func startAudioCapture(ctx context.Context, o AudioOptions) (*audioCapture, error) {
	ffmpeg, err := ffmpegPath()
	if err != nil {
		return nil, err
	}
	if !ffmpegHasEncoder(ffmpeg, "libopus") {
		return nil, fmt.Errorf("the host's ffmpeg (%s) has no libopus encoder, which audio forwarding needs; install an ffmpeg build with libopus", ffmpeg)
	}
	srcs, why := audioSourcesFunc(ctx, ffmpeg)
	if len(srcs) == 0 {
		return nil, errors.New(why)
	}
	var reasons []string
	for _, src := range srcs {
		c, err := launchAudio(ctx, ffmpeg, src, o)
		if err == nil {
			return c, nil
		}
		reasons = append(reasons, src.desc+": "+err.Error())
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, fmt.Errorf("cannot capture the host's audio output (%s). %s", strings.Join(reasons, "; "), audioHelp())
}

func launchAudio(ctx context.Context, ffmpeg string, src audioSource, o AudioOptions) (*audioCapture, error) {
	cmd := nowindow.Set(exec.Command(ffmpeg, audioArgs(src, o)...))
	cmd.Env = src.env
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	c := &audioCapture{cmd: cmd, stderr: &tailBuffer{max: 8 << 10}, src: src,
		pkts: make(chan audioPacket, 64), done: make(chan struct{})}
	cmd.Stderr = c.stderr
	if err := startGroup(cmd); err != nil {
		return nil, fmt.Errorf("start ffmpeg: %w", err)
	}
	go c.read(stdout)
	timer := time.NewTimer(audioFirstPacket)
	defer timer.Stop()
	select {
	case p := <-c.pkts:
		// Put it back for the consumer (the channel has room: it was empty).
		c.pkts <- p
		return c, nil
	case <-c.done:
		err := c.failure()
		return nil, err
	case <-timer.C:
		c.stop()
		if msg := c.stderr.errorText(); msg != "" {
			return nil, fmt.Errorf("no audio within %v: %s", audioFirstPacket, msg)
		}
		return nil, fmt.Errorf("no audio data within %v", audioFirstPacket)
	case <-ctx.Done():
		c.stop()
		return nil, ctx.Err()
	}
}

// read pumps Opus packets into c.pkts; when the queue is full the oldest
// packet goes, so what is queued is always the freshest sound.
func (c *audioCapture) read(stdout io.Reader) {
	defer close(c.done)
	or := newOggReader(stdout)
	seen := 0
	var err error
	for {
		var pkt []byte
		if pkt, err = or.next(); err != nil {
			break
		}
		if seen < 2 && (strings.HasPrefix(string(pkt), "OpusHead") || strings.HasPrefix(string(pkt), "OpusTags")) {
			seen++
			continue
		}
		p := audioPacket{data: pkt, readAt: time.Now()}
		for {
			select {
			case c.pkts <- p:
			default:
				select {
				case <-c.pkts:
				default:
				}
				continue
			}
			break
		}
	}
	killGroup(c.cmd)
	c.cmd.Wait()
	msg := c.stderr.errorText()
	c.mu.Lock()
	switch {
	case msg != "":
		c.err = errors.New(msg)
	case err != nil && err != io.EOF && err != io.ErrUnexpectedEOF:
		c.err = err
	default:
		c.err = errors.New("ffmpeg exited without audio")
	}
	c.mu.Unlock()
}

var (
	encMu    sync.Mutex
	encCache = map[string]bool{}
)

// ffmpegHasEncoder reports whether ffmpeg has the encoder, remembering the
// answer.
func ffmpegHasEncoder(ffmpeg, name string) bool {
	k := ffmpeg + "\x00" + name
	encMu.Lock()
	defer encMu.Unlock()
	if v, ok := encCache[k]; ok {
		return v
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := nowindow.Set(exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-h", "encoder="+name)).CombinedOutput()
	ok := err == nil && strings.Contains(string(out), "Encoder "+name)
	encCache[k] = ok
	return ok
}

// audioClock turns the arrival times of packets, which carry the jitter of
// the pipe and of ffmpeg's scheduling, into capture times. Packets are a
// fixed duration apart, so packet n was captured at base + n*frame; base is
// the smallest (arrival - n*frame) seen, with a slow leak so a small clock
// difference between the audio device and this machine's clock cannot
// accumulate. The smallest value belongs to the packet that spent the least
// time in ffmpeg, so the estimate is off only by that minimum (a frame plus
// the capture buffer, subtracted as audioPipeline).
type audioClock struct {
	frame time.Duration
	n     int64
	base  int64 // microseconds
	init  bool
}

// audioPipeline is what a packet minimally spends between capture and
// being read: the Opus frame it was collected for, the capture buffer and
// the encoder's look-ahead.
func audioPipeline(frame time.Duration) time.Duration {
	return frame + 10*time.Millisecond + 7*time.Millisecond
}

func (c *audioClock) stamp(arrival time.Time) time.Time {
	media := c.n * c.frame.Microseconds()
	c.n++
	off := arrival.UnixMicro() - media
	switch {
	case !c.init || off < c.base:
		c.base, c.init = off, true
	case off-c.base > 500_000:
		// A long gap (the source stalled): start again.
		c.base = off
	default:
		c.base += (off - c.base) / 1024
	}
	return time.UnixMicro(c.base + media).Add(-audioPipeline(c.frame))
}

func serveAudio(ctx context.Context, c *peer.Conn, s peer.Stream, raw json.RawMessage) error {
	var o AudioOptions
	err := peer.DecodeReq(raw, &o)
	if err == nil {
		err = o.normalize()
	}
	if err != nil {
		writeFatal(s, fmt.Errorf("bad request: %w", err))
		return err
	}
	if audioDisabled() {
		err = errors.New("audio forwarding is switched off on this host (" + hostAudioEnv + "=off)")
		writeFatal(s, err)
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cap, err := startAudioCapture(ctx, o)
	if err != nil {
		writeFatal(s, err)
		return err
	}
	defer cap.stop()
	hello := AudioHello{Codec: "opus", SampleRate: audioSampleRate, Channels: audioChannels, FrameMS: o.FrameMS,
		Bitrate: o.Bitrate, Source: cap.src.desc, Warning: cap.src.warning}
	body, _ := json.Marshal(hello)
	s.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := wire.WriteFrame(s, frameHello, body); err != nil {
		return err
	}
	if o.Probe {
		return nil
	}
	// The client says nothing on this stream: reading is how the host
	// notices it went away.
	go func() {
		io.Copy(io.Discard, s)
		cancel()
	}()

	clock := audioClock{frame: time.Duration(o.FrameMS) * time.Millisecond}
	var seq uint32
	dropped := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-cap.done:
			err := cap.failure()
			writeFatal(s, fmt.Errorf("audio capture stopped: %w", err))
			return err
		case p := <-cap.pkts:
			// The clock counts every packet, sent or not.
			captured := clock.stamp(p.readAt)
			if age := time.Since(p.readAt); age > audioMaxAge {
				dropped++
				if dropped == 1 || dropped%100 == 0 {
					slog.Debug("desktop: audio packets dropped, the link is too slow", "dropped", dropped)
				}
				continue
			}
			seq++
			msg := make([]byte, 5+audioHeaderLen+len(p.data))
			putFrameHead(msg, frameAudio, audioHeaderLen+len(p.data))
			putUint32(msg[5:], seq)
			putUint64(msg[9:], uint64(captured.UnixMicro()))
			copy(msg[5+audioHeaderLen:], p.data)
			s.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err := s.Write(msg); err != nil {
				return err
			}
		}
	}
}

func putUint32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v>>24), byte(v>>16), byte(v>>8), byte(v)
}
func putUint64(b []byte, v uint64) {
	putUint32(b, uint32(v>>32))
	putUint32(b[4:], uint32(v))
}
