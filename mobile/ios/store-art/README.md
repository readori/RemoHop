# App Store artwork

The iPhone images are made from real screenshots of iOS 1.0.0 on an iPhone, connected to the review demo host. The iPad images are made from real captures of the app on an iPad Pro 13-inch simulator (see below). There is one set for each locale, en-US and zh-Hans.

| Output | Size | Where it goes |
|---|---|---|
| `iPhone_1320x2868_*.jpg` (7) | 1320 × 2868 | `../fastlane/screenshots/<locale>/`, iPhone 6.9" |
| `iPad_2064x2752_*.jpg` (4) | 2064 × 2752 | `../fastlane/screenshots/<locale>/`, iPad 13" |
| `marketing/<locale>/Header_5244x2950.jpg` | 5244 × 2950 | promotional artwork (not a screenshot) |
| `marketing/<locale>/SearchResults_3840x2560.jpg` | 3840 × 2560 | promotional artwork (not a screenshot) |

The CI repository's `sodesk-ios.yml` in `metadata` mode uploads the files in `mobile/ios/fastlane/screenshots` with fastlane deliver and replaces the sets that are in App Store Connect. Nothing is submitted for review.

The iPad images show the app's real iPad UI (Guideline 2.3.3), one screen per poster in an iPad frame. The file names stay the ones App Store Connect already has, so the slugs do not all match the screens:

| File | Screen |
|---|---|
| `iPad_2064x2752_1_desktop-terminal.jpg` | remote desktop |
| `iPad_2064x2752_2_files-clipboard.jpg` | files |
| `iPad_2064x2752_3_connect-info.jpg` | connect screen with the open session and a saved host |
| `iPad_2064x2752_4_privacy-settings.jpg` | terminal |

## iPad captures

`src/<locale>-ipad-<screen>.webp` (2064 × 2752) come from the CI repository's `sodesk-ios.yml` in `capture` mode (request `MODE=capture`, `DEVICES=ipad13`, `[sodesk-ios-capture]` in the commit message). It runs `RemoHopTests/GatewayE2ETests/testStoreScreens` on an iPad Pro 13-inch simulator, once with the simulator in English and once in Simplified Chinese, against a `remohop host` on the CI Mac: a made-up home directory (`/Users/demo`) and the Mac's real desktop. The host is saved and renamed in the app ("Studio Mac", "工作室 Mac"). Capture mode uploads nothing; the PNGs are a 7-day artifact, `remohop-ios-capture`. Convert them with e.g. `cwebp -q 95` (or ImageMagick) to the names above.

The current captures are from readori/test-env-setup runs 45528358043 (all screens except the English terminal) and 45525623790 (the English terminal; in the later run a slow first `git` call let the test type the command twice). Both ran the same setup on the same iPad Pro 13-inch (M5) simulator, iOS 26.5.

## Rebuild

`src/<locale>-<screen>.webp` holds the iPhone screenshots, `src/<locale>-ipad-<screen>.webp` the iPad captures, and `src/icon.png` is the 1024 px app icon. The headlines are in `build.js` (`T`). To rebuild:

```bash
npm install puppeteer-core@23
PPTR=puppeteer-core CHROME="/path/to/chrome" node build.js     # writes out/<locale>/*.jpg
ONLY=ipad PPTR=puppeteer-core CHROME="/path/to/chrome" node build.js   # only the iPad posters
```

Then copy the iPhone and iPad files to `../fastlane/screenshots/<locale>/`, and copy Header and SearchResults to `marketing/<locale>/`.
