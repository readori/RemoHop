# Install

[中文](../zh/install.md)

Releases are built by the release workflow on every `v*` tag and published on [GitHub Releases](https://github.com/readori/RemoHop/releases); the commands below always fetch the latest one. You can also [build from source](#from-source).

| Platform | Command |
|---|---|
| Linux, macOS | `curl -fsSLO https://github.com/readori/RemoHop/releases/latest/download/install.sh`, read it, then `sh install.sh` |
| Windows | `Invoke-WebRequest https://github.com/readori/RemoHop/releases/latest/download/install.ps1 -OutFile install.ps1`, read it, then `powershell -ExecutionPolicy Bypass -File .\install.ps1` |
| Debian, Ubuntu | download `remohop_<version>_<amd64\|arm64>.deb` from the release, then `sudo apt install ./remohop_*.deb` |
| Fedora, RHEL, SUSE | download `remohop-<version>-1.<x86_64\|aarch64>.rpm`, then `sudo dnf install ./remohop-*.rpm` |
| Alpine | download `remohop_<version>_<x86_64\|aarch64>.apk`, then `sudo apk add --allow-untrusted ./remohop_*.apk` |
| Docker (rendezvous server only) | `docker run ... ghcr.io/readori/remohop-signal:<version>`, see [packaging/docker/README.md](../../packaging/docker/README.md) |

The scripts download the archive for your OS and CPU, check it against the release's `SHA256SUMS`, check that file's signature when [cosign](https://docs.sigstore.dev/cosign/system_config/installation/) is installed (it must come from this repository's release workflow for exactly the tag being installed; "latest" is first resolved to its tag), check that the binary reports that version, and copy `remohop` to `~/.local/bin` (as root: `/usr/local/bin`; Windows: `%LOCALAPPDATA%\Programs\RemoHop`, added to your user `PATH`). They never use `sudo` and never edit shell profiles. Options: `--version vX.Y.Z`, `--bindir DIR`, `--verify require` (fail unless the signature checks out); `install.ps1` takes `-Version`, `-BinDir`, `-Verify`, `-NoPath`.

Package managers:

| Manager | Command |
|---|---|
| Homebrew (macOS, Linux) | `brew install readori/tap/remohop` |
| Homebrew cask (macOS, the desktop app) | `brew install --cask readori/tap/remohop-quick` |
| Scoop (Windows) | `scoop bucket add readori https://github.com/readori/scoop-bucket` then `scoop install remohop` |
| winget (Windows; **not published yet**) | `winget install RemoHop.RemoHop` |
| AUR (Arch; **not published yet**) | `yay -S remohop-bin` |
| Snap (Linux; **coming soon**: the Snap Store listing is not public yet) | `sudo snap install remohop --classic`; also installs quick support as `remohop.quick` with a launcher entry. See [packaging/snap/README.md](../../packaging/snap/README.md) |
| Nix | `callPackage` of the generated `remohop.nix` |

The tap and the bucket are updated by hand after each release, so they can trail the newest release for a while.

Notes:

- **macOS:** releases carry one universal (Apple silicon and Intel) binary, which is signed with a Developer ID and notarized by Apple; the release workflow rebuilds it from the tagged source and checks that the signed file is exactly that code. Gatekeeper checks the notarization online on first run; a bare executable cannot carry an offline ticket.
- **Windows:** the binaries are **not code-signed yet**, so SmartScreen may warn on first run. The checksum and cosign signature are how to verify a download until signing is set up.
- **Linux packages** install `remohop` and the systemd unit files and enable nothing on a first install. Enable a unit with `systemctl` (the package says how) or use `remohop service install`; either way `remohop service status`, `logs`, `restart` and `uninstall` manage it.

## Running the host as a service

`remohop service install` runs the host at boot or login (details: `remohop service --help`).

- A **per-user** service (the default without root) runs as you, from wherever `remohop` is installed.
- A **system** service (`sudo remohop service install --system` on Linux and macOS) runs as root, and every **Windows** service runs as LocalSystem. Whoever can change the binary it starts would get those rights, so:
  - On Linux and macOS, install refuses a binary that another account can change, such as `~/.local/bin/remohop` or one in a Homebrew prefix, and prints the commands that copy it where only root can write (`/usr/local/bin`, or `/Library/RemoHop` on macOS). Simplest on Linux: install with `sudo sh install.sh`, which puts `remohop` in `/usr/local/bin`.
  - On Windows, install copies `remohop.exe` to `%ProgramFiles%\RemoHop` and the service runs that copy. Its state lives in `%ProgramData%\RemoHop`, owned by the Administrators and open only to SYSTEM and Administrators; install refuses an existing directory there that another user created.

## Upgrading

Install the new version the way you installed the old one. A host that is already running keeps the old version until it restarts:

| Installed with | Then |
|---|---|
| `install.sh` | `remohop service restart` (the script reminds you when a service runs). A system service runs its own root-owned copy: replace it (`sudo sh install.sh`, or the `sudo install` command `service install` printed) and run `sudo remohop service restart` |
| `install.ps1`, Scoop | from an elevated prompt, `remohop service install` again: it stops the service, replaces its copy in Program Files and starts it. A `remohop host` you started yourself: stop and start it |
| `.deb`, `.rpm` | a running system service (`remohop.service`) is restarted by the upgrade, unless the upgrade runs inside a RemoHop shell (a restart would cut that shell off); then run `sudo systemctl restart remohop.service` afterwards. Per-user services: `systemctl --user restart remohop.service` |
| Homebrew | `brew upgrade remohop`, then `remohop service restart` (the service names Homebrew's `bin/remohop` link, which the upgrade keeps) |
| Snap | after a refresh, `sudo remohop service restart` (the unit runs `/snap/remohop/current/...`, which follows refreshes) |

## Uninstalling

Remove the service before the binary, or it keeps trying to start a program that no longer exists: `remohop service uninstall` (`sudo` for a system service; elevated on Windows, which also deletes the copy in Program Files). Then remove the package or the binary. Removing the `.deb`/`.rpm` stops and disables the system unit; `apt purge remohop` also deletes `/var/lib/remohop`.

What stays behind is the hosts' state (identity key, password, pairings): `~/.config/remohop` (macOS: `~/Library/Application Support/remohop`), and for a system service `/var/lib/remohop`, `/Library/Application Support/remohop` or `%ProgramData%\RemoHop`. Delete those directories to forget the machine's identity.

## Desktop app

The [desktop app](desktop-app.md) (connect to your computers, let others connect to this one) is a download of its own in each release:

| System | File | Then |
|---|---|---|
| Windows 10/11 | `RemoHop-windows-amd64.zip` (ARM64: `RemoHop-windows-arm64.zip`) | unzip, double-click `RemoHop.exe`; not code-signed yet, so SmartScreen warns ("More info", "Run anyway"). `install.ps1 -App` installs it with a Start menu entry |
| macOS 12+ | `RemoHop-macos.dmg` (Apple silicon and Intel) | open it, drag `RemoHop.app` to Applications, open it from there; signed with a Developer ID, notarized and stapled |
| Linux | `remohop-app_linux_amd64.tar.gz` (ARM64: `..._arm64.tar.gz`) | unpack, run `./remohop-app` (`remohop.desktop` and `remohop.png` are a menu entry and its icon). It needs glibc, and WebKitGTK for its own window (else it opens in a browser); on musl systems such as Alpine, run `remohop app` from the static `remohop` instead, which opens in a browser |

## Quick support

The double-click program for the person being helped ([Quick support](quick-support.md)): nothing to install, closing its window ends it. It is the same program as the desktop app under a name that makes it start in quick-support mode:

| System | File | Then |
|---|---|---|
| Windows 10/11 | `RemoHop-QuickSupport-windows-amd64.zip` (ARM64: `RemoHop-QuickSupport-windows-arm64.zip`) | unzip, double-click `RemoHop-QuickSupport.exe` (SmartScreen as above) |
| macOS 12+ | `RemoHop-QuickSupport-macos.zip` (Apple silicon and Intel) | unzip (Safari does it by itself), open `RemoHop Quick Support.app`; signed, notarized and stapled |
| Linux | `remohop-quick_linux_amd64.tar.gz` (ARM64: `..._arm64.tar.gz`) | unpack, run `./remohop-quick` |

Each is at `https://github.com/readori/RemoHop/releases/latest/download/<file>` and covered by the release's `SHA256SUMS`. A Microsoft Store version is in preparation.

## Verifying a download

Every file of a release is listed in its `SHA256SUMS`, and that file carries a keyless [cosign](https://docs.sigstore.dev/cosign/system_config/installation/) signature which ties it to this repository's release workflow and the tag. Download `SHA256SUMS`, `SHA256SUMS.sigstore.json` and the files you want into one directory, then:

```bash
sha256sum --ignore-missing -c SHA256SUMS
cosign verify-blob --bundle SHA256SUMS.sigstore.json \
  --certificate-identity https://github.com/readori/RemoHop/.github/workflows/release.yml@refs/tags/vX.Y.Z \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com SHA256SUMS
```

Put the tag you downloaded in place of `vX.Y.Z`: a signature names the tag it was made for, so an older release cannot pass for the one you asked for.

Each release also lists its dependencies in `remohop_sbom.cdx.json` (CycloneDX).

## From source

With the Go version given in `go.mod`:

```bash
make build
```

This writes `bin/remohop` with `CGO_ENABLED=0`. Put it on your `PATH`. A plain `go build -o remohop ./cmd/remohop` also works.
