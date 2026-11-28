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
- **Key frames.** When the viewer needs a key frame (it starts or recovers its decoder, or the host dropped frames on a slow link), the host asks the running encoder for one instead of starting a new encoder: ffmpeg's `zmq` filter takes the request over a Unix socket in a private temporary directory, and the next frame is coded as a key frame. Measured with libx264 on Linux, requests at random points of the frame period (`REMOHOP_KEYFRAME_REPORT=1 go test ./internal/svc/desktop -run TestKeyframeReportXvfb -v`), the wait from request to key frame fell from a median 120 ms (p95 143 ms) to 19 ms (p95 34 ms) at 30 fps, and from 89 ms (95 ms) to 11 ms (19 ms) at 60 fps, with no more encoder CPU; a second request soon after waits at most half a second, not the two seconds that spaced out restarts. This needs an ffmpeg with the `zmq` filter (`ffmpeg -hide_banner -filters | grep zmq`; it comes with libzmq, which Ubuntu's ffmpeg has). Without it, for an encoder that ignores forced key frames (SVT-AV1 1.7 with ffmpeg 6.1, as measured here; possibly Media Foundation and AMF), or with `REMOHOP_FORCE_KEYFRAMES=0` on the host, a key frame takes an encoder restart as before. Another size, frame rate, bitrate or monitor always restarts the encoder. Only libx264 and libaom are verified (on Linux); the GPU encoders, macOS and Windows are not tested.
- **Viewer.** The page has buttons for key combinations a browser keeps to itself (Ctrl+Alt+Del, Alt+Tab, the Windows key), Unicode typing, quality presets (native size, or at most 2160p down to 480p), a frame-rate menu, monitor switching, locking the remote screen, clipboard transfer, file upload, audio and recording. The frame rate is Auto, 30, 60, 90, 120 or 144 fps; Auto asks for the display's refresh rate when it is 90 Hz or more (at most 120), else 60, and the choice applies at once and is remembered on the device. **Stats** overlays the resolution, codec and encoder (GPU or CPU), bitrate, latency and round trip, the host's frame rate, and the frames dropped at the host and skipped in the viewer, in total and over the last 10 seconds.
- **Pointer.** The page draws the remote mouse pointer itself, as the picture's cursor, so it follows your hand at your mouse's own rate instead of arriving with the next frame. The host leaves the pointer out of the video and sends its shape when it changes (an image with its hotspot, sent once and then named by an ID) and its position and visibility when they change; the page scales the shape to the picture's size on screen, sharp on high-density displays. When an application on the host moves the pointer itself, or the mouse is outside the picture, or the session is view-only, the page also draws the pointer where the host says it is; it never moves your own pointer. A pointer that the remote app hides (a game, a video player) is hidden over the picture too. This works on Windows hosts (GetCursorInfo; monochrome pointers such as the text I-beam, which invert the screen, are drawn black with a white outline) and on Linux X11 hosts (XFixes). The desktop app's Desktop tab and the browser client (`remohop web`) draw the pointer the same way. macOS hosts, the Windows service's console capture (sign-in and lock screens, UAC prompts) and older hosts keep the pointer in the video, as does a pointer the host cannot read (it falls back after two seconds), and so does the video while a recording runs. The Windows side is cross-compiled and unit-tested on Linux, not run on a real Windows machine yet; pointer images there are as large as Windows makes them for the pointer size setting and display scale.
- **Mouse lock.** For games and 3D apps, **Lock mouse** (or Ctrl+Alt+Shift+M, which the page keeps for itself) locks the mouse to the picture with the browser's Pointer Lock and sends its movement instead of positions; Esc or the shortcut releases it. The page asks for raw movement where the browser offers it (`unadjustedMovement`, Chromium), so this machine's pointer acceleration, macOS's in particular, does not add to the host's. When a remote app hides its pointer the page suggests the lock; with **Auto lock** on, the next click on the picture locks the mouse (that click is not passed on). The host injects relative moves as a mouse does: SendInput without absolute coordinates on Windows (Windows applies its pointer speed; games that read raw input get the movement as sent), XTEST relative motion on X11, and Quartz events with their delta fields on macOS (not verified on a Mac). Relative movement is input like any other: it needs the host's `input` feature and is refused in a view-only session. The desktop app's Desktop tab and `remohop web` have the same **Lock mouse** and **Auto lock** buttons and shortcut (hidden on touch screens); there the gateway passes relative movement on only while the session is not view-only.
- **Recording.** `--record FILE.mp4` (or the Record button) writes the received stream to a fragmented MP4 on this machine without re-encoding.
- **Audio.** `--audio` (or the page's Audio button; Mute silences it without stopping it) forwards what the host plays: the host captures its output with ffmpeg, encodes Opus (libopus, low-delay mode, 20 ms frames, 96 kbit/s) and sends it on its own stream next to the video, so a late video frame never delays sound. The page decodes it with WebCodecs and plays it through Web Audio with a jitter buffer, scheduling every packet against its capture time and the video's own latency so picture and sound stay together. Audio is off unless asked for, belongs to the host's `screen` feature (a host with `screen` off serves none), can be switched off alone with `REMOHOP_DESKTOP_AUDIO=off` on the host, and only ever captures the machine's output, never a microphone. When the host cannot capture, `--audio` stops with the reason and what to install, and the page's button shows the same message. The desktop app's Desktop tab and `remohop web` have the same **Audio** and **Mute** buttons; a web engine without a WebCodecs audio decoder says so instead (WebKitGTK, which the desktop app uses on Linux, may have none). What the host needs:

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

The viewer listens on `127.0.0.1` behind a random URL; Ctrl-C ends it. The host needs ffmpeg with an H.264 or AV1 encoder (see [Screen sharing and ffmpeg](#screen-sharing-and-ffmpeg) and [troubleshooting.md](../troubleshooting.md#ffmpeg-and-encoders)).

A macOS host needs the Screen Recording permission to capture and Accessibility for input. macOS gives the screen to one capture at a time, so all sessions of a macOS host share a single capture; `REMOHOP_MAC_DIRECT_CAPTURE=1` on the host turns that off (one capture per session) if the shared one misbehaves.

## Screen sharing and ffmpeg

The host (the computer whose screen is shared) encodes its screen with ffmpeg, which it runs as a separate program. It uses, in this order:

1. the program that `REMOHOP_FFMPEG` names;
2. `ffmpeg` on `PATH`, the system's (`winget install Gyan.FFmpeg`, `brew install ffmpeg`, `apt install ffmpeg`);
3. RemoHop's own copy, on Windows and macOS.

RemoHop's copy is downloaded only when someone asks for it: `remohop ffmpeg install`, or **Install screen sharing component** in the desktop app (on Home, while incoming connections are on) and in the Quick Support window, which show its progress. It is a build pinned in RemoHop's source: a fixed, versioned file and its SHA-256. The download goes over HTTPS, through the proxy that `HTTPS_PROXY` names if there is one, is checked against that hash before it is unpacked, and only the `ffmpeg` program is taken out of it. A file that does not match is deleted and never run.

| Host | Build | Download |
|---|---|---|
| Windows x64 | ffmpeg 9.1 "essentials" from [gyan.dev](https://www.gyan.dev/ffmpeg/builds/) (its GitHub mirror first) | 119 MB |
| Windows arm64 | ffmpeg 9.0.2 from [BtbN/FFmpeg-Builds](https://github.com/BtbN/FFmpeg-Builds) (the build of 31 October 2026) | 137 MB |
| macOS, Apple silicon / Intel | ffmpeg 9.1 from [Martin Riedl's build server](https://ffmpeg.martin-riedl.de/) (signed; macOS 12 or newer) | 29 MB / 34 MB |

All three are GPL builds. They include libx264, the fast software H.264 encoder the host falls back to where no hardware encoder works (a virtual machine, a computer without a GPU); builds under the LGPL lack it. RemoHop runs ffmpeg as a separate program and does not ship it: the computer downloads it from its publisher.

The program goes to `%LocalAppData%\remohop\ffmpeg\ffmpeg.exe` on Windows and to `~/Library/Application Support/remohop/ffmpeg/ffmpeg` on macOS (under `REMOHOP_CONFIG_DIR`, when that is set), next to `ffmpeg.json`, which says what it is. On macOS RemoHop clears the download quarantine of that one file. `remohop ffmpeg` (and `remohop doctor`) shows which ffmpeg is used and its version; `remohop ffmpeg remove` deletes RemoHop's copy. A newer RemoHop may pin a newer build: `remohop ffmpeg` then says so, and `remohop ffmpeg install` replaces the copy.

On managed computers, `REMOHOP_FFMPEG_DOWNLOAD=off` in the environment (or `remohop config set ffmpeg-download off`) switches downloads off: the windows then say so and offer no button. A copy that is already there is still used.

The Windows service (unattended access) runs as LocalSystem with its own directory, `%ProgramData%\RemoHop`, and never uses a copy from a user's profile: that user could replace it with a program the service would then run as LocalSystem. It finds ffmpeg the same way, with its own environment: `REMOHOP_FFMPEG` in the service's environment, the machine's `PATH` (not a user's), then a copy of its own in `%ProgramData%\RemoHop\ffmpeg`. The desktop app gives it that copy: when **Unattended access** is turned on and the service has no ffmpeg, the same administrator step that installs the service downloads the pinned build again, checks it the same way, and installs it there (the switch shows *Working…* until it is done). With unattended access already on, **Install screen sharing component** asks Windows for an administrator once more and installs the service's copy too. From an elevated prompt, `remohop ffmpeg install --service` does the same, and `remohop ffmpeg` shows what the service uses.

The service's copy is not copied from the user's: it is downloaded and checked anew, as the administrator. Its directory belongs to the Administrators and only SYSTEM and the Administrators can open it, like the rest of `%ProgramData%\RemoHop`; a directory there that someone else made, or that contains links, is moved aside first. The service itself runs its copy only when that is still so. Downloads switched off by `REMOHOP_FFMPEG_DOWNLOAD=off` (in the user's or the machine's environment), by the user's setting or by the service's own (`set REMOHOP_CONFIG_DIR=%ProgramData%\RemoHop & remohop config set ffmpeg-download off`, elevated) install nothing for it. On macOS and Linux the unattended-access service runs as the user, with the user's directory, and uses the user's copy.

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
