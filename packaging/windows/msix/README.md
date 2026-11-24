# Microsoft Store package (MSIX) of quick support

RemoHop quick support (`RemoHop.exe`, see [packaging/quick](../../quick/README.md)) packaged as MSIX for the
Microsoft Store. The Store build is the same program as the `RemoHop-windows-*.zip` download: a full-trust
desktop app, started without arguments, so it runs `remohop quick`.

| File | What it is |
|---|---|
| `AppxManifest.xml.in` | the manifest, with `@IDENTITY_NAME@`, `@PUBLISHER@`, `@PUBLISHER_DISPLAY_NAME@`, `@VERSION@`, `@ARCH@` |
| `build.ps1` | builds `RemoHop_<ver>_x64.msix`, `RemoHop_<ver>_arm64.msix` and `RemoHop_<ver>.msixbundle` (Windows, Windows SDK) |

The logos are drawn by `go run ./packaging/quick/gen -msix-assets DIR` from the same code as the icon of
`RemoHop.exe`: `Square44x44Logo` (44 px, plus the unplated taskbar sizes 16, 24, 32, 48 and 256),
`Square71x71Logo`, `Square150x150Logo`, `Wide310x150Logo`, `Square310x310Logo` and `StoreLogo` (50 px), each at
100% and 200% scale. `makepri` indexes them into `resources.pri`, so the manifest names them without qualifiers.

## Build

On Windows with Go, Git Bash (for `packaging/quick/build.sh`) and the Windows 10/11 SDK:

```powershell
$env:TARGETS = 'windows'; sh packaging/quick/build.sh        # dist/quick/windows-{amd64,arm64}/RemoHop.exe
pwsh packaging/windows/msix/build.ps1 -Version v1.0.0 `
  -IdentityName '<Package/Identity/Name>' -Publisher '<Package/Identity/Publisher>' `
  -PublisherDisplayName '<Package/Properties/PublisherDisplayName>'
```

The three values are in Partner Center, under the app's **Product management > Product identity**. The package
version is the release version with a fourth part of `0` (`v1.2.3` gives `1.2.3.0`); the Store reserves the fourth
part, and every submission needs a higher version than the last one.

**Store submission:** upload `RemoHop_<ver>.msixbundle` unsigned; the Store signs it. The CI repository can build
it for you: run `remohop-windows-msix.yml` (Actions, workflow_dispatch) with the ref, the version, the three
identity values and `upload` ticked, and download the artifact `remohop-msix-store`.

**Local test install:** packages must be signed to install. Make a code-signing certificate whose subject equals
the Publisher, trust it, and pass it to `build.ps1` (in an elevated PowerShell):

```powershell
$cert = New-SelfSignedCertificate -Type Custom -Subject 'CN=RemoHop Test' -KeyUsage DigitalSignature `
  -CertStoreLocation Cert:\CurrentUser\My -TextExtension @('2.5.29.37={text}1.3.6.1.5.5.7.3.3', '2.5.29.19={text}')
$pw = Read-Host -AsSecureString 'pfx password'
Export-PfxCertificate -Cert $cert -FilePath test.pfx -Password $pw
Export-Certificate -Cert $cert -FilePath test.cer; Import-Certificate -FilePath test.cer -CertStoreLocation Cert:\LocalMachine\TrustedPeople
pwsh packaging/windows/msix/build.ps1 -Version v1.0.0 -IdentityName RemoHop.Test -Publisher 'CN=RemoHop Test' `
  -PublisherDisplayName 'RemoHop Test' -CertPfx test.pfx -CertPassword '<the password>'
Add-AppxPackage dist/msix/RemoHop_1.0.0.0_x64.msix
```

The CI workflow `remohop-windows-msix.yml` in the CI repository does exactly this on `windows-latest` (x64) and
`windows-11-arm` (arm64) for every request, then starts the installed app through its AppUserModelID and checks
that it works from inside the package (below).

## Decisions in the manifest, and what works under MSIX

- **Full trust.** `EntryPoint="Windows.FullTrustApplication"` and the restricted capability `runFullTrust`: a
  desktop program with the user's rights, not an AppContainer app. That is what lets it capture the screen
  (ddagrab/gdigrab through ffmpeg), inject input (SendInput), listen on UDP and serve its page on `127.0.0.1`
  (an AppContainer app may not receive loopback connections from another process, which the Edge window is).
  Partner Center asks why `runFullTrust` is needed: "desktop remote-support program: captures the screen and
  injects input for an approved helper, runs a local web UI and peer-to-peer networking".
- **No AppData virtualization.** `desktop6:FileSystemWriteVirtualization` and `RegistryWriteVirtualization` are
  `disabled`, with the restricted capability `unvirtualizedResources` (Windows 10 2004 / build 19041 or later, which
  is the package's `MinVersion`). With the default virtualization, new files in `%APPDATA%` go to a private
  per-package folder. Two things would break: Microsoft Edge, which runs outside the package, could not read the
  redirect file the window is opened through (`internal/quick/open.go`), and the packaged app and an installed
  `remohop` CLI would keep two identities (two IDs) for one computer. The CI run checks that `quick.url` lands in the
  real `%APPDATA%\remohop` and that the Edge window opens the page. Partner Center needs a justification for this
  capability too: "shares its configuration and identity in %APPDATA%\remohop with the RemoHop command-line tool and
  hands a local file to Microsoft Edge to open its window".
- **Approval dialogs** are `MessageBox` calls from the app's own process; nothing about MSIX changes them.
- **Firewall.** No `windows.firewallRules` extension: like the zip download, Windows Firewall may ask about network
  access on the first start, and connections work with either answer on most networks because the host connects
  outwards. To skip the question, add to `<Application>` (namespace
  `xmlns:desktop2="http://schemas.microsoft.com/appx/manifest/desktop/windows10/2"`):
  `<Extensions><desktop2:Extension Category="windows.firewallRules"><desktop2:FirewallRules Executable="RemoHop.exe"><desktop2:Rule Direction="in" IPProtocol="UDP" Profile="all"/></desktop2:FirewallRules></desktop2:Extension></Extensions>`.
  It has not been tried.
- **Languages.** The manifest declares `en-us` and `zh-cn` (the window follows the system language); the display
  name is "RemoHop" in both, so there are no localized manifest strings.

## Limitations

- **Updates come from the Store.** The program has no updater of its own, so nothing conflicts; but a user who also
  has the zip version has two copies with one shared identity and configuration (by design, see above).
- **The CLI is not in the package.** The Store app is quick support only. There is no `remohop` command-line alias
  (`uap3:AppExecutionAlias`); the CLI comes from the zip, `install.ps1`, Scoop or winget.
- **Unattended access and services** (`remohop service install`) need an elevated, unpackaged `remohop.exe`; a Store
  app cannot install a Windows service. Quick support never does.
- **Elevated windows.** As with the zip version, the app runs without administrator rights, so a helper cannot
  operate UAC prompts or windows of programs running as administrator.
- **Edge is required for the app window.** Where Microsoft Edge has been removed, the page opens in the default
  browser, as it does outside the package.
- **Store review** has not happened yet: both restricted capabilities need the justifications above, and the
  Windows App Certification Kit has not been run on the package.
