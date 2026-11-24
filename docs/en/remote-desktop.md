# Screen and remote desktop

[中文](../zh/remote-desktop.md)

Every remote command has the form `remohop <ID> <command>`. `dev` is the example alias from `remohop <ID> pair --name dev` (see [Quick start](quick-start.md)). Run `remohop help <command>` for the full syntax.

Capture a screenshot, of a display or a region:

```bash
remohop dev screenshot -o /tmp/screen.png
remohop dev screenshot --max-width 1280 --format jpeg --json
remohop dev screenshot -R 0,0,800,600 -o /tmp/corner.png
```

Show the remote screen in a browser tab and control it:

```bash
remohop dev desktop
remohop dev desktop --max-height 1080 --fps 30
remohop dev desktop --codec h264 --record meeting.mp4
remohop dev desktop --audio --display all
remohop dev desktop --bench --bench-duration 20s
```

- **Codec.** AV1 when the host has a GPU AV1 encoder; otherwise low-latency H.264, hardware-encoded where available, else libx264. On a CPU-only host, H.264 measured about half the latency of software AV1 or less. `--codec av1` forces AV1.
- **Viewer.** The page has buttons for key combinations a browser keeps to itself (Ctrl+Alt+Del, Alt+Tab, the Windows key), Unicode typing, quality presets, monitor switching, locking the remote screen, clipboard transfer, file upload, audio and recording.
- **Recording.** `--record FILE.mp4` (or the Record button) writes the received stream to a fragmented MP4 on this machine without re-encoding.
- **Audio.** `--audio` (or the page's Audio button; Mute silences it without stopping it) forwards what the host plays: the host captures its output with ffmpeg, encodes Opus (libopus, low-delay mode, 20 ms frames, 96 kbit/s) and sends it on its own stream next to the video, so a late video frame never delays sound. The page decodes it with WebCodecs and plays it through Web Audio with a jitter buffer, scheduling every packet against its capture time and the video's own latency so picture and sound stay together. Audio is off unless asked for, belongs to the host's `screen` feature (a host with `screen` off serves none), can be switched off alone with `REMOHOP_DESKTOP_AUDIO=off` on the host, and only ever captures the machine's output, never a microphone. When the host cannot capture, `--audio` stops with the reason and what to install, and the page's button shows the same message. What the host needs:

  | Host | How the output is captured |
  |---|---|
  | Linux | PulseAudio, or PipeWire with `pipewire-pulse`, reachable from the process running `remohop` (it records the monitor of the default output), and an ffmpeg with libpulse and libopus. Tested with a PulseAudio null sink. |
  | Windows | ffmpeg's WASAPI loopback if the build has it, else a DirectShow loopback device: `virtual-audio-capturer` (the free screen-capture-recorder driver) or the sound card's "Stereo Mix" once enabled. Each candidate is test-run before use. Not verified on a real Windows machine. |
  | macOS | macOS cannot record its own output: install a loopback driver such as BlackHole, create a Multi-Output Device (speakers plus BlackHole) in Audio MIDI Setup, select it as the output, and give `remohop` the Microphone permission. Without such a device `--audio` says so; a microphone is never picked on its own. Not verified on a real Mac. |

  `REMOHOP_DESKTOP_AUDIO_SOURCE` on the host names a different source (a PulseAudio source, a DirectShow device, an AVFoundation device name or index).
- **Monitors.** The page's monitor menu switches while the session runs, between one monitor and **All monitors**, a single picture of the whole virtual desktop (also with monitors of different sizes and positions; the gaps are black). Pointer input is mapped onto what is shown. `--display all` or `--display N` starts on one; the command lists the host's monitors. All monitors is one X11 root window on Linux and a GDI capture of the virtual-desktop bounding box on Windows (Desktop Duplication is per monitor, so this view uses more CPU; not verified on a multi-monitor Windows machine). macOS has no all-monitors view: AVFoundation captures one display at a time, and the host reports that instead of offering it.
- **Files.** Drop files from your file manager onto the page, or use its Upload button, to copy them to the host user's Downloads folder (`--upload-dir DIR` picks another host directory) with a progress row per file. It goes through the host's file service, so it needs the `file` feature, never overwrites (an existing name gets ` (1)`), and is refused in a view-only session. Limits: files only (no folders: zip one first); and no copy-and-paste of files. The page hands Ctrl+V to the remote machine, the remote clipboard carries text and images but no file lists, and a browser exposes pasted files only to a paste event, which the remote-keyboard capture would cancel. Drop or Upload, then paste the file's path from the remote file manager.
- **Numbers.** `remohop <ID> desktop --bench` (`--bench-duration 20s`, `--audio`, `--json`) opens a session without a browser and reports frame rate, video bitrate and capture-to-received latency (host clock to arrival, with the session's clock-offset estimate), plus the host's own pipeline delay and drops. Measured in a Linux container (4 vCPU, ffmpeg 6.1.1, Xvfb 1280x720 with a changing picture, loopback QUIC between the two ends, `REMOHOP_BENCH_REPORT=1 go test ./internal/svc/desktop -run TestBenchReportXvfb -v`):

  | Stream | fps | video | capture to received (mean, p95) |
  |---|---|---|---|
  | H.264 libx264, 60 fps | 60.0 | 2.4 Mbit/s | 5.9 ms, 8.5 ms |
  | H.264 libx264, 30 fps | 30.0 | 1.5 Mbit/s | 6.7 ms, 9.0 ms |
  | AV1 SVT-AV1 (software), 30 fps | 30.0 | 1.7 Mbit/s | 70.0 ms, 72.1 ms |

  These stop at the moment a frame reaches the viewer process, and x11grab stamps a frame when it grabs it, up to one frame period after the screen changed. The true change-to-picture figure, measured in headless Chromium 157 (H.264, 800x300, 30 fps; the test flashes a patch of the remote screen and a `requestAnimationFrame` loop notes when the canvas shows it, so it includes up to 33 ms of grab wait and up to 16 ms of detection): median 31 ms, p95 47 ms over 40 flashes (`go test ./internal/svc/desktop -run TestViewerPageXvfb -v`). Audio at 96 kbit/s adds 50 packets per second; its capture time is estimated from packet arrival (the host assumes a 37 ms capture-and-encode pipeline), so its capture-to-received mean of 47 ms is mostly that assumption, not an independent measurement. Real links add their own round trip, and a GPU encoder changes the encode figure.

The viewer listens on `127.0.0.1` behind a random URL; Ctrl-C ends it. The host needs ffmpeg with an H.264 or AV1 encoder; see [troubleshooting.md](../troubleshooting.md#ffmpeg-and-encoders).

A macOS host needs the Screen Recording permission to capture and Accessibility for input. macOS gives the screen to one capture at a time, so all sessions of a macOS host share a single capture; `REMOHOP_MAC_DIRECT_CAPTURE=1` on the host turns that off (one capture per session) if the shared one misbehaves.

## GUI automation

These commands drive the remote pointer and keyboard. Coordinates are pixels of the selected display as a full-size screenshot shows it, with 0,0 at the top left:

```bash
remohop dev click 412 230
remohop dev move 800 12
remohop dev type "hello world"
remohop dev press ctrl+a Delete
remohop dev scroll 0 5
```

If you measured coordinates in a downscaled screenshot, pass its size:

```bash
remohop dev screenshot --max-width 1280 -o s.png
remohop dev click 640 360 --scale-from 1280x720
```

Use `-d` with a monitor index or name from `screenshot --list`. Do not type secrets with `type`: the text passes through your terminal or agent transcript.
