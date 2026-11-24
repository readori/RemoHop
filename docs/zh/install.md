# 安装

[English](../en/install.md)

每次推送 `v*` 标签，发布工作流都会构建发布版本。目前还没有发布过任何版本，下面的下载命令要等首个发布版本之后才能用；在此之前请[从源码编译](#从源码编译)。

| 平台 | 命令 |
|---|---|
| Linux、macOS | `curl -fsSLO https://github.com/readori/RemoHop/releases/latest/download/install.sh`，先读一遍，再执行 `sh install.sh` |
| Windows | `Invoke-WebRequest https://github.com/readori/RemoHop/releases/latest/download/install.ps1 -OutFile install.ps1`，先读一遍，再执行 `powershell -ExecutionPolicy Bypass -File .\install.ps1` |
| Debian、Ubuntu | 从发布页下载 `remohop_<版本>_<amd64\|arm64>.deb`，然后 `sudo apt install ./remohop_*.deb` |
| Fedora、RHEL、SUSE | 下载 `remohop-<版本>-1.<x86_64\|aarch64>.rpm`，然后 `sudo dnf install ./remohop-*.rpm` |
| Alpine | 下载 `remohop_<版本>_<x86_64\|aarch64>.apk`，然后 `sudo apk add --allow-untrusted ./remohop_*.apk` |
| Docker（仅汇合服务器） | `docker run ... ghcr.io/readori/remohop-signal:<版本>`，见 [packaging/docker/README.md](../../packaging/docker/README.md) |

安装脚本会下载与你的系统和 CPU 对应的压缩包，用发布页的 `SHA256SUMS` 校验；如果装了 [cosign](https://docs.sigstore.dev/cosign/system_config/installation/)，还会校验该文件的签名；然后把 `remohop` 复制到 `~/.local/bin`（root 用户为 `/usr/local/bin`；Windows 为 `%LOCALAPPDATA%\Programs\RemoHop`，并加入当前用户的 `PATH`）。脚本从不使用 `sudo`，也不会修改 shell 配置文件。选项：`--version vX.Y.Z`、`--bindir DIR`、`--verify require`（签名校验不通过就失败）；`install.ps1` 对应 `-Version`、`-BinDir`、`-Verify`、`-NoPath`。

包管理器（计划中；tap、bucket 和软件包尚未发布，下表是模板，见 [packaging/README.md](../../packaging/README.md)）：

| 包管理器 | 发布后的命令 |
|---|---|
| Homebrew（macOS、Linux） | `brew install <owner>/<tap>/remohop` |
| Scoop（Windows） | `scoop bucket add <名称> <bucket 地址>`，然后 `scoop install remohop` |
| winget（Windows） | `winget install <Publisher>.RemoHop` |
| AUR（Arch） | `yay -S remohop-bin` |
| Nix | 对生成的 `remohop.nix` 使用 `callPackage` |

说明：

- **macOS：** 发布版本只有一个通用二进制（同时支持 Apple 芯片和 Intel），由发布工作流用 Developer ID 签名并提交 Apple 公证。首次运行时 Gatekeeper 会联网检查公证结果；裸可执行文件无法携带离线票据。
- **Windows：** 二进制**尚未做代码签名**，首次运行时 SmartScreen 可能给出警告。在签名就绪之前，请用校验和与 cosign 签名来验证下载。
- **Linux 软件包**会安装 `remohop` 和 systemd 单元文件，但不会启用任何东西。让被控端开机或登录时自启的受支持方式是 `remohop service install`。
- **手动验证下载：**

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
