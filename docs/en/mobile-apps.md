# Mobile apps

[中文](../zh/mobile-apps.md)

The repository has two mobile apps: `mobile/android` and `mobile/ios`. Both are **controllers**: a native shell plus a WebView that shows an in-process `remohop web` gateway (the Go core is bound with gomobile). The Android app can also let the phone itself be viewed and, if you allow it, controlled from another RemoHop device: see [This phone as a host](#this-phone-as-a-host-android). The iOS app can share the screen of an iPhone or iPad for viewing only: see [Share this screen](#share-this-screen-ios).

- iOS does not let any app control the device: an iPhone or iPad can be viewed, never controlled.
- Get the iOS app from the [App Store](https://apps.apple.com/app/id6838609056). The Android app has been submitted to Google Play for review; until it is approved, build it yourself.
- Android: run `mobile/android/build.sh` (needs Go, JDK 17+, the Android SDK and an NDK, with `ANDROID_HOME` set). By default it builds a debug APK and runs the JVM unit tests.
- iOS: on macOS, run `mobile/ios/build-xcframework.sh` to produce `Remohopcore.xcframework`, then build `mobile/ios/RemoHop.xcodeproj` in Xcode.
- Tested in CI on every change: the iOS app in the iOS Simulator, where a test inside the app connects to a `remohop host` by address and password, runs a command in the terminal and lists a directory in Files; the Android app on an emulator, where instrumented tests pair with a real host and use the terminal, files, downloads, the clipboard and the desktop tab, and check that Settings survives rotation, recreation and being reopened from the launcher. Please report problems you see on your device.
- Background and foreground: iOS takes the listening socket of a suspended app away. The iOS app therefore closes its in-process gateway's listener when it goes to the background and reopens it, on the same port, when it comes back; both apps also check on every return that the gateway answers, and reopen its listener if not. Host sessions held by the gateway are kept. If the page still gets no answer it asks the app to check again and retries; failing that it says "The gateway is not answering" with a Retry button.
- Sessions stay connected until you disconnect. If the connection drops (the app was suspended, the phone switched networks, a short outage), the app reconnects by itself, shows "Reconnecting…" meanwhile, and brings back the open tab: the terminal re-attaches to the same shell on the host. A host that asks its owner to approve every login asks again; a refusal ends the session with the reason. On Android a notification ("Connected to …", with Disconnect) keeps the session while the app is in the background. On iOS a short trip to another app keeps it; after a longer one it reconnects when you return. See [security.md](../security.md#mobile-apps) for how the app logs in again.
- IPv6-only networks: the iOS app reaches IPv4 addresses (a host typed as `IP:port`, a rendezvous server set by address) through the network's NAT64; see [How it works](how-it-works.md#ipv6-only-networks-nat64).
- A rendezvous server address that turns out to be a RemoHop host is reported as such (code `signal_is_host`): connect to the host by that address instead.
- The mobile apps do not support account login yet.
- Language: English or Simplified Chinese, for the native screens and the page alike. The default follows the system (Chinese for any Chinese system language, otherwise English); Settings → Language switches it at once, without dropping the sessions. On Android 13 and later the system's per-app language setting is the same choice.

## This phone as a host (Android)

Settings → This device → **Allow remote connection** lets other RemoHop devices (the desktop app, the browser console, `remohop <ID> desktop`, another phone) connect to the phone. The page shows the phone's ID (or, without a rendezvous server, its address on the local network), the temporary password with Copy and New password, the status and who is connected. The explanations are behind the page's ⓘ button.

- Every request asks you on the phone, with a dialog (or a notification when the app is in the background) that shows the device's name, key fingerprint and address, whether it is new, and the scam warning. Nobody answering within 60 seconds means no.
- A device you allow sees the screen. It can also control the phone only if you tick **Control**, which needs RemoHop's accessibility service: the app explains what the service does before it opens the system's accessibility settings, and you can switch it off there at any time. Without it, sessions are view-only.
- Only the temporary password works, no pairing is issued, and nothing but the screen (and basic system information) is offered: no files, clipboard or terminal.
- Android asks before your screen is shared, for every sharing. While someone is connected, a notification says who views or controls the phone, with Stop. The sharing ends when the last device disconnects, when you tap Stop or Disconnect, or from Android's screen sharing indicator. Host mode is off again whenever the app restarts.
- Control from the viewer: click to tap, hold to long press, drag to swipe, the wheel scrolls (Ctrl + wheel pinches). Right click or Esc is Back, middle click or Home is Home, the Menu key is Recents, F1 opens the notifications. Typed text goes into the focused field.
- The screen is encoded on the phone (H.264, hardware), at most 1920 pixels on the long edge and 60 fps, and follows rotation.

## Share this screen (iOS)

Settings → This device → **Share this screen** lets other RemoHop devices view the iPhone's or iPad's screen. iOS lets no app control the device, so this is view only, as with other remote-support apps on iOS. The page shows the status, **Start sharing**, and while sharing the ID (or, without a rendezvous server, the address on the local network), the temporary password with Copy, the requests and the connected devices with Disconnect. The explanations and the scam warning are behind the page's ⓘ button.

- Sharing starts only when you tap **Start sharing** and then **Start Broadcast** in the system's sheet (it is also in Control Center, under Screen Recording, as RemoHop). Stop it any time from the red status bar or Control Center, or with **Stop sharing** on the page. It also ends when the phone restarts, or when iOS ends it.
- Sharing runs in RemoHop's broadcast extension, so it continues while you use other apps; the app itself may be closed.
- Every request asks you, with a notification (Allow (view only) or Deny) that shows the device's name, whether it is new, and a short scam warning, and on the page. Allow needs the phone unlocked; dismissing the notification denies. Nobody answering within 60 seconds means no. Turn on RemoHop's notifications, or keep the page open to answer there.
- Only the temporary password works (a new one every time sharing starts), no pairing is issued, and nothing but the screen (and basic system information) is offered. No sound is shared.
- Viewers see everything on the screen while sharing, including notifications that appear.
- The screen is encoded on the device (H.264, hardware), at most 1920 pixels on the long edge and 30 fps, and turns upright when the device turns. Under memory pressure it lowers the resolution (iOS gives the extension about 50 MB).
- The extension uses the app's identity and ID: on its first start after the update, the app moves its settings into a folder it shares with the extension (an App Group). Open the app once before starting a broadcast from Control Center.
