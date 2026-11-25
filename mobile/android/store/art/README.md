# Google Play artwork

Phone screenshots for the Play listing, as posters in the style of the App Store artwork (`../../../ios/store-art`): the same background, type and headlines, with the Android captures in a phone frame.

| Output | Size | Where it goes |
|---|---|---|
| `out/<locale>/<n>-<scene>.jpg` | 1242 × 2208 (9:16) | `../screenshots/<locale>/`, Play phone screenshots |

Locales are `en-US` and `zh-CN`.

## Captures

`src/<locale>/<n>-<screen>.webp` are captures of the real app made by CI, nothing by hand: the CI repository's `sodesk-android.yml` with `screenshots=only` runs `StoreScreenshotsTest` on a 1080 × 1920 emulator against a `remohop host` on a demo machine (host name `studio-pc`, user `demo`, made-up files, an Xvfb desktop), and keeps the PNGs as the artifact `remohop-play-screenshots`. To refresh them, download that artifact and convert:

```bash
python3 -c 'import sys,glob,os; from PIL import Image
for p in glob.glob(sys.argv[1]+"/*/*.png"):
    d=os.path.join("src",os.path.basename(os.path.dirname(p))); os.makedirs(d,exist_ok=True)
    Image.open(p).save(os.path.join(d,os.path.basename(p)[:-4]+".webp"),lossless=True)' /path/to/remohop-play-screenshots
```

## Rebuild

The headlines come from the iPhone posters (`T` in `../../../ios/store-art/build.js`); `SCENES` in `build.js` sets the order. A scene whose capture is missing is left out.

```bash
npm install puppeteer-core@23
PPTR=puppeteer-core CHROME="/path/to/chrome" node build.js     # writes out/<locale>/*.jpg
```

Then replace `../screenshots/<locale>/` with `out/<locale>/`.

## Upload

The CI repository's `sodesk-android-release.yml` with `mode=screenshots` and `shots_run_id=repo` uploads `../screenshots/<locale>/` at `ref` (default `main`) to the Play listing: in one edit, per language, it deletes the phone screenshots and uploads these in file-name order. Images only; the listing's text is not touched.
