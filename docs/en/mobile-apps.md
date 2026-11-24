# Mobile apps

[中文](../zh/mobile-apps.md)

The repository has two mobile apps: `mobile/android` and `mobile/ios`. Both are **controllers**: a native shell plus a WebView that shows an in-process `remohop web` gateway (the Go core is bound with gomobile).

- Controller only. Running an Android or iOS device as a controlled host is not implemented.
- There are no store releases yet. Build them yourself.
- Android: run `mobile/android/build.sh` (needs Go, JDK 17+, the Android SDK and an NDK, with `ANDROID_HOME` set). By default it builds a debug APK and runs the JVM unit tests.
- iOS: on macOS, run `mobile/ios/build-xcframework.sh` to produce `Remohopcore.xcframework`, then build `mobile/ios/RemoHop.xcodeproj` in Xcode.
- Tested: the iOS app runs in the iOS Simulator in CI, where a test inside the app connects to a `remohop host` by address and password, runs a command in the terminal and lists a directory in Files. It has not been run on a physical iPhone or iPad, and no signed build has been released. The Android app has had no real-device test.
- Background and foreground: iOS takes the listening socket of a suspended app away. The iOS app therefore closes its in-process gateway's listener when it goes to the background and reopens it, on the same port, when it comes back; both apps also check on every return that the gateway answers, and reopen its listener if not. Host sessions held by the gateway are kept. If the page still gets no answer it asks the app to check again and retries; failing that it says "The gateway is not answering" with a Retry button.
- IPv6-only networks: the iOS app reaches IPv4 addresses (a host typed as `IP:port`, a rendezvous server set by address) through the network's NAT64; see [How it works](how-it-works.md#ipv6-only-networks-nat64).
- A rendezvous server address that turns out to be a RemoHop host is reported as such (code `signal_is_host`): connect to the host by that address instead.
- The mobile apps do not support account login yet.
- Language: English or Simplified Chinese, for the native screens and the page alike. The default follows the system (Chinese for any Chinese system language, otherwise English); Settings → Language switches it at once, without dropping the sessions. On Android 13 and later the system's per-app language setting is the same choice.
