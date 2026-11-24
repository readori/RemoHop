# 安装

[English](../en/install.md)

每次推送 `v*` 标签，发布工作流都会构建发布版本并发布到 [GitHub Releases](https://github.com/readori/RemoHop/releases)；下面的命令总是下载最新版本。也可以[从源码编译](#从源码编译)。

| 平台 | 命令 |
|---|---|
| Linux、macOS | `curl -fsSLO https://github.com/readori/RemoHop/releases/latest/download/install.sh`，先读一遍，再执行 `sh install.sh` |
| Windows | `Invoke-WebRequest https://github.com/readori/RemoHop/releases/latest/download/install.ps1 -OutFile install.ps1`，先读一遍，再执行 `powershell -ExecutionPolicy Bypass -File .\install.ps1` |
| Debian、Ubuntu | 从发布页下载 `remohop_<版本>_<amd64\|arm64>.deb`，然后 `sudo apt install ./remohop_*.deb` |
| Fedora、RHEL、SUSE | 下载 `remohop-<版本>-1.<x86_64\|aarch64>.rpm`，然后 `sudo dnf install ./remohop-*.rpm` |
| Alpine | 下载 `remohop_<版本>_<x86_64\|aarch64>.apk`，然后 `sudo apk add --allow-untrusted ./remohop_*.apk` |
| Docker（仅汇合服务器） | `docker run ... ghcr.io/readori/remohop-signal:<版本>`，见 [packaging/docker/README.md](../../packaging/docker/README.md) |

安装脚本会下载与你的系统和 CPU 对应的压缩包，用发布页的 `SHA256SUMS` 校验；如果装了 [cosign](https://docs.sigstore.dev/cosign/system_config/installation/)，还会校验该文件的签名；然后把 `remohop` 复制到 `~/.local/bin`（root 用户为 `/usr/local/bin`；Windows 为 `%LOCALAPPDATA%\Programs\RemoHop`，并加入当前用户的 `PATH`）。脚本从不使用 `sudo`，也不会修改 shell 配置文件。选项：`--version vX.Y.Z`、`--bindir DIR`、`--verify require`（签名校验不通过就失败）；`install.ps1` 对应 `-Version`、`-BinDir`、`-Verify`、`-NoPath`。

包管理器（计划中；tap、bucket 和软件包尚未发布，见 [packaging/README.md](../../packaging/README.md)）：

| 包管理器 | 发布后的命令 |
|---|---|
| Homebrew（macOS、Linux） | `brew install readori/tap/remohop` |
| Homebrew cask（macOS，快速协助） | `brew install --cask readori/tap/remohop-quick` |
| Scoop（Windows） | `scoop bucket add readori https://github.com/readori/scoop-bucket`，然后 `scoop install remohop` |
| winget（Windows） | `winget install RemoHop.RemoHop` |
| AUR（Arch） | `yay -S remohop-bin` |
| Snap（Linux；**即将推出**：Snap Store 页面尚未公开） | `sudo snap install remohop --classic`；同时以 `remohop.quick` 安装快速协助，并带有启动器图标。见 [packaging/snap/README.md](../../packaging/snap/README.md) |
| Nix | 对生成的 `remohop.nix` 使用 `callPackage` |

说明：

- **macOS：** 发布版本只有一个通用二进制（同时支持 Apple 芯片和 Intel），由发布工作流用 Developer ID 签名并提交 Apple 公证。首次运行时 Gatekeeper 会联网检查公证结果；裸可执行文件无法携带离线票据。
- **Windows：** 二进制**尚未做代码签名**，首次运行时 SmartScreen 可能给出警告。在签名就绪之前，请用校验和与 cosign 签名来验证下载。
- **Linux 软件包**会安装 `remohop` 和 systemd 单元文件，但不会启用任何东西。让被控端开机或登录时自启的受支持方式是 `remohop service install`。

## 快速协助

给被协助的人双击打开的程序（见[快速协助](quick-support.md)）在每个发布版本里单独提供：

| 系统 | 文件 | 然后 |
|---|---|---|
| Windows 10/11 | `RemoHop-windows-amd64.zip`（ARM64：`RemoHop-windows-arm64.zip`） | 解压，双击 `RemoHop.exe`；尚未做代码签名，SmartScreen 会提示（点“更多信息”，再点“仍要运行”） |
| macOS 12+ | `RemoHop-macos.dmg`（Apple 芯片和 Intel） | 打开后把 `RemoHop.app` 拖到“应用程序”文件夹，再从那里打开；已用 Developer ID 签名、经 Apple 公证并附带公证票据 |
| Linux | `remohop-quick_linux_amd64.tar.gz`（ARM64：`..._arm64.tar.gz`） | 解压，运行 `./remohop-quick` |

下载地址都是 `https://github.com/readori/RemoHop/releases/latest/download/<文件名>`，都列在发布页的 `SHA256SUMS` 里。Microsoft Store 版本正在准备中。

## 校验下载

发布版本里的每个文件都列在 `SHA256SUMS` 中，这个文件带有 [cosign](https://docs.sigstore.dev/cosign/system_config/installation/) 无密钥签名，证明它出自本仓库针对该标签运行的发布工作流。把 `SHA256SUMS`、`SHA256SUMS.sigstore.json` 和要校验的文件下载到同一个目录，然后执行：

```bash
sha256sum --ignore-missing -c SHA256SUMS
cosign verify-blob --bundle SHA256SUMS.sigstore.json \
  --certificate-identity-regexp '^https://github\.com/readori/RemoHop/\.github/workflows/release\.yml@refs/tags/v.+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com SHA256SUMS
```

每个发布版本还附带 `remohop_sbom.cdx.json`（CycloneDX 格式的依赖清单）。

## 从源码编译

使用 `go.mod` 中指定的 Go 版本：

```bash
make build
```

这会以 `CGO_ENABLED=0` 生成 `bin/remohop`。把它放进 `PATH` 即可。直接执行 `go build -o remohop ./cmd/remohop` 也可以。
