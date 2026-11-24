# App Store artwork

Made from real screenshots of iOS 1.0.0 on an iPhone, connected to the review demo host. There is one set for each locale, en-US and zh-Hans.

| Output | Size | Where it goes |
|---|---|---|
| `iPhone_1320x2868_*.jpg` (7) | 1320 × 2868 | `../fastlane/screenshots/<locale>/`, iPhone 6.9" |
| `iPad_2064x2752_*.jpg` (4) | 2064 × 2752 | `../fastlane/screenshots/<locale>/`, iPad 13" |
| `marketing/<locale>/Header_5244x2950.jpg` | 5244 × 2950 | promotional artwork (not a screenshot) |
| `marketing/<locale>/SearchResults_3840x2560.jpg` | 3840 × 2560 | promotional artwork (not a screenshot) |

The CI repository's `sodesk-ios.yml` in `metadata` mode uploads the files in `mobile/ios/fastlane/screenshots` with fastlane deliver and replaces the sets that are in App Store Connect. Nothing is submitted for review.

The iPad images show the iPhone UI inside phone frames. Guideline 2.3.3 expects iPad screenshots to show the app running on an iPad, so replace them with real iPad captures before an iPad review if the reviewer objects.

## Rebuild

`src/<locale>-<screen>.webp` holds the device screenshots, and `src/icon.png` is the 1024 px app icon. The headlines are in `build.js` (`T`). To rebuild:

```bash
npm install puppeteer-core@23
PPTR=puppeteer-core CHROME="/path/to/chrome" node build.js     # writes out/<locale>/*.jpg
```

Then copy the iPhone and iPad files to `../fastlane/screenshots/<locale>/`, and copy Header and SearchResults to `marketing/<locale>/`.
