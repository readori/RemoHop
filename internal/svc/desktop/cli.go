package desktop

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/readori/RemoHop/internal/bg"
	"github.com/readori/RemoHop/internal/cli"
)

func init() {
	cli.Register(&cli.Command{
		Name:    "desktop",
		Summary: "view and control the remote screen in your browser (AV1/H.264, P2P)",
		Remote:  true,
		Usage: `Usage: remohop <ID> desktop [flags]

Show the remote machine's screen in a local browser tab and control it with
mouse and keyboard. The host captures and encodes the screen with ffmpeg
and streams it over the end-to-end encrypted P2P session; the browser
decodes it with WebCodecs.

Codec: AV1 when the host has a GPU AV1 encoder (NVENC, QSV, VAAPI, AMF);
otherwise low-latency H.264, hardware-encoded where available (NVENC, QSV,
VAAPI, AMF, VideoToolbox, Media Foundation), else libx264, which on a
CPU-only host has about half the delay of software AV1 or less. Software
AV1 (SVT-AV1, libaom) is used when the browser decodes only AV1 or with
--codec av1. The page tells the host what the browser can decode before
the stream starts. The video travels over the same session as
everything else: directly between the two machines, unless the session had to
fall back to a relay the rendezvous operator enabled.

remohop serves the viewer on 127.0.0.1 only, behind an unguessable URL, and
opens it in your default browser. It runs until Ctrl-C or until the session
ends; reloading the page reconnects. The page also has buttons for what a
browser keeps to itself (Ctrl+Alt+Del, Alt+Tab, the Windows key,
Ctrl+Alt+F1..F12), typing text, quality presets, switching monitors,
locking the remote screen, sending the local clipboard to the remote and
fetching the remote one, and starting or stopping a recording.

Audio (--audio, or the Audio button in the page) forwards what the host
plays: the host captures its output (Linux: the PulseAudio/PipeWire monitor
of the default output; Windows: WASAPI loopback or a DirectShow loopback such
as virtual-audio-capturer or Stereo Mix; macOS: a loopback device such as
BlackHole, which has to be installed and made part of the system output),
encodes it to Opus at low delay and sends it on its own stream. The page
plays it through WebAudio with a small jitter buffer, delayed to match the
video, and has a Mute button. Audio is off unless asked for, needs the
host's screen feature, and can be switched off on the host with
REMOHOP_DESKTOP_AUDIO=off. When the host cannot capture audio, --audio stops
with the reason and what to install.

Monitors: the page has a monitor menu (every monitor in one picture, or one
monitor, switchable while the session runs) and pointer input follows the
shown monitor. --display all starts with every monitor in one picture;
--display N with the Nth monitor. The all-monitors picture is not available
on macOS hosts.

Files: drop files from your file manager onto the page (or use its Upload
button) to copy them into the host user's Downloads folder, or into the
directory --upload-dir names on the host, with progress. It uses the host's
file feature; nothing is ever overwritten (a name that exists gets " (1)").

Recording (--record, or the Record button) writes what the viewer receives
to an MP4 on this machine, without re-encoding. The file is fragmented MP4,
so it plays even if remohop is killed while recording. If the stream changes
size or codec mid-recording, recording continues in FILE-2.mp4, FILE-3.mp4
and so on. Recordings from the button go to the current directory.

Flags:
  --fps N           frame rate to request (default 60; a CPU encoder that
                    cannot keep up delivers fewer)
  --bitrate RATE    video bitrate, e.g. 8M or 2500k (default: automatic
                    from resolution and frame rate, adapted to the link)
  --max-height PX   downscale so the video is at most PX pixels high
  --scale F         downscale by factor F (0.05..1)
  --display N|all   monitor to show: all = every monitor in one picture,
                    N = Nth monitor, 0 = the host's default (default; on
                    X11 that is every monitor, elsewhere the primary)
  --audio           forward the host's audio (Opus); fail if it cannot
  --upload-dir DIR  host directory for files dropped on the page (default:
                    the host user's Downloads)
  --bench           measure instead of opening a browser: receive video
                    (and audio with --audio) for --bench-duration and print
                    frame rate, bitrate and capture-to-receive latency
  --bench-duration D  length of the measured period (default 10s)
  --view-only       never send mouse or keyboard input
  --codec C         force the codec: av1 or h264
  --encoder NAME    force an ffmpeg encoder (av1_nvenc, av1_qsv, av1_vaapi,
                    av1_amf, libsvtav1, libaom-av1, h264_nvenc, h264_qsv,
                    h264_vaapi, h264_amf, h264_videotoolbox, h264_mf,
                    libx264, libopenh264)
  --record FILE     record the session to FILE (.mp4), created mode 0600
  --force           let --record replace an existing file
  --port P          local port for the viewer (default: random)
  --no-open         print the URL instead of opening a browser
  --json            print the viewer URL and stream parameters as JSON
  -b, --background  keep running in the background (see remohop ps / remohop stop)

Host requirements: ffmpeg (6.0+ recommended) with an AV1 or H.264 encoder
and an X11 session on Linux (Windows: ddagrab capture; macOS: AVFoundation,
which needs the Screen Recording permission, with input when remohop has the
Accessibility permission; the sessions of a macOS host share one capture,
REMOHOP_MAC_DIRECT_CAPTURE=1 on the host turns that off).
Set REMOHOP_DESKTOP_VIEW_ONLY=1 on the host to refuse input on every session.

Examples:
  remohop 123456789 desktop
  remohop 123456789 desktop --max-height 1080 --fps 30
  remohop 123456789 desktop --view-only --bitrate 4M
  remohop 123456789 desktop --codec h264 --record meeting.mp4
  remohop 123456789 desktop --no-open --port 8800 --json
  remohop 123456789 desktop --audio --display all
  remohop 123456789 desktop --bench --bench-duration 20s --fps 60 --json`,
		Run: runDesktop,
	})
}

// parseRate parses a bitrate such as "8M", "2500k", "1.5G" or "800000".
func parseRate(s string) (int64, error) {
	t := strings.TrimSuffix(strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), "bps"), "bit/s")
	mult := 1.0
	switch {
	case strings.HasSuffix(t, "k"):
		mult, t = 1e3, t[:len(t)-1]
	case strings.HasSuffix(t, "m"):
		mult, t = 1e6, t[:len(t)-1]
	case strings.HasSuffix(t, "g"):
		mult, t = 1e9, t[:len(t)-1]
	}
	v, err := strconv.ParseFloat(t, 64)
	if r := v * mult; err != nil || !(r >= minBitrate && r <= maxBitrate) {
		return 0, fmt.Errorf("bad bitrate %q (want e.g. 8M or 2500k, between %dk and %dM)", s, minBitrate/1000, maxBitrate/1_000_000)
	}
	return int64(v * mult), nil
}

type desktopResult struct {
	URL string `json:"url"`
	HostInfo
	// Record is the recording file, if any.
	Record string `json:"record,omitempty"`
}

func runDesktop(ctx context.Context, env *cli.Env, args []string) int {
	fs := cli.NewFlagSet(env, "desktop", "")
	var (
		opts    Options
		rate    string
		port    int
		noOpen  bool
		asJSON  bool
		backgr  bool
		encName string
		record  string
		force   bool
		display string
		audio   bool
		upload  string
		bench   bool
		benchD  time.Duration
	)
	fs.IntVar(&opts.FPS, "fps", defaultFPS, "frame rate")
	fs.StringVar(&rate, "bitrate", "", "bitrate (e.g. 8M)")
	fs.IntVar(&opts.MaxHeight, "max-height", 0, "maximum video height")
	fs.Float64Var(&opts.Scale, "scale", 1, "downscale factor")
	fs.StringVar(&display, "display", "0", "monitor (all, or N; 0 = the host's default)")
	fs.BoolVar(&audio, "audio", false, "forward the host's audio")
	fs.StringVar(&upload, "upload-dir", "", "host directory for dropped files")
	fs.BoolVar(&bench, "bench", false, "measure frame rate, bitrate and latency")
	fs.DurationVar(&benchD, "bench-duration", 10*time.Second, "length of the --bench measurement")
	fs.BoolVar(&opts.ViewOnly, "view-only", false, "no input")
	fs.StringVar(&encName, "encoder", "", "force an encoder")
	fs.StringVar(&opts.Codec, "codec", "", "force a codec (av1, h264)")
	fs.StringVar(&record, "record", "", "record to an MP4 file")
	fs.BoolVar(&force, "force", false, "replace an existing recording file")
	fs.IntVar(&port, "port", 0, "viewer port")
	fs.BoolVar(&noOpen, "no-open", false, "do not open a browser")
	fs.BoolVar(&asJSON, "json", false, "JSON output")
	fs.BoolVar(&backgr, "b", false, "run in the background")
	fs.BoolVar(&backgr, "background", false, "run in the background")
	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}
	if fs.NArg() > 0 {
		env.Errorf("desktop: unexpected argument %q", fs.Arg(0))
		return cli.ExitUsage
	}
	if d, err := parseDisplay(display); err != nil {
		env.Errorf("desktop: %v", err)
		return cli.ExitUsage
	} else {
		opts.Display = d
	}
	if bench && (backgr || record != "") {
		env.Errorf("desktop: --bench cannot be combined with --background or --record")
		return cli.ExitUsage
	}
	if benchD < time.Second || benchD > 10*time.Minute {
		env.Errorf("desktop: --bench-duration must be between 1s and 10m")
		return cli.ExitUsage
	}
	if rate != "" {
		b, err := parseRate(rate)
		if err != nil {
			env.Errorf("desktop: %v", err)
			return cli.ExitUsage
		}
		opts.Bitrate = b
	}
	opts.Encoder = encName
	if port < 0 || port > 65535 {
		env.Errorf("desktop: bad port %d", port)
		return cli.ExitUsage
	}
	check := opts
	if err := check.normalize(); err != nil {
		env.Errorf("desktop: %v", err)
		return cli.ExitUsage
	}
	if record != "" && !strings.EqualFold(filepath.Ext(record), ".mp4") {
		env.Errorf("desktop: --record: the file name must end in .mp4")
		return cli.ExitUsage
	}
	if force && record == "" {
		env.Errorf("desktop: --force only applies to --record")
		return cli.ExitUsage
	}

	if backgr && !bg.InChild() {
		msg, pid, err := bg.Start(60 * time.Second)
		if err != nil {
			env.Errorf("desktop: %v", err)
			return cli.ExitError
		}
		var res desktopResult
		if json.Unmarshal([]byte(msg), &res) != nil {
			env.Errorf("desktop: unexpected background reply %q", msg)
			return cli.ExitError
		}
		report(env, res, asJSON)
		if !asJSON {
			fmt.Fprintf(env.Stdout, "running in the background (pid %d); stop with `remohop stop %d`\n", pid, pid)
		}
		if !noOpen {
			openBrowser(env, res.URL)()
		}
		return cli.ExitOK
	}

	conn, err := env.Conn(ctx)
	if err != nil {
		env.Errorf("%v", err)
		return cli.ExitConnect
	}
	// Probe first: capture or encoder problems surface here, before a
	// browser opens. The session itself starts when the page connects and
	// has said which codecs its browser decodes.
	info, err := Probe(ctx, conn)
	if err != nil {
		env.Errorf("%s", probeError(env.Target, err))
		return cli.ExitError
	}
	if opts.ViewOnly {
		info.Input, info.InputError = false, "view-only session"
	}
	if opts.Codec != "" && info.Encoders[opts.Codec] == "" {
		env.Errorf("desktop: the host has no working %s encoder (it has: %s)", opts.Codec, encoderList(info))
		return cli.ExitError
	}
	if opts.Display == DisplayAll {
		if reason := info.Unsupported[ControlAllMonitors]; reason != "" {
			env.Errorf("desktop: --display all: %s", reason)
			return cli.ExitError
		}
	}
	if audio {
		ah, err := ProbeAudio(ctx, conn)
		if err != nil {
			env.Errorf("desktop: --audio: %v", err)
			return cli.ExitError
		}
		if ah.Warning != "" {
			env.Errorf("desktop: audio: %s", ah.Warning)
		}
	}
	if bench {
		res, err := Bench(ctx, conn, opts, BenchOptions{Duration: benchD, Audio: audio})
		if err != nil {
			env.Errorf("desktop: bench: %v", err)
			return cli.ExitError
		}
		if asJSON {
			enc := json.NewEncoder(env.Stdout)
			enc.SetIndent("", "  ")
			enc.Encode(res)
		} else {
			fmt.Fprint(env.Stdout, res.String())
		}
		return cli.ExitOK
	}
	v, err := newViewer(conn, opts, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		env.Errorf("desktop: viewer: %v", err)
		return cli.ExitError
	}
	v.target = env.Target
	v.autoAudio = audio
	v.uploadDir = upload
	if record != "" {
		v.recordDir = filepath.Dir(record)
		if _, err := v.startRecording(record, force); err != nil {
			v.close()
			env.Errorf("desktop: %v", err)
			return cli.ExitError
		}
	} else if wd, err := os.Getwd(); err == nil {
		v.recordDir = wd
	}
	defer func() {
		files, err := v.stopRecording()
		v.close()
		if err != nil {
			env.Errorf("desktop: %v", err)
		}
		if len(files) > 0 && !asJSON {
			fmt.Fprintf(env.Stdout, "recorded: %s\n", strings.Join(files, ", "))
		}
	}()
	closeBrowser := func() {}
	defer func() { closeBrowser() }()
	served := make(chan error, 1)
	go func() { served <- v.serve() }()

	res := desktopResult{URL: v.URL(), HostInfo: info, Record: record}
	if bg.InChild() {
		b, _ := json.Marshal(res)
		done := bg.Ready(bg.Info{Kind: "desktop", Target: env.Target, Detail: res.URL}, string(b))
		defer done()
	} else {
		report(env, res, asJSON)
		if !noOpen {
			closeBrowser = openBrowser(env, res.URL)
		}
		done := bg.Ready(bg.Info{Kind: "desktop", Target: env.Target, Detail: res.URL}, "")
		defer done()
	}

	select {
	case <-ctx.Done():
		return cli.ExitOK
	case <-conn.Done():
		env.Errorf("desktop: session ended")
		return cli.ExitError
	case err := <-served:
		env.Errorf("desktop: viewer: %v", err)
		return cli.ExitError
	}
}

// parseDisplay reads --display: "all", or a monitor number (0: default).
func parseDisplay(s string) (int, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "all" {
		return DisplayAll, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n > maxDisplay {
		return 0, fmt.Errorf("bad --display %q (want all, or a monitor number from 1; 0 is the host's default)", s)
	}
	return n, nil
}

func encoderList(info HostInfo) string {
	var parts []string
	for _, c := range []string{codecAV1, codecH264} {
		if e := info.Encoders[c]; e != "" {
			parts = append(parts, c+" ("+e+")")
		}
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

func report(env *cli.Env, res desktopResult, asJSON bool) {
	if asJSON {
		enc := json.NewEncoder(env.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(res)
		return
	}
	input := "input on"
	if !res.Input {
		input = "view only"
		if res.InputError != "" {
			input += " (" + res.InputError + ")"
		}
	}
	screen := "screen size unknown"
	if res.ScreenWidth > 0 {
		screen = fmt.Sprintf("%dx%d", res.ScreenWidth, res.ScreenHeight)
		if len(res.Displays) > 1 {
			screen += fmt.Sprintf(", %d monitors", len(res.Displays))
		}
	}
	fmt.Fprintf(env.Stdout, "remote desktop %s, encoders: %s, %s\n", screen, encoderList(res.HostInfo), input)
	if len(res.Displays) > 1 {
		for i, d := range res.Displays {
			fmt.Fprintf(env.Stdout, "  monitor %d: %dx%d at %d,%d\n", i+1, d.Width, d.Height, d.X, d.Y)
		}
		if r := res.Unsupported[ControlAllMonitors]; r != "" {
			fmt.Fprintf(env.Stdout, "  all-monitors view: %s\n", r)
		}
	}
	if res.Warning != "" {
		env.Errorf("desktop: host warning: %s", res.Warning)
	}
	if res.Record != "" {
		fmt.Fprintf(env.Stdout, "recording to %s (starts when the viewer connects)\n", res.Record)
	}
	fmt.Fprintf(env.Stdout, "viewer: %s\n", res.URL)
}

// probeError words a failed probe for the person at this end. Its errors
// already start with "desktop: " (unlike a failure to open the stream);
// and a host without ffmpeg, the commonest case, gets a way to see its
// screen meanwhile.
func probeError(target string, err error) string {
	msg := err.Error()
	if !strings.HasPrefix(msg, "desktop: ") {
		msg = "desktop: " + msg
	}
	if strings.Contains(msg, ffmpegMissing) {
		return fmt.Sprintf("desktop: the host has no ffmpeg, which screen sharing needs. Until it is installed there, "+
			"`remohop %s screenshot` takes still pictures of its screen without it.\n  %s", target, strings.TrimPrefix(msg, "desktop: "))
	}
	return msg
}
