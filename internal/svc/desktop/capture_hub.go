package desktop

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/readori/RemoHop/internal/nowindow"
)

// A capture hub lets several sessions show one screen where the operating
// system gives the screen to one capture at a time. On macOS a second
// AVFoundation screen capture started while one is running never delivers
// a frame (measured on a hosted Apple Silicon Mac), so two viewers of one
// Mac cannot each run their own capture.
//
// The hub runs the capture once, as an ffmpeg that writes raw NV12 frames
// of a fixed size, and copies each frame to a pipe per session; the
// session's own ffmpeg reads its pipe as its input and scales and encodes
// as it would from the device. A session that is slow simply misses
// frames: the hub never waits for a reader. The capture runs at the
// highest frame rate a session asked for and sessions that want less get
// every Nth frame. It stops shortly after the last session leaves.

const (
	// hubLinger keeps the capture running across an encoder restart.
	hubLinger = 3 * time.Second
	// hubFirstFrame is how long a new capture may take to deliver.
	hubFirstFrame = 10 * time.Second
)

var hubs = struct {
	mu sync.Mutex
	m  map[string]*captureHub
}{m: map[string]*captureHub{}}

type hubFrame struct {
	data []byte
	refs atomic.Int32
	pool *sync.Pool
}

func (f *hubFrame) release() {
	if f.refs.Add(-1) == 0 {
		f.pool.Put(f)
	}
}

// hubRun is one capture process.
type hubRun struct {
	cmd    *exec.Cmd
	stderr *tailBuffer
	fps    int
	frames int
	err    error
	ended  bool
}

type captureHub struct {
	ffmpeg string
	// input builds the capture arguments (ending with -i) for a rate.
	input func(fps int) []string
	env   []string
	w, h  int
	size  int // bytes per frame

	mu   sync.Mutex
	subs map[*hubSub]struct{}
	run  *hubRun
	idle *time.Timer
	pool sync.Pool
}

type hubSub struct {
	hub    *captureHub
	w      *os.File
	latest chan *hubFrame
	fps    int
	last   time.Time
	done   chan struct{}
	once   sync.Once
}

// hubInput is what a session's ffmpeg is given to read the hub's frames.
// Frames are stamped with the time they are read, which is within a frame
// of when they were captured.
func hubInput(w, h, fps int) []string {
	return []string{"-f", "rawvideo", "-pixel_format", "nv12", "-video_size", fmt.Sprintf("%dx%d", w, h),
		"-framerate", strconv.Itoa(fps), "-use_wallclock_as_timestamps", "1", "-i", "pipe:0"}
}

// attachCapture subscribes a session to the hub for key, starting the
// capture if needed. It returns the pipe the session's ffmpeg reads (the
// caller closes its copy once that process has it) and a function that
// ends the subscription. w and h must be the capture's size in pixels;
// odd sizes lose their last row or column.
func attachCapture(key, ffmpeg string, input func(fps int) []string, env []string, w, h, fps int) (*os.File, func(), error) {
	w, h = w&^1, h&^1
	if w <= 0 || h <= 0 {
		return nil, nil, errors.New("the size of the screen is unknown")
	}
	key = fmt.Sprintf("%s|%s|%dx%d", key, ffmpeg, w, h)
	hubs.mu.Lock()
	hub := hubs.m[key]
	if hub == nil {
		hub = &captureHub{ffmpeg: ffmpeg, input: input, env: env, w: w, h: h, size: w * h * 3 / 2, subs: map[*hubSub]struct{}{}}
		hub.pool.New = func() any { return &hubFrame{data: make([]byte, hub.size), pool: &hub.pool} }
		hubs.m[key] = hub
	}
	hubs.mu.Unlock()

	// Twice: a capture that was already on its way out when this session
	// joined (its display went away, say) is started afresh once.
	for attempt := 1; ; attempt++ {
		pr, release, retry, err := hub.join(fps)
		if err == nil {
			return pr, release, nil
		}
		if !retry || attempt == 2 {
			return nil, nil, err
		}
	}
}

// join subscribes to the capture, starting it if needed, and waits for a
// frame captured after joining. retry reports that the capture had been
// delivering and ended just now.
func (hub *captureHub) join(fps int) (pr *os.File, release func(), retry bool, err error) {
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, nil, false, err
	}
	sub := &hubSub{hub: hub, w: pw, fps: fps, latest: make(chan *hubFrame, 1), done: make(chan struct{})}
	hub.mu.Lock()
	if hub.idle != nil {
		hub.idle.Stop()
		hub.idle = nil
	}
	if hub.run != nil && hub.run.fps < fps {
		// A faster viewer joined: capture again at its rate. The others
		// keep their pipes and see a short pause.
		killGroup(hub.run.cmd)
		hub.run = nil
	}
	if hub.run == nil {
		if err := hub.start(fps); err != nil {
			hub.mu.Unlock()
			pr.Close()
			pw.Close()
			return nil, nil, false, err
		}
	}
	run := hub.run
	before := run.frames
	hub.subs[sub] = struct{}{}
	hub.mu.Unlock()
	go sub.write()

	deadline := time.Now().Add(hubFirstFrame)
	for {
		hub.mu.Lock()
		frames, ended, rerr, tail := run.frames, run.ended, run.err, run.stderr.errorText()
		hub.mu.Unlock()
		if frames > before && !ended {
			return pr, sub.close, false, nil
		}
		if ended || time.Now().After(deadline) {
			sub.close()
			pr.Close()
			switch {
			case rerr != nil:
				return nil, nil, false, fmt.Errorf("screen capture failed: %w", rerr)
			case ended:
				return nil, nil, true, errors.New("screen capture ended")
			case tail != "":
				return nil, nil, false, fmt.Errorf("screen capture delivered no picture within %v: %s", hubFirstFrame, tail)
			}
			return nil, nil, false, fmt.Errorf("screen capture delivered no picture within %v", hubFirstFrame)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// start launches the capture; h.mu is held.
func (h *captureHub) start(fps int) error {
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-fflags", "nobuffer", "-probesize", "32", "-analyzeduration", "0"}
	args = append(args, h.input(fps)...)
	// The size is forced so that the stream is exactly the frames readers
	// expect, whatever the device delivers.
	args = append(args, "-an", "-vf", fmt.Sprintf("scale=%d:%d:flags=fast_bilinear,format=nv12", h.w, h.h),
		"-fps_mode", "passthrough", "-f", "rawvideo", "pipe:1")
	cmd := nowindow.Set(exec.Command(h.ffmpeg, args...))
	cmd.Env = h.env
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	run := &hubRun{cmd: cmd, stderr: &tailBuffer{max: 8 << 10}, fps: fps}
	cmd.Stderr = run.stderr
	if err := startGroup(cmd); err != nil {
		return fmt.Errorf("start ffmpeg: %w", err)
	}
	h.run = run
	slog.Debug("desktop: shared capture started", "size", fmt.Sprintf("%dx%d", h.w, h.h), "fps", fps)
	go h.read(run, stdout)
	return nil
}

// read distributes the capture's frames until it ends.
func (h *captureHub) read(run *hubRun, stdout io.Reader) {
	var err error
	for {
		f := h.pool.Get().(*hubFrame)
		f.refs.Store(1)
		if _, err = io.ReadFull(stdout, f.data); err != nil {
			f.release()
			break
		}
		now := time.Now()
		h.mu.Lock()
		run.frames++
		for s := range h.subs {
			// A reader that asked for less than the capture's rate gets
			// the frames that are due at its own.
			if s.fps < run.fps && now.Sub(s.last) < time.Second/time.Duration(s.fps)*85/100 {
				continue
			}
			s.last = now
			f.refs.Add(1)
			// Newest frame wins: replace one the reader has not taken.
			select {
			case old := <-s.latest:
				old.release()
			default:
			}
			s.latest <- f
		}
		h.mu.Unlock()
		f.release()
	}
	killGroup(run.cmd)
	run.cmd.Wait()
	h.mu.Lock()
	run.ended = true
	if run.frames == 0 {
		if msg := run.stderr.errorText(); msg != "" {
			run.err = errors.New("ffmpeg: " + msg)
		} else {
			run.err = errors.New("ffmpeg exited without a picture")
		}
	}
	var drop []*hubSub
	if h.run == run {
		// The capture ended on its own: end the readers' streams, so
		// that each session notices and starts again.
		h.run = nil
		for s := range h.subs {
			drop = append(drop, s)
		}
	}
	h.mu.Unlock()
	for _, s := range drop {
		s.close()
	}
}

// write copies frames into the session's pipe.
func (s *hubSub) write() {
	for {
		select {
		case <-s.done:
			return
		case f := <-s.latest:
			_, err := s.w.Write(f.data)
			f.release()
			if err != nil {
				s.close()
				return
			}
		}
	}
}

// close ends the subscription; the capture stops if it was the last.
func (s *hubSub) close() {
	s.once.Do(func() {
		h := s.hub
		h.mu.Lock()
		delete(h.subs, s)
		if len(h.subs) == 0 && h.run != nil && h.idle == nil {
			h.idle = time.AfterFunc(hubLinger, func() {
				h.mu.Lock()
				defer h.mu.Unlock()
				if len(h.subs) == 0 && h.run != nil {
					killGroup(h.run.cmd)
					h.run = nil
				}
				h.idle = nil
			})
		}
		h.mu.Unlock()
		close(s.done)
		s.w.Close()
		select {
		case f := <-s.latest:
			f.release()
		default:
		}
	})
}
