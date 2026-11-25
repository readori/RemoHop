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

安装脚本会下载与你的系统和 CPU 对应的压缩包，用发布页的 `SHA256SUMS` 校验；如果装了 [cosign](https://docs.sigstore.dev/cosign/system_config/installation/)，还会校验该文件的签名（签名必须出自本仓库针对所安装标签运行的发布工作流；“最新版本”会先解析成具体标签）；并确认二进制报告的正是这个版本；然后把 `remohop` 复制到 `~/.local/bin`（root 用户为 `/usr/local/bin`；Windows 为 `%LOCALAPPDATA%\Programs\RemoHop`，并加入当前用户的 `PATH`）。脚本从不使用 `sudo`，也不会修改 shell 配置文件。选项：`--version vX.Y.Z`、`--bindir DIR`、`--verify require`（签名校验不通过就失败）；`install.ps1` 对应 `-Version`、`-BinDir`、`-Verify`、`-NoPath`。

包管理器：

| 包管理器 | 命令 |
|---|---|
| Homebrew（macOS、Linux） | `brew install readori/tap/remohop` |
| Homebrew cask（macOS，桌面应用） | `brew install --cask readori/tap/remohop-quick` |
| Scoop（Windows） | `scoop bucket add readori https://github.com/readori/scoop-bucket`，然后 `scoop install remohop` |
| winget（Windows；**尚未发布**） | `winget install RemoHop.RemoHop` |
| AUR（Arch；**尚未发布**） | `yay -S remohop-bin` |
| Snap（Linux；**即将推出**：Snap Store 页面尚未公开） | `sudo snap install remohop --classic`；同时以 `remohop.quick` 安装快速协助，并带有启动器图标。见 [packaging/snap/README.md](../../packaging/snap/README.md) |
| Nix | 对生成的 `remohop.nix` 使用 `callPackage` |

tap 和 bucket 在每次发布后手动更新，所以可能会比最新发布版本晚一段时间。

说明：

- **macOS：** 发布版本只有一个通用二进制（同时支持 Apple 芯片和 Intel），用 Developer ID 签名并经 Apple 公证；发布工作流会从打标签的源码重新构建，确认签过名的文件正是这份代码。首次运行时 Gatekeeper 会联网检查公证结果；裸可执行文件无法携带离线票据。
- **Windows：** 二进制**尚未做代码签名**，首次运行时 SmartScreen 可能给出警告。在签名就绪之前，请用校验和与 cosign 签名来验证下载。
- **Linux 软件包**会安装 `remohop` 和 systemd 单元文件，首次安装时不会启用任何东西。可以用 `systemctl` 启用单元（安装时会提示怎么做），也可以用 `remohop service install`；两种方式都可以用 `remohop service status`、`logs`、`restart` 和 `uninstall` 管理。

## 以服务方式运行被控端

`remohop service install` 让被控端开机或登录时自启（详见 `remohop service --help`）。

- **按用户**的服务（非 root 时的默认方式）以你自己的身份运行，用的就是 `remohop` 所在的位置。
- **系统**服务（Linux 和 macOS 上的 `sudo remohop service install --system`）以 root 运行，**Windows** 服务一律以 LocalSystem 运行。谁能改动它启动的二进制，谁就能拿到这些权限，因此：
  - 在 Linux 和 macOS 上，如果二进制能被其他账户改动（例如 `~/.local/bin/remohop` 或 Homebrew 目录里的），安装会拒绝，并给出把它复制到只有 root 能写的位置的命令（`/usr/local/bin`，macOS 上为 `/Library/RemoHop`）。Linux 上最简单的做法：用 `sudo sh install.sh` 安装，`remohop` 就会放进 `/usr/local/bin`。
  - 在 Windows 上，安装会把 `remohop.exe` 复制到 `%ProgramFiles%\RemoHop`，服务运行的是这份副本。它的状态目录是 `%ProgramData%\RemoHop`，属主为 Administrators，只有 SYSTEM 和 Administrators 能访问；如果那里已经有其他用户创建的目录，安装会拒绝。

## 升级

用安装旧版本的同一种方式安装新版本。已经在运行的被控端在重启之前仍是旧版本：

| 安装方式 | 之后 |
|---|---|
| `install.sh` | `remohop service restart`（有服务在运行时脚本会提醒）。系统服务运行的是它自己那份属于 root 的副本：先替换它（`sudo sh install.sh`，或 `service install` 打印的 `sudo install` 命令），再执行 `sudo remohop service restart` |
| `install.ps1`、Scoop | 在管理员命令提示符中再执行一次 `remohop service install`：它会停止服务、替换 Program Files 里的副本并重新启动。自己启动的 `remohop host`：停止后重新启动 |
| `.deb`、`.rpm` | 升级会重启正在运行的系统服务（`remohop.service`），除非升级本身是在 RemoHop shell 里执行的（重启会切断这个 shell）；这时请在升级完成后执行 `sudo systemctl restart remohop.service`。按用户的服务：`systemctl --user restart remohop.service` |
| Homebrew | `brew upgrade remohop`，然后 `remohop service restart`（服务引用的是 Homebrew 的 `bin/remohop` 链接，升级后仍然有效） |
| Snap | 刷新后执行 `sudo remohop service restart`（单元运行的是 `/snap/remohop/current/...`，会跟随刷新） |

## 卸载

先删除服务，再删除二进制，否则服务会不停尝试启动一个已经不存在的程序：`remohop service uninstall`（系统服务加 `sudo`；Windows 上在管理员命令提示符中执行，同时会删除 Program Files 里的副本）。然后再卸载软件包或删除二进制。卸载 `.deb`/`.rpm` 会停止并禁用系统单元；`apt purge remohop` 还会删除 `/var/lib/remohop`。

留下的是被控端的状态（身份密钥、密码、配对）：`~/.config/remohop`（macOS：`~/Library/Application Support/remohop`），系统服务则是 `/var/lib/remohop`、`/Library/Application Support/remohop` 或 `%ProgramData%\RemoHop`。删除这些目录即可让这台机器的身份作废。

## 桌面应用

[桌面应用](desktop-app.md)（连接你的电脑，也让别人连接这台电脑）在每个发布版本里单独提供：

| 系统 | 文件 | 然后 |
|---|---|---|
| Windows 10/11 | `RemoHop-windows-amd64.zip`（ARM64：`RemoHop-windows-arm64.zip`） | 解压，双击 `RemoHop.exe`；尚未做代码签名，SmartScreen 会提示（点“更多信息”，再点“仍要运行”）。`install.ps1 -App` 会安装它并添加开始菜单项 |
| macOS 12+ | `RemoHop-macos.dmg`（Apple 芯片和 Intel） | 打开后把 `RemoHop.app` 拖到“应用程序”文件夹，再从那里打开；已用 Developer ID 签名、经 Apple 公证并附带公证票据 |
| Linux | `remohop-app_linux_amd64.tar.gz`（ARM64：`..._arm64.tar.gz`） | 解压，运行 `./remohop-app`（`remohop.desktop` 和 `remohop.png` 是菜单项及其图标）。它需要 glibc，自己的窗口还需要 WebKitGTK（否则在浏览器中打开）；在 Alpine 等 musl 系统上，请改用静态链接的 `remohop` 运行 `remohop app`，它会在浏览器中打开 |

## 快速协助

给被协助的人双击打开的程序（见[快速协助](quick-support.md)）：无需安装，关闭窗口即结束。它与桌面应用是同一个程序，只是换了一个让它以快速协助方式启动的名字：

| 系统 | 文件 | 然后 |
|---|---|---|
| Windows 10/11 | `RemoHop-QuickSupport-windows-amd64.zip`（ARM64：`RemoHop-QuickSupport-windows-arm64.zip`） | 解压，双击 `RemoHop-QuickSupport.exe`（SmartScreen 提示同上） |
| macOS 12+ | `RemoHop-QuickSupport-macos.zip`（Apple 芯片和 Intel） | 解压（Safari 会自动解压），打开 `RemoHop Quick Support.app`；已签名、经公证并附带公证票据 |
| Linux | `remohop-quick_linux_amd64.tar.gz`（ARM64：`..._arm64.tar.gz`） | 解压，运行 `./remohop-quick` |

下载地址都是 `https://github.com/readori/RemoHop/releases/latest/download/<文件名>`，都列在发布页的 `SHA256SUMS` 里。Microsoft Store 版本正在准备中。

## 校验下载

发布版本里的每个文件都列在 `SHA256SUMS` 中，这个文件带有 [cosign](https://docs.sigstore.dev/cosign/system_config/installation/) 无密钥签名，证明它出自本仓库针对该标签运行的发布工作流。把 `SHA256SUMS`、`SHA256SUMS.sigstore.json` 和要校验的文件下载到同一个目录，然后执行：

```bash
sha256sum --ignore-missing -c SHA256SUMS
cosign verify-blob --bundle SHA256SUMS.sigstore.json \
  --certificate-identity https://github.com/readori/RemoHop/.github/workflows/release.yml@refs/tags/vX.Y.Z \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com SHA256SUMS
```

把 `vX.Y.Z` 换成你下载的标签：签名里写明了它所属的标签，旧版本就无法冒充你要的版本。

每个发布版本还附带 `remohop_sbom.cdx.json`（CycloneDX 格式的依赖清单）。

## 从源码编译

使用 `go.mod` 中指定的 Go 版本：

```bash
make build
```

这会以 `CGO_ENABLED=0` 生成 `bin/remohop`。把它放进 `PATH` 即可。直接执行 `go build -o remohop ./cmd/remohop` 也可以。
