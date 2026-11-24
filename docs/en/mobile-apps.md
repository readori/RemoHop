# Mobile apps

[中文](../zh/mobile-apps.md)

The repository has two mobile apps: `mobile/android` and `mobile/ios`. Both are **controllers**: a native shell plus a WebView that shows an in-process `remohop web` gateway (the Go core is bound with gomobile).

- Controller only. Running an Android or iOS device as a controlled host is not implemented.
- There are no store releases yet. Build them yourself.
- Android: run `mobile/android/build.sh` (needs Go, JDK 17+, the Android SDK and an NDK, with `ANDROID_HOME` set). By default it builds a debug APK and runs the JVM unit tests.
- iOS: on macOS, run `mobile/ios/build-xcframework.sh` to produce `Remohopcore.xcframework`, then build `mobile/ios/RemoHop.xcodeproj` in Xcode.
- Tested: the iOS app runs in the iOS Simulator in CI, where a test inside the app connects to a `remohop host` by address and password, runs a command in the terminal and lists a directory in Files. It has not been run on a physical iPhone or iPad, and no signed build has been released. The Android app has had no real-device test.
- The mobile apps do not support account login yet.
- Language: English or Simplified Chinese, for the native screens and the page alike. The default follows the system (Chinese for any Chinese system language, otherwise English); Settings → Language switches it at once, without dropping the sessions. On Android 13 and later the system's per-app language setting is the same choice.
