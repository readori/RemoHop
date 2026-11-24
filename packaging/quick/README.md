# Quick support packages

The double-click builds of `remohop quick` (the program is `cmd/remohop-quick`; the feature is `internal/quick`).

```bash
make quick                       # everything below, into dist/quick
EXTRA_LDFLAGS="-X github.com/readori/RemoHop/internal/dial.DefaultSignal=HOST:21116" make quick
```

| Output | What it is |
|---|---|
| `dist/quick/windows-amd64/RemoHop.exe`, `windows-arm64/RemoHop.exe` | Windows GUI program (`-H windowsgui`: no console window), with the icon and an application manifest linked in |
| `dist/quick/macos/RemoHop.app` | macOS bundle with a universal (arm64 + x86_64) program, `Info.plist` and `RemoHop.icns`. **Unsigned.** |
| `dist/quick/linux-amd64/remohop-quick`, `linux-arm64/remohop-quick` | the same program for Linux |
| `dist/quick/RemoHop.ico`, `RemoHop.icns`, `RemoHop.png` | the icon by itself, for installers and the website |

Everything is made with Go alone (`CGO_ENABLED=0`), from any system; `packaging/quick/build.sh` is the whole recipe.

## How the pieces are made

`packaging/quick/gen` is a small Go program without dependencies:

- **Icon.** Drawn in code (`icon.go`), the same mark as the mobile apps, the website and the page's `icon.svg`. Windows gets 16 to 64 pixels as bitmaps and 256 as PNG; macOS gets PNG entries from 32 to 1024 pixels with the usual margin.
- **Windows resources.** `gen -syso cmd/remohop-quick` writes `rsrc_windows_{amd64,arm64,386}.syso` next to the main package: COFF objects with one `.rsrc` section holding the icon group, its images and the manifest (run as the invoking user, Windows 10/11, current visual style; no DPI setting, because the capture and input code sets that itself). The Go linker links a `.syso` it finds there; no `windres`, no cgo, no extra module. The files are generated and git-ignored. `gen -verify RemoHop.exe` reads the linked program back and fails the build when the resources or the GUI subsystem are missing. There is no version-information resource yet (the file's Properties > Details page is empty).
- **macOS bundle.** `gen -app RemoHop.app -binary arm64=...,amd64=...` joins the two programs into a universal binary (the same layout `lipo` writes) and lays out the bundle:

  ```
  RemoHop.app/Contents/Info.plist              com.remohop.quick, LSUIElement, macOS 12+
  RemoHop.app/Contents/PkgInfo
  RemoHop.app/Contents/MacOS/RemoHop            cmd/remohop-quick, universal
  RemoHop.app/Contents/Resources/RemoHop.icns
  ```

  `LSUIElement` keeps the app out of the Dock: its window is a browser page, and a Dock icon for a process that never opens a window of its own would bounce and then show as not responding.

## Signing

Nothing here is signed.

**Windows.** `RemoHop.exe` goes through the same hook as `remohop.exe` (`scripts/sign-windows.sh`, see `packaging/README.md`); add the two files to the list it signs. Until then SmartScreen warns on first start.

**macOS.** The macOS signing workflow in the CI repository (see [packaging/README.md](../README.md)) signs and notarizes the bare `remohop` binary with the Developer ID certificate. For the bundle the same workflow needs these changes:

1. Build the bundle on the runner (`make quick`, or only the darwin part of `build.sh`), or build the two thin programs there and run `gen -app`. Do not sign the thin programs first: signing happens once, on the finished bundle.
2. Sign the **bundle**, not the file inside it: `codesign --force --options runtime --timestamp --sign "Developer ID Application: ..." dist/quick/macos/RemoHop.app`. That signs the main program and seals `Info.plist` and the resources into `Contents/_CodeSignature`. It is the same program as `remohop`, which is signed without entitlements today, so none should be needed (it loads only system frameworks, through purego, and sends no Apple events to other apps).
3. Notarize an archive of the bundle, then staple the bundle itself, which a bare binary cannot do (so the app passes Gatekeeper offline):

   ```bash
   ditto -c -k --keepParent RemoHop.app RemoHop.zip
   xcrun notarytool submit RemoHop.zip --keychain-profile ... --wait
   xcrun stapler staple RemoHop.app
   ditto -c -k --keepParent RemoHop.app RemoHop-macos.zip   # zip again: the ticket is inside now
   spctl --assess --type execute -vv RemoHop.app            # "accepted, source=Notarized Developer ID"
   ```

4. Ship the zip made with `ditto` (or a `.dmg`). `zip -r` and `tar` from other tools can drop the bundle's symlinks and extended attributes on some systems; `ditto` is what Apple's tools expect.
5. The identity that macOS privacy settings remember (Screen Recording, Accessibility) is the bundle identifier plus the signing team. It must stay `com.remohop.quick` signed by the same team across releases, or users are asked to grant the permissions again. An unsigned or ad-hoc signed build gets a new identity with every build.

The release workflow in this repository then downloads the signed zip the way it downloads `remohop-macos-universal`, and should check after unpacking that `Contents/MacOS/RemoHop` is a universal Mach-O carrying the version string, as it does for `remohop`.

## Not verified yet

The page and its server are tested in headless Chromium on Linux, and the app-window mode was run with Chromium under a virtual display. The Windows program and the macOS bundle are cross-compiled and checked structurally (`gen -verify`, the tests in `packaging/quick/gen`), but have not been started on Windows or macOS: the Edge app window, the message box / `osascript` approval dialogs, the icon in Explorer and Finder, and Gatekeeper's verdict on a signed bundle still need a run on real systems.
