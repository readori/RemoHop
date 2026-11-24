# Install

[中文](../zh/install.md)

Releases are built by the release workflow on every `v*` tag. No release has been published yet, so the download commands below work only from the first release; until then, [build from source](#from-source).

| Platform | Command |
|---|---|
| Linux, macOS | `curl -fsSLO https://github.com/readori/RemoHop/releases/latest/download/install.sh`, read it, then `sh install.sh` |
| Windows | `Invoke-WebRequest https://github.com/readori/RemoHop/releases/latest/download/install.ps1 -OutFile install.ps1`, read it, then `powershell -ExecutionPolicy Bypass -File .\install.ps1` |
| Debian, Ubuntu | download `remohop_<version>_<amd64\|arm64>.deb` from the release, then `sudo apt install ./remohop_*.deb` |
| Fedora, RHEL, SUSE | download `remohop-<version>-1.<x86_64\|aarch64>.rpm`, then `sudo dnf install ./remohop-*.rpm` |
| Alpine | download `remohop_<version>_<x86_64\|aarch64>.apk`, then `sudo apk add --allow-untrusted ./remohop_*.apk` |
| Docker (rendezvous server only) | `docker run ... ghcr.io/readori/remohop-signal:<version>`, see [packaging/docker/README.md](../../packaging/docker/README.md) |

The scripts download the archive for your OS and CPU, check it against the release's `SHA256SUMS`, check that file's signature when [cosign](https://docs.sigstore.dev/cosign/system_config/installation/) is installed, and copy `remohop` to `~/.local/bin` (as root: `/usr/local/bin`; Windows: `%LOCALAPPDATA%\Programs\RemoHop`, added to your user `PATH`). They never use `sudo` and never edit shell profiles. Options: `--version vX.Y.Z`, `--bindir DIR`, `--verify require` (fail unless the signature checks out); `install.ps1` takes `-Version`, `-BinDir`, `-Verify`, `-NoPath`.

Package managers (planned; the tap, bucket and packages are not published yet, so these are templates, see [packaging/README.md](../../packaging/README.md)):

| Manager | Command once published |
|---|---|
| Homebrew (macOS, Linux) | `brew install <owner>/<tap>/remohop` |
| Scoop (Windows) | `scoop bucket add <name> <bucket-url>` then `scoop install remohop` |
| winget (Windows) | `winget install <Publisher>.RemoHop` |
| AUR (Arch) | `yay -S remohop-bin` |
| Nix | `callPackage` of the generated `remohop.nix` |

Notes:

- **macOS:** releases carry one universal (Apple silicon and Intel) binary, which the release workflow signs with a Developer ID and submits to Apple for notarization. Gatekeeper checks the notarization online on first run; a bare executable cannot carry an offline ticket.
- **Windows:** the binaries are **not code-signed yet**, so SmartScreen may warn on first run. The checksum and cosign signature are how to verify a download until signing is set up.
- **Linux packages** install `remohop` and the systemd unit files and enable nothing. `remohop service install` is the supported way to run the host at boot or login.
- **Verify a download by hand:**

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
