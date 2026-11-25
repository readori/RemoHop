# Mobile apps

[中文](../zh/mobile-apps.md)

The repository has two mobile apps: `mobile/android` and `mobile/ios`. Both are **controllers**: a native shell plus a WebView that shows an in-process `remohop web` gateway (the Go core is bound with gomobile).

- Controller only. Running an Android or iOS device as a controlled host is not implemented.
- Both apps have been submitted for review and are coming to the App Store and Google Play; until they are approved, build them yourself.
- Android: run `mobile/android/build.sh` (needs Go, JDK 17+, the Android SDK and an NDK, with `ANDROID_HOME` set). By default it builds a debug APK and runs the JVM unit tests.
- iOS: on macOS, run `mobile/ios/build-xcframework.sh` to produce `Remohopcore.xcframework`, then build `mobile/ios/RemoHop.xcodeproj` in Xcode.
- Tested in CI on every change: the iOS app in the iOS Simulator, where a test inside the app connects to a `remohop host` by address and password, runs a command in the terminal and lists a directory in Files; the Android app on an emulator, where instrumented tests pair with a real host and use the terminal, files, downloads, the clipboard and the desktop tab, and check that Settings survives rotation, recreation and being reopened from the launcher. Please report problems you see on your device.
- Background and foreground: iOS takes the listening socket of a suspended app away. The iOS app therefore closes its in-process gateway's listener when it goes to the background and reopens it, on the same port, when it comes back; both apps also check on every return that the gateway answers, and reopen its listener if not. Host sessions held by the gateway are kept. If the page still gets no answer it asks the app to check again and retries; failing that it says "The gateway is not answering" with a Retry button.
- Sessions stay connected until you disconnect. If the connection drops (the app was suspended, the phone switched networks, a short outage), the app reconnects by itself, shows "Reconnecting…" meanwhile, and brings back the open tab: the terminal re-attaches to the same shell on the host. A host that asks its owner to approve every login asks again; a refusal ends the session with the reason. On Android a notification ("Connected to …", with Disconnect) keeps the session while the app is in the background. On iOS a short trip to another app keeps it; after a longer one it reconnects when you return. See [security.md](../security.md#mobile-apps) for how the app logs in again.
- IPv6-only networks: the iOS app reaches IPv4 addresses (a host typed as `IP:port`, a rendezvous server set by address) through the network's NAT64; see [How it works](how-it-works.md#ipv6-only-networks-nat64).
- A rendezvous server address that turns out to be a RemoHop host is reported as such (code `signal_is_host`): connect to the host by that address instead.
- The mobile apps do not support account login yet.
- Language: English or Simplified Chinese, for the native screens and the page alike. The default follows the system (Chinese for any Chinese system language, otherwise English); Settings → Language switches it at once, without dropping the sessions. On Android 13 and later the system's per-app language setting is the same choice.
