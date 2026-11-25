# Platform support and limitations

[中文](../zh/platforms.md)

## Platform support

Linux is the reference platform. macOS and Windows are tested by automated runs on hosted runners, not by a person on a physical desktop. The repository's own CI (`.github/workflows/ci.yml`) runs the tests, including the NAT lab, on Linux only and cross-compiles for macOS and Windows; the macOS and Windows results below come from separate test runs on GitHub-hosted macOS and Windows runners.

| | Linux | macOS | Windows |
|---|---|---|---|
| Automated tests (hosted runners) | all pass, including the NAT lab, systemd service, USB/IP with the real kernel, SMTP accounts and a Cloudflare tunnel | all pass, including service install, WebDAV mount, utun VPN, remote desktop and input | all pass, including service install, WebDAV mount (Windows 11), SendInput, screenshots, remote desktop, Wintun VPN and unattended access |
| Shell | PTY | PTY | ConPTY; commands in PowerShell |
| Files, forwards, clipboard, screenshot | yes | yes | yes; resumable downloads are off on Windows clients |
| Mount | FUSE (or `--webdav` with davfs2) | WebDAV (built in); macFUSE optional, untested | WebDAV on a drive letter; verified on a hosted Windows 11 runner |
| Remote desktop (host) | X11 | AVFoundation capture, VideoToolbox or software H.264/AV1 (verified on a hosted macOS VM); input with the Accessibility permission (capture needs Screen Recording) | ddagrab capture, gdigrab fallback without a GPU (the fallback is verified on hosted runners) |
| GUI input | X11 | Quartz events (CoreGraphics via purego, no cgo); host needs the Accessibility permission; verified on a hosted macOS VM, not on a physical Mac | SendInput; verified on hosted Windows runners |
| VPN | yes (TUN) | client (utun) verified on a hosted macOS VM, `--exit` not run on a real network; cannot forward as a host | controlling side (Wintun, you supply the DLL), adapter verified on a hosted runner; cannot forward as a host |
| Serial (virtual port on this side) | yes | yes | remote side only |
| USB/IP | yes (verified with the real kernel on a hosted runner) | no | no |
| Service | systemd | launchd | Windows service; sees the console, sign-in and lock screens only with `--unattended` (verified on a hosted runner) |

## Status and limitations

- **NAT traversal can fail.** Two symmetric NATs, or a symmetric NAT facing a port-restricted one, cannot be punched; the connection fails in about a second with a diagnostic unless a relay is available. NATs that track unsolicited inbound packets are handled by low-TTL priming on Linux hosts over IPv4.
- **IPv6 paths and real consumer NAT hardware are not tested.** The NAT tests use Linux network namespaces.
- **Remote desktop.** Measured glass-to-glass in a browser on a CPU-only 4-core VM: H.264 at about 20 ms (720p) and 31 ms (1080p), against about 50 ms and 61 to 227 ms for software AV1. Hardware encoders are preferred. Of those, VideoToolbox (macOS) is verified: on a GitHub-hosted Apple Silicon macOS VM (one 1024x768 display at scale 1) the host picks it, and the decoded picture, pointer and click input, view-only and two viewers at once are checked there, as are libx264 and SVT-AV1 on macOS. Retina and multi-monitor Macs, the Lock control on macOS and physical Macs are not tested. NVENC, QSV, VAAPI, AMF and Media Foundation are untested, and 4K at 60 fps was not tested. When AV1 is forced or chosen in software, SVT-AV1 2.2 to 3.0 has an upstream bug in its real-time presets (small changes not shown until the next key frame), so RemoHop uses libaom on those hosts and says so. Input blocking and privacy blackout are reported as unsupported on X11 hosts. Native Wayland capture is not supported.
- **Serial** was verified with pseudo-terminals and picocom; no real UART was available. **USB/IP** was verified with the real kernel on a hosted runner (a dummy_hcd mass-storage gadget), not with physical USB devices.
- **The browser client** was verified in headless Chromium against a real host.
- **Shell sessions** survive client disconnects for the keep-alive period. They do not survive a restart of the host.
- **Packaging and releases.** `packaging/` holds the nfpm configuration (.deb, .rpm, .apk), the install scripts, systemd units, a launchd template, the rendezvous-server Dockerfile and templates for Homebrew, Scoop, winget, the AUR and Nix. Releases are published on GitHub Releases by the `v*` tag workflow; the macOS binary and `RemoHop.app` are signed and notarized, Windows binaries are unsigned. `remohop service install` is the supported way to run the host at boot.
- **Not implemented:** running a mobile device (Android or iOS) as a controlled host.
- **Untested on real hardware:** anything on a physical Mac (the macOS VPN client, desktop host and input are verified on a hosted VM only), Windows WebDAV mounts, USB/IP on a real kernel, GPU encoders other than VideoToolbox in that VM. The phone apps are tested in CI on every change (the iOS app in the Simulator, the Android app on an emulator); both are coming to the App Store and Google Play.
- **Accounts** are hosted by the rendezvous server and need the operator to switch them on. A server can withhold the newest log entries from a device; see [security.md](../security.md#accounts).
- **License:** AGPL-3.0, with a commercial license available; see [License](../../README.md#license).
