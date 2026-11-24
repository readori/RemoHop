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

The scripts download the archive for your OS and CPU, check it against the release's `SHA256SUMS`, check that file's signature when [cosign](https://docs.sigstore.dev/cosign/system_config/installation/) is installed, and copy `remohop` to `~/.local/bin` (as root: `/usr/local/bin`; Windows: `%LOCALAPPDATA%\Programs\RemoHop`, added to your user `PATH`). They never use `sudo` and never edit shell profiles. Options: `--version vX.Y.Z`, `--bindir DIR`, `--verify require` (fail unless the signature checks out); `install.ps1` takes `-Version`, `-BinDir`, `-Verify`, `-NoPath`.

Package managers (planned; the tap, bucket and packages are not published yet, see [packaging/README.md](../../packaging/README.md)):

| Manager | Command once published |
|---|---|
| Homebrew (macOS, Linux) | `brew install readori/tap/remohop` |
| Homebrew cask (macOS, quick support) | `brew install --cask readori/tap/remohop-quick` |
| Scoop (Windows) | `scoop bucket add readori https://github.com/readori/scoop-bucket` then `scoop install remohop` |
| winget (Windows) | `winget install RemoHop.RemoHop` |
| AUR (Arch) | `yay -S remohop-bin` |
| Nix | `callPackage` of the generated `remohop.nix` |

Notes:

- **macOS:** releases carry one universal (Apple silicon and Intel) binary, which the release workflow signs with a Developer ID and submits to Apple for notarization. Gatekeeper checks the notarization online on first run; a bare executable cannot carry an offline ticket.
- **Windows:** the binaries are **not code-signed yet**, so SmartScreen may warn on first run. The checksum and cosign signature are how to verify a download until signing is set up.
- **Linux packages** install `remohop` and the systemd unit files and enable nothing. `remohop service install` is the supported way to run the host at boot or login.

## Quick support

The double-click program for the person being helped ([Quick support](quick-support.md)) is a separate download in each release:

| System | File | Then |
|---|---|---|
| Windows 10/11 | `RemoHop-windows-amd64.zip` (ARM64: `RemoHop-windows-arm64.zip`) | unzip, double-click `RemoHop.exe`; not code-signed yet, so SmartScreen warns ("More info", "Run anyway") |
| macOS 12+ | `RemoHop-macos.dmg` (Apple silicon and Intel) | open it, drag `RemoHop.app` to Applications, open it from there; signed with a Developer ID, notarized and stapled |
| Linux | `remohop-quick_linux_amd64.tar.gz` (ARM64: `..._arm64.tar.gz`) | unpack, run `./remohop-quick` |

Each is at `https://github.com/readori/RemoHop/releases/latest/download/<file>` and covered by the release's `SHA256SUMS`. A Microsoft Store version is in preparation.

## Verifying a download

Every file of a release is listed in its `SHA256SUMS`, and that file carries a keyless [cosign](https://docs.sigstore.dev/cosign/system_config/installation/) signature which ties it to this repository's release workflow and the tag. Download `SHA256SUMS`, `SHA256SUMS.sigstore.json` and the files you want into one directory, then:

```bash
sha256sum --ignore-missing -c SHA256SUMS
cosign verify-blob --bundle SHA256SUMS.sigstore.json \
  --certificate-identity-regexp '^https://github\.com/readori/RemoHop/\.github/workflows/release\.yml@refs/tags/v.+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com SHA256SUMS
```

Each release also lists its dependencies in `remohop_sbom.cdx.json` (CycloneDX).

## From source

With the Go version given in `go.mod`:

```bash
make build
```

This writes `bin/remohop` with `CGO_ENABLED=0`. Put it on your `PATH`. A plain `go build -o remohop ./cmd/remohop` also works.
